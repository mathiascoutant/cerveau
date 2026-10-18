import { api, ApiError, Connaissance, EquipeInfo, EtatProjets, Membre } from '../api';
import { load, patch, peek, useSlot } from './cache';

/**
 * L'espace perso, côté app : l'équipe, qui a la parole, et l'état des projets.
 *
 * L'équipe se charge une fois au lancement : c'est elle qui dit de quelle
 * couleur peindre une réponse et quel nom mettre au-dessus. Le point sur les
 * projets se charge à l'ouverture de l'onglet Perso — il interroge le VPS et
 * GitHub, et personne ne veut payer ça à chaque démarrage.
 */

export const EQUIPE_KEY = 'equipe';
export const PROJETS_KEY = 'projets';
export const CONNAISSANCES_KEY = 'connaissances';

/** Raoul, tel qu'on l'affiche tant que l'équipe n'est pas chargée. */
export const RAOUL: Membre = {
  id: 'raoul',
  nom: 'Raoul',
  role: 'Assistant personnel',
  couleur: '#FFB547',
  presentation: '',
};

/**
 * Un serveur sans espace perso répond 501 : ce n'est pas une erreur à
 * afficher, c'est une absence. On la range comme une donnée vide.
 */
function orAbsent<T>(fetcher: () => Promise<T>): () => Promise<T | false> {
  return () =>
    fetcher().catch((err) => {
      if (err instanceof ApiError && (err.status === 501 || err.status === 404)) return false;
      throw err;
    });
}

export function loadEquipe(force = false) {
  return load<EquipeInfo | false>(EQUIPE_KEY, orAbsent(api.equipe), force);
}

export function loadProjets(force = false) {
  return load<EtatProjets | false>(PROJETS_KEY, orAbsent(() => api.projets(force)), force);
}

export function loadConnaissances(force = false) {
  return load<Connaissance[] | false>(CONNAISSANCES_KEY, orAbsent(() => api.connaissances().then((r) => r.connaissances)), force);
}

/** L'équipe chargée, ou null tant qu'elle ne l'est pas (ou n'existe pas). */
export function useEquipe(): EquipeInfo | null {
  const slot = useSlot<EquipeInfo | false>(EQUIPE_KEY);
  return slot.data || null;
}

/** La fiche d'un agent ; Raoul pour un identifiant vide ou inconnu. */
export function membre(equipe: EquipeInfo | null, id?: string): Membre {
  if (!id || id === 'raoul') return equipe?.membres.find((m) => m.id === 'raoul') ?? RAOUL;
  return equipe?.membres.find((m) => m.id === id) ?? { ...RAOUL, id, nom: id, role: '' };
}

/** Qui a la parole en ce moment, d'après ce que l'app sait. */
export function currentInterlocuteur(): string {
  const e = peek<EquipeInfo | false>(EQUIPE_KEY);
  return (e && e.interlocuteur) || 'raoul';
}

/**
 * Note qui a la parole sans repasser par le serveur : l'événement du flux ou la
 * fin d'une réponse le disent déjà.
 */
export function noteInterlocuteur(agent?: string) {
  patch<EquipeInfo | false>(EQUIPE_KEY, (e) => (e ? { ...e, interlocuteur: agent || 'raoul' } : e));
}

/** Donne la parole à un membre, depuis l'app. */
export async function parlerA(agent: string): Promise<void> {
  noteInterlocuteur(agent);
  try {
    await api.setInterlocuteur(agent);
  } catch (err) {
    await loadEquipe(true);
    throw err;
  }
}

export async function oublierConnaissance(id: string): Promise<void> {
  patch<Connaissance[] | false>(CONNAISSANCES_KEY, (list) => (list ? list.filter((c) => c.id !== id) : list));
  try {
    await api.oublierConnaissance(id);
  } catch (err) {
    await loadConnaissances(true);
    throw err;
  }
}
