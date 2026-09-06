import React, { useEffect, useMemo, useState } from 'react';
import { ActivityIndicator, Animated, Easing, Pressable, StyleSheet, View } from 'react-native';
import { Feather } from '@expo/vector-icons';
import * as Haptics from 'expo-haptics';

import { Glass } from './glass';
import { SectionLabel, Txt } from './ui';
import { Todo } from '../api';
import { useSlot } from '../lib/cache';
import {
  TODOS_KEY,
  TodoGroup,
  groupTodos,
  isoDay,
  loadTodos,
  removeTodo,
  scheduleTodo,
  timeLabel,
  toggleTodo,
} from '../lib/todos';
import { alpha, theme } from '../theme';

/**
 * Ce que tu t'es engagé à faire, rangé par jour.
 *
 * La section d'à côté, « À traiter », dit ce qui t'arrive. Celle-ci dit ce que
 * tu as décidé d'en faire, et quand. C'est pour ça qu'elle passe devant : un
 * message non lu peut attendre demain, une tâche datée d'aujourd'hui non.
 *
 * Une case se coche sous le doigt, sans attendre le serveur, et la ligne reste
 * barrée à l'écran quelques heures au lieu de disparaître. Voir ce qu'on vient
 * de faire est la moitié de l'intérêt d'une liste — et la seule façon de
 * rattraper la ligne qu'on a cochée par erreur.
 */
export function AFaire() {
  const { data, error, loading } = useSlot<Todo[]>(TODOS_KEY);
  const [open, setOpen] = useState<string | null>(null);

  useEffect(() => {
    void loadTodos();
  }, []);

  const todos = data;
  const groups = useMemo(() => groupTodos(todos ?? []), [todos]);
  const pending = todos?.filter((t) => !t.done).length ?? 0;

  return (
    <View style={styles.section}>
      <View style={styles.head}>
        <SectionLabel>À faire</SectionLabel>
        {pending ? (
          <View style={styles.count}>
            <Txt variant="label" style={styles.countText}>
              {pending}
            </Txt>
          </View>
        ) : null}
        {loading && todos ? <ActivityIndicator size="small" color={theme.colors.textFaint} /> : null}
      </View>

      {!todos && loading ? (
        <Glass radius={theme.radius.lg} style={styles.calm}>
          <ActivityIndicator size="small" color={theme.colors.textFaint} />
          <Txt variant="small" tone="faint" style={styles.flex}>
            Un instant…
          </Txt>
        </Glass>
      ) : error && !todos ? (
        <Glass radius={theme.radius.lg} tone={theme.colors.danger} style={styles.calm}>
          <Feather name="cloud-off" size={15} color={theme.colors.danger} />
          <Txt variant="small" tone="muted" style={styles.flex}>
            Liste indisponible : {error}
          </Txt>
        </Glass>
      ) : groups.length === 0 ? (
        <Glass radius={theme.radius.lg} style={styles.calm}>
          <Feather name="check-square" size={16} color={theme.colors.textFaint} />
          <Txt variant="small" tone="faint" style={styles.flex}>
            Rien d’inscrit. Dis « ajoute ça à ma liste » à Raoul, il te demandera pour quand.
          </Txt>
        </Glass>
      ) : (
        <Glass radius={theme.radius.lg}>
          {groups.map((group, g) => (
            <Day
              key={group.key}
              group={group}
              first={g === 0}
              open={open}
              onOpen={(id) => setOpen((cur) => (cur === id ? null : id))}
            />
          ))}
        </Glass>
      )}
    </View>
  );
}

/** Un jour et ses tâches. L'en-tête date le bloc, les lignes ne se datent pas. */
function Day({
  group,
  first,
  open,
  onOpen,
}: {
  group: TodoGroup;
  first: boolean;
  open: string | null;
  onOpen: (id: string) => void;
}) {
  return (
    <View style={[styles.day, !first && styles.divided]}>
      <View style={styles.dayHead}>
        <Txt variant="label" tone={group.late ? 'warning' : 'faint'} style={styles.dayLabel}>
          {group.label.toUpperCase()}
        </Txt>
        {group.late ? <Feather name="alert-circle" size={12} color={theme.colors.warning} /> : null}
      </View>

      {group.todos.map((todo) => (
        <Line key={todo.id} todo={todo} open={open === todo.id} onOpen={() => onOpen(todo.id)} />
      ))}
    </View>
  );
}

function Line({ todo, open, onOpen }: { todo: Todo; open: boolean; onOpen: () => void }) {
  const hour = timeLabel(todo);

  return (
    <View>
      <View style={styles.row}>
        <Pressable
          onPress={() => {
            void Haptics.impactAsync(
              todo.done ? Haptics.ImpactFeedbackStyle.Light : Haptics.ImpactFeedbackStyle.Medium,
            );
            void toggleTodo(todo);
          }}
          hitSlop={theme.space.md}
          accessibilityRole="checkbox"
          accessibilityLabel={todo.title}
          accessibilityState={{ checked: todo.done }}
          style={({ pressed }) => [styles.boxTap, pressed && styles.pressed]}
        >
          <View style={[styles.box, todo.done && styles.boxOn]}>
            {todo.done ? <Feather name="check" size={13} color={theme.colors.onPrimary} /> : null}
          </View>
        </Pressable>

        <Pressable
          onPress={() => {
            void Haptics.selectionAsync();
            onOpen();
          }}
          accessibilityRole="button"
          accessibilityLabel={`Détail de : ${todo.title}`}
          accessibilityState={{ expanded: open }}
          style={({ pressed }) => [styles.title, pressed && styles.pressed]}
        >
          <Txt variant="body" style={[styles.flex, todo.done && styles.doneText]} numberOfLines={2}>
            {todo.title}
          </Txt>
          {hour ? (
            <Txt variant="mono" tone={todo.done ? 'faint' : 'primary'}>
              {hour}
            </Txt>
          ) : null}
          <Feather
            name={open ? 'chevron-up' : 'chevron-down'}
            size={16}
            color={theme.colors.textFaint}
          />
        </Pressable>
      </View>

      {open ? <Detail todo={todo} /> : null}
    </View>
  );
}

/** Le pourquoi, d'où ça vient, et de quoi la déplacer ou la retirer. */
function Detail({ todo }: { todo: Todo }) {
  const fade = React.useRef(new Animated.Value(0)).current;

  useEffect(() => {
    const anim = Animated.timing(fade, {
      toValue: 1,
      duration: theme.motion.base,
      easing: Easing.bezier(...theme.motion.easing),
      useNativeDriver: true,
    });
    anim.start();
    return () => anim.stop();
  }, [fade]);

  const source = todo.source;

  return (
    <Animated.View
      style={[
        styles.detail,
        {
          opacity: fade,
          transform: [{ translateY: fade.interpolate({ inputRange: [0, 1], outputRange: [-4, 0] }) }],
        },
      ]}
    >
      {todo.note ? (
        <Txt variant="small" tone="muted">
          {todo.note}
        </Txt>
      ) : null}

      {source ? (
        <View style={styles.source}>
          <Feather
            name={source.origine === 'mail' ? 'mail' : 'hash'}
            size={12}
            color={theme.colors.textFaint}
          />
          <Txt variant="mono" tone="faint" style={styles.flex} numberOfLines={1}>
            {source.de ? `${source.de} · ` : ''}
            {source.titre || source.origine}
          </Txt>
        </View>
      ) : null}

      <View style={styles.actions}>
        <Action icon="sun" label="Aujourd’hui" onPress={() => void scheduleTodo(todo, isoDay(0))} />
        <Action icon="sunrise" label="Demain" onPress={() => void scheduleTodo(todo, isoDay(1))} />
        <Action icon="trash-2" label="Retirer" danger onPress={() => void removeTodo(todo)} />
      </View>
    </Animated.View>
  );
}

function Action({
  icon,
  label,
  onPress,
  danger,
}: {
  icon: React.ComponentProps<typeof Feather>['name'];
  label: string;
  onPress: () => void;
  danger?: boolean;
}) {
  const color = danger ? theme.colors.danger : theme.colors.textMuted;
  return (
    <Pressable
      onPress={() => {
        void Haptics.selectionAsync();
        onPress();
      }}
      accessibilityRole="button"
      accessibilityLabel={label}
      style={({ pressed }) => pressed && styles.pressed}
    >
      <Glass radius={theme.radius.pill} variant="subtle" style={styles.action}>
        <Feather name={icon} size={12} color={color} />
        <Txt variant="mono" style={{ color }}>
          {label}
        </Txt>
      </Glass>
    </Pressable>
  );
}

const styles = StyleSheet.create({
  flex: { flex: 1 },
  section: { gap: theme.space.sm },
  head: { flexDirection: 'row', alignItems: 'center', gap: theme.space.sm },
  count: {
    minWidth: 20,
    paddingHorizontal: 6,
    paddingVertical: 1,
    borderRadius: theme.radius.pill,
    backgroundColor: alpha(theme.colors.primary, 0.18),
    alignItems: 'center',
  },
  countText: { color: theme.colors.primary, fontVariant: ['tabular-nums'] },

  calm: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: theme.space.md,
    padding: theme.space.lg,
  },

  day: { paddingHorizontal: theme.space.lg, paddingBottom: theme.space.sm },
  divided: {
    borderTopWidth: StyleSheet.hairlineWidth,
    borderTopColor: theme.colors.border,
  },
  dayHead: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: theme.space.xs,
    paddingTop: theme.space.md,
  },
  dayLabel: { letterSpacing: 1.1 },

  row: { flexDirection: 'row', alignItems: 'center' },
  // La zone de la case déborde largement le carré dessiné : 22 pt se ratent au
  // pouce, et rater une case coche la ligne d'à côté.
  boxTap: {
    width: theme.touchMin,
    height: theme.touchMin,
    alignItems: 'center',
    justifyContent: 'center',
    marginLeft: -theme.space.md,
  },
  box: {
    width: 21,
    height: 21,
    borderRadius: 7,
    borderWidth: 1.5,
    borderColor: theme.colors.borderStrong,
    alignItems: 'center',
    justifyContent: 'center',
  },
  boxOn: { backgroundColor: theme.colors.primary, borderColor: theme.colors.primary },

  title: {
    flex: 1,
    flexDirection: 'row',
    alignItems: 'center',
    gap: theme.space.sm,
    minHeight: theme.touchMin,
    paddingVertical: theme.space.sm,
  },
  doneText: {
    textDecorationLine: 'line-through',
    color: theme.colors.textFaint,
  },
  pressed: { opacity: 0.6 },

  detail: {
    gap: theme.space.sm,
    paddingLeft: theme.touchMin - theme.space.md,
    paddingBottom: theme.space.md,
  },
  source: { flexDirection: 'row', alignItems: 'center', gap: theme.space.sm },
  actions: { flexDirection: 'row', flexWrap: 'wrap', gap: theme.space.sm, marginTop: theme.space.xs },
  action: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: theme.space.xs + 2,
    paddingHorizontal: theme.space.md,
    paddingVertical: theme.space.sm,
  },
});
