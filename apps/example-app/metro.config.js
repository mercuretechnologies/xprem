const path = require('path')
const { getDefaultConfig } = require('expo/metro-config')

const config = getDefaultConfig(__dirname)

// @xprem/control-center is a sibling reached through a symlink in node_modules,
// so it resolves by the normal algorithm — including in release bundles. But its
// OWN imports resolve from where its files live, and there is no node_modules
// above apps/, so react and expo-updates have to be pointed back here.
const controlCenterRoot = path.resolve(__dirname, '../control-center')
config.watchFolders = [...(config.watchFolders ?? []), controlCenterRoot]
config.resolver.nodeModulesPaths = [
  ...(config.resolver.nodeModulesPaths ?? []),
  path.resolve(__dirname, 'node_modules'),
]


// index.js requires one of the two navigation trees (see navigation/mode.ts),
// but Metro walks both branches of that `if` and would pull expo-router and
// @react-navigation into the same graph — which SDK 56 refuses. Resolve the
// entry of the tree this build isn't using to nothing, so only one ships.
const unusedEntry =
  process.env.EXPO_PUBLIC_NAV === 'react-navigation'
    ? 'expo-router/entry'
    : './navigation/entry'

config.resolver.resolveRequest = (context, moduleName, platform) => {
  if (moduleName === unusedEntry) {
    return { type: 'empty' }
  }
  return context.resolveRequest(context, moduleName, platform)
}

module.exports = config
