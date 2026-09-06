import React from 'react';
import { StyleSheet, View, ViewProps } from 'react-native';
import { BlurView } from 'expo-blur';
import { LinearGradient } from 'expo-linear-gradient';

import { alpha, theme } from '../theme';

/**
 * `plain` ne floute rien : il pose le voile, le liseré et le cadre, sans
 * couche de flou. C'est la variante des éléments posés SUR une autre plaque —
 * un champ dans une carte, un bouton dans une carte. Deux flous empilés ne
 * rendent pas l'image plus profonde, ils coûtent deux fois le prix et délavent
 * le texte.
 */
type Variant = 'card' | 'chrome' | 'subtle' | 'plain';

const FILL: Record<Variant, string> = {
  card: theme.colors.glass,
  chrome: theme.colors.glassChrome,
  subtle: 'rgba(255,255,255,0.03)',
  plain: theme.colors.glassRaised,
};

const INTENSITY: Record<Variant, number> = {
  card: theme.blur.card,
  chrome: theme.blur.chrome,
  subtle: theme.blur.subtle,
  plain: 0,
};

export type GlassProps = ViewProps & {
  variant?: Variant;
  radius?: number;
  /** Teinte mêlée au verre, pour porter un état (danger, succès, actif). */
  tone?: string;
  /** Le liseré clair de l'arête supérieure. Actif par défaut. */
  sheen?: boolean;
};

/**
 * Une plaque de verre.
 *
 * Trois couches, toujours dans cet ordre : le flou, puis un voile teinté qui
 * garantit le contraste du texte quoi qu'il y ait derrière, puis un liseré
 * clair en haut qui donne son épaisseur à la plaque.
 */
export function Glass({
  variant = 'card',
  radius = theme.radius.lg,
  tone,
  sheen = true,
  style,
  children,
  ...rest
}: GlassProps) {
  const frame = {
    borderRadius: radius,
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: tone ? alpha(tone, 0.35) : theme.colors.border,
    // Sans overflow caché, ni le flou ni le liseré ne suivent l'arrondi.
    overflow: 'hidden' as const,
  };

  // Sans flou : le voile suffit, et il n'y a rien derrière qui vaille d'être
  // flouté puisque la plaque du dessous l'a déjà fait.
  if (variant === 'plain') {
    return (
      <View
        style={[frame, { backgroundColor: tone ? alpha(tone, 0.12) : FILL.plain }, style]}
        {...rest}
      >
        {sheen ? <Sheen /> : null}
        {children}
      </View>
    );
  }

  return (
    <View style={[frame, style]} {...rest}>
      <BlurView
        intensity={INTENSITY[variant]}
        tint="dark"
        // Le flou natif d'Android reste expérimental et coûte cher : on ne le
        // demande que pour les barres fixes, où le contenu défile dessous et
        // où l'absence de flou rendrait les deux plans illisibles ensemble.
        // Ailleurs, expo-blur pose un voile translucide, ce qui suffit.
        experimentalBlurMethod={variant === 'chrome' ? 'dimezisBlurView' : 'none'}
        style={StyleSheet.absoluteFill}
      />
      <View
        style={[StyleSheet.absoluteFill, { backgroundColor: tone ? alpha(tone, 0.12) : FILL[variant] }]}
      />
      {sheen ? <Sheen /> : null}
      {children}
    </View>
  );
}

/** Arête éclairée : un trait d'un pixel qui s'éteint sur les côtés. */
function Sheen() {
  return (
    <LinearGradient
      colors={['transparent', theme.colors.sheen, 'transparent']}
      start={{ x: 0, y: 0 }}
      end={{ x: 1, y: 0 }}
      style={styles.sheen}
      pointerEvents="none"
    />
  );
}

/** Plaque de verre avec le rembourrage et le rythme interne d'une carte. */
export function GlassCard({ style, ...rest }: GlassProps) {
  return <Glass {...rest} style={[styles.card, style]} />;
}

/* -------------------------------------------------------------------------- */
/* Fond                                                                        */
/* -------------------------------------------------------------------------- */

/**
 * Le fond de l'app : un gris nocturne qui s'éclaircit très légèrement vers le
 * haut, et rien d'autre.
 *
 * Pas de halos colorés. Une app qu'on ouvre vingt fois par jour pour savoir
 * quoi faire n'a pas besoin d'un fond qui raconte quelque chose ; le fond doit
 * se faire oublier et laisser le verre, le teal et le texte être les seules
 * choses qu'on regarde. Le dégradé reste parce qu'une plaque translucide sur
 * un aplat parfaitement uniforme ne se voit pas — il lui faut une variation,
 * même infime, pour avoir un bord.
 */
export function Backdrop() {
  return (
    <View style={StyleSheet.absoluteFill} pointerEvents="none">
      <LinearGradient
        colors={[...theme.colors.backdrop]}
        locations={[0, 0.45, 1]}
        style={StyleSheet.absoluteFill}
        start={{ x: 0.5, y: 0 }}
        end={{ x: 0.5, y: 1 }}
      />
    </View>
  );
}

const styles = StyleSheet.create({
  sheen: { position: 'absolute', top: 0, left: 0, right: 0, height: 1 },
  card: { padding: theme.space.lg, gap: theme.space.md },
});
