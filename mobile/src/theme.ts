/**
 * Jetons de design de Raoul — verre sur fond nocturne.
 *
 * Le principe tient en une phrase : rien n'est opaque, mais rien n'est
 * décoratif non plus. Les surfaces sont des plaques de verre translucides
 * posées sur un gris très sombre ; c'est le flou et le liseré qui font la
 * profondeur, pas des ombres portées, qui n'ont rien à porter sur un fond noir.
 *
 * Le fond n'a aucune couleur : la seule couleur de l'app est le teal, et il ne
 * sert qu'à désigner ce sur quoi on peut agir. Un fond coloré volerait cette
 * fonction et fatiguerait à la vingtième ouverture de la journée.
 *
 * Trois règles pour que ça tienne :
 *
 *   — jamais de #000000. Sur OLED, le noir pur avale les bords du verre et
 *     l'écran perd son relief ;
 *   — le texte ne repose jamais sur la seule transparence. Chaque surface
 *     porte un voile teinté sous son flou, sinon le contraste tombe sous le
 *     seuil lisible dès que le fond varie ;
 *   — la lumière vient d'en haut. Les surfaces ont un liseré clair sur leur
 *     arête supérieure, comme du verre réel.
 *
 * Rien n'est écrit en dur dans les écrans : toute couleur, tout espacement et
 * toute taille de texte passe par ici.
 */

const palette = {
  // Fonds, du haut vers le bas. Trois valeurs très proches et sans teinte :
  // une plaque de verre posée sur un aplat parfaitement uniforme n'a pas de
  // bord visible, il lui faut une variation — mais une variation qu'on ne
  // remarque pas.
  night: '#14161A',
  nightMid: '#0C0E12',
  nightDeep: '#07080B',

  // Teal : couleur d'action et d'état actif. La seule couleur de l'app.
  teal400: '#2DD4BF',
  teal500: '#14B8A6',
  teal600: '#0D9488',

  white: '#FFFFFF',
  slate50: '#F1F5F9',
  slate300: '#A9B7C6',
  slate400: '#74848F',

  emerald: '#34D399',
  amber: '#FBBF24',
  rose: '#FB7185',
} as const;

/** Compose une couleur hexadécimale avec un canal alpha (0 → 1). */
export function alpha(hex: string, a: number): string {
  const v = Math.round(Math.min(Math.max(a, 0), 1) * 255)
    .toString(16)
    .padStart(2, '0');
  return `${hex}${v}`;
}

export const theme = {
  colors: {
    /** Étapes du dégradé de fond, du haut vers le bas. */
    backdrop: [palette.night, palette.nightMid, palette.nightDeep] as const,
    /** Aplat de repli, pour les écrans de chargement et les fonds de liste. */
    background: palette.nightDeep,

    /** Voile posé sous le flou d'une plaque de verre ordinaire. */
    glass: 'rgba(255,255,255,0.055)',
    /** Verre d'un élément posé sur une autre plaque (champ, pastille). */
    glassRaised: 'rgba(255,255,255,0.09)',
    /** Verre pressé ou sélectionné. */
    glassActive: 'rgba(255,255,255,0.14)',
    /** Verre des barres fixes : plus dense, il doit isoler le contenu qui défile dessous. */
    glassChrome: 'rgba(12,16,24,0.55)',

    /** Arête éclairée, en haut de chaque plaque. */
    sheen: 'rgba(255,255,255,0.22)',
    border: 'rgba(255,255,255,0.10)',
    borderStrong: 'rgba(255,255,255,0.20)',

    /** Texte principal — contraste supérieur à 4,5:1 sur le verre le plus clair. */
    text: palette.slate50,
    /** Texte secondaire — supérieur à 3:1, réservé aux libellés et métadonnées. */
    textMuted: palette.slate300,
    /** Texte tertiaire, à n'utiliser que pour de l'accessoire non essentiel. */
    textFaint: palette.slate400,

    primary: palette.teal400,
    primaryDim: palette.teal600,
    onPrimary: '#04211E',

    success: palette.emerald,
    warning: palette.amber,
    danger: palette.rose,
  },

  /** Rythme d'espacement en pas de 4. */
  space: {
    xs: 4,
    sm: 8,
    md: 12,
    lg: 16,
    xl: 24,
    xxl: 32,
    xxxl: 48,
  },

  radius: {
    sm: 10,
    md: 16,
    lg: 22,
    xl: 30,
    pill: 999,
  },

  /**
   * Intensités de flou, sur l'échelle 1-100 d'expo-blur.
   *
   * `chrome` est le plus fort : une barre fixe doit couper net le contenu qui
   * passe dessous, sinon on lit deux choses à la fois.
   */
  blur: {
    chrome: 42,
    card: 26,
    subtle: 16,
  },

  /** Échelle typographique. `font` désigne la graisse Inter à charger. */
  type: {
    display: { fontSize: 34, lineHeight: 41, font: 'Inter_700Bold' },
    title: { fontSize: 22, lineHeight: 28, font: 'Inter_600SemiBold' },
    heading: { fontSize: 17, lineHeight: 24, font: 'Inter_600SemiBold' },
    body: { fontSize: 16, lineHeight: 24, font: 'Inter_400Regular' },
    bodyStrong: { fontSize: 16, lineHeight: 24, font: 'Inter_500Medium' },
    small: { fontSize: 14, lineHeight: 20, font: 'Inter_400Regular' },
    label: { fontSize: 12, lineHeight: 16, font: 'Inter_600SemiBold' },
    mono: { fontSize: 12, lineHeight: 18, font: 'Inter_400Regular' },
  },

  /**
   * Durées d'animation : 150-300 ms pour les micro-interactions, davantage
   * pour ce qui coule (halos du fond, anneaux de l'orbe).
   */
  motion: {
    fast: 140,
    base: 240,
    slow: 420,
    /** Courbe d'entrée, très décélérée : le mouvement arrive puis se pose. */
    easing: [0.16, 1, 0.3, 1] as const,
    /** Ressort des pressions. */
    spring: { damping: 20, stiffness: 90 },
  },

  /**
   * Zone tactile minimale. En dessous de 44 pt, une cible devient difficile à
   * atteindre au pouce et ne respecte plus les recommandations d'Apple.
   */
  touchMin: 44,
} as const;

export type Theme = typeof theme;
