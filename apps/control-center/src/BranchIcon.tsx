import { View } from 'react-native';
import { palette } from './theme';

/**
 * The git-branch glyph, drawn with borders instead of an SVG because this package
 * ships no native modules. The curve is the bottom-right quadrant of a box whose
 * corner radius equals its height, which is exactly a quarter circle.
 *
 * Geometry is lucide's 24-unit grid, so it lines up with the icon everyone knows.
 */
const GRID = 24;

export function BranchIcon({ size = 24, color = palette.ink, weight = 2 }) {
  const s = size / GRID;
  const stroke = weight * s;
  const dot = (cx: number, cy: number) => ({
    position: 'absolute' as const,
    left: (cx - 3) * s,
    top: (cy - 3) * s,
    width: 6 * s,
    height: 6 * s,
    borderRadius: 3 * s,
    borderWidth: stroke,
    borderColor: color,
  });

  return (
    <View style={{ width: size, height: size }}>
      <View
        style={{
          position: 'absolute',
          left: (6 - weight / 2) * s,
          top: 4 * s,
          width: stroke,
          height: 16 * s,
          backgroundColor: color,
        }}
      />
      <View
        style={{
          position: 'absolute',
          left: 6 * s,
          top: 7 * s,
          width: 12 * s,
          height: 6 * s,
          borderBottomWidth: stroke,
          borderRightWidth: stroke,
          borderColor: color,
          borderBottomRightRadius: 6 * s,
        }}
      />
      <View style={dot(6, 4)} />
      <View style={dot(6, 20)} />
      <View style={dot(18, 4)} />
    </View>
  );
}
