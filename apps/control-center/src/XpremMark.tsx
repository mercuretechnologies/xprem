import { StyleSheet, View } from 'react-native';

/**
 * The xprem mark — the X glyph and its azure dot on the dark tile — rebuilt from
 * plain Views because the design of this package forbids native modules and
 * react-native-svg is one. Every shape in the mark is a rounded rect or a
 * circle, so nothing is lost except the dot's radial gradient, flattened to its
 * dominant stop; at footer size the difference does not survive the screen.
 *
 * Geometry is the SVG's, scaled from its 64-unit viewBox. Colours are baked in,
 * exactly like the dashboard's mark: the tile must stay identical everywhere.
 */
const VIEWBOX = 64;
// The strokes run (18,22)–(38,46) and (38,22)–(18,46): not a 45° X, slightly
// taller than wide, and that asymmetry is part of the mark.
const STROKE = 6.5;
const STROKE_LENGTH = Math.hypot(38 - 18, 46 - 22) + STROKE; // round caps add a radius each
const STROKE_ANGLE = (Math.atan2(46 - 22, 38 - 18) * 180) / Math.PI;

export function XpremMark({ size = 16 }: { size?: number }) {
  const s = size / VIEWBOX;
  const bar = {
    position: 'absolute' as const,
    width: STROKE_LENGTH * s,
    height: STROKE * s,
    borderRadius: (STROKE / 2) * s,
    backgroundColor: '#EEF2FA',
    left: 28 * s - (STROKE_LENGTH * s) / 2,
    top: 34 * s - (STROKE * s) / 2,
  };
  return (
    <View
      style={{
        width: size,
        height: size,
        borderRadius: 14 * s,
        backgroundColor: '#0A0E16',
        borderWidth: StyleSheet.hairlineWidth,
        borderColor: '#232F42',
      }}
      accessibilityRole="image"
      accessibilityLabel="xprem">
      <View style={[bar, { transform: [{ rotate: `${STROKE_ANGLE}deg` }] }]} />
      <View style={[bar, { transform: [{ rotate: `${-STROKE_ANGLE}deg` }] }]} />
      <View
        style={{
          position: 'absolute',
          width: 13 * s,
          height: 13 * s,
          borderRadius: 6.5 * s,
          backgroundColor: '#4E97F2',
          left: (47 - 6.5) * s,
          top: (43 - 6.5) * s,
        }}
      />
    </View>
  );
}
