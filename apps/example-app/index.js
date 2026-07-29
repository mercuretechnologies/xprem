// BOOT CRASH SIMULATION (temporary): throws during bundle evaluation, before
// any React render, so expo-updates' content-appeared gate never fires and the
// launch is marked failed (rollback + Expo-Recent-Failed-Update-Ids expected).
// global.HermesInternal only exists in the Hermes runtime on device, never in
// Node during `expo export`, so exporting still works.
if (global.HermesInternal) {
  // throw new Error('BOOT CRASH TEST');
}
// Must run before any navigation provider mounts: the expo-observe
// integrations latch their enabled state when their provider first renders.
require('./observe.config');
// Both trees ship in the bundle; EXPO_PUBLIC_NAV picks which one boots.
if (require('./navigation/mode').NAV_MODE === 'react-navigation') {
  require('./navigation/entry');
} else {
  require('expo-router/entry');
}
