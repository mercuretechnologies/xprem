export type NavMode = 'expo-router' | 'react-navigation'

/**
 * Which navigation tree the app boots into. expo-observe ships one integration
 * per framework and they are mutually exclusive: the react-navigation one needs
 * to own the NavigationContainer, which expo-router renders itself. So the app
 * carries both trees and picks one here.
 *
 * `EXPO_PUBLIC_*` is inlined by Metro, so this is decided when the bundle is
 * built (`expo start`, `eoas publish`), not at runtime.
 */
export const NAV_MODE: NavMode =
  process.env.EXPO_PUBLIC_NAV === 'react-navigation'
    ? 'react-navigation'
    : 'expo-router'
