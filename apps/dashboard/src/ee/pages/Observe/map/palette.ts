// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import type { ResolvedTheme } from '@/lib/theme';

// The map reads its accent from the same token the rest of the dashboard uses,
// so it cannot drift from the design system the day someone retunes --primary.
const readToken = (name: string, fallback: string) => {
  if (typeof window === 'undefined') return fallback;
  const value = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return value || fallback;
};

export type MapPalette = {
  oceanInner: string;
  oceanOuter: string;
  landTop: string;
  landBottom: string;
  border: string;
  coast: string;
  graticule: string;
  outline: string;
  // The marker is opaque on a light basemap and translucent on a dark one. Same
  // shape, opposite problem: over pale land a see-through disc leaves its white
  // label on near-white, while over dark ocean an opaque disc loses the sense
  // of sitting on top of the map.
  markerFill: string;
  markerStroke: string;
  core: string;
  flash: string;
  label: string;
  ring: string;
  accent: (alpha: number) => string;
  // The halo of air at the limb, and the darkening of the ground curving away
  // from the light. Both take an alpha because they are drawn as gradients.
  atmosphere: (alpha: number) => string;
  shade: (alpha: number) => string;
};

export const readMapPalette = (theme: ResolvedTheme): MapPalette => {
  const primary = readToken('--primary', theme === 'light' ? '221 68% 48%' : '217 87% 62%');
  const accent = (alpha: number) => `hsl(${primary} / ${alpha})`;
  if (theme === 'light') {
    return {
      oceanInner: 'hsl(211 58% 82%)',
      oceanOuter: 'hsl(216 40% 76%)',
      landTop: 'hsl(0 0% 100%)',
      landBottom: 'hsl(222 24% 95%)',
      border: 'hsl(222 16% 52% / 0.9)',
      coast: 'hsl(220 24% 38% / 0.95)',
      graticule: 'hsl(220 20% 42% / 0.09)',
      outline: accent(0.28),
      markerFill: 'hsl(221 74% 50% / 0.94)',
      markerStroke: 'hsl(221 76% 32%)',
      core: 'hsl(0 0% 100%)',
      flash: 'hsl(0 0% 100%)',
      label: 'hsl(0 0% 100%)',
      ring: accent(0.9),
      accent,
      atmosphere: alpha => `hsl(210 80% 52% / ${alpha})`,
      shade: alpha => `hsl(220 42% 28% / ${alpha})`,
    };
  }
  return {
    oceanInner: 'hsl(223 40% 13%)',
    oceanOuter: 'hsl(230 28% 9%)',
    landTop: 'hsl(233 12% 17%)',
    landBottom: 'hsl(240 8% 11%)',
    border: 'hsl(230 10% 40% / 0.75)',
    coast: 'hsl(228 14% 58% / 0.9)',
    graticule: 'hsl(228 14% 62% / 0.10)',
    outline: accent(0.3),
    markerFill: accent(0.32),
    markerStroke: accent(0.88),
    core: 'hsl(210 100% 95%)',
    flash: 'hsl(0 0% 100%)',
    label: 'hsl(0 0% 100% / 0.95)',
    ring: accent(0.95),
    accent,
    atmosphere: alpha => `hsl(207 92% 58% / ${alpha})`,
    shade: alpha => `hsl(230 60% 2% / ${alpha})`,
  };
};

// A halo is a radial gradient drawn once per cluster per frame. Building the
// gradient every time is the expensive part, so it is baked into a sprite and
// stamped at whatever size the amplitude calls for.
export const createHaloSprite = (palette: MapPalette) => {
  const size = 128;
  const canvas = document.createElement('canvas');
  canvas.width = size;
  canvas.height = size;
  const context = canvas.getContext('2d');
  if (!context) return canvas;
  const gradient = context.createRadialGradient(
    size / 2,
    size / 2,
    0,
    size / 2,
    size / 2,
    size / 2
  );
  gradient.addColorStop(0, palette.accent(0.6));
  gradient.addColorStop(0.35, palette.accent(0.24));
  gradient.addColorStop(1, palette.accent(0));
  context.fillStyle = gradient;
  context.fillRect(0, 0, size, size);
  return canvas;
};
