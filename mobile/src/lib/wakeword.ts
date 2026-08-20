/**
 * Détection du mot d'activation « OK Raoul ».
 *
 * On travaille sur le texte produit par la reconnaissance vocale native, donc
 * il faut tolérer ce que le moteur français renvoie réellement : « ok raoul »,
 * « OK Raoule », « okay Raoul », « hey Raoul », « ok raul »…
 */

const WAKE_PATTERN =
  /\b(?:o\.?\s?k\.?|okay|okey|hey|h[ée]|eh)?\s*(?:raoul[ea]?|raoult|raul[ea]?|rahoul)\b/i;

export function normalize(text: string): string {
  return text
    .normalize('NFD')
    .replace(/[̀-ͯ]/g, '') // accents
    .toLowerCase();
}

export type WakeMatch = {
  /** Index, dans le texte d'origine, du premier caractère qui suit le mot d'activation. */
  endIndex: number;
};

/**
 * Cherche le mot d'activation. Renvoie null si absent, sinon la position juste
 * après — ce qui suit est la demande de l'utilisateur.
 */
export function findWake(text: string): WakeMatch | null {
  // La normalisation ne change ni la longueur ni les positions (NFD retire des
  // combinants, on retombe donc sur des index alignés pour du texte français
  // courant). On travaille par sécurité sur le texte normalisé pour la
  // recherche, et on borne l'index au texte d'origine.
  const haystack = normalize(text);
  const match = WAKE_PATTERN.exec(haystack);
  if (!match) return null;
  return { endIndex: Math.min(match.index + match[0].length, text.length) };
}

/** Vrai si la phrase contient le mot d'activation. */
export function hasWake(text: string): boolean {
  return findWake(text) !== null;
}

/** Nettoie la demande extraite après le mot d'activation. */
export function cleanCommand(text: string): string {
  return text.replace(/^[\s,.:;!?-]+/, '').trim();
}
