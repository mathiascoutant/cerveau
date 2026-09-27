import { Platform } from 'react-native';

/**
 * Le système de design de Raoul — un poste de pilotage nocturne.
 *
 * Tout part d'une idée : l'écran est un instrument, pas une brochure. Le fond
 * est un bleu-noir profond, les surfaces sont des panneaux plats et opaques
 * cernés d'un trait fin, et la couleur ne sert qu'à dire un ÉTAT. Le cyan est
 * l'utilisateur et l'écoute, l'ambre est Raoul quand il parle, le violet est la
 * réflexion, le vert et le rouge disent le succès et l'échec. Rien d'autre
 * n'est coloré — un onglet qu'on ouvre trente fois par jour ne doit pas
 * fatiguer.
 *
 * Deux typographies : Inter pour ce qui se lit, une monospace pour ce qui se
 * relève — libellés, horodatages, mesures, références de tickets. Le mono
 * en capitales espacées est la signature de l'interface : c'est ce qui la fait
 * ressembler à un tableau de bord plutôt qu'à un fil de messagerie.
 *
 * Aucune couleur, aucun espacement, aucune taille n'est écrit en dur dans un
 * écran : tout passe par ici.
 */

export const palette = {
  ink0: '#04070D',
  ink1: '#070B14',
  ink2: '#0B1220',
  ink3: '#101A2B',
  ink4: '#172436',

  line: '#192640',
  lineStrong: '#28395A',

  text: '#E6EEF8',
  text2: '#9CB0C8',
  text3: '#5D7190',

  cyan: '#5EE6FF',
  cyanDeep: '#18B6D8',
  amber: '#FFB547',
  violet: '#9C8BFF',
  green: '#4BE29A',
  red: '#FF6C7C',
} as const;

/** Compose une couleur hexadécimale avec un canal alpha (0 → 1). */
export function alpha(hex: string, a: number): string {
  const v = Math.round(Math.min(Math.max(a, 0), 1) * 255)
    .toString(16)
    .padStart(2, '0');
  return `${hex}${v}`;
}

const mono = Platform.select({ ios: 'Menlo', android: 'monospace', default: 'monospace' });

export const tokens = {
  colors: {
    bg: palette.ink1,
    bgDeep: palette.ink0,
    panel: palette.ink2,
    panelRaised: palette.ink3,
    panelPressed: palette.ink4,

    line: palette.line,
    lineStrong: palette.lineStrong,

    text: palette.text,
    text2: palette.text2,
    text3: palette.text3,

    /** L'accent : l'action, l'utilisateur, l'écoute. */
    accent: palette.cyan,
    accentDeep: palette.cyanDeep,
    onAccent: '#03191F',
    /** Raoul quand il parle. */
    raoul: palette.amber,
    /** Raoul quand il réfléchit ou interroge une source. */
    thinking: palette.violet,

    ok: palette.green,
    warn: palette.amber,
    danger: palette.red,
  },

  /** Rythme d'espacement en pas de 4. */
  space: { xs: 4, sm: 8, md: 12, lg: 16, xl: 24, xxl: 32, xxxl: 48 } as const,

  radius: { sm: 8, md: 12, lg: 16, xl: 22, pill: 999 } as const,

  type: {
    display: { fontSize: 30, lineHeight: 36, fontFamily: 'Inter_700Bold' },
    title: { fontSize: 22, lineHeight: 28, fontFamily: 'Inter_600SemiBold' },
    h: { fontSize: 17, lineHeight: 24, fontFamily: 'Inter_600SemiBold' },
    body: { fontSize: 16, lineHeight: 24, fontFamily: 'Inter_400Regular' },
    bodyStrong: { fontSize: 16, lineHeight: 24, fontFamily: 'Inter_500Medium' },
    small: { fontSize: 14, lineHeight: 20, fontFamily: 'Inter_400Regular' },
    smallStrong: { fontSize: 14, lineHeight: 20, fontFamily: 'Inter_500Medium' },
    mono: { fontSize: 12, lineHeight: 17, fontFamily: mono },
    /** Capitales espacées : les libellés de section et les relevés. */
    label: { fontSize: 11, lineHeight: 14, fontFamily: mono, letterSpacing: 1.6 },
  } as const,

  motion: {
    fast: 130,
    base: 220,
    slow: 400,
    easing: [0.2, 0.9, 0.3, 1] as const,
    spring: { damping: 18, stiffness: 160 },
  },

  /** Zone tactile minimale (recommandation Apple : 44 pt). */
  touch: 44,

  /** Largeur au-delà de laquelle le contenu se centre au lieu de s'étirer. */
  maxContent: 640,
} as const;

export type Tokens = typeof tokens;
