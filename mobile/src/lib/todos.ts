import { api, Todo } from '../api';
import { load, patch } from './cache';

/**
 * La liste à faire, côté app.
 *
 * Elle ne ressemble à la liste « à traiter » qu'en apparence. Celle-ci est
 * déduite des messages non traités et se recalcule ; celle-là est écrite par
 * lui — dictée à Raoul, ou versée d'un geste depuis une urgence — et reste
 * jusqu'à ce qu'elle soit cochée. C'est la différence entre ce qui arrive et ce
 * qu'on a décidé de faire.
 *
 * Toutes les modifications sont optimistes : la case se remplit sous le doigt,
 * le serveur suit, et un échec relit la vérité plutôt que de laisser un état
 * inventé à l'écran.
 */

export const TODOS_KEY = 'todos';

export function loadTodos(force = false) {
  return load<Todo[]>(TODOS_KEY, () => api.todos().then((r) => r.todos), force);
}

/** Coche ou décoche. */
export async function toggleTodo(todo: Todo): Promise<void> {
  const done = !todo.done;
  patch<Todo[]>(TODOS_KEY, (list) =>
    list.map((t) => (t.id === todo.id ? { ...t, done, done_at: done ? new Date().toISOString() : undefined } : t)),
  );
  try {
    await api.updateTodo(todo.id, { done });
  } catch {
    await loadTodos(true);
  }
}

/** Déplace une tâche. `due` vide la remet sans date. */
export async function scheduleTodo(todo: Todo, due: string): Promise<void> {
  patch<Todo[]>(TODOS_KEY, (list) =>
    list.map((t) => (t.id === todo.id ? { ...t, due: due || undefined, timed: false } : t)),
  );
  try {
    await api.updateTodo(todo.id, { due });
  } finally {
    await loadTodos(true);
  }
}

export async function removeTodo(todo: Todo): Promise<void> {
  patch<Todo[]>(TODOS_KEY, (list) => list.filter((t) => t.id !== todo.id));
  try {
    await api.deleteTodo(todo.id);
  } catch {
    await loadTodos(true);
  }
}

/** Inscrit une tâche depuis l'app (le versement d'une urgence dans la liste). */
export async function addTodo(input: {
  title: string;
  note?: string;
  due?: string;
  source?: { origine?: string; de?: string; titre?: string };
}): Promise<void> {
  await api.addTodo(input);
  await loadTodos(true);
}

/* -------------------------------------------------------------------------- */
/* Dates                                                                       */
/* -------------------------------------------------------------------------- */

const MS_PER_DAY = 86400000;

function startOfDay(d: Date): Date {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate());
}

/**
 * Écart en jours de calendrier. L'arrondi n'est pas une approximation : les
 * jours de changement d'heure durent 23 ou 25 heures, et une division sèche
 * les compterait pour zéro ou deux.
 */
function dayDiff(a: Date, b: Date): number {
  return Math.round((startOfDay(a).getTime() - startOfDay(b).getTime()) / MS_PER_DAY);
}

/** Jour ISO décalé de `offset` jours — ce que le serveur attend dans `due`. */
export function isoDay(offset = 0): string {
  const d = startOfDay(new Date());
  d.setDate(d.getDate() + offset);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

const WEEKDAYS = ['Dimanche', 'Lundi', 'Mardi', 'Mercredi', 'Jeudi', 'Vendredi', 'Samedi'];
const MONTHS = [
  'janvier', 'février', 'mars', 'avril', 'mai', 'juin',
  'juillet', 'août', 'septembre', 'octobre', 'novembre', 'décembre',
];

/** Nom du jour, tel qu'on en parle : « Demain », « Jeudi », « Le 10 septembre ». */
export function dayLabel(iso: string, now = new Date()): string {
  const d = new Date(iso);
  const diff = dayDiff(d, now);
  if (diff === 0) return 'Aujourd’hui';
  if (diff === 1) return 'Demain';
  if (diff === -1) return 'Hier';
  if (diff > 1 && diff < 7) return WEEKDAYS[d.getDay()];
  const year = d.getFullYear() === now.getFullYear() ? '' : ` ${d.getFullYear()}`;
  return `Le ${d.getDate()} ${MONTHS[d.getMonth()]}${year}`;
}

/** L'heure d'une tâche, quand elle en a une. */
export function timeLabel(todo: Todo): string {
  if (!todo.due || !todo.timed) return '';
  return new Date(todo.due).toLocaleTimeString('fr-FR', { hour: '2-digit', minute: '2-digit' });
}

export function isLate(todo: Todo, now = new Date()): boolean {
  return Boolean(todo.due) && !todo.done && dayDiff(new Date(todo.due as string), now) < 0;
}

export type TodoGroup = {
  key: string;
  label: string;
  /** Vrai pour le groupe du retard, qui se signale au lieu de se dater. */
  late: boolean;
  todos: Todo[];
};

/**
 * Range les tâches par jour, dans l'ordre où elles se lisent.
 *
 * Le retard n'est pas éclaté sur ses jours d'origine : trois tâches oubliées
 * mardi, mercredi et jeudi font un seul bloc « En retard ». Savoir lequel des
 * trois jours a été manqué n'aide personne — savoir qu'il en traîne trois, si.
 * Ce qui n'a pas encore de jour ferme la liste : c'est le seul endroit d'où on
 * le sort en lui donnant une date.
 */
export function groupTodos(todos: Todo[], now = new Date()): TodoGroup[] {
  const groups = new Map<string, TodoGroup>();
  const push = (key: string, label: string, late: boolean, todo: Todo) => {
    const group = groups.get(key) ?? { key, label, late, todos: [] };
    group.todos.push(todo);
    groups.set(key, group);
  };

  for (const todo of todos) {
    if (!todo.due) continue;
    if (isLate(todo, now)) {
      push('late', 'En retard', true, todo);
      continue;
    }
    const day = new Date(todo.due);
    push(startOfDay(day).toDateString(), dayLabel(todo.due, now), false, todo);
  }
  // Les non datées ferment la marche, quel que soit leur ordre d'arrivée.
  for (const todo of todos) {
    if (!todo.due) push('undated', 'Sans date', false, todo);
  }

  // « En retard » d'abord : c'est ce qui coûte quelque chose de ne pas voir.
  return [...groups.values()].sort((a, b) => Number(b.late) - Number(a.late));
}
