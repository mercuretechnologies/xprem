import { useId } from 'react';

/**
 * The xprem logo mark: the X glyph with the azure→indigo dot on its dark tile.
 * Colours are baked in rather than themed, so the mark stays identical in light
 * and dark and matches the favicon and the landing exactly. The gradient id is
 * per-instance because the mark renders twice (sidebar and mobile header).
 */
export const XpremMark = ({ className = 'h-8 w-8' }: { className?: string }) => {
  const dotGradient = `${useId().replace(/:/g, '')}-dot`;

  return (
    <svg viewBox="0 0 64 64" className={className} role="img" aria-label="xprem">
      <defs>
        <radialGradient id={dotGradient} cx="0.35" cy="0.3" r="0.9">
          <stop offset="0" stopColor="#7FB6FF" />
          <stop offset="0.45" stopColor="#4E97F2" />
          <stop offset="1" stopColor="#5673F0" />
        </radialGradient>
      </defs>
      <rect width="64" height="64" rx="14" fill="#0A0E16" />
      <rect width="64" height="64" rx="14" fill="none" stroke="#232F42" strokeWidth="1" />
      <path
        d="M18 22 L38 46 M38 22 L18 46"
        stroke="#EEF2FA"
        strokeWidth="6.5"
        strokeLinecap="round"
      />
      <circle cx="47" cy="43" r="6.5" fill={`url(#${dotGradient})`} />
    </svg>
  );
};
