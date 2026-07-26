// Telemetry carries `device.model.identifier`, which on Apple hardware is a
// board name like `iPhone18,2`. Nobody reads a performance table written in
// board names, so this maps the ones we can state with certainty to their
// commercial name and leaves everything else alone.
//
// Deliberately partial. An unknown identifier renders verbatim: showing the
// wrong phone name next to a p90 is worse than showing a cryptic one, because
// it sends someone optimising for hardware they do not have. Add entries as
// generations ship (the community list at github.com/pluwen/apple-device-model-list
// is the usual reference).
//
// Android is not mapped: `Build.MODEL` already reads as a product name on
// Pixel ("Pixel 8 Pro") and the Samsung/Xiaomi codes would need the full
// Play device catalogue, which is thousands of rows and changes weekly.
const appleModels: Record<string, string> = {
  'iPhone10,1': 'iPhone 8',
  'iPhone10,4': 'iPhone 8',
  'iPhone10,2': 'iPhone 8 Plus',
  'iPhone10,5': 'iPhone 8 Plus',
  'iPhone10,3': 'iPhone X',
  'iPhone10,6': 'iPhone X',
  'iPhone11,2': 'iPhone XS',
  'iPhone11,4': 'iPhone XS Max',
  'iPhone11,6': 'iPhone XS Max',
  'iPhone11,8': 'iPhone XR',
  'iPhone12,1': 'iPhone 11',
  'iPhone12,3': 'iPhone 11 Pro',
  'iPhone12,5': 'iPhone 11 Pro Max',
  'iPhone12,8': 'iPhone SE (2nd gen)',
  'iPhone13,1': 'iPhone 12 mini',
  'iPhone13,2': 'iPhone 12',
  'iPhone13,3': 'iPhone 12 Pro',
  'iPhone13,4': 'iPhone 12 Pro Max',
  'iPhone14,4': 'iPhone 13 mini',
  'iPhone14,5': 'iPhone 13',
  'iPhone14,2': 'iPhone 13 Pro',
  'iPhone14,3': 'iPhone 13 Pro Max',
  'iPhone14,6': 'iPhone SE (3rd gen)',
  'iPhone14,7': 'iPhone 14',
  'iPhone14,8': 'iPhone 14 Plus',
  'iPhone15,2': 'iPhone 14 Pro',
  'iPhone15,3': 'iPhone 14 Pro Max',
  'iPhone15,4': 'iPhone 15',
  'iPhone15,5': 'iPhone 15 Plus',
  'iPhone16,1': 'iPhone 15 Pro',
  'iPhone16,2': 'iPhone 15 Pro Max',
  'iPhone17,3': 'iPhone 16',
  'iPhone17,4': 'iPhone 16 Plus',
  'iPhone17,1': 'iPhone 16 Pro',
  'iPhone17,2': 'iPhone 16 Pro Max',
  'iPhone17,5': 'iPhone 16e',
  'iPhone18,3': 'iPhone 17',
  'iPhone18,1': 'iPhone 17 Pro',
  'iPhone18,2': 'iPhone 17 Pro Max',
};

export type DeviceName = {
  // What to show. Falls back to the raw identifier.
  label: string;
  // True when the label is the raw identifier, so the UI can mark it as
  // unrecognised instead of pretending it is a product name.
  known: boolean;
};

export const deviceName = (identifier: string): DeviceName => {
  const trimmed = identifier.trim();
  if (!trimmed) return { label: 'Unknown device', known: false };
  const mapped = appleModels[trimmed];
  if (mapped) return { label: mapped, known: true };
  // Android reports a product name already; only Apple board names look like
  // "iPhone18,2", so anything without that shape is treated as readable.
  const isBoardName = /^(iPhone|iPad|iPod|Watch|Mac)\d+,\d+$/.test(trimmed);
  return { label: trimmed, known: !isBoardName };
};

// "iOS 26.1" from the two columns, without ever parsing a label back apart.
export const osLabel = (name: string, version: string) =>
  [name, version].filter(Boolean).join(' ') || 'Unknown OS';
