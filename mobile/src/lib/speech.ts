import * as Speech from 'expo-speech';

/**
 * Choix de la voix de Raoul.
 *
 * iOS embarque par défaut une voix française « compacte », très synthétique.
 * Les voix Enhanced et Premium sonnent nettement plus naturelles, mais elles ne
 * sont présentes que si l'utilisateur les a téléchargées dans Réglages →
 * Accessibilité → Contenu énoncé → Voix. On prend donc la meilleure disponible
 * plutôt que d'imposer un identifiant qui n'existerait pas.
 */

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

/**
 * Lit un texte à voix haute et se résout quand la lecture est terminée.
 *
 * Le débit est très légèrement ralenti et la hauteur à peine abaissée : à
 * vitesse nominale, les voix iOS enchaînent les mots sans respiration, ce qui
 * accentue l'effet machine.
 */
export async function speak(text: string): Promise<void> {
  const voice = await bestFrenchVoice();
  return new Promise((resolve) => {
    Speech.speak(text, {
      language: 'fr-FR',
      voice: voice ?? undefined,
      rate: 0.96,
      pitch: 0.98,
      onDone: () => resolve(),
      onStopped: () => resolve(),
      onError: () => resolve(),
    });
  });
}

export function stopSpeaking(): void {
  Speech.stop();
}

/** Diagnostic : liste ce que l'appareil propose réellement en français. */
export async function frenchVoices(): Promise<Speech.Voice[]> {
  const voices = await Speech.getAvailableVoicesAsync();
  return voices
    .filter((v) => v.language?.toLowerCase().startsWith('fr'))
    .sort((a, b) => score(b) - score(a));
}
