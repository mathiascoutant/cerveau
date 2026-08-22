import React, { useRef } from 'react';
import {
  ActivityIndicator,
  Animated,
  Pressable,
  StyleSheet,
  Text,
  TextInput,
  TextInputProps,
  TextProps,
  View,
  ViewProps,
} from 'react-native';
import { Feather } from '@expo/vector-icons';

import { theme } from '../theme';

export type IconName = React.ComponentProps<typeof Feather>['name'];

/* -------------------------------------------------------------------------- */
/* Texte                                                                       */
/* -------------------------------------------------------------------------- */

type Variant = keyof typeof theme.type;
type Tone = 'default' | 'muted' | 'faint' | 'primary' | 'success' | 'warning' | 'danger';

const TONES: Record<Tone, string> = {
  default: theme.colors.text,
  muted: theme.colors.textMuted,
  faint: theme.colors.textFaint,
  primary: theme.colors.primary,
  success: theme.colors.success,
  warning: theme.colors.warning,
  danger: theme.colors.danger,
};

export function Txt({
  variant = 'body',
  tone = 'default',
  style,
  ...rest
}: TextProps & { variant?: Variant; tone?: Tone }) {
  const t = theme.type[variant];
  return (
    <Text
      {...rest}
      style={[
        { fontFamily: t.font, fontSize: t.fontSize, lineHeight: t.lineHeight, color: TONES[tone] },
        style,
      ]}
    />
  );
}

/** Libellé de section : capitales espacées, discret mais lisible. */
export function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <Text style={styles.sectionLabel} accessibilityRole="header">
      {children}
    </Text>
  );
}

/* -------------------------------------------------------------------------- */
/* Conteneurs                                                                  */
/* -------------------------------------------------------------------------- */

export function Card({ style, children, ...rest }: ViewProps) {
  return (
    <View style={[styles.card, style]} {...rest}>
      {children}
    </View>
  );
}

/** En-tête d'écran : titre large et sous-titre optionnel. */
export function ScreenHeader({ title, subtitle }: { title: string; subtitle?: string }) {
  return (
    <View style={styles.screenHeader}>
      <Txt variant="display" accessibilityRole="header">
        {title}
      </Txt>
      {subtitle ? <Txt variant="small" tone="muted">{subtitle}</Txt> : null}
    </View>
  );
}

/** Séparateur fin, visible sans découper la carte en deux. */
export function Divider() {
  return <View style={styles.divider} />;
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
};

/**
 * Le retour au toucher passe par une mise à l'échelle légère plutôt qu'un
 * changement de disposition : la carte ne bouge pas, seul le bouton réagit.
 */
export function Button({
  label,
  onPress,
  variant = 'primary',
  icon,
  loading,
  disabled,
}: ButtonProps) {
  const scale = useRef(new Animated.Value(1)).current;
  const inactive = Boolean(disabled || loading);

  const animate = (to: number) =>
    Animated.spring(scale, {
      toValue: to,
      useNativeDriver: true,
      speed: 40,
      bounciness: 4,
    }).start();

  const fg =
    variant === 'primary'
      ? theme.colors.onPrimary
      : variant === 'danger'
        ? theme.colors.danger
        : theme.colors.text;

  return (
    <Animated.View style={{ transform: [{ scale }] }}>
      <Pressable
        onPress={onPress}
        onPressIn={() => animate(0.97)}
        onPressOut={() => animate(1)}
        disabled={inactive}
        accessibilityRole="button"
        accessibilityLabel={label}
        accessibilityState={{ disabled: inactive, busy: Boolean(loading) }}
        style={({ pressed }) => [
          styles.button,
          variant === 'primary' && styles.buttonPrimary,
          variant === 'secondary' && styles.buttonSecondary,
          variant === 'ghost' && styles.buttonGhost,
          variant === 'danger' && styles.buttonDanger,
          pressed && styles.buttonPressed,
          inactive && styles.buttonDisabled,
        ]}
      >
        {loading ? (
          <ActivityIndicator color={fg} size="small" />
        ) : (
          <>
            {icon ? <Feather name={icon} size={16} color={fg} /> : null}
            <Text style={[styles.buttonLabel, { color: fg }]}>{label}</Text>
          </>
        )}
      </Pressable>
    </Animated.View>
  );
}

/* -------------------------------------------------------------------------- */
/* Formulaire                                                                  */
/* -------------------------------------------------------------------------- */

type FieldProps = TextInputProps & {
  /** Libellé visible au-dessus du champ. Un placeholder seul ne suffit pas :
   *  il disparaît dès la saisie et l'utilisateur perd le contexte. */
  label: string;
  hint?: string;
  error?: string;
};

export function Field({ label, hint, error, style, ...rest }: FieldProps) {
  const [focused, setFocused] = React.useState(false);
  return (
    <View style={styles.field}>
      <Text style={styles.fieldLabel}>{label}</Text>
      <TextInput
        placeholderTextColor={theme.colors.textFaint}
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
        style={[
          styles.input,
          focused && styles.inputFocused,
          Boolean(error) && styles.inputError,
          style,
        ]}
      />
      {error ? (
        <View style={styles.fieldFooter}>
          <Feather name="alert-circle" size={12} color={theme.colors.danger} />
          <Txt variant="mono" tone="danger">
            {error}
          </Txt>
        </View>
      ) : hint ? (
        <Txt variant="mono" tone="faint">
          {hint}
        </Txt>
      ) : null}
    </View>
  );
}

/* -------------------------------------------------------------------------- */
/* Indicateurs                                                                 */
/* -------------------------------------------------------------------------- */

export type DotState = 'on' | 'off' | 'warn';

/**
 * L'état ne repose pas sur la seule couleur : la pastille est doublée d'un
 * libellé chez tous les appelants, et le halo distingue l'état actif même en
 * vision monochrome.
 */
export function StatusDot({ state }: { state: DotState }) {
  const color =
    state === 'warn'
      ? theme.colors.warning
      : state === 'on'
        ? theme.colors.success
        : theme.colors.textFaint;
  return (
    <View style={[styles.dotHalo, state === 'on' && { backgroundColor: `${color}22` }]}>
      <View style={[styles.dot, { backgroundColor: color }]} />
    </View>
  );
}

/** Pastille compacte : icône, libellé, compteur optionnel. */
export function Chip({
  icon,
  label,
  count,
  state,
}: {
  icon: IconName;
  label: string;
  count?: number;
  state: DotState;
}) {
  const tint =
    state === 'warn'
      ? theme.colors.warning
      : state === 'on'
        ? theme.colors.primary
        : theme.colors.textFaint;
  return (
    <View
      style={[styles.chip, state === 'on' && styles.chipActive]}
      accessibilityLabel={`${label}${count ? `, ${count}` : ''}${state === 'on' ? ', connecté' : ', non connecté'}`}
    >
      <Feather name={icon} size={13} color={tint} />
      <Text style={[styles.chipLabel, state === 'on' && { color: theme.colors.text }]}>{label}</Text>
      {count ? <Text style={styles.chipCount}>{count}</Text> : null}
    </View>
  );
}

/** Tuile de statistique pour l'écran Journal. */
export function StatTile({ value, label, icon }: { value: number; label: string; icon: IconName }) {
  return (
    <View style={styles.stat} accessibilityLabel={`${value} ${label}`}>
      <Feather name={icon} size={15} color={theme.colors.textMuted} />
      <Text style={styles.statValue}>{value}</Text>
      <Text style={styles.statLabel}>{label}</Text>
    </View>
  );
}

/** État vide : jamais un écran nu, toujours une explication. */
export function EmptyState({
  icon,
  title,
  message,
}: {
  icon: IconName;
  title: string;
  message: string;
}) {
  return (
    <View style={styles.empty}>
      <View style={styles.emptyIcon}>
        <Feather name={icon} size={20} color={theme.colors.textFaint} />
      </View>
      <Txt variant="bodyStrong" tone="muted">
        {title}
      </Txt>
      <Txt variant="small" tone="faint" style={{ textAlign: 'center' }}>
        {message}
      </Txt>
    </View>
  );
}

/** Bandeau d'information ou d'erreur. */
export function Banner({
  tone,
  icon,
  children,
}: {
  tone: 'warning' | 'danger' | 'info';
  icon: IconName;
  children: React.ReactNode;
}) {
  const color =
    tone === 'danger'
      ? theme.colors.danger
      : tone === 'warning'
        ? theme.colors.warning
        : theme.colors.primary;
  return (
    <View style={[styles.banner, { borderColor: `${color}55`, backgroundColor: `${color}12` }]}>
      <Feather name={icon} size={15} color={color} style={{ marginTop: 2 }} />
      <View style={{ flex: 1, gap: theme.space.xs }}>{children}</View>
    </View>
  );
}

/* -------------------------------------------------------------------------- */

const styles = StyleSheet.create({
  sectionLabel: {
    fontFamily: theme.type.label.font,
    fontSize: theme.type.label.fontSize,
    lineHeight: theme.type.label.lineHeight,
    letterSpacing: 1.1,
    textTransform: 'uppercase',
    color: theme.colors.textFaint,
  },
  card: {
    backgroundColor: theme.colors.surface,
    borderRadius: theme.radius.lg,
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: theme.colors.border,
    padding: theme.space.lg,
    gap: theme.space.md,
  },
  screenHeader: { gap: theme.space.xs, marginBottom: theme.space.xs },
  divider: {
    height: StyleSheet.hairlineWidth,
    backgroundColor: theme.colors.border,
    marginVertical: theme.space.xs,
  },

  button: {
    minHeight: theme.touchMin,
    borderRadius: theme.radius.md,
    paddingHorizontal: theme.space.lg,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    gap: theme.space.sm,
  },
  buttonPrimary: { backgroundColor: theme.colors.primary },
  buttonSecondary: {
    backgroundColor: theme.colors.surfaceRaised,
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: theme.colors.borderStrong,
  },
  buttonGhost: { backgroundColor: 'transparent' },
  buttonDanger: {
    backgroundColor: 'transparent',
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: `${theme.colors.danger}66`,
  },
  buttonPressed: { opacity: 0.85 },
  buttonDisabled: { opacity: 0.45 },
  buttonLabel: { fontFamily: theme.type.bodyStrong.font, fontSize: 15 },

  field: { gap: theme.space.sm },
  fieldLabel: {
    fontFamily: theme.type.label.font,
    fontSize: theme.type.label.fontSize,
    color: theme.colors.textMuted,
    letterSpacing: 0.4,
  },
  input: {
    minHeight: theme.touchMin,
    backgroundColor: theme.colors.surfaceRaised,
    borderRadius: theme.radius.md,
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: theme.colors.border,
    color: theme.colors.text,
    fontFamily: theme.type.body.font,
    fontSize: theme.type.body.fontSize,
    paddingHorizontal: theme.space.lg,
    paddingVertical: theme.space.md,
  },
  inputFocused: { borderColor: theme.colors.primary, backgroundColor: theme.colors.surfaceActive },
  inputError: { borderColor: theme.colors.danger },
  fieldFooter: { flexDirection: 'row', alignItems: 'center', gap: theme.space.xs },

  dotHalo: {
    width: 16,
    height: 16,
    borderRadius: 8,
    alignItems: 'center',
    justifyContent: 'center',
  },
  dot: { width: 7, height: 7, borderRadius: 4 },

  chip: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: theme.space.xs + 2,
    backgroundColor: theme.colors.surface,
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: theme.colors.border,
    borderRadius: theme.radius.pill,
    paddingHorizontal: theme.space.md,
    paddingVertical: theme.space.sm,
  },
  chipActive: { borderColor: `${theme.colors.primary}55` },
  chipLabel: {
    fontFamily: theme.type.small.font,
    fontSize: 13,
    color: theme.colors.textFaint,
  },
  chipCount: {
    fontFamily: theme.type.label.font,
    fontSize: 11,
    color: theme.colors.primary,
    fontVariant: ['tabular-nums'],
  },

  stat: {
    flex: 1,
    backgroundColor: theme.colors.surface,
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: theme.colors.border,
    borderRadius: theme.radius.md,
    paddingVertical: theme.space.md,
    alignItems: 'center',
    gap: 2,
  },
  statValue: {
    fontFamily: theme.type.title.font,
    fontSize: 20,
    color: theme.colors.text,
    fontVariant: ['tabular-nums'],
  },
  statLabel: { fontFamily: theme.type.mono.font, fontSize: 11, color: theme.colors.textFaint },

  empty: { alignItems: 'center', gap: theme.space.sm, paddingVertical: theme.space.xl },
  emptyIcon: {
    width: 44,
    height: 44,
    borderRadius: 22,
    backgroundColor: theme.colors.surfaceRaised,
    alignItems: 'center',
    justifyContent: 'center',
    marginBottom: theme.space.xs,
  },

  banner: {
    flexDirection: 'row',
    gap: theme.space.md,
    borderWidth: StyleSheet.hairlineWidth,
    borderRadius: theme.radius.md,
    padding: theme.space.md,
  },
});
