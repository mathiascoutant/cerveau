import { api, ApiError, Entourage, Personne, PersonneEdit } from '../api';
import { load, patch } from './cache';

/**
 * Son entourage pro, côté app : les gens que Raoul connaît, le reste de ce
 * qu'il retient, et ce qu'il a lu récemment. Chargé à l'ouverture de l'écran,
 * corrigé sur place : une fiche modifiée remplace l'ancienne dans le cache sans
 * attendre de recharger.
 */

export const ENTOURAGE_KEY = 'entourage';

export function loadEntourage(force = false) {
  return load<Entourage>(
    ENTOURAGE_KEY,
    () =>
      api.entourage().catch((err) => {
        // Un serveur d'avant l'entourage ne connaît pas la route : ce n'est
        // pas une panne, c'est une version à déployer.
        if (err instanceof ApiError && err.status === 404) {
          throw new Error('Ton serveur ne connaît pas encore l’entourage : déploie la dernière version du backend.');
        }
        throw err;
      }),
    force,
  );
}

function replace(list: Personne[], updated: Personne): Personne[] {
  const at = list.findIndex((p) => p.id === updated.id);
  if (at < 0) return [updated, ...list];
  const next = list.slice();
  next[at] = updated;
  return next;
}

export async function ajouterPersonne(edit: PersonneEdit): Promise<Personne> {
  const saved = await api.ajouterPersonne(edit);
  patch<Entourage>(ENTOURAGE_KEY, (e) => ({ ...e, personnes: replace(e.personnes, saved) }));
  return saved;
}

export async function modifierPersonne(id: string, edit: PersonneEdit): Promise<Personne> {
  const saved = await api.modifierPersonne(id, edit);
  patch<Entourage>(ENTOURAGE_KEY, (e) => ({ ...e, personnes: replace(e.personnes, saved) }));
  return saved;
}

export async function oublierFiche(id: string): Promise<void> {
  await api.oublierFiche(id);
  patch<Entourage>(ENTOURAGE_KEY, (e) => ({
    ...e,
    personnes: e.personnes.filter((p) => p.id !== id),
    autres: e.autres.filter((p) => p.id !== id),
  }));
}
