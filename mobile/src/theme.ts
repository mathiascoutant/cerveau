/**
 * Jetons de design de Raoul.
 *
 * Palette sombre, accent teal, typographie Inter. Rien n'est écrit en dur dans
 * les écrans : toute couleur, tout espacement et toute taille de texte passe
 * par ici, pour que l'ensemble reste cohérent et modifiable d'un seul endroit.
 */

const palette = {
  // Fonds, du plus profond au plus élevé. L'écart entre chaque niveau reste
  // faible : sur OLED, des sauts trop marqués font ressortir les cartes comme
  // des rustines.
  ink900: '#080C11',
  ink800: '#0D131A',
  ink700: '#131C25',
  ink600: '#1A2530',
  ink500: '#243240',

  // Teal : couleur d'action et d'état actif.
  teal400: '#2DD4BF',
  teal500: '#14B8A6',
  teal600: '#0D9488',

  white: '#FFFFFF',
  slate50: '#EEF3F8',
  slate300: '#A7B6C4',
  slate400: '#7C8D9C',

  emerald: '#34D399',
  amber: '#FBBF24',
  rose: '#FB7185',
} as const;

export const theme = {
  colors: {
    /** Fond de l'application. */
    background: palette.ink900,
    /** Surface d'une carte posée sur le fond. */
    surface: palette.ink800,
    /** Surface d'un élément posé sur une carte (champ, pastille). */
    surfaceRaised: palette.ink700,
    /** Surface pressée ou sélectionnée. */
    surfaceActive: palette.ink600,

    border: palette.ink600,
    borderStrong: palette.ink500,

    /** Texte principal — contraste supérieur à 4,5:1 sur le fond. */
    text: palette.slate50,
    /** Texte secondaire — supérieur à 3:1, réservé aux libellés et métadonnées. */
    textMuted: palette.slate300,
    /** Texte tertiaire, à n'utiliser que pour de l'accessoire non essentiel. */
    textFaint: palette.slate400,

    primary: palette.teal400,
    primaryDim: palette.teal600,
    onPrimary: palette.ink900,

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
    sm: 8,
    md: 14,
    lg: 20,
    xl: 28,
    pill: 999,
  },

  /** Échelle typographique. `font` désigne la graisse Inter à charger. */
  type: {
    display: { fontSize: 34, lineHeight: 40, font: 'Inter_700Bold' },
    title: { fontSize: 22, lineHeight: 28, font: 'Inter_600SemiBold' },
    heading: { fontSize: 17, lineHeight: 24, font: 'Inter_600SemiBold' },
    body: { fontSize: 16, lineHeight: 24, font: 'Inter_400Regular' },
    bodyStrong: { fontSize: 16, lineHeight: 24, font: 'Inter_500Medium' },
    small: { fontSize: 14, lineHeight: 20, font: 'Inter_400Regular' },
    label: { fontSize: 12, lineHeight: 16, font: 'Inter_600SemiBold' },
    mono: { fontSize: 12, lineHeight: 18, font: 'Inter_400Regular' },
  },

  /** Durées d'animation : 150-300 ms pour les micro-interactions. */
  motion: {
    fast: 140,
    base: 220,
    slow: 320,
  },

  /**
   * Zone tactile minimale. En dessous de 44 pt, une cible devient difficile à
   * atteindre au pouce et ne respecte plus les recommandations d'Apple.
   */
  touchMin: 44,
} as const;

export type Theme = typeof theme;
