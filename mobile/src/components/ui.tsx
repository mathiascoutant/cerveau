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
import { LinearGradient } from 'expo-linear-gradient';
import * as Haptics from 'expo-haptics';

import { Glass, GlassCard } from './glass';
import { alpha, theme } from '../theme';

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

/** Carte de contenu : une plaque de verre au rythme interne fixé. */
export function Card({ style, children, ...rest }: ViewProps) {
  return (
    <GlassCard style={style} {...rest}>
      {children}
    </GlassCard>
  );
}

/** En-tête d'écran : titre large et sous-titre optionnel. */
export function ScreenHeader({ title, subtitle }: { title: string; subtitle?: string }) {
  return (
    <View style={styles.screenHeader}>
      <Txt variant="display" accessibilityRole="header">
        {title}
      </Txt>
      {subtitle ? (
        <Txt variant="small" tone="muted">
          {subtitle}
        </Txt>
      ) : null}
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
 * changement de disposition : la carte ne bouge pas, seul le bouton réagit. Un
 * retour haptique le double — sur verre, l'écrasement visuel est discret, la
 * vibration confirme que l'appui a été pris.
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
      ...theme.motion.spring,
    }).start();

  const fg =
    variant === 'primary'
      ? theme.colors.onPrimary
      : variant === 'danger'
        ? theme.colors.danger
        : theme.colors.text;

  const inner = loading ? (
    <ActivityIndicator color={fg} size="small" />
  ) : (
    <>
      {icon ? <Feather name={icon} size={16} color={fg} /> : null}
      <Text style={[styles.buttonLabel, { color: fg }]}>{label}</Text>
    </>
  );

  const press = (
    <Pressable
      onPress={() => {
        void Haptics.impactAsync(Haptics.ImpactFeedbackStyle.Light);
        onPress();
      }}
      onPressIn={() => animate(0.97)}
      onPressOut={() => animate(1)}
      disabled={inactive}
      accessibilityRole="button"
      accessibilityLabel={label}
      accessibilityState={{ disabled: inactive, busy: Boolean(loading) }}
      style={({ pressed }) => [styles.button, pressed && styles.buttonPressed]}
    >
      {inner}
    </Pressable>
  );

  return (
    <Animated.View style={[{ transform: [{ scale }] }, inactive && styles.buttonDisabled]}>
      {variant === 'primary' ? (
        <View style={styles.buttonGlowWrap}>
          <LinearGradient
            colors={[theme.colors.primary, theme.colors.primaryDim]}
            start={{ x: 0, y: 0 }}
            end={{ x: 1, y: 1 }}
            style={styles.buttonFill}
          >
            {press}
          </LinearGradient>
        </View>
      ) : variant === 'ghost' ? (
        press
      ) : (
        <Glass
          radius={theme.radius.md}
          variant="plain"
          tone={variant === 'danger' ? theme.colors.danger : undefined}
        >
          {press}
        </Glass>
      )}
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
  const tone = error ? theme.colors.danger : focused ? theme.colors.primary : undefined;
  return (
    <View style={styles.field}>
      <Text style={styles.fieldLabel}>{label}</Text>
      <Glass radius={theme.radius.md} variant="plain" tone={tone} sheen={false}>
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
          style={[styles.input, style]}
        />
      </Glass>
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
    <View style={[styles.dotHalo, state === 'on' && { backgroundColor: alpha(color, 0.18) }]}>
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
    <Glass
      radius={theme.radius.pill}
      variant="subtle"
      tone={state === 'on' ? theme.colors.primary : undefined}
      style={styles.chip}
      accessibilityLabel={`${label}${count ? `, ${count}` : ''}${state === 'on' ? ', connecté' : ', non connecté'}`}
    >
      <Feather name={icon} size={13} color={tint} />
      <Text style={[styles.chipLabel, state === 'on' && { color: theme.colors.text }]}>{label}</Text>
      {count ? <Text style={styles.chipCount}>{count}</Text> : null}
    </Glass>
  );
}

/** Tuile de statistique pour l'écran Journal. */
export function StatTile({ value, label, icon }: { value: number; label: string; icon: IconName }) {
  return (
    <Glass radius={theme.radius.md} style={styles.stat} accessibilityLabel={`${value} ${label}`}>
      <Feather name={icon} size={15} color={theme.colors.textMuted} />
      <Text style={styles.statValue}>{value}</Text>
      <Text style={styles.statLabel}>{label}</Text>
    </Glass>
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
      <Glass radius={theme.radius.pill} style={styles.emptyIcon}>
        <Feather name={icon} size={20} color={theme.colors.textFaint} />
      </Glass>
      <Txt variant="bodyStrong" tone="muted">
        {title}
      </Txt>
      <Txt variant="small" tone="faint" style={styles.centered}>
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
    <Glass radius={theme.radius.md} tone={color} style={styles.banner}>
      <Feather name={icon} size={15} color={color} style={styles.bannerIcon} />
      <View style={styles.bannerBody}>{children}</View>
    </Glass>
  );
}

/* -------------------------------------------------------------------------- */

const styles = StyleSheet.create({
  centered: { textAlign: 'center' },
  sectionLabel: {
    fontFamily: theme.type.label.font,
    fontSize: theme.type.label.fontSize,
    lineHeight: theme.type.label.lineHeight,
    letterSpacing: 1.1,
    textTransform: 'uppercase',
    color: theme.colors.textFaint,
  },
  screenHeader: { gap: theme.space.xs, marginBottom: theme.space.xs },
  divider: {
    height: StyleSheet.hairlineWidth,
    backgroundColor: theme.colors.border,
    marginVertical: theme.space.xs,
  },

  button: {
    minHeight: theme.touchMin,
    paddingHorizontal: theme.space.lg,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    gap: theme.space.sm,
  },
  buttonFill: { borderRadius: theme.radius.md, overflow: 'hidden' },
  // Le halo du bouton principal : une lueur teal juste sous la plaque, qui le
  // désigne comme l'action de l'écran sans avoir à grossir.
  buttonGlowWrap: {
    borderRadius: theme.radius.md,
    shadowColor: theme.colors.primary,
    shadowOpacity: 0.45,
    shadowRadius: 18,
    shadowOffset: { width: 0, height: 6 },
    elevation: 6,
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
    color: theme.colors.text,
    fontFamily: theme.type.body.font,
    fontSize: theme.type.body.fontSize,
    paddingHorizontal: theme.space.lg,
    paddingVertical: theme.space.md,
  },
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
    paddingHorizontal: theme.space.md,
    paddingVertical: theme.space.sm,
  },
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
    width: 48,
    height: 48,
    alignItems: 'center',
    justifyContent: 'center',
    marginBottom: theme.space.xs,
  },

  banner: {
    flexDirection: 'row',
    gap: theme.space.md,
    padding: theme.space.md,
  },
  bannerIcon: { marginTop: 2 },
  bannerBody: { flex: 1, gap: theme.space.xs },
});
