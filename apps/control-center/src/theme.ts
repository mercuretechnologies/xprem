/**
 * White cards floating on a light page, bold near-black type, one black pill for
 * the primary action — the register of the Expo dashboard's branch page, which is
 * where these testers already read about branches.
 */
export const palette = {
  page: '#F6F7F9',
  surface: '#FFFFFF',
  surfacePressed: '#EFF1F4',
  border: 'rgba(17, 24, 39, 0.07)',
  ink: '#111418',
  muted: '#6B7280',
  /** The warm chip the dashboard uses for "Available on". */
  chipBg: '#FFF1E7',
  chipInk: '#8A4B1A',
  pill: '#14171A',
  pillInk: '#FFFFFF',
  live: '#22C55E',
  warnBg: '#FFF6E5',
  warnInk: '#8A5A08',
  danger: '#C5221F',
};

export const type = {
  hero: { fontSize: 28, fontWeight: '700' as const, color: palette.ink },
  section: { fontSize: 18, fontWeight: '700' as const, color: palette.ink },
  rowTitle: { fontSize: 17, fontWeight: '600' as const, color: palette.ink },
  meta: { fontSize: 14, color: palette.muted },
  label: { fontSize: 15, color: palette.muted },
  chip: { fontSize: 14, fontWeight: '500' as const, color: palette.chipInk },
  pill: { fontSize: 16, fontWeight: '600' as const, color: palette.pillInk },
};

export const radius = { card: 16, chip: 8, pill: 999 };
export const space = { xs: 4, sm: 8, md: 16, lg: 24, xl: 32 };

export const cardShadow = {
  shadowColor: '#0B1220',
  shadowOpacity: 0.05,
  shadowRadius: 10,
  shadowOffset: { width: 0, height: 2 },
  elevation: 1,
};
