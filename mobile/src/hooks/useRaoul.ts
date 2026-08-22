import { useCallback, useEffect, useRef, useState } from 'react';
import { speak, speakOnDevice, stopSpeaking } from '../lib/speech';

import { api, AssistantAnswer } from '../api';
import { applyActions } from '../lib/calendar';
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

export type Exchange = {
  id: string;
  question: string;
  answer: string;
  steps?: string[];
  effects?: string[];
  at: Date;
};

/** Silence après lequel on considère que la demande est terminée. */
const SILENCE_MS = 1700;
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
  const [history, setHistory] = useState<Exchange[]>([]);
  const [inConversation, setInConversation] = useState(false);

  const mode = useRef<Mode>('off');
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

  const resetBuffers = useCallback(() => {
    finalized.current = '';
    utterance.current = '';
    anchor.current = 0;
  }, []);

  const startRecognition = useCallback(async (nextMode: Mode) => {
    if (!SpeechRecognition) {
      setError(VOICE_UNAVAILABLE);
      return;
    }
    try {
      resetBuffers();
      mode.current = nextMode;
      SpeechRecognition.start(RECOGNITION_OPTIONS);
      setState(nextMode === 'wake' ? 'waiting' : 'listening');
      setError(null);
    } catch (err) {
      setError((err as Error).message);
      setState('off');
      mode.current = 'off';
    }
  }, [resetBuffers]);

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

  /** Envoie la demande au backend, lit la réponse, applique les actions. */
  const submit = useCallback(
    async (question: string) => {
      clearTimers();
      mode.current = 'off';
      SpeechRecognition?.abort();
      setPartial('');
      setState('thinking');

      let answer: AssistantAnswer;
      try {
        answer = await api.ask(question);
      } catch (err) {
        const message = (err as Error).message;
        setError(message);
        await speakOnDevice("Je n'ai pas réussi à joindre le serveur.");
        resume();
        return;
      }

      // La voix démarre AVANT l'exécution des actions, et les deux courent en
      // parallèle. C'est ce qui permet à Raoul de finir sa phrase quand une
      // action ouvre Waze : iOS laisse tourner un son déjà en cours (mode
      // audio en fond), mais rien ne garantit qu'on puisse en démarrer un une
      // fois passé en arrière-plan.
      setState('speaking');
      const spoken = speak(answer.reply);

      let effects: string[] = [];
      if (answer.actions?.length) {
        effects = await applyActions(answer.actions);
      }

      setHistory((prev) => [
        {
          id: `${Date.now()}`,
          question: answer.transcript || question,
          answer: answer.reply,
          steps: answer.steps,
          effects,
          at: new Date(),
        },
        ...prev,
      ]);

      await spoken;
      resume();
    },
    [clearTimers, resume],
  );

  const armSilence = useCallback(() => {
    if (silenceTimer.current) clearTimeout(silenceTimer.current);
    silenceTimer.current = setTimeout(() => {
      const command = cleanCommand(currentCommand(finalized, utterance, anchor));
      if (command.length >= 2) void submit(command);
      else resume();
    }, SILENCE_MS);
  }, [resume, submit]);

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
    // iOS coupe régulièrement la session de reconnaissance. On la relance dans
    // le mode courant : en conversation ouverte, repartir en attente du mot
    // d'activation obligerait à redire « OK Raoul » sans raison.
    const current = mode.current;
    if ((current === 'wake' || current === 'command') && enabled.current) {
      setTimeout(() => {
        if (mode.current === current && enabled.current) void startRecognition(current);
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
    if (!(await ensureMic())) return false;
    enabled.current = true;
    conversing.current = true;
    setInConversation(true);
    stopSpeaking();
    SpeechRecognition?.abort();
    await startRecognition('command');
    armSilence();
    return true;
  }, [armSilence, ensureMic, startRecognition]);

  const stop = useCallback(() => {
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
    SpeechRecognition?.abort();
    await startRecognition('command');
    armSilence();
  }, [armSilence, ensureMic, startRecognition]);

  /** Saisie clavier, pour tester sans parler. */
  const askText = useCallback((text: string) => submit(text), [submit]);

  useEffect(() => {
    return () => {
      clearTimers();
      SpeechRecognition?.abort();
      stopSpeaking();
    };
  }, [clearTimers]);

  return {
    state,
    partial,
    error,
    history,
    start,
    startConversation,
    stop,
    pushToTalk,
    askText,
    voiceAvailable,
    inConversation,
    isEnabled: enabled,
  };
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


