/**
 * Chargement optionnel du moteur de reconnaissance vocale natif.
 *
 * `expo-speech-recognition` est un module natif tiers : il n'existe pas dans
 * Expo Go, et son import lève immédiatement (`requireNativeModule` échoue).
 * On l'isole ici pour que l'app reste utilisable en mode texte dans Expo Go,
 * et bascule automatiquement en vocal dès qu'on tourne sur un dev build.
 */
import type {
  ExpoSpeechRecognitionNativeEventMap,
  ExpoSpeechRecognitionOptions,
} from 'expo-speech-recognition';

type SpeechRecognitionModule = typeof import('expo-speech-recognition');

let engine: SpeechRecognitionModule | null = null;
let failure: string | null = null;

try {
  engine = require('expo-speech-recognition') as SpeechRecognitionModule;
} catch (err) {
  failure = (err as Error)?.message ?? 'module natif indisponible';
}

/** Vrai uniquement sur un dev build / TestFlight, jamais dans Expo Go. */
export const voiceAvailable = engine !== null;
export const voiceUnavailableReason = failure;

export const SpeechRecognition = engine?.ExpoSpeechRecognitionModule ?? null;

/**
 * Variante de `useSpeechRecognitionEvent` qui ne fait rien si le moteur est
 * absent. `engine` est fixé au chargement du module, donc le nombre de hooks
 * appelés reste constant sur toute la durée de vie de l'app — l'ordre des
 * hooks React est préservé.
 */
export function useSpeechEvent<K extends keyof ExpoSpeechRecognitionNativeEventMap>(
  eventName: K,
  listener: (event: ExpoSpeechRecognitionNativeEventMap[K]) => void,
): void {
  if (!engine) return;
  // Le hook amont est typé par intersection de tous les handlers possibles ;
  // notre signature générique est plus stricte mais équivalente à l'usage.
  (engine.useSpeechRecognitionEvent as (name: K, cb: typeof listener) => void)(
    eventName,
    listener,
  );
}

export type RecognitionOptions = ExpoSpeechRecognitionOptions;
