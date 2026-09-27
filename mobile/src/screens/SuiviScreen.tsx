import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { ActivityIndicator, Alert, Pressable, RefreshControl, ScrollView, StyleSheet, View } from 'react-native';
import * as Clipboard from 'expo-clipboard';
import { Feather } from '@expo/vector-icons';
import * as Haptics from 'expo-haptics';
import { useSafeAreaInsets } from 'react-native-safe-area-context';

import { Button, Chip, Divider, Empty, Notice, Panel, ScreenHeader, Section, Skeleton, T } from '../design/ui';
import { navBarSpace } from '../design/NavBar';
import { tokens } from '../design/tokens';
import { api, Digest, EmailDraft, Todo, Urgent, UrgentTask } from '../api';
import { load, useSlot } from '../lib/cache';
import { addTodo, groupTodos, isoDay, loadTodos, removeTodo, scheduleTodo, timeLabel, TODOS_KEY, toggleTodo } from '../lib/todos';
import { speak, stopSpeaking } from '../lib/speech';

export const DIGEST_KEY = 'digest';
export const URGENT_KEY = 'urgent';
const DRAFTS_KEY = 'drafts';

export function loadDigest(force = false) {
  return load<Digest>(DIGEST_KEY, () => api.digest(force), force);
}
export function loadUrgent(force = false) {
  return load<Urgent>(URGENT_KEY, () => api.urgent(force), force);
}
export function loadDrafts(force = false) {
  return load<EmailDraft[]>(DRAFTS_KEY, () => api.drafts().then((r) => r.drafts), force);
}

/**
 * Suivi : ce que Raoul tient pour toi, sans avoir à lui demander.
 *
 * Quatre blocs, dans l'ordre où ils comptent : ce que tu t'es engagé à faire
 * (daté par toi), ce qui t'arrive et attend une action (déduit de tes
 * messages), le point du jour (rédigé par Raoul), et les réponses de mail
 * qu'il a préparées. Tout se charge au lancement de l'app, pas à l'ouverture
 * de l'onglet : on arrive sur du contenu, pas sur une roue.
 */
export function SuiviScreen() {
  const insets = useSafeAreaInsets();
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    void loadTodos();
    void loadUrgent();
    void loadDigest();
    void loadDrafts();
    return () => stopSpeaking();
  }, []);

  const refresh = useCallback(() => {
    setBusy(true);
    void Promise.all([loadTodos(true), loadUrgent(true), loadDigest(true), loadDrafts(true)]).finally(() => setBusy(false));
  }, []);

  return (
    <ScrollView
      style={styles.flex}
      contentContainerStyle={[styles.content, { paddingBottom: navBarSpace(insets.bottom) }]}
      refreshControl={<RefreshControl refreshing={busy} onRefresh={refresh} tintColor={tokens.colors.accent} />}
    >
      <ScreenHeader title="Suivi" subtitle="à faire · à traiter · le point · réponses" />
      <AFaire />
      <ATraiter />
      <PointDuJour onRefresh={refresh} busy={busy} />
      <Reponses />
    </ScrollView>
  );
}

/* -------------------------------------------------------------------------- */
/* À faire                                                                     */
/* -------------------------------------------------------------------------- */

function AFaire() {
  const { data, error, loading } = useSlot<Todo[]>(TODOS_KEY);
  const [open, setOpen] = useState<string | null>(null);
  const groups = useMemo(() => groupTodos(data ?? []), [data]);
  const pending = data?.filter((t) => !t.done).length ?? 0;

  return (
    <Section title="À faire" count={pending} right={loading && data ? <ActivityIndicator size="small" color={tokens.colors.text3} /> : null}>
      {!data && loading ? (
        <Skeleton lines={2} style={styles.skeleton} />
      ) : error && !data ? (
        <Notice tone="danger" icon="cloud-off">
          <T v="small" tone="muted">
            Liste indisponible : {error}
          </T>
        </Notice>
      ) : groups.length === 0 ? (
        <Panel>
          <T v="small" tone="faint">
            Rien d’inscrit. Dis « ajoute ça à ma liste » à Raoul, il te demandera pour quand.
          </T>
        </Panel>
      ) : (
        <Panel padded={false}>
          {groups.map((group, g) => (
            <View key={group.key} style={[styles.group, g > 0 && styles.divided]}>
              <View style={styles.groupHead}>
                <T v="label" tone={group.late ? 'warn' : 'faint'} style={{ textTransform: 'uppercase' }}>
                  {group.label}
                </T>
                {group.late ? <Feather name="alert-circle" size={12} color={tokens.colors.warn} /> : null}
              </View>
              {group.todos.map((todo) => (
                <TodoLine key={todo.id} todo={todo} open={open === todo.id} onOpen={() => setOpen((cur) => (cur === todo.id ? null : todo.id))} />
              ))}
            </View>
          ))}
        </Panel>
      )}
    </Section>
  );
}

function TodoLine({ todo, open, onOpen }: { todo: Todo; open: boolean; onOpen: () => void }) {
  const hour = timeLabel(todo);
  return (
    <View>
      <View style={styles.todoRow}>
        <Pressable
          onPress={() => {
            void Haptics.impactAsync(todo.done ? Haptics.ImpactFeedbackStyle.Light : Haptics.ImpactFeedbackStyle.Medium);
            void toggleTodo(todo);
          }}
          hitSlop={tokens.space.md}
          accessibilityRole="checkbox"
          accessibilityLabel={todo.title}
          accessibilityState={{ checked: todo.done }}
          style={({ pressed }) => [styles.boxTap, pressed && styles.pressed]}
        >
          <View style={[styles.box, todo.done && styles.boxOn]}>{todo.done ? <Feather name="check" size={13} color={tokens.colors.onAccent} /> : null}</View>
        </Pressable>
        <Pressable
          onPress={() => {
            void Haptics.selectionAsync();
            onOpen();
          }}
          accessibilityRole="button"
          accessibilityLabel={`Détail de : ${todo.title}`}
          accessibilityState={{ expanded: open }}
          style={({ pressed }) => [styles.todoTitle, pressed && styles.pressed]}
        >
          <T v="body" style={[styles.flex, todo.done && styles.done]} numberOfLines={2}>
            {todo.title}
          </T>
          {hour ? (
            <T v="mono" tone={todo.done ? 'faint' : 'accent'}>
              {hour}
            </T>
          ) : null}
          <Feather name={open ? 'chevron-up' : 'chevron-down'} size={16} color={tokens.colors.text3} />
        </Pressable>
      </View>
      {open ? (
        <View style={styles.detail}>
          {todo.note ? (
            <T v="small" tone="muted">
              {todo.note}
            </T>
          ) : null}
          {todo.source ? (
            <View style={styles.source}>
              <Feather name={todo.source.origine === 'mail' ? 'mail' : 'hash'} size={12} color={tokens.colors.text3} />
              <T v="mono" tone="faint" numberOfLines={1} style={styles.flex}>
                {todo.source.de ? `${todo.source.de} · ` : ''}
                {todo.source.titre || todo.source.origine}
              </T>
            </View>
          ) : null}
          <View style={styles.actions}>
            <Chip icon="sun" label="Aujourd’hui" onPress={() => void scheduleTodo(todo, isoDay(0))} />
            <Chip icon="sunrise" label="Demain" onPress={() => void scheduleTodo(todo, isoDay(1))} />
            <Chip icon="trash-2" label="Retirer" tone="danger" onPress={() => void removeTodo(todo)} />
          </View>
        </View>
      ) : null}
    </View>
  );
}

/* -------------------------------------------------------------------------- */
/* À traiter                                                                   */
/* -------------------------------------------------------------------------- */

function ATraiter() {
  const { data, error, loading } = useSlot<Urgent>(URGENT_KEY);
  const [open, setOpen] = useState<number | null>(null);
  const tasks = data?.taches ?? null;

  // Rien de branché : la section n'a pas lieu d'être.
  if (data && data.sources.length === 0 && !error) return null;

  return (
    <Section title="À traiter" count={tasks?.length ?? 0} right={loading && tasks ? <ActivityIndicator size="small" color={tokens.colors.text3} /> : null}>
      {!data && loading ? (
        <Panel style={styles.calm}>
          <ActivityIndicator size="small" color={tokens.colors.thinking} />
          <T v="small" tone="faint">
            Raoul fait le tri…
          </T>
        </Panel>
      ) : error && !tasks ? (
        <Notice tone="danger" icon="cloud-off">
          <T v="small" tone="muted">
            Impossible de faire le point : {error}
          </T>
        </Notice>
      ) : tasks && tasks.length === 0 ? (
        <Panel tone="ok" style={styles.calm}>
          <Feather name="check-circle" size={16} color={tokens.colors.ok} />
          <T v="small" tone="muted">
            Rien qui te réclame. Le reste peut attendre.
          </T>
        </Panel>
      ) : tasks ? (
        <Panel padded={false}>
          {tasks.map((task, i) => (
            <UrgentLine key={`${task.action}-${i}`} task={task} first={i === 0} open={open === i} onToggle={() => setOpen((cur) => (cur === i ? null : i))} />
          ))}
        </Panel>
      ) : null}
    </Section>
  );
}

function UrgentLine({ task, first, open, onToggle }: { task: UrgentTask; first: boolean; open: boolean; onToggle: () => void }) {
  const [picking, setPicking] = useState(false);
  const [added, setAdded] = useState<string | null>(null);

  const file = (due: string, label: string) => {
    void Haptics.impactAsync(Haptics.ImpactFeedbackStyle.Light);
    const source = task.sources[0];
    void addTodo({ title: task.action, note: task.pourquoi, due, source: source ? { origine: source.origine, de: source.de, titre: source.titre } : undefined })
      .then(() => setAdded(label))
      .catch(() => setPicking(false));
  };

  return (
    <View style={[styles.line, !first && styles.divided]}>
      <Pressable
        onPress={() => {
          void Haptics.selectionAsync();
          onToggle();
        }}
        accessibilityRole="button"
        accessibilityLabel={task.action}
        accessibilityState={{ expanded: open }}
        style={({ pressed }) => [styles.urgentRow, pressed && styles.pressed]}
      >
        <View style={[styles.dot, { backgroundColor: task.urgence === 'haute' ? tokens.colors.warn : tokens.colors.accent }]} />
        <T v="bodyStrong" style={styles.flex}>
          {task.action}
        </T>
        <Feather name={open ? 'chevron-up' : 'chevron-down'} size={16} color={tokens.colors.text3} />
      </Pressable>
      {open ? (
        <View style={styles.detail}>
          <T v="small" tone="muted">
            {task.pourquoi}
          </T>
          {task.sources.map((src, i) => (
            <View key={`${src.titre}-${i}`} style={styles.source}>
              <Feather name={src.origine === 'mail' ? 'mail' : 'hash'} size={12} color={tokens.colors.text3} />
              <T v="mono" tone="faint" numberOfLines={1} style={styles.flex}>
                {src.de ? `${src.de} · ` : ''}
                {src.titre}
              </T>
              <T v="mono" tone="faint">
                {src.quand}
              </T>
            </View>
          ))}
          {added ? (
            <View style={styles.source}>
              <Feather name="check" size={12} color={tokens.colors.ok} />
              <T v="mono" tone="ok">
                Dans ta liste {added}
              </T>
            </View>
          ) : !picking ? (
            <View style={styles.actions}>
              <Chip icon="plus" label="Ajouter à ma liste" onPress={() => setPicking(true)} />
            </View>
          ) : (
            <View style={styles.actions}>
              <T v="mono" tone="faint">
                Pour quand ?
              </T>
              <Chip icon="sun" label="Aujourd’hui" onPress={() => file(isoDay(0), 'pour aujourd’hui')} />
              <Chip icon="sunrise" label="Demain" onPress={() => file(isoDay(1), 'pour demain')} />
              <Chip icon="clock" label="Sans date" onPress={() => file('', 'sans date')} />
            </View>
          )}
        </View>
      ) : null}
    </View>
  );
}

/* -------------------------------------------------------------------------- */
/* Le point du jour                                                            */
/* -------------------------------------------------------------------------- */

function PointDuJour({ onRefresh, busy }: { onRefresh: () => void; busy: boolean }) {
  const { data, error, loading } = useSlot<Digest>(DIGEST_KEY);
  return (
    <Section title="Le point du jour" right={data?.generated_at ? <T v="mono" tone="faint">{data.stale ? 'daté · ' : ''}{formatWhen(data.generated_at)}</T> : null}>
      {!data && loading ? (
        <Skeleton lines={4} style={styles.skeleton} />
      ) : error && !data ? (
        <Notice tone="danger" icon="cloud-off">
          <T v="small" tone="muted">
            {error}
          </T>
        </Notice>
      ) : (
        <Panel>
          <T v="body">{data?.summary || 'Aucune synthèse pour l’instant. Tire l’écran vers le bas pour en générer une.'}</T>
          {data?.unavailable?.length ? (
            <T v="mono" tone="warn">
              Sources indisponibles : {data.unavailable.join(', ')}
            </T>
          ) : null}
          {data?.events.length ? (
            <>
              <Divider />
              {data.events.map((e, i) => (
                <View key={`${e.debut}-${i}`} style={styles.event}>
                  <T v="mono" tone="accent" style={styles.eventTime}>
                    {formatHour(e.debut)}
                  </T>
                  <View style={styles.flex}>
                    <T v="bodyStrong">{e.titre}</T>
                    <T v="mono" tone="faint">
                      jusqu’à {formatHour(e.fin)}
                      {e.lieu ? ` · ${e.lieu}` : ''}
                    </T>
                  </View>
                </View>
              ))}
            </>
          ) : null}
          <View style={styles.actions}>
            <Button label="Régénérer" variant="ghost" icon="refresh-cw" compact loading={busy} onPress={onRefresh} />
          </View>
        </Panel>
      )}
    </Section>
  );
}

/* -------------------------------------------------------------------------- */
/* Réponses                                                                    */
/* -------------------------------------------------------------------------- */

function Reponses() {
  const { data, error, loading } = useSlot<EmailDraft[]>(DRAFTS_KEY);
  const [copied, setCopied] = useState<string | null>(null);
  const [reading, setReading] = useState<string | null>(null);
  const drafts = data ?? [];

  const copy = async (draft: EmailDraft) => {
    await Clipboard.setStringAsync(draft.body);
    setCopied(draft.id);
    setTimeout(() => setCopied((id) => (id === draft.id ? null : id)), 2000);
  };
  const read = async (draft: EmailDraft) => {
    if (reading === draft.id) {
      stopSpeaking();
      setReading(null);
      return;
    }
    stopSpeaking();
    setReading(draft.id);
    await speak(draft.body, draft.language);
    setReading((id) => (id === draft.id ? null : id));
  };
  const remove = (draft: EmailDraft) => {
    Alert.alert('Supprimer cette réponse ?', `La réponse pour ${draft.to} sera perdue.`, [
      { text: 'Annuler', style: 'cancel' },
      {
        text: 'Supprimer',
        style: 'destructive',
        onPress: () => {
          api
            .deleteDraft(draft.id)
            .then(() => loadDrafts(true))
            .catch(() => loadDrafts(true));
        },
      },
    ]);
  };

  return (
    <Section title="Réponses préparées" count={drafts.length}>
      {!data && loading ? (
        <Skeleton lines={3} style={styles.skeleton} />
      ) : error && !data ? (
        <Notice tone="danger" icon="cloud-off">
          <T v="small" tone="muted">
            {error}
          </T>
        </Notice>
      ) : drafts.length === 0 ? (
        <Panel>
          <Empty icon="edit-3" title="Aucune réponse préparée" message="Dis à Raoul « prépare-moi une réponse à ce mail » : elle apparaîtra ici, prête à copier. Rien ne part sans toi." />
        </Panel>
      ) : (
        drafts.map((draft) => (
          <Panel key={draft.id}>
            <View style={styles.draftHead}>
              <View style={styles.flex}>
                <T v="label" tone="faint" style={{ textTransform: 'uppercase' }}>
                  pour {draft.to}
                </T>
                <T v="bodyStrong" numberOfLines={2}>
                  {draft.subject || '(sans objet)'}
                </T>
              </View>
              {draft.language && draft.language !== 'fr' ? (
                <T v="mono" tone="accent">
                  {draft.language.toUpperCase()}
                </T>
              ) : null}
            </View>
            <T v="body">{draft.body}</T>
            <T v="mono" tone="faint">
              {draft.to_addr ? `${draft.to_addr} · ` : ''}
              {formatWhen(draft.updated_at)}
            </T>
            <Divider />
            <View style={styles.draftActions}>
              <Button label={copied === draft.id ? 'Copié' : 'Copier'} icon={copied === draft.id ? 'check' : 'copy'} variant="secondary" compact onPress={() => void copy(draft)} />
              <Button label={reading === draft.id ? 'Stop' : 'Écouter'} icon={reading === draft.id ? 'square' : 'volume-2'} variant="ghost" compact onPress={() => void read(draft)} />
              <Button label="Supprimer" icon="trash-2" variant="danger" compact onPress={() => remove(draft)} />
            </View>
          </Panel>
        ))
      )}
    </Section>
  );
}

/* -------------------------------------------------------------------------- */

function formatWhen(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const sameDay = d.toDateString() === new Date().toDateString();
  const time = d.toLocaleTimeString('fr-FR', { hour: '2-digit', minute: '2-digit' });
  return sameDay ? time : `${d.toLocaleDateString('fr-FR', { day: 'numeric', month: 'short' })} ${time}`;
}

function formatHour(iso: string): string {
  return new Date(iso).toLocaleTimeString('fr-FR', { hour: '2-digit', minute: '2-digit' });
}

const styles = StyleSheet.create({
  flex: { flex: 1 },
  pressed: { opacity: 0.6 },
  content: { paddingHorizontal: tokens.space.lg, paddingTop: tokens.space.sm, gap: tokens.space.xl },
  skeleton: { borderWidth: 1, borderColor: tokens.colors.line, borderRadius: tokens.radius.lg, backgroundColor: tokens.colors.panel },
  calm: { flexDirection: 'row', alignItems: 'center', gap: tokens.space.md },
  divided: { borderTopWidth: 1, borderTopColor: tokens.colors.line },

  group: { paddingHorizontal: tokens.space.lg, paddingBottom: tokens.space.sm },
  groupHead: { flexDirection: 'row', alignItems: 'center', gap: tokens.space.xs, paddingTop: tokens.space.md },
  todoRow: { flexDirection: 'row', alignItems: 'center' },
  boxTap: { width: tokens.touch, height: tokens.touch, alignItems: 'center', justifyContent: 'center', marginLeft: -tokens.space.md },
  box: { width: 21, height: 21, borderRadius: 6, borderWidth: 1.5, borderColor: tokens.colors.lineStrong, alignItems: 'center', justifyContent: 'center' },
  boxOn: { backgroundColor: tokens.colors.accent, borderColor: tokens.colors.accent },
  todoTitle: { flex: 1, flexDirection: 'row', alignItems: 'center', gap: tokens.space.sm, minHeight: tokens.touch, paddingVertical: tokens.space.sm },
  done: { textDecorationLine: 'line-through', color: tokens.colors.text3 },

  line: { paddingHorizontal: tokens.space.lg },
  urgentRow: { flexDirection: 'row', alignItems: 'center', gap: tokens.space.md, minHeight: tokens.touch + 6, paddingVertical: tokens.space.md },
  dot: { width: 7, height: 7, borderRadius: 4 },

  detail: { gap: tokens.space.sm, paddingLeft: tokens.space.lg + 3, paddingBottom: tokens.space.lg },
  source: { flexDirection: 'row', alignItems: 'center', gap: tokens.space.sm },
  actions: { flexDirection: 'row', flexWrap: 'wrap', alignItems: 'center', gap: tokens.space.sm, marginTop: tokens.space.xs },

  event: { flexDirection: 'row', gap: tokens.space.md, alignItems: 'flex-start' },
  eventTime: { width: 48, paddingTop: 3 },

  draftHead: { flexDirection: 'row', alignItems: 'flex-start', gap: tokens.space.sm },
  draftActions: { flexDirection: 'row', flexWrap: 'wrap', gap: tokens.space.sm },
});
