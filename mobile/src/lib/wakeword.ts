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

/**
 * Formule de sortie : « merci Raoul », et ses cousines.
 *
 * Elle referme la conversation, qui reste sinon ouverte tant que l'écoute
 * tourne. Le prénom est exigé : « merci » tout seul revient trop souvent au
 * milieu d'une phrase dictée pour servir de commande.
 */
const FAREWELL_PATTERN =
  /\b(?:merci(?:\s+beaucoup)?|c'?est\s+bon|stop|termin[ée])\s+(?:raoul[ea]?|raoult|raul[ea]?|rahoul)\b/i;

export type FarewellMatch = {
  /** Index, dans le texte d'origine, du premier caractère de la formule. */
  startIndex: number;
};

/**
 * Cherche la formule de sortie. Ce qui la précède reste une demande à traiter :
 * « qu'est-ce que j'ai raté, merci Raoul » vaut une question ET un au revoir.
 */
export function findFarewell(text: string): FarewellMatch | null {
  const match = FAREWELL_PATTERN.exec(normalize(text));
  if (!match) return null;
  return { startIndex: Math.min(match.index, text.length) };
}

/** Nettoie la demande extraite après le mot d'activation. */
export function cleanCommand(text: string): string {
  return text.replace(/^[\s,.:;!?-]+/, '').trim();
}
