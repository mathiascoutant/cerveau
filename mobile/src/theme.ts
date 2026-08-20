export const theme = {
  colors: {
    bg: '#0B0E14',
    surface: '#141924',
    surfaceAlt: '#1C2331',
    border: '#252D3D',
    text: '#EEF2F8',
    textMuted: '#8C97AC',
    accent: '#6D8BFF',
    accentSoft: '#1E2740',
    success: '#3DD68C',
    warning: '#FFB84D',
    danger: '#FF6B6B',
  },
  radius: { sm: 10, md: 16, lg: 24, pill: 999 },
  space: (n: number) => n * 8,
} as const;
