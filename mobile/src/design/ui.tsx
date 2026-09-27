import React, { useEffect, useRef } from 'react';
import {
  ActivityIndicator,
  Animated,
  Easing,
  Pressable,
  StyleSheet,
  Text,
  TextInput,
  TextInputProps,
  TextProps,
  View,
  ViewProps,
  ViewStyle,
} from 'react-native';
import { Feather } from '@expo/vector-icons';
import * as Haptics from 'expo-haptics';

import { alpha, tokens } from './tokens';

export type IconName = React.ComponentProps<typeof Feather>['name'];
export type Tone = 'default' | 'muted' | 'faint' | 'accent' | 'raoul' | 'ok' | 'warn' | 'danger' | 'thinking';

const TONES: Record<Tone, string> = {
  default: tokens.colors.text,
  muted: tokens.colors.text2,
  faint: tokens.colors.text3,
  accent: tokens.colors.accent,
  raoul: tokens.colors.raoul,
  ok: tokens.colors.ok,
  warn: tokens.colors.warn,
  danger: tokens.colors.danger,
  thinking: tokens.colors.thinking,
};

export function toneColor(tone: Tone): string {
  return TONES[tone];
}

/* -------------------------------------------------------------------------- */
/* Texte                                                                       */
/* -------------------------------------------------------------------------- */

type Variant = keyof typeof tokens.type;

export function T({
  v = 'body',
  tone = 'default',
  style,
  ...rest
}: TextProps & { v?: Variant; tone?: Tone }) {
  return <Text {...rest} style={[tokens.type[v], { color: TONES[tone] }, style]} />;
}

/** Libellé de section : mono, capitales espacées. */
export function Label({ children, tone = 'faint', style }: { children: React.ReactNode; tone?: Tone; style?: TextProps['style'] }) {
  return (
    <Text style={[tokens.type.label, { color: TONES[tone], textTransform: 'uppercase' }, style]} accessibilityRole="header">
      {children}
    </Text>
  );
}

/** Une ligne de relevé : clé à gauche en mono, valeur à droite. */
export function Readout({ k, v, tone = 'muted' }: { k: string; v: string; tone?: Tone }) {
  return (
    <View style={styles.readout}>
      <Text style={[tokens.type.label, { color: tokens.colors.text3, textTransform: 'uppercase' }]}>{k}</Text>
      <Text style={[tokens.type.mono, { color: TONES[tone] }]} numberOfLines={1}>
        {v}
      </Text>
    </View>
  );
}

/* -------------------------------------------------------------------------- */
/* Surfaces                                                                    */
/* -------------------------------------------------------------------------- */

type PanelProps = ViewProps & {
  /** Teinte du contour, pour porter un état. */
  tone?: Tone;
  /** Les quatre équerres d'angle, signature des panneaux importants. */
  corners?: boolean;
  raised?: boolean;
  padded?: boolean;
};

/**
 * Un panneau : une surface opaque, un trait fin, rien d'autre. Pas de flou,
 * pas d'ombre — sur un fond nocturne, la profondeur vient du contraste entre
 * deux plats et de la précision du trait.
 */
export function Panel({ tone, corners, raised, padded = true, style, children, ...rest }: PanelProps) {
  const border = tone ? alpha(TONES[tone], 0.45) : tokens.colors.line;
  return (
    <View
      {...rest}
      style={[
        styles.panel,
        raised && styles.panelRaised,
        padded && styles.panelPadded,
        { borderColor: border },
        style,
      ]}
    >
      {corners ? <Corners color={tone ? TONES[tone] : tokens.colors.lineStrong} /> : null}
      {children}
    </View>
  );
}

/** Quatre équerres, comme les repères d'un viseur. */
export function Corners({ color = tokens.colors.lineStrong, size = 10 }: { color?: string; size?: number }) {
  const base: ViewStyle = { position: 'absolute', width: size, height: size, borderColor: color };
  return (
    <View style={StyleSheet.absoluteFill} pointerEvents="none">
      <View style={[base, { top: -1, left: -1, borderTopWidth: 1.5, borderLeftWidth: 1.5 }]} />
      <View style={[base, { top: -1, right: -1, borderTopWidth: 1.5, borderRightWidth: 1.5 }]} />
      <View style={[base, { bottom: -1, left: -1, borderBottomWidth: 1.5, borderLeftWidth: 1.5 }]} />
      <View style={[base, { bottom: -1, right: -1, borderBottomWidth: 1.5, borderRightWidth: 1.5 }]} />
    </View>
  );
}

export function Divider({ style }: { style?: ViewStyle }) {
  return <View style={[styles.divider, style]} />;
}

/** Titre d'une section d'écran, avec une action facultative à droite. */
export function Section({
  title,
  count,
  right,
  children,
}: {
  title: string;
  count?: number;
  right?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <View style={styles.section}>
      <View style={styles.sectionHead}>
        <Label>{title}</Label>
        {typeof count === 'number' && count > 0 ? <Counter n={count} /> : null}
        <View style={styles.flex} />
        {right}
      </View>
      {children}
    </View>
  );
}

export function Counter({ n, tone = 'accent' }: { n: number; tone?: Tone }) {
  return (
    <View style={[styles.counter, { backgroundColor: alpha(TONES[tone], 0.16) }]}>
      <Text style={[tokens.type.label, { color: TONES[tone] }]}>{n}</Text>
    </View>
  );
}

/* -------------------------------------------------------------------------- */
/* Boutons                                                                     */
/* -------------------------------------------------------------------------- */

type ButtonProps = {
  label: string;
  onPress: () => void;
  variant?: 'primary' | 'secondary' | 'ghost' | 'danger';
  icon?: IconName;
  loading?: boolean;
  disabled?: boolean;
  compact?: boolean;
};

export function Button({ label, onPress, variant = 'primary', icon, loading, disabled, compact }: ButtonProps) {
  const inactive = Boolean(disabled || loading);
  const fg =
    variant === 'primary'
      ? tokens.colors.onAccent
      : variant === 'danger'
        ? tokens.colors.danger
        : variant === 'ghost'
          ? tokens.colors.text2
          : tokens.colors.text;
  return (
    <Pressable
      onPress={() => {
        void Haptics.impactAsync(Haptics.ImpactFeedbackStyle.Light);
        onPress();
      }}
      disabled={inactive}
      accessibilityRole="button"
      accessibilityLabel={label}
      accessibilityState={{ disabled: inactive, busy: Boolean(loading) }}
      style={({ pressed }) => [
        styles.button,
        compact && styles.buttonCompact,
        variant === 'primary' && styles.buttonPrimary,
        variant === 'secondary' && styles.buttonSecondary,
        variant === 'danger' && styles.buttonDanger,
        pressed && styles.pressed,
        inactive && styles.disabled,
      ]}
    >
      {loading ? (
        <ActivityIndicator color={fg} size="small" />
      ) : (
        <>
          {icon ? <Feather name={icon} size={15} color={fg} /> : null}
          <Text style={[tokens.type.smallStrong, { color: fg }]}>{label}</Text>
        </>
      )}
    </Pressable>
  );
}

export function IconButton({
  icon,
  onPress,
  label,
  tone = 'muted',
  size = 18,
  disabled,
  filled,
}: {
  icon: IconName;
  onPress: () => void;
  label: string;
  tone?: Tone;
  size?: number;
  disabled?: boolean;
  filled?: boolean;
}) {
  return (
    <Pressable
      onPress={() => {
        void Haptics.selectionAsync();
        onPress();
      }}
      disabled={disabled}
      hitSlop={6}
      accessibilityRole="button"
      accessibilityLabel={label}
      accessibilityState={{ disabled: Boolean(disabled) }}
      style={({ pressed }) => [
        styles.iconButton,
        filled && { backgroundColor: TONES[tone] },
        pressed && styles.pressed,
        disabled && styles.disabled,
      ]}
    >
      <Feather name={icon} size={size} color={filled ? tokens.colors.onAccent : TONES[tone]} />
    </Pressable>
  );
}

/** Petite pastille d'action ou de filtre. */
export function Chip({
  label,
  icon,
  onPress,
  active,
  tone = 'muted',
}: {
  label: string;
  icon?: IconName;
  onPress?: () => void;
  active?: boolean;
  tone?: Tone;
}) {
  const color = active ? tokens.colors.accent : TONES[tone];
  const inner = (
    <>
      {icon ? <Feather name={icon} size={12} color={color} /> : null}
      <Text style={[tokens.type.mono, { color }]}>{label}</Text>
    </>
  );
  if (!onPress) {
    return <View style={[styles.chip, active && styles.chipActive]}>{inner}</View>;
  }
  return (
    <Pressable
      onPress={() => {
        void Haptics.selectionAsync();
        onPress();
      }}
      accessibilityRole="button"
      accessibilityLabel={label}
      accessibilityState={{ selected: Boolean(active) }}
      style={({ pressed }) => [styles.chip, active && styles.chipActive, pressed && styles.pressed]}
    >
      {inner}
    </Pressable>
  );
}

/* -------------------------------------------------------------------------- */
/* Formulaire                                                                  */
/* -------------------------------------------------------------------------- */

type FieldProps = TextInputProps & { label?: string; hint?: string; error?: string; icon?: IconName };

export function Field({ label, hint, error, icon, style, ...rest }: FieldProps) {
  const [focused, setFocused] = React.useState(false);
  const border = error ? tokens.colors.danger : focused ? tokens.colors.accent : tokens.colors.line;
  return (
    <View style={styles.field}>
      {label ? <Label tone="muted">{label}</Label> : null}
      <View style={[styles.inputWrap, { borderColor: border }]}>
        {icon ? <Feather name={icon} size={15} color={tokens.colors.text3} /> : null}
        <TextInput
          placeholderTextColor={tokens.colors.text3}
          autoCapitalize="none"
          autoCorrect={false}
          accessibilityLabel={label}
          {...rest}
          onFocus={(e) => {
            setFocused(true);
            rest.onFocus?.(e);
          }}
          onBlur={(e) => {
            setFocused(false);
            rest.onBlur?.(e);
          }}
          style={[styles.input, style]}
        />
      </View>
      {error ? (
        <T v="mono" tone="danger">
          {error}
        </T>
      ) : hint ? (
        <T v="mono" tone="faint">
          {hint}
        </T>
      ) : null}
    </View>
  );
}

/* -------------------------------------------------------------------------- */
/* Indicateurs                                                                 */
/* -------------------------------------------------------------------------- */

export function Dot({ tone, glow }: { tone: Tone; glow?: boolean }) {
  const color = TONES[tone];
  return (
    <View style={[styles.dotHalo, glow && { backgroundColor: alpha(color, 0.2) }]}>
      <View style={[styles.dot, { backgroundColor: color }]} />
    </View>
  );
}

export function Badge({ label, tone = 'muted' }: { label: string; tone?: Tone }) {
  const color = TONES[tone];
  return (
    <View style={[styles.badge, { borderColor: alpha(color, 0.5), backgroundColor: alpha(color, 0.1) }]}>
      <Text style={[tokens.type.label, { color, textTransform: 'uppercase' }]} numberOfLines={1}>
        {label}
      </Text>
    </View>
  );
}

export function Notice({
  tone,
  icon,
  title,
  children,
}: {
  tone: 'info' | 'warn' | 'danger' | 'ok';
  icon: IconName;
  title?: string;
  children?: React.ReactNode;
}) {
  const t: Tone = tone === 'info' ? 'accent' : tone;
  const color = TONES[t];
  return (
    <View style={[styles.notice, { borderColor: alpha(color, 0.4), backgroundColor: alpha(color, 0.06) }]}>
      <Feather name={icon} size={15} color={color} style={styles.noticeIcon} />
      <View style={styles.flex}>
        {title ? (
          <T v="smallStrong" style={{ color }}>
            {title}
          </T>
        ) : null}
        {children}
      </View>
    </View>
  );
}

export function Empty({ icon, title, message }: { icon: IconName; title: string; message?: string }) {
  return (
    <View style={styles.empty}>
      <View style={styles.emptyIcon}>
        <Feather name={icon} size={20} color={tokens.colors.text3} />
      </View>
      <T v="bodyStrong" tone="muted">
        {title}
      </T>
      {message ? (
        <T v="small" tone="faint" style={styles.centered}>
          {message}
        </T>
      ) : null}
    </View>
  );
}

/** Un bloc qui respire pendant qu'on attend : mieux qu'une roue seule. */
export function Skeleton({ lines = 3, style }: { lines?: number; style?: ViewStyle }) {
  const shimmer = useRef(new Animated.Value(0.35)).current;
  useEffect(() => {
    const loop = Animated.loop(
      Animated.sequence([
        Animated.timing(shimmer, { toValue: 0.8, duration: 700, easing: Easing.inOut(Easing.ease), useNativeDriver: true }),
        Animated.timing(shimmer, { toValue: 0.35, duration: 700, easing: Easing.inOut(Easing.ease), useNativeDriver: true }),
      ]),
    );
    loop.start();
    return () => loop.stop();
  }, [shimmer]);
  return (
    <View style={[styles.skeleton, style]} accessibilityLabel="Chargement">
      {Array.from({ length: lines }).map((_, i) => (
        <Animated.View
          key={i}
          style={[styles.skeletonLine, { opacity: shimmer, width: `${i === lines - 1 ? 55 : 90 - i * 8}%` }]}
        />
      ))}
    </View>
  );
}

/* -------------------------------------------------------------------------- */
/* En-tête d'écran                                                             */
/* -------------------------------------------------------------------------- */

export function ScreenHeader({
  title,
  subtitle,
  onBack,
  right,
}: {
  title: string;
  subtitle?: string;
  onBack?: () => void;
  right?: React.ReactNode;
}) {
  return (
    <View style={styles.header}>
      {onBack ? <IconButton icon="arrow-left" label="Retour" onPress={onBack} tone="default" /> : null}
      <View style={styles.flex}>
        <T v="title" accessibilityRole="header">
          {title}
        </T>
        {subtitle ? (
          <T v="mono" tone="faint">
            {subtitle}
          </T>
        ) : null}
      </View>
      {right}
    </View>
  );
}

/* -------------------------------------------------------------------------- */

const styles = StyleSheet.create({
  flex: { flex: 1 },
  centered: { textAlign: 'center' },
  pressed: { opacity: 0.65 },
  disabled: { opacity: 0.4 },

  readout: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', gap: tokens.space.md },

  panel: {
    backgroundColor: tokens.colors.panel,
    borderWidth: 1,
    borderRadius: tokens.radius.lg,
  },
  panelRaised: { backgroundColor: tokens.colors.panelRaised },
  panelPadded: { padding: tokens.space.lg, gap: tokens.space.md },
  divider: { height: 1, backgroundColor: tokens.colors.line },

  section: { gap: tokens.space.sm },
  sectionHead: { flexDirection: 'row', alignItems: 'center', gap: tokens.space.sm, minHeight: 24 },
  counter: { minWidth: 20, paddingHorizontal: 6, paddingVertical: 2, borderRadius: tokens.radius.pill, alignItems: 'center' },

  button: {
    minHeight: tokens.touch,
    paddingHorizontal: tokens.space.lg,
    borderRadius: tokens.radius.md,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    gap: tokens.space.sm,
    borderWidth: 1,
    borderColor: 'transparent',
  },
  buttonCompact: { minHeight: 36, paddingHorizontal: tokens.space.md },
  buttonPrimary: { backgroundColor: tokens.colors.accent },
  buttonSecondary: { backgroundColor: tokens.colors.panelRaised, borderColor: tokens.colors.lineStrong },
  buttonDanger: { backgroundColor: alpha(tokens.colors.danger, 0.08), borderColor: alpha(tokens.colors.danger, 0.45) },
  iconButton: {
    width: tokens.touch - 6,
    height: tokens.touch - 6,
    borderRadius: (tokens.touch - 6) / 2,
    alignItems: 'center',
    justifyContent: 'center',
  },

  chip: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 6,
    paddingHorizontal: tokens.space.md,
    paddingVertical: tokens.space.sm,
    borderRadius: tokens.radius.pill,
    borderWidth: 1,
    borderColor: tokens.colors.line,
    backgroundColor: tokens.colors.panelRaised,
  },
  chipActive: { borderColor: alpha(tokens.colors.accent, 0.6), backgroundColor: alpha(tokens.colors.accent, 0.1) },

  field: { gap: tokens.space.sm },
  inputWrap: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: tokens.space.sm,
    borderWidth: 1,
    borderRadius: tokens.radius.md,
    backgroundColor: tokens.colors.bgDeep,
    paddingHorizontal: tokens.space.md,
  },
  input: {
    flex: 1,
    minHeight: tokens.touch,
    color: tokens.colors.text,
    ...tokens.type.body,
    paddingVertical: tokens.space.sm,
  },

  dotHalo: { width: 16, height: 16, borderRadius: 8, alignItems: 'center', justifyContent: 'center' },
  dot: { width: 7, height: 7, borderRadius: 4 },
  badge: { paddingHorizontal: 8, paddingVertical: 3, borderRadius: tokens.radius.sm, borderWidth: 1 },

  notice: { flexDirection: 'row', gap: tokens.space.md, padding: tokens.space.md, borderWidth: 1, borderRadius: tokens.radius.md },
  noticeIcon: { marginTop: 2 },

  empty: { alignItems: 'center', gap: tokens.space.sm, paddingVertical: tokens.space.xl },
  emptyIcon: {
    width: 48,
    height: 48,
    borderRadius: 24,
    borderWidth: 1,
    borderColor: tokens.colors.line,
    alignItems: 'center',
    justifyContent: 'center',
    marginBottom: tokens.space.xs,
  },

  skeleton: { gap: tokens.space.sm, padding: tokens.space.lg },
  skeletonLine: { height: 12, borderRadius: 6, backgroundColor: tokens.colors.panelPressed },

  header: { flexDirection: 'row', alignItems: 'center', gap: tokens.space.md, paddingVertical: tokens.space.sm },
});
