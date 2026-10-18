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
export function speakOnDevice(text: string, lang?: string, agent?: string): Promise<void> {
  const locale = localeFor(lang);
  const timbre = timbreOf(agent);
  return new Promise((resolve) => {
    // La voix française choisie avec soin ne sert qu'au français : lui faire
    // lire de l'anglais donne un accent qui rend le texte incompréhensible.
    void (locale === 'fr-FR' ? voiceFor(agent) : Promise.resolve(null)).then((choice) => {
      if (stopped) {
        resolve();
        return;
      }
      Speech.speak(text, {
        language: locale,
        voice: choice?.id ?? undefined,
        // À vitesse nominale, les voix iOS enchaînent les mots sans respiration.
        rate: timbre.rate,
        // Même voix qu'un collègue faute de mieux sur l'appareil : un ton
        // plus grave suffit à savoir qui parle.
        pitch: choice?.shared ? timbre.pitch - 0.08 : timbre.pitch,
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
 *
 * `prepared` est l'adresse d'un son dont le serveur a déjà lancé la fabrication
 * — c'est ce que rend /assistant/ask avec la réponse. La donner évite l'aller-
 * retour qui servait à réclamer un ticket, et surtout laisse ElevenLabs
 * travailler pendant que la réponse arrive jusqu'ici. Sans elle, on demande le
 * ticket comme avant : une phrase fabriquée par l'app n'en a pas.
 */
export async function speak(text: string, lang?: string, prepared?: string, agent?: string): Promise<void> {
  stopped = false;
  if (!text.trim()) return;

  if (remoteAvailable) {
    try {
      const path = prepared ?? (await api.speak(text)).url;
      if (stopped) return;
      const base = await getApiUrl();
      await prepareAudioMode();
      if (stopped) return;
      if (await play(base + path)) {
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

  await speakOnDevice(text, lang, agent);
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

/* -- Voix système : une par membre de l'équipe ---------------------------- */

/**
 * Les voix gratuites de l'iPhone, distribuées entre Raoul, Claire et Léo : on
 * doit savoir qui parle sans regarder l'écran. Chacun a ses prénoms de voix
 * préférés, son genre, son ton et son débit ; on prend la meilleure voix
 * installée qui lui correspond, et Léo évite celle de Raoul quand l'appareil
 * en propose une autre.
 */
type Timbre = { names: string[]; gender: 'male' | 'female'; pitch: number; rate: number };

const TIMBRES: Record<string, Timbre> = {
  raoul: { names: ['thomas', 'nicolas', 'daniel', 'jacques'], gender: 'male', pitch: 0.98, rate: 0.96 },
  responsable_info: {
    names: ['audrey', 'aurélie', 'aurelie', 'marie', 'amélie', 'amelie', 'céline', 'celine', 'virginie'],
    gender: 'female',
    pitch: 1.04,
    rate: 0.98,
  },
  ingenieur: { names: ['nicolas', 'daniel', 'jacques', 'henri', 'thomas'], gender: 'male', pitch: 0.94, rate: 1.0 },
};

function timbreOf(agent?: string): Timbre {
  return TIMBRES[agent ?? 'raoul'] ?? TIMBRES.raoul;
}

const FEMALE = ['audrey', 'aurélie', 'aurelie', 'marie', 'amélie', 'amelie', 'céline', 'celine', 'virginie', 'chantal', 'julie'];
const MALE = ['thomas', 'nicolas', 'daniel', 'jacques', 'henri'];
// Voix fantaisie (« Grandma », « Rocko »…) : présentes en français, mais on
// ne confie pas une réponse sérieuse à un grand-père de dessin animé.
const NOVELTY = ['eddy', 'flo', 'grandma', 'grandpa', 'reed', 'rocko', 'sandy', 'shelley', 'grand-mère', 'grand-père'];

function genderOf(voice: Speech.Voice): 'male' | 'female' | null {
  const id = voice.identifier?.toLowerCase() ?? '';
  const name = voice.name?.toLowerCase() ?? '';
  if (id.includes('female')) return 'female';
  if (id.includes('male')) return 'male';
  if (FEMALE.some((n) => name.includes(n))) return 'female';
  if (MALE.some((n) => name.includes(n))) return 'male';
  return null;
}

function score(voice: Speech.Voice, timbre: Timbre): number {
  const name = voice.name?.toLowerCase() ?? '';
  const id = voice.identifier?.toLowerCase() ?? '';
  let n = 0;

  // Premium > Enhanced > compacte. « premium » n'est pas dans l'énumération
  // d'expo-speech mais apparaît dans l'identifiant iOS.
  if (id.includes('premium')) n += 100;
  else if (voice.quality === Speech.VoiceQuality.Enhanced) n += 60;
  if (id.includes('siri')) n += 40; // les voix Siri sont les plus naturelles

  const gender = genderOf(voice);
  if (gender === timbre.gender) n += 50;
  else if (gender) n -= 50;

  const rank = timbre.names.findIndex((p) => name.includes(p));
  if (rank >= 0) n += 20 - rank;

  // Le français de France plutôt que canadien ou belge.
  if (voice.language === 'fr-FR') n += 10;
  return n;
}

type Choice = { id: string; shared: boolean } | null;
const chosen = new Map<string, Choice>();
let frenchCache: Speech.Voice[] | null = null;

async function usableFrenchVoices(): Promise<Speech.Voice[]> {
  if (frenchCache) return frenchCache;
  const voices = await Speech.getAvailableVoicesAsync();
  frenchCache = voices.filter((v) => {
    const name = v.name?.toLowerCase() ?? '';
    const id = v.identifier?.toLowerCase() ?? '';
    return v.language?.toLowerCase().startsWith('fr') && !id.includes('eloquence') && !NOVELTY.some((n) => name === n || name.startsWith(n + ' '));
  });
  return frenchCache;
}

/** La voix d'un membre de l'équipe, choisie une fois par session. */
async function voiceFor(agent?: string): Promise<Choice> {
  const key = agent && TIMBRES[agent] ? agent : 'raoul';
  if (chosen.has(key)) return chosen.get(key) ?? null;
  let choice: Choice = null;
  try {
    const french = await usableFrenchVoices();
    const timbre = TIMBRES[key];
    const ranked = [...french].sort((a, b) => score(b, timbre) - score(a, timbre));
    // Léo ne prend pas la voix de Raoul s'il en existe une autre de son genre.
    const taken = key === 'ingenieur' ? (await voiceFor('raoul'))?.id : undefined;
    const own = ranked.find((v) => v.identifier !== taken && genderOf(v) !== (timbre.gender === 'male' ? 'female' : 'male'));
    if (own) choice = { id: own.identifier, shared: false };
    else if (ranked[0]) choice = { id: ranked[0].identifier, shared: ranked[0].identifier === taken };
  } catch {
    choice = null;
  }
  chosen.set(key, choice);
  return choice;
}

/** Qui parle avec quelle voix de l'iPhone — pour l'écran Accès. */
export async function teamVoices(): Promise<{ agent: string; who: string; voice: string | null; shared: boolean }[]> {
  const french = await usableFrenchVoices();
  const nameOf = (id?: string) => french.find((v) => v.identifier === id)?.name ?? null;
  const out = [];
  for (const [agent, who] of [
    ['raoul', 'Raoul'],
    ['responsable_info', 'Claire'],
    ['ingenieur', 'Léo'],
  ] as const) {
    const choice = await voiceFor(agent);
    out.push({ agent, who, voice: nameOf(choice?.id), shared: Boolean(choice?.shared) });
  }
  return out;
}

/** Diagnostic : liste ce que l'appareil propose réellement en français. */
export async function frenchVoices(): Promise<Speech.Voice[]> {
  const french = await usableFrenchVoices();
  return [...french].sort((a, b) => score(b, TIMBRES.raoul) - score(a, TIMBRES.raoul));
}
