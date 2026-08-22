import { AudioPlayer, createAudioPlayer, setAudioModeAsync } from 'expo-audio';
import * as Speech from 'expo-speech';

import { ApiError, api, getApiUrl } from '../api';

/**
 * La voix de Raoul.
 *
 * Par défaut elle vient d'ElevenLabs, via le backend : le téléphone poste le
 * texte, reçoit une URL jetable et joue le flux audio pendant qu'il se
 * fabrique. C'est ce qui donne une diction qui respire, là où la synthèse iOS
 * pose chaque mot au même rythme.
 *
 * La voix système reste le filet : serveur sans clé ElevenLabs, réseau coupé,
 * quota dépassé — Raoul parle quand même, moins bien.
 */

/** Au-delà, on considère que le flux ne viendra pas et on prend le relais. */
const LOAD_TIMEOUT_MS = 6000;
/** Garde-fou de fin de lecture, ajouté à la durée annoncée du son. */
const PLAYBACK_GRACE_MS = 5000;

let remoteAvailable = true;
let player: AudioPlayer | null = null;
let stopped = false;

/**
 * Réglé avant CHAQUE lecture, jamais mis en cache : entre deux réponses, la
 * reconnaissance vocale reprend la main sur la session audio et la repasse en
 * « playAndRecord / measurement », un mode qui sort un son faible et sourd.
 */
async function prepareAudioMode(): Promise<void> {
  await setAudioModeAsync({
    // Sans ça, Raoul est muet quand le petit interrupteur latéral est sur
    // silencieux — c'est-à-dire la moitié du temps.
    playsInSilentMode: true,
    // La reconnaissance vocale est arrêtée avant qu'il parle : on rend la
    // session audio à la lecture seule, qui sonne plus fort et plus propre.
    allowsRecording: false,
    // Il finit sa phrase quand une action fait passer l'app en arrière-plan
    // (ouverture de Waze). UIBackgroundModes « audio » est déclaré dans app.json.
    shouldPlayInBackground: true,
    interruptionMode: 'duckOthers',
  });
}

/** Joue un flux distant et se résout à la fin, ou `false` si rien n'est sorti. */
function play(uri: string): Promise<boolean> {
  return new Promise((resolve) => {
    let settled = false;
    let started = false;
    let loadTimer: ReturnType<typeof setTimeout> | null = null;
    let endTimer: ReturnType<typeof setTimeout> | null = null;

    const finish = (ok: boolean) => {
      if (settled) return;
      settled = true;
      if (loadTimer) clearTimeout(loadTimer);
      if (endTimer) clearTimeout(endTimer);
      sub?.remove();
      current?.remove();
      if (player === current) player = null;
      resolve(ok);
    };

    let current: AudioPlayer;
    try {
      current = createAudioPlayer({ uri });
    } catch {
      resolve(false);
      return;
    }
    player = current;

    const sub = current.addListener('playbackStatusUpdate', (status) => {
      if (status.isLoaded && !started) {
        started = true;
        if (loadTimer) clearTimeout(loadTimer);
        // La durée n'est pas connue avant le chargement ; une fois connue, elle
        // borne l'attente si l'événement de fin ne venait jamais.
        const ms = (status.duration > 0 ? status.duration * 1000 : 60000) + PLAYBACK_GRACE_MS;
        endTimer = setTimeout(() => finish(true), ms);
      }
      if (status.didJustFinish) finish(true);
    });

    // Un flux qui n'a pas commencé à charger en six secondes ne chargera pas :
    // on rend la main à la voix système plutôt que de laisser un blanc.
    loadTimer = setTimeout(() => finish(false), LOAD_TIMEOUT_MS);

    try {
      current.play();
    } catch {
      finish(false);
    }
  });
}

/**
 * Voix système : le repli, mais aussi le bon choix quand on sait déjà que le
 * serveur ne répond pas — inutile de lui demander de synthétiser la phrase qui
 * annonce qu'il est injoignable.
 */
export function speakOnDevice(text: string, lang?: string): Promise<void> {
  const locale = localeFor(lang);
  return new Promise((resolve) => {
    // La voix française choisie avec soin ne sert qu'au français : lui faire
    // lire de l'anglais donne un accent qui rend le texte incompréhensible.
    void (locale === 'fr-FR' ? bestFrenchVoice() : Promise.resolve(null)).then((voice) => {
      if (stopped) {
        resolve();
        return;
      }
      Speech.speak(text, {
        language: locale,
        voice: voice ?? undefined,
        // À vitesse nominale, les voix iOS enchaînent les mots sans respiration.
        rate: 0.96,
        pitch: 0.98,
        onDone: () => resolve(),
        onStopped: () => resolve(),
        onError: () => resolve(),
      });
    });
  });
}

/**
 * Dernière raison pour laquelle la voix ElevenLabs a été écartée.
 *
 * Le repli sur la voix système est silencieux par construction — Raoul parle
 * quand même — ce qui rend la panne invisible : on croit entendre un réglage
 * alors qu'on entend un échec. La raison est donc gardée ici et journalisée.
 */
let lastIssue: string | null = null;

export function lastVoiceIssue(): string | null {
  return lastIssue;
}

function fallback(reason: string): void {
  lastIssue = reason;
  console.warn(`[voix] repli sur la voix système : ${reason}`);
}

/**
 * Lit un texte à voix haute et se résout quand la lecture est terminée.
 *
 * `lang` ne concerne QUE la voix système : le modèle ElevenLabs est
 * multilingue et reconnaît de lui-même la langue du texte, ce qui compte quand
 * Raoul lit une réponse de mail rédigée en anglais au milieu d'une phrase
 * française.
 */
export async function speak(text: string, lang?: string): Promise<void> {
  stopped = false;
  if (!text.trim()) return;

  if (remoteAvailable) {
    try {
      const ticket = await api.speak(text);
      if (stopped) return;
      const base = await getApiUrl();
      await prepareAudioMode();
      if (stopped) return;
      if (await play(base + ticket.url)) {
        lastIssue = null;
        return;
      }
      fallback("le lecteur audio n'a pas réussi à jouer le flux");
    } catch (err) {
      // 501 : le serveur n'a pas de clé ElevenLabs. Inutile de retenter à
      // chaque phrase, on bascule sur la voix système pour la session.
      if (err instanceof ApiError && err.status === 501) {
        remoteAvailable = false;
        fallback('le serveur n’a pas de clé ElevenLabs (501)');
      } else {
        fallback((err as Error).message);
      }
    }
    if (stopped) return;
  }

  await speakOnDevice(text, lang);
}

/**
 * Traduit un code court de langue en identifiant que la synthèse iOS accepte.
 * Tout ce qu'on ne reconnaît pas retombe sur le français : c'est la langue de
 * Raoul, et une locale inconnue rendrait la voix muette.
 */
function localeFor(lang?: string): string {
  switch (lang?.slice(0, 2).toLowerCase()) {
    case 'en':
      return 'en-US';
    case 'es':
      return 'es-ES';
    case 'de':
      return 'de-DE';
    case 'it':
      return 'it-IT';
    case 'pt':
      return 'pt-PT';
    case 'nl':
      return 'nl-NL';
    default:
      return 'fr-FR';
  }
}

export function stopSpeaking(): void {
  stopped = true;
  Speech.stop();
  try {
    player?.remove();
  } catch {
    // le lecteur peut déjà avoir été libéré à la fin de la lecture
  }
  player = null;
}

/* -- Voix système : sélection de la moins mauvaise ------------------------- */

let selected: string | null | undefined;

/** Voix connues pour bien rendre en français, par ordre de préférence. */
const PREFERRED_NAMES = ['thomas', 'audrey', 'aurélie', 'aurelie', 'marie', 'daniel'];

function score(voice: Speech.Voice): number {
  const name = voice.name?.toLowerCase() ?? '';
  const id = voice.identifier?.toLowerCase() ?? '';
  let n = 0;

  // Premium > Enhanced > compacte. « premium » n'est pas dans l'énumération
  // d'expo-speech mais apparaît dans l'identifiant iOS.
  if (id.includes('premium')) n += 100;
  else if (voice.quality === Speech.VoiceQuality.Enhanced) n += 60;
  if (id.includes('siri')) n += 40; // les voix Siri sont les plus naturelles

  const rank = PREFERRED_NAMES.findIndex((p) => name.includes(p));
  if (rank >= 0) n += 20 - rank;

  // Le français de France plutôt que canadien ou belge.
  if (voice.language === 'fr-FR') n += 10;
  return n;
}

async function bestFrenchVoice(): Promise<string | null> {
  if (selected !== undefined) return selected ?? null;
  try {
    const voices = await Speech.getAvailableVoicesAsync();
    const french = voices.filter((v) => v.language?.toLowerCase().startsWith('fr'));
    if (french.length === 0) {
      selected = null;
      return null;
    }
    french.sort((a, b) => score(b) - score(a));
    selected = french[0].identifier;
  } catch {
    selected = null;
  }
  return selected ?? null;
}

/** Diagnostic : liste ce que l'appareil propose réellement en français. */
export async function frenchVoices(): Promise<Speech.Voice[]> {
  const voices = await Speech.getAvailableVoicesAsync();
  return voices
    .filter((v) => v.language?.toLowerCase().startsWith('fr'))
    .sort((a, b) => score(b) - score(a));
}
