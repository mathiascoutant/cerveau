import React, { useEffect, useState } from 'react';
import { ActivityIndicator, Animated, Easing, Pressable, StyleSheet, View } from 'react-native';
import { Feather } from '@expo/vector-icons';
import * as Haptics from 'expo-haptics';

import { Glass } from './glass';
import { SectionLabel, Txt } from './ui';
import { api, Urgent, UrgentTask } from '../api';
import { load, useSlot } from '../lib/cache';
import { addTodo, isoDay } from '../lib/todos';
import { alpha, theme } from '../theme';

export const URGENT_KEY = 'urgent';

/** Charge la liste sans attendre qu'on regarde l'écran d'accueil. */
export function loadUrgent(force = false) {
  return load<Urgent>(URGENT_KEY, () => api.urgent(force), force);
}

/**
 * Ce qu'il te reste à faire.
 *
 * Ce n'est pas une boîte de réception en plus petit : chaque ligne est une
 * action, pas un message. Trois mails sur le même sujet font une ligne. Ce qui
 * n'attend aucune action de ta part n'en fait aucune. Tout ce travail est fait
 * par le serveur (tri déterministe puis synthèse) — l'app affiche la liste,
 * elle ne la fabrique pas.
 *
 * Une ligne se déplie plutôt qu'elle n'ouvre : la question qu'on se pose devant
 * « Répondre au mail de Westent » est « c'est quoi déjà ? », et la réponse
 * tient en deux phrases. Ouvrir le message serait toujours plus long que les
 * lire.
 */
export function Urgences() {
  const { data, error, loading } = useSlot<Urgent>(URGENT_KEY);
  const [open, setOpen] = useState<number | null>(null);

  useEffect(() => {
    void loadUrgent();
  }, []);

  const tasks = data?.taches ?? null;
  const sources = data?.sources ?? [];

  // Rien de branché : la section n'a pas lieu d'être. Annoncer « rien à
  // traiter » à quelqu'un qui n'a connecté ni mail ni Slack, c'est lui donner
  // une bonne nouvelle sur un sujet qui n'existe pas.
  if (data && sources.length === 0 && !error) return null;

  return (
    <View style={styles.section}>
      <View style={styles.head}>
        <SectionLabel>À traiter</SectionLabel>
        {tasks?.length ? (
          <View style={styles.count}>
            <Txt variant="label" style={styles.countText}>
              {tasks.length}
            </Txt>
          </View>
        ) : null}
        {loading && tasks ? <ActivityIndicator size="small" color={theme.colors.textFaint} /> : null}
      </View>

      {!data && loading ? (
        <Glass radius={theme.radius.lg} style={styles.calm}>
          <ActivityIndicator size="small" color={theme.colors.textFaint} />
          <Txt variant="small" tone="faint" style={styles.flex}>
            Raoul fait le tri…
          </Txt>
        </Glass>
      ) : error && !tasks ? (
        <Glass radius={theme.radius.lg} tone={theme.colors.danger} style={styles.calm}>
          <Feather name="cloud-off" size={15} color={theme.colors.danger} />
          <Txt variant="small" tone="muted" style={styles.flex}>
            Impossible de faire le point : {error}
          </Txt>
        </Glass>
      ) : tasks && tasks.length === 0 ? (
        <Glass radius={theme.radius.lg} tone={theme.colors.success} style={styles.calm}>
          <Feather name="check-circle" size={16} color={theme.colors.success} />
          <Txt variant="small" tone="muted" style={styles.flex}>
            Rien qui te réclame. Le reste peut attendre.
          </Txt>
        </Glass>
      ) : tasks ? (
        <Glass radius={theme.radius.lg}>
          {tasks.map((task, i) => (
            <Line
              key={`${task.action}-${i}`}
              task={task}
              first={i === 0}
              open={open === i}
              onToggle={() => setOpen((cur) => (cur === i ? null : i))}
            />
          ))}
        </Glass>
      ) : null}
    </View>
  );
}

function Line({
  task,
  first,
  open,
  onToggle,
}: {
  task: UrgentTask;
  first: boolean;
  open: boolean;
  onToggle: () => void;
}) {
  // L'urgence se lit à la puce, mais jamais à elle seule : l'ordre de la liste
  // la porte aussi, et le détail dit qui attend depuis quand.
  const dot = task.urgence === 'haute' ? theme.colors.warning : theme.colors.primary;

  return (
    <View style={[styles.line, !first && styles.lineDivided]}>
      <Pressable
        onPress={() => {
          void Haptics.selectionAsync();
          onToggle();
        }}
        accessibilityRole="button"
        accessibilityLabel={task.action}
        accessibilityHint={open ? 'Replier le détail' : 'Déplier le détail'}
        accessibilityState={{ expanded: open }}
        style={({ pressed }) => [styles.row, pressed && styles.pressed]}
      >
        <View style={[styles.dot, { backgroundColor: dot }]} />
        <Txt variant="bodyStrong" style={styles.flex}>
          {task.action}
        </Txt>
        <Feather
          name={open ? 'chevron-up' : 'chevron-down'}
          size={16}
          color={theme.colors.textFaint}
        />
      </Pressable>

      {open ? <Detail task={task} /> : null}
    </View>
  );
}

/** Le pourquoi, les messages d'où ça vient, et de quoi la verser dans la liste. */
function Detail({ task }: { task: UrgentTask }) {
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
      <Txt variant="small" tone="muted">
        {task.pourquoi}
      </Txt>

      {task.sources.map((src, i) => (
        <View key={`${src.titre}-${i}`} style={styles.source}>
          <Feather
            name={src.origine === 'mail' ? 'mail' : 'hash'}
            size={12}
            color={theme.colors.textFaint}
          />
          <Txt variant="mono" tone="faint" style={styles.flex} numberOfLines={1}>
            {src.de ? `${src.de} · ` : ''}
            {src.titre}
          </Txt>
          <Txt variant="mono" tone="faint">
            {src.quand}
          </Txt>
        </View>
      ))}

      <Verser task={task} />
    </Animated.View>
  );
}

/**
 * Verse une urgence dans la liste à faire.
 *
 * Le même geste qu'à la voix, sans la voix : Raoul demande « c'est pour
 * quand ? », ici c'est le choix du jour qui pose la question. Ce n'est pas un
 * détail d'ergonomie — une tâche qu'on inscrit sans dire quand se retrouve dans
 * un tas qu'on ne relit plus, et c'est ce tas qu'on essaie d'éviter.
 *
 * La ligne reste dans « À traiter » après le versement : elle en sortira quand
 * le message qui l'a produite sera traité, pas parce qu'on l'a recopiée
 * ailleurs.
 */
function Verser({ task }: { task: UrgentTask }) {
  const [picking, setPicking] = useState(false);
  const [added, setAdded] = useState<string | null>(null);

  if (added) {
    return (
      <View style={styles.filed}>
        <Feather name="check" size={12} color={theme.colors.success} />
        <Txt variant="mono" tone="success">
          Dans ta liste {added}
        </Txt>
      </View>
    );
  }

  const file = (due: string, label: string) => {
    void Haptics.impactAsync(Haptics.ImpactFeedbackStyle.Light);
    const source = task.sources[0];
    void addTodo({
      title: task.action,
      note: task.pourquoi,
      due,
      source: source ? { origine: source.origine, de: source.de, titre: source.titre } : undefined,
    })
      .then(() => setAdded(label))
      .catch(() => setPicking(false));
  };

  if (!picking) {
    return (
      <View style={styles.actions}>
        <Chip
          icon="plus"
          label="Ajouter à ma liste"
          onPress={() => {
            void Haptics.selectionAsync();
            setPicking(true);
          }}
        />
      </View>
    );
  }

  return (
    <View style={styles.actions}>
      <Txt variant="mono" tone="faint" style={styles.forWhen}>
        Pour quand ?
      </Txt>
      <Chip icon="sun" label="Aujourd’hui" onPress={() => file(isoDay(0), 'pour aujourd’hui')} />
      <Chip icon="sunrise" label="Demain" onPress={() => file(isoDay(1), 'pour demain')} />
      <Chip icon="clock" label="Sans date" onPress={() => file('', 'sans date')} />
    </View>
  );
}

function Chip({
  icon,
  label,
  onPress,
}: {
  icon: React.ComponentProps<typeof Feather>['name'];
  label: string;
  onPress: () => void;
}) {
  return (
    <Pressable
      onPress={onPress}
      accessibilityRole="button"
      accessibilityLabel={label}
      style={({ pressed }) => pressed && styles.pressed}
    >
      <Glass radius={theme.radius.pill} variant="subtle" style={styles.chip}>
        <Feather name={icon} size={12} color={theme.colors.textMuted} />
        <Txt variant="mono" tone="muted">
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

  line: { paddingHorizontal: theme.space.lg },
  lineDivided: {
    borderTopWidth: StyleSheet.hairlineWidth,
    borderTopColor: theme.colors.border,
  },
  row: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: theme.space.md,
    minHeight: theme.touchMin + 6,
    paddingVertical: theme.space.md,
  },
  pressed: { opacity: 0.6 },
  dot: { width: 7, height: 7, borderRadius: 4 },

  detail: {
    gap: theme.space.sm,
    // Aligné sur le texte de la ligne, pas sur la puce : le détail appartient
    // à l'action, il ne recommence pas une colonne.
    paddingLeft: 7 + theme.space.md,
    paddingBottom: theme.space.lg,
  },
  source: { flexDirection: 'row', alignItems: 'center', gap: theme.space.sm },

  actions: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    alignItems: 'center',
    gap: theme.space.sm,
    marginTop: theme.space.xs,
  },
  forWhen: { marginRight: theme.space.xs },
  chip: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: theme.space.xs + 2,
    paddingHorizontal: theme.space.md,
    paddingVertical: theme.space.sm,
  },
  filed: { flexDirection: 'row', alignItems: 'center', gap: theme.space.sm, marginTop: theme.space.xs },
});
