# @xprem/control-center

See which branch this build is running, and switch to another — from the build
itself.

Built for acceptance testing: one TestFlight or Play build becomes a shell for
every branch that is compatible with it, so five people can test five branches in
parallel without five builds.

```tsx
import { ControlCenter } from '@xprem/control-center';

export default function App() {
  return (
    <>
      <YourApp />
      <ControlCenter />
    </>
  );
}
```

That is the whole integration. There is nothing to configure: the panel reads the
update URL, app id, channel and runtime version from the config the build already
carries.

## It turns itself on

At launch, once the first frame has painted, the component asks the server one
question — is branch surfing allowed on this build's channel? On no, it renders
`null` and registers nothing. On yes, a small blue marker appears on the right
edge; press it and the panel opens, branches already in hand. One request per
app session, answered from the server's cache.

Turn branch surfing on for a channel from the xprem dashboard and the panel is
there at the next launch of every build on that channel — nothing to republish.
Turn it off and it disappears the same way. A production channel never shows it.

## Footprint

JavaScript only. No native module, no config plugin, no `prebuild`. It ships in
the JS bundle, which means **it can be delivered over the air to a build that is
already in testers' hands** — the picker itself is an update.

The only dependencies are `expo-updates` and `expo-constants`, which the app has
already. The edge marker is the built-in way in; your own trigger works from
anywhere:

```tsx
import { openControlCenter } from '@xprem/control-center';
```

## What a tester sees

The panel opens on the branch currently running — live dot, channel and runtime
under it — then the branches this build can switch to, drawn as a rail of
branch dots, newest first. Only branches with an update built for **this** binary's
runtime version are listed: a branch that changed native code cannot be reached
without a new build, so offering it would be a dead end.

When a branch crashes on launch, expo-updates falls back on its own and the server
refuses to serve that branch again. The panel says so, names the branch, and says
what unblocks it — publishing a fix to that same branch.

## Requirements

- Expo SDK 54 or newer (`setUpdateRequestHeadersOverride`)
- `xprem-branch` declared in `updates.requestHeaders` at build time

The second one is not optional: expo-updates only accepts a runtime override for
keys that existed when the app was built. `eoas init` writes it for you.

```ts
updates: {
  requestHeaders: {
    'expo-channel-name': process.env.RELEASE_CHANNEL,
    'expo-app-id': '...',
    'xprem-branch': 'default',
  },
}
```

## If a build gets stuck

It cannot be bricked. A branch that fails to launch is rolled back by
expo-updates itself, onto the bundle embedded in the binary. To get moving again,
in order of preference:

1. publish a fix to the branch being tested
2. turn branch surfing off for the channel, which returns every device
3. reinstall the app, which clears the stored choice
