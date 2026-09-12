// « /legacy » et non « expo-calendar » tout court : depuis le SDK 56, l'entrée
// principale du module a basculé sur une API à objets, et les fonctions *Async
// qui restent sous ce nom ne sont plus que des souches qui lèvent à l'appel.
// Elles compilent toujours — c'est ce qui rend le piège discret : ici, la sonde
// ci-dessous les aurait attrapées et l'app serait passée en mode « pas
// d'agenda » sans rien dire. Le jour où l'on migre vers l'API à objets, c'est
// tout ce fichier qui change ; en attendant, la ligne de repli est explicite.
import * as Calendar from 'expo-calendar/legacy';

import { api, AssistantAction } from '../api';
import { openNavigation } from './navigation';
import { loadTodos } from './todos';

/** Fenêtre synchronisée vers le backend : hier → 45 jours. */
const WINDOW_PAST_DAYS = 1;
const WINDOW_FUTURE_DAYS = 45;

/**
 * Le module calendrier n'est pas garanti présent partout (selon le SDK, Expo Go
 * embarque ou non l'implémentation native). On sonde une fois et on mémorise,
 * pour que l'app bascule proprement en mode dégradé au lieu de planter.
 */
let supported: boolean | null = null;

export async function calendarSupported(): Promise<boolean> {
  if (supported !== null) return supported;
  try {
    await Calendar.getCalendarPermissionsAsync();
    supported = true;
  } catch {
    supported = false;
  }
  return supported;
}

export async function requestCalendarAccess(): Promise<boolean> {
  if (!(await calendarSupported())) {
    throw new Error(
      "Le calendrier n'est pas accessible dans cet environnement. Il faut un dev build (eas build --profile development).",
    );
  }
  const { status } = await Calendar.requestCalendarPermissionsAsync();
  return status === 'granted';
}

export async function hasCalendarAccess(): Promise<boolean> {
  if (!(await calendarSupported())) return false;
  const { status } = await Calendar.getCalendarPermissionsAsync();
  return status === 'granted';
}

/**
 * Pousse le calendrier du téléphone vers le backend.
 *
 * C'est l'app qui lit EventKit et envoie le miroir : ça évite tout OAuth
 * calendrier et couvre d'un coup TOUS les comptes agrégés sur l'iPhone
 * (iCloud, Google, Exchange…), pro comme perso.
 */
export async function syncCalendar(): Promise<number> {
  if (!(await hasCalendarAccess())) return 0;

  const calendars = await Calendar.getCalendarsAsync(Calendar.EntityTypes.EVENT);
  if (calendars.length === 0) return 0;

  const from = new Date();
  from.setDate(from.getDate() - WINDOW_PAST_DAYS);
  const to = new Date();
  to.setDate(to.getDate() + WINDOW_FUTURE_DAYS);

  const names = new Map(calendars.map((c) => [c.id, c.title]));
  const events = await Calendar.getEventsAsync(
    calendars.map((c) => c.id),
    from,
    to,
  );

  const payload = events.map((e) => ({
    id: String(e.id),
    calendar: names.get(String(e.calendarId)) ?? undefined,
    title: e.title ?? '(sans titre)',
    location: e.location ?? undefined,
    start: new Date(e.startDate).toISOString(),
    end: new Date(e.endDate).toISOString(),
    all_day: Boolean(e.allDay),
  }));

  const res = await api.syncCalendar({
    from: from.toISOString(),
    to: to.toISOString(),
    events: payload,
  });
  return res.synced;
}

/** Calendrier par défaut où Raoul écrit ses événements. */
async function writableCalendarId(): Promise<string | null> {
  try {
    const preferred = await Calendar.getDefaultCalendarAsync();
    if (preferred?.id && preferred.allowsModifications) return preferred.id;
  } catch {
    // getDefaultCalendarAsync est iOS-only et peut lever : on cherche à la main.
  }
  const calendars = await Calendar.getCalendarsAsync(Calendar.EntityTypes.EVENT);
  return calendars.find((c) => c.allowsModifications)?.id ?? null;
}

/**
 * Met en mots ce que Raoul vient de faire de sa liste. Le libellé porte le jour
 * retenu : « c'est noté » sans date ne dit pas ce qui a été inscrit.
 */
function todoEffect(action: AssistantAction): string {
  const title = action.payload?.title ? `« ${action.payload.title} »` : 'La tâche';
  const when = action.payload?.when ? ` pour ${action.payload.when}` : '';
  switch (action.payload?.state) {
    case 'done':
      return `${title} coché dans ta liste.`;
    case 'moved':
      return `${title} déplacé${when}.`;
    case 'dropped':
      return `${title} retiré de ta liste.`;
    default:
      return `${title} ajouté à ta liste${when || ', sans date'}.`;
  }
}

/**
 * Exécute les actions décidées par Raoul côté serveur : écrire un événement
 * dans EventKit, ouvrir Waze. Ce sont des choses que seule l'app peut faire,
 * d'où l'aller-retour.
 */
export async function applyActions(actions: AssistantAction[]): Promise<string[]> {
  const done: string[] = [];
  let touchedCalendar = false;
  let touchedTodos = false;

  for (const action of actions) {
    if (action.type === 'navigate') {
      done.push(await openNavigation(action));
      continue;
    }
    // Rien à exécuter sur le téléphone : la réponse est déjà rangée côté
    // serveur. On le signale seulement, pour que l'échange porte la trace du
    // mail qui attend dans l'onglet Réponses.
    // Une tâche a bougé côté serveur : rien à écrire sur le téléphone, mais la
    // section « À faire » doit refléter tout de suite ce qu'il vient de dicter.
    if (action.type === 'todo') {
      done.push(todoEffect(action));
      touchedTodos = true;
      continue;
    }
    if (action.type === 'email_draft') {
      const to = action.payload?.to ? String(action.payload.to) : 'ton correspondant';
      done.push(`Réponse à ${to} prête dans l’onglet Réponses.`);
      continue;
    }
    if (action.type !== 'create_event') continue;

    if (!(await hasCalendarAccess())) {
      done.push("Créneau validé, mais l'accès au calendrier manque pour l'y écrire.");
      continue;
    }

    const calendarId = await writableCalendarId();
    if (!calendarId) {
      done.push("Impossible d'écrire : aucun calendrier modifiable sur l'appareil.");
      continue;
    }

    const { title, start, end, location, notes } = action.payload;
    try {
      await Calendar.createEventAsync(calendarId, {
        title: String(title ?? 'Rendez-vous'),
        startDate: new Date(String(start)),
        endDate: new Date(String(end)),
        location: location ? String(location) : undefined,
        notes: notes ? String(notes) : 'Ajouté par Raoul',
        timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone,
      });
      done.push(`« ${title} » ajouté au calendrier.`);
      touchedCalendar = true;
    } catch (err) {
      done.push(`Échec de l'ajout au calendrier : ${(err as Error).message}`);
    }
  }

  if (touchedTodos) {
    try {
      await loadTodos(true);
    } catch {
      // la liste se rechargera au prochain retour au premier plan
    }
  }

  // Le miroir serveur doit refléter tout de suite ce qu'on vient d'écrire.
  if (touchedCalendar) {
    try {
      await syncCalendar();
    } catch {
      // la resynchro repassera au prochain retour au premier plan
    }
  }

  return done;
}
