import { Observe } from 'expo-observe'

import { NAV_MODE } from './navigation/mode'

/**
 * Runs at module scope, before any component mounts. The navigation
 * integrations read `isInitialized()` when their provider mounts and throw if
 * the value changes afterwards, so `configure` cannot be called from a hook or
 * an effect.
 */
Observe.configure({
  environment: __DEV__ ? 'development' : 'production',
  // Debug builds mark their metrics as sent without dispatching them. This app
  // exists to be watched against a local server, so opt in.
  dispatchInDebug: true,
  integrations:
    NAV_MODE === 'react-navigation'
      ? { 'react-navigation': true }
      : { 'expo-router': true },
})
