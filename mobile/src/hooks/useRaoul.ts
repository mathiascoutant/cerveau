import { useCallback, useEffect, useRef, useState } from 'react';
import { AppState } from 'react-native';
import { speak, speakOnDevice, stopSpeaking } from '../lib/speech';

import { AssistantAction, AssistantMetrics } from '../api';
import { applyActions } from '../lib/calendar';
import { AbortedError, askStream } from '../lib/stream';
import { cleanCommand, findFarewell, findWake } from '../lib/wakeword';
import {
  RecognitionOptions,
  SpeechRecognition,
  useSpeechEvent,
  voiceAvailable,
} from '../lib/voiceEngine';

export type RaoulState =
  | 'off' // micro coupé
  | 'waiting' // écoute le mot d'activation
  | 'listening' // « OK Raoul » entendu, on capte la demande
  | 'thinking' // le backend consulte agenda/mails/Slack/WhatsApp
  | 'speaking'; // Raoul répond à voix haute

/** Une étape visible pendant que Raoul travaille : un outil, sa durée, son sort. */
export type ToolStep = {
  tool: string;
  label: string;
  ms?: number;
  err?: string;
};

/**
 * Un élément du fil de conversation. Un message de Raoul se construit au fil
 * du flux : `streaming` tant que le texte arrive, puis les mesures à la fin.
 */
export type ChatItem = {
  id: string;
  role: 'user' | 'raoul';
  text: string;
  steps?: ToolStep[];
  effects?: string[];
  metrics?: AssistantMetrics;
  streaming?: boolean;
  error?: string;
  at: Date;
};

/** Silence après lequel on considère que la demande est terminée. */
const SILENCE_MS = 1700;
/**
 * Délai laissé pour COMMENCER à parler quand on ouvre le micro sans mot
 * d'activation. Le silence de fin de phrase ne peut pas servir ici : 1,7 s
 * après un appui sur le widget, on a à peine remonté le téléphone à l'oreille.
 */
const OPENING_MS = 9000;
/** Garde-fou : au-delà, on envoie ce qu'on a. */
const MAX_COMMAND_MS = 25000;
/**
 * Battement entre la fin de la phrase de Raoul et la reprise du micro. Sans
 * lui, la traîne de sa propre voix revient dans la reconnaissance et se fait
 * traiter comme une demande.
 */
const ECHO_GUARD_MS = 400;

/** Réponses à « merci Raoul » — jamais deux fois la même de suite. */
const FAREWELLS = ['De rien.', 'Quand tu veux.', 'Ok.'];

const RECOGNITION_OPTIONS: RecognitionOptions = {
  lang: 'fr-FR',
  interimResults: true,
  continuous: true,
  requiresOnDeviceRecognition: true,
  addsPunctuation: false,
  // « Raoul » n'est pas dans le lexique courant : on aide le moteur.
  contextualStrings: ['Raoul', 'OK Raoul', 'Slack', 'WhatsApp', 'Gandi'],
  iosCategory: {
    category: 'playAndRecord',
    categoryOptions: ['defaultToSpeaker', 'allowBluetooth', 'duckOthers'],
    mode: 'measurement',
  },
};

type Mode = 'off' | 'wake' | 'command';

const VOICE_UNAVAILABLE =
  "Mode texte : la reconnaissance vocale est un module natif, absent d'Expo Go. Écris ta demande, ou lance un dev build pour activer « OK Raoul ».";

export function useRaoul() {
  const [state, setState] = useState<RaoulState>('off');
  const [partial, setPartial] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [thread, setThread] = useState<ChatItem[]>([]);
  const [inConversation, setInConversation] = useState(false);
  // La demande en cours, pour pouvoir l'interrompre : couper le flux côté
  // client annule aussi le modèle et les outils côté serveur.
  const inflight = useRef<AbortController | null>(null);

  const mode = useRef<Mode>('off');
  // running : une session de reconnaissance est ouverte côté natif. iOS ne la
  // ferme pas au retour d'abort() — il émet « end » plus tard — et démarrer
  // dans l'intervalle donne une session qui s'annonce active et n'entend rien.
  const running = useRef(false);
  // closing : une fermeture volontaire est en cours. Le « end » qu'elle
  // provoque ne doit pas déclencher la relance automatique, sinon on rouvre
  // une session par-dessus celle qu'on vient d'ouvrir.
  const closing = useRef(false);
  const endWaiters = useRef<Array<() => void>>([]);
  // startEpoch départage les démarrages concurrents : deux appuis rapprochés
  // sur le widget, ou une relance automatique qui croise une ouverture
  // manuelle. Seul le plus récent va au bout, les autres se retirent.
  const startEpoch = useRef(0);
  // conversing : « OK Raoul » a été dit et la conversation n'est pas refermée.
  // Tant qu'il est vrai, tout ce qui est prononcé est une demande — plus besoin
  // de réveiller Raoul à chaque phrase.
  const conversing = useRef(false);
  const lastFarewell = useRef(-1);
  const enabled = useRef(false); // l'utilisateur veut l'écoute permanente
  const finalized = useRef(''); // énoncés déjà finalisés depuis le démarrage
  const utterance = useRef(''); // énoncé en cours
  const anchor = useRef(0); // position juste après « OK Raoul »
  const silenceTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const maxTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const clearTimers = useCallback(() => {
    if (silenceTimer.current) clearTimeout(silenceTimer.current);
    if (maxTimer.current) clearTimeout(maxTimer.current);
    silenceTimer.current = null;
    maxTimer.current = null;
  }, []);

  /** Réveille ceux qui attendaient la fin de la session. */
  const releaseEndWaiters = useCallback(() => {
    const waiters = endWaiters.current;
    endWaiters.current = [];
    closing.current = false;
    waiters.forEach((resolve) => resolve());
  }, []);

  /**
   * Ferme la session en cours et attend sa fin réelle.
   *
   * Le filet de 700 ms couvre le cas où « end » ne viendrait jamais : mieux
   * vaut repartir sur une session peut-être bancale que rester bloqué sans
   * micro.
   */
  const stopSession = useCallback(
    () =>
      new Promise<void>((resolve) => {
        if (!SpeechRecognition || !running.current) {
          resolve();
          return;
        }
        closing.current = true;
        endWaiters.current.push(resolve);
        SpeechRecognition.abort();
        setTimeout(() => {
          if (endWaiters.current.length > 0) {
            running.current = false;
            releaseEndWaiters();
          }
        }, 700);
      }),
    [releaseEndWaiters],
  );

  const resetBuffers = useCallback(() => {
    finalized.current = '';
    utterance.current = '';
    anchor.current = 0;
  }, []);

  const startRecognition = useCallback(
    async (nextMode: Mode) => {
      if (!SpeechRecognition) {
        setError(VOICE_UNAVAILABLE);
        return;
      }
      // Jamais deux sessions à la fois : on attend la fermeture de la
      // précédente plutôt que d'empiler un start() par-dessus un abort().
      const epoch = ++startEpoch.current;
      await stopSession();
      if (startEpoch.current !== epoch) return; // un démarrage plus récent a pris la main
      try {
        resetBuffers();
        mode.current = nextMode;
        running.current = true;
        SpeechRecognition.start(RECOGNITION_OPTIONS);
        setState(nextMode === 'wake' ? 'waiting' : 'listening');
        setError(null);
      } catch (err) {
        running.current = false;
        setError((err as Error).message);
        setState('off');
        mode.current = 'off';
      }
    },
    [resetBuffers, stopSession],
  );

  /**
   * Rend le micro après une réponse. Tant que la conversation est ouverte, on
   * repart directement en écoute de commande : c'est ce qui évite d'avoir à
   * redire « OK Raoul » à chaque phrase.
   */
  const resume = useCallback(() => {
    clearTimers();
    if (!enabled.current) {
      setState('off');
      return;
    }
    setTimeout(() => {
      if (!enabled.current) return;
      void startRecognition(conversing.current ? 'command' : 'wake');
    }, ECHO_GUARD_MS);
  }, [clearTimers, startRecognition]);

  /** Envoie la demande au backend en flux, lit la réponse, applique les actions. */
  const submit = useCallback(
    async (question: string) => {
      clearTimers();
      mode.current = 'off';
      SpeechRecognition?.abort();
      setPartial('');
      setState('thinking');
      setError(null);

      inflight.current?.abort();
      const controller = new AbortController();
      inflight.current = controller;

      const userId = `u${Date.now()}`;
      const raoulId = `r${Date.now()}`;
      setThread((prev) => [
        ...prev,
        { id: userId, role: 'user', text: question, at: new Date() },
        { id: raoulId, role: 'raoul', text: '', steps: [], streaming: true, at: new Date() },
      ]);
      const patch = (edit: (item: ChatItem) => ChatItem) =>
        setThread((prev) => prev.map((item) => (item.id === raoulId ? edit(item) : item)));

      let reply = '';
      let actions: AssistantAction[] = [];
      let metrics: AssistantMetrics | undefined;
      let speechUrl: string | undefined;

      try {
        await askStream(
          question,
          (ev) => {
            switch (ev.type) {
              case 'status':
                patch((item) => {
                  const steps = [...(item.steps ?? [])];
                  // Le second événement d'un même outil porte sa durée : on
                  // complète la ligne au lieu d'en ouvrir une deuxième.
                  const open = steps.findIndex((st) => st.tool === ev.tool && st.ms === undefined);
                  if (ev.ms !== undefined && open >= 0) {
                    steps[open] = { ...steps[open], ms: ev.ms, err: ev.err };
                  } else if (ev.ms === undefined) {
                    steps.push({ tool: ev.tool, label: ev.label });
                  }
                  return { ...item, steps };
                });
                break;
              case 'delta':
                reply += ev.text;
                patch((item) => ({ ...item, text: item.text + ev.text }));
                break;
              case 'reset':
                reply = '';
                patch((item) => ({ ...item, text: '' }));
                break;
              case 'done':
                reply = ev.result.reply || reply;
                actions = ev.result.actions ?? [];
                metrics = ev.metrics ?? ev.result.metrics;
                speechUrl = ev.speech_url;
                patch((item) => ({ ...item, text: reply, metrics, streaming: false }));
                break;
              case 'error':
                throw new Error(ev.message);
              default:
                break;
            }
          },
          controller.signal,
        );
      } catch (err) {
        if (inflight.current === controller) inflight.current = null;
        if (err instanceof AbortedError) {
          patch((item) => ({ ...item, streaming: false, text: item.text || 'Interrompu.', error: undefined }));
          resume();
          return;
        }
        const message = (err as Error).message;
        setError(message);
        patch((item) => ({ ...item, streaming: false, error: message, text: item.text || "Je n'ai pas réussi à joindre le serveur." }));
        await speakOnDevice("Je n'ai pas réussi à joindre le serveur.");
        resume();
        return;
      }
      if (inflight.current === controller) inflight.current = null;
      if (controller.signal.aborted) {
        resume();
        return;
      }

      // La voix démarre AVANT l'exécution des actions, et les deux courent en
      // parallèle. C'est ce qui permet à Raoul de finir sa phrase quand une
      // action ouvre Waze : iOS laisse tourner un son déjà en cours (mode
      // audio en fond), mais rien ne garantit qu'on puisse en démarrer un une
      // fois passé en arrière-plan.
      setState('speaking');
      const spoken = speak(reply, undefined, speechUrl);

      let effects: string[] = [];
      if (actions.length) {
        effects = await applyActions(actions);
      }
      if (effects.length) patch((item) => ({ ...item, effects }));

      await spoken;
      resume();
    },
    [clearTimers, resume],
  );

  /** Interrompt la demande en cours et la voix, sans couper l'écoute. */
  const cancel = useCallback(() => {
    inflight.current?.abort();
    inflight.current = null;
    stopSpeaking();
  }, []);

  const armSilence = useCallback(
    (delay = SILENCE_MS) => {
      if (silenceTimer.current) clearTimeout(silenceTimer.current);
      silenceTimer.current = setTimeout(() => {
        const command = cleanCommand(currentCommand(finalized, utterance, anchor));
        if (command.length >= 2) void submit(command);
        else resume();
      }, delay);
    },
    [resume, submit],
  );

  /**
   * Garde-fou de longueur, armé au premier mot entendu et pas avant : en
   * conversation ouverte, l'écoute peut rester silencieuse des heures, et un
   * minuteur lancé à l'ouverture enverrait du vide.
   */
  const armMax = useCallback(() => {
    if (maxTimer.current) return;
    maxTimer.current = setTimeout(() => {
      const command = cleanCommand(currentCommand(finalized, utterance, anchor));
      if (command.length >= 2) void submit(command);
    }, MAX_COMMAND_MS);
  }, [submit]);

  /**
   * Referme la conversation sur « merci Raoul ». Ce qui précédait la formule
   * reste une demande : on la traite avant de rendre la main.
   */
  const endConversation = useCallback(
    (pending: string) => {
      conversing.current = false;
      setInConversation(false);
      clearTimers();
      mode.current = 'off';
      SpeechRecognition?.abort();
      setPartial('');

      if (pending.length >= 2) {
        void submit(pending); // submit reprendra en mode « wake »
        return;
      }

      void (async () => {
        setState('speaking');
        let i = Math.floor(Math.random() * FAREWELLS.length);
        if (i === lastFarewell.current) i = (i + 1) % FAREWELLS.length;
        lastFarewell.current = i;
        await speak(FAREWELLS[i]);
        resume();
      })();
    },
    [clearTimers, resume, submit],
  );

  useSpeechEvent('result', (event) => {
    if (mode.current === 'off') return;

    const text = event.results?.[0]?.transcript ?? '';
    utterance.current = text;

    if (event.isFinal) {
      finalized.current = joinText(finalized.current, text);
      utterance.current = '';
    }

    const full = joinText(finalized.current, utterance.current);

    if (mode.current === 'wake') {
      const match = findWake(full);
      if (!match) {
        // On ne garde pas indéfiniment le bruit de fond en mémoire.
        if (full.length > 400) resetBuffers();
        return;
      }
      // À partir d'ici la conversation est ouverte : les demandes suivantes
      // n'auront plus besoin du mot d'activation.
      conversing.current = true;
      setInConversation(true);
      mode.current = 'command';
      anchor.current = match.endIndex;
      setState('listening');
      armMax();
    }

    if (mode.current === 'command') {
      const command = cleanCommand(currentCommand(finalized, utterance, anchor));

      // « merci Raoul » referme la conversation. Ce qui la précède reste une
      // demande à traiter.
      const bye = findFarewell(command);
      if (bye) {
        endConversation(cleanCommand(command.slice(0, bye.startIndex)));
        return;
      }

      setPartial(command);
      if (command.length > 0) armMax();
      armSilence();
    }
  });

  useSpeechEvent('end', () => {
    running.current = false;

    // Fermeture demandée : quelqu'un attend cette fin pour rouvrir derrière.
    // Relancer ici ferait une session de trop, et c'est précisément ce qui
    // rendait Raoul sourd quand on l'ouvrait depuis le widget.
    if (closing.current) {
      releaseEndWaiters();
      return;
    }

    // Coupure spontanée : iOS ferme régulièrement la session de reconnaissance.
    // On la relance dans le mode courant — en conversation ouverte, repartir en
    // attente du mot d'activation obligerait à redire « OK Raoul » sans raison.
    const current = mode.current;
    if ((current === 'wake' || current === 'command') && enabled.current) {
      setTimeout(() => {
        if (mode.current === current && enabled.current && !running.current) {
          void startRecognition(current);
        }
      }, 400);
    }
  });

  useSpeechEvent('error', (event) => {
    // « no-speech » est le cas nominal quand personne ne parle.
    if (event.error === 'no-speech' || event.error === 'aborted') return;
    setError(`${event.error} — ${event.message}`);
  });

  /** Vérifie que le micro est utilisable, et dit pourquoi il ne l'est pas. */
  const ensureMic = useCallback(async () => {
    if (!SpeechRecognition) {
      setError(VOICE_UNAVAILABLE);
      return false;
    }
    const perms = await SpeechRecognition.requestPermissionsAsync();
    if (!perms.granted) {
      setError('Accès au micro ou à la reconnaissance vocale refusé.');
      return false;
    }
    if (!SpeechRecognition.isRecognitionAvailable()) {
      setError('La reconnaissance vocale est indisponible sur cet appareil.');
      return false;
    }
    return true;
  }, []);

  const start = useCallback(async () => {
    if (!(await ensureMic())) return false;
    enabled.current = true;
    await startRecognition('wake');
    return true;
  }, [ensureMic, startRecognition]);

  /**
   * Entrée directe en conversation, sans mot d'activation : c'est ce que
   * déclenche le widget de l'écran d'accueil, dont l'appui tient lieu de
   * « OK Raoul ». La conversation reste ensuite ouverte comme si on l'avait dit.
   */
  const startConversation = useCallback(async () => {
    // L'app vient peut-être d'être réveillée par le widget : iOS refuse
    // d'activer le micro tant que le processus n'est pas vraiment au premier
    // plan, et l'échec est silencieux.
    await waitForForeground();
    if (!(await ensureMic())) return false;
    enabled.current = true;
    conversing.current = true;
    setInConversation(true);
    stopSpeaking();
    // Pas d'abort() ici : startRecognition ferme la session précédente et
    // attend sa fin. Les enchaîner à la main rouvrait par-dessus.
    await startRecognition('command');
    armSilence(OPENING_MS);
    return true;
  }, [armSilence, ensureMic, startRecognition]);

  const stop = useCallback(() => {
    // Invalide un démarrage encore en attente de la fermeture précédente :
    // sans ça, couper le micro pouvait être suivi d'une session qui s'ouvre.
    startEpoch.current++;
    enabled.current = false;
    conversing.current = false;
    setInConversation(false);
    mode.current = 'off';
    clearTimers();
    SpeechRecognition?.abort();
    stopSpeaking();
    setPartial('');
    setState('off');
  }, [clearTimers]);

  /** Bouton « appuyer pour parler » : on saute l'étape du mot d'activation. */
  const pushToTalk = useCallback(async () => {
    if (!(await ensureMic())) return;
    await startRecognition('command');
    armSilence(OPENING_MS);
  }, [armSilence, ensureMic, startRecognition]);

  /** Saisie clavier, pour tester sans parler. */
  const askText = useCallback((text: string) => submit(text), [submit]);

  useEffect(() => {
    return () => {
      clearTimers();
      SpeechRecognition?.abort();
      inflight.current?.abort();
      stopSpeaking();
    };
  }, [clearTimers]);

  const clearThread = useCallback(() => setThread([]), []);

  return {
    state,
    partial,
    error,
    thread,
    start,
    startConversation,
    stop,
    cancel,
    pushToTalk,
    askText,
    clearThread,
    voiceAvailable,
    inConversation,
    isEnabled: enabled,
  };
}

/**
 * Attend que l'app soit réellement active.
 *
 * Ouvrir Raoul depuis le widget démarre l'écoute au moment où l'écran se monte,
 * alors qu'iOS peut encore être en transition (« inactive »). Une session de
 * reconnaissance démarrée là s'annonce active et ne délivre jamais de résultat.
 * Le délai de garde évite de rester bloqué si l'état n'arrivait jamais.
 */
function waitForForeground(timeoutMs = 2500): Promise<void> {
  if (AppState.currentState === 'active') return Promise.resolve();
  return new Promise((resolve) => {
    let timer: ReturnType<typeof setTimeout>;
    const sub = AppState.addEventListener('change', (next) => {
      if (next !== 'active') return;
      clearTimeout(timer);
      sub.remove();
      resolve();
    });
    timer = setTimeout(() => {
      sub.remove();
      resolve();
    }, timeoutMs);
  });
}

function joinText(a: string, b: string): string {
  if (!a) return b;
  if (!b) return a;
  return `${a} ${b}`;
}

function currentCommand(
  finalized: React.MutableRefObject<string>,
  utterance: React.MutableRefObject<string>,
  anchor: React.MutableRefObject<number>,
): string {
  const full = joinText(finalized.current, utterance.current);
  if (full.length <= anchor.current) return '';
  return full.slice(anchor.current);
}


