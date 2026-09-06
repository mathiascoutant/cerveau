import { useSyncExternalStore } from 'react';

/**
 * Un cache mémoire partagé entre les écrans.
 *
 * Il existe pour une raison précise : le Journal demande une synthèse au
 * modèle, ce qui prend plusieurs secondes. Tant qu'il ne se chargeait qu'à
 * l'ouverture de son onglet, chaque visite commençait par un écran d'attente,
 * alors que rien n'empêchait de la demander pendant qu'on regardait ailleurs.
 * Le chargement part donc dès l'ouverture de l'app, et l'onglet ne fait plus
 * que lire ce qui est déjà là.
 *
 * Volontairement minuscule — pas de bibliothèque d'état, pas d'invalidation
 * automatique, pas de nouvelle tentative. Quatre ressources à garder au chaud
 * ne justifient rien de plus.
 */

export type Slot<T> = {
  data: T | null;
  error: string | null;
  loading: boolean;
  /** Instant du dernier chargement réussi, en millisecondes. */
  at: number;
};

const EMPTY: Slot<never> = { data: null, error: null, loading: false, at: 0 };

const slots = new Map<string, Slot<unknown>>();
const subscribers = new Map<string, Set<() => void>>();
const inflight = new Map<string, Promise<void>>();

function write<T>(key: string, patch: Partial<Slot<T>>) {
  const next = { ...(slots.get(key) ?? EMPTY), ...patch } as Slot<unknown>;
  slots.set(key, next);
  subscribers.get(key)?.forEach((notify) => notify());
}

/**
 * Charge une ressource si elle n'est pas déjà là.
 *
 * Les appels concurrents partagent la même requête : l'app peut demander la
 * même chose au lancement et à l'affichage de l'écran sans la payer deux fois.
 * `force` relance quoi qu'il arrive — c'est le geste « tirer pour rafraîchir ».
 */
export function load<T>(key: string, fetcher: () => Promise<T>, force = false): Promise<void> {
  const current = slots.get(key);
  const running = inflight.get(key);
  if (running && !force) return running;
  if (!force && current?.data != null) return Promise.resolve();

  write<T>(key, { loading: true, error: null });
  const run = fetcher()
    .then((data) => write<T>(key, { data, error: null, loading: false, at: Date.now() }))
    .catch((err: Error) => write<T>(key, { error: err.message, loading: false }))
    .finally(() => {
      if (inflight.get(key) === run) inflight.delete(key);
    });
  inflight.set(key, run);
  return run;
}

/**
 * Réécrit une ressource déjà chargée, sans passer par le réseau.
 *
 * Sert aux gestes qui doivent répondre sous le doigt — cocher une tâche : on
 * écrit tout de suite le résultat attendu, on envoie au serveur derrière, et on
 * recharge s'il n'est pas d'accord. Sans ça, la case attend l'aller-retour pour
 * se remplir, et une liste qui répond en 300 ms passe pour cassée.
 *
 * Ne crée rien : une ressource jamais chargée n'a rien à corriger.
 */
export function patch<T>(key: string, edit: (current: T) => T): void {
  const slot = slots.get(key);
  if (!slot || slot.data == null) return;
  write<T>(key, { data: edit(slot.data as T) });
}

/**
 * Dit si une ressource mérite d'être redemandée.
 *
 * Sert au retour au premier plan : une liste vieille d'une heure a de bonnes
 * chances d'être fausse, une liste vieille de trente secondes n'en a aucune, et
 * tout recharger à chaque bascule d'application coûterait un appel au modèle
 * pour rien.
 */
export function isStale(key: string, maxAge: number): boolean {
  const slot = slots.get(key);
  if (!slot || slot.data == null) return true;
  return Date.now() - slot.at > maxAge;
}

/** Vide tout : appelé quand on change de serveur, donc d'utilisateur. */
export function clearCache() {
  const keys = [...slots.keys()];
  slots.clear();
  inflight.clear();
  keys.forEach((key) => subscribers.get(key)?.forEach((notify) => notify()));
}

/** Lit une ressource et se réabonne à ses changements. */
export function useSlot<T>(key: string): Slot<T> {
  return useSyncExternalStore(
    (notify) => {
      const set = subscribers.get(key) ?? new Set();
      set.add(notify);
      subscribers.set(key, set);
      return () => set.delete(notify);
    },
    // L'objet n'est remplacé qu'au changement réel : React peut comparer les
    // instantanés par identité sans boucler.
    () => (slots.get(key) ?? EMPTY) as Slot<T>,
  );
}
