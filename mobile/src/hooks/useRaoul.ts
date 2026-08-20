import { useCallback, useEffect, useRef, useState } from 'react';
import * as Speech from 'expo-speech';

import { api, AssistantAnswer } from '../api';
import { applyActions } from '../lib/calendar';
import { cleanCommand, findWake } from '../lib/wakeword';
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

  const mode = useRef<Mode>('off');
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
        await speak("Je n'ai pas réussi à joindre le serveur.");
        if (enabled.current) void startRecognition('wake');
        else setState('off');
        return;
      }

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

      setState('speaking');
      await speak(answer.reply);

      if (enabled.current) void startRecognition('wake');
      else setState('off');
    },
    [clearTimers, startRecognition],
  );

  const armSilence = useCallback(() => {
    if (silenceTimer.current) clearTimeout(silenceTimer.current);
    silenceTimer.current = setTimeout(() => {
      const command = cleanCommand(currentCommand(finalized, utterance, anchor));
      if (command.length >= 2) void submit(command);
      else if (enabled.current) void startRecognition('wake');
      else setState('off');
    }, SILENCE_MS);
  }, [startRecognition, submit]);

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
      mode.current = 'command';
      anchor.current = match.endIndex;
      setState('listening');
      if (maxTimer.current) clearTimeout(maxTimer.current);
      maxTimer.current = setTimeout(() => {
        const command = cleanCommand(currentCommand(finalized, utterance, anchor));
        if (command.length >= 2) void submit(command);
      }, MAX_COMMAND_MS);
    }

    if (mode.current === 'command') {
      setPartial(cleanCommand(currentCommand(finalized, utterance, anchor)));
      armSilence();
    }
  });

  useSpeechEvent('end', () => {
    // iOS coupe régulièrement la session de reconnaissance : on la relance
    // tant que l'utilisateur veut rester à l'écoute.
    if (mode.current === 'wake' && enabled.current) {
      setTimeout(() => {
        if (mode.current === 'wake' && enabled.current) void startRecognition('wake');
      }, 400);
    }
  });

  useSpeechEvent('error', (event) => {
    // « no-speech » est le cas nominal quand personne ne parle.
    if (event.error === 'no-speech' || event.error === 'aborted') return;
    setError(`${event.error} — ${event.message}`);
  });

  const start = useCallback(async () => {
    if (!SpeechRecognition) {
      setError(VOICE_UNAVAILABLE);
      return false;
    }
    const perms = await SpeechRecognition.requestPermissionsAsync();
    if (!perms.granted) {
      setError("Accès au micro ou à la reconnaissance vocale refusé.");
      return false;
    }
    if (!SpeechRecognition.isRecognitionAvailable()) {
      setError('La reconnaissance vocale est indisponible sur cet appareil.');
      return false;
    }
    enabled.current = true;
    await startRecognition('wake');
    return true;
  }, [startRecognition]);

  const stop = useCallback(() => {
    enabled.current = false;
    mode.current = 'off';
    clearTimers();
    SpeechRecognition?.abort();
    Speech.stop();
    setPartial('');
    setState('off');
  }, [clearTimers]);

  /** Bouton « appuyer pour parler » : on saute l'étape du mot d'activation. */
  const pushToTalk = useCallback(async () => {
    if (!SpeechRecognition) {
      setError(VOICE_UNAVAILABLE);
      return;
    }
    const perms = await SpeechRecognition.requestPermissionsAsync();
    if (!perms.granted) {
      setError("Accès au micro refusé.");
      return;
    }
    SpeechRecognition.abort();
    await startRecognition('command');
    armSilence();
  }, [armSilence, startRecognition]);

  /** Saisie clavier, pour tester sans parler. */
  const askText = useCallback((text: string) => submit(text), [submit]);

  useEffect(() => {
    return () => {
      clearTimers();
      SpeechRecognition?.abort();
      Speech.stop();
    };
  }, [clearTimers]);

  return {
    state,
    partial,
    error,
    history,
    start,
    stop,
    pushToTalk,
    askText,
    voiceAvailable,
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

function speak(text: string): Promise<void> {
  return new Promise((resolve) => {
    Speech.speak(text, {
      language: 'fr-FR',
      rate: 1.0,
      onDone: () => resolve(),
      onStopped: () => resolve(),
      onError: () => resolve(),
    });
  });
}
