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
- `expo-app-id`, `expo-channel-name` and `xprem-branch` declared in
  `updates.requestHeaders` at build time

```ts
updates: {
  requestHeaders: {
    'expo-channel-name': 'staging',
    'expo-app-id': '...',
    'xprem-branch': '',
  },
}
```

Those three are not optional, and the panel refuses to appear without them —
switching branches replaces the whole header set, and expo-updates only accepts an
override for keys that existed when the app was built. A build missing one of them
would drop it from every poll from then on, which the server answers with a 400,
which means no update can reach the device to undo it. Reinstalling is the only way
out, so the package checks first and logs which header is missing. `eoas init`
writes them for you.

Declare `expo-channel-name` as a literal, not `process.env.SOMETHING`. The config is
evaluated when the JS bundle is exported, so an unset variable silently removes the
key — and an export run with the wrong value would bake in the wrong channel. Only
the key matters here: the value sent at runtime is always the build's real channel.

## If a build gets stuck

It cannot be bricked. A branch that fails to launch is rolled back by
expo-updates itself, onto the bundle embedded in the binary. To get moving again,
in order of preference:

1. publish a fix to the branch being tested
2. turn branch surfing off for the channel, which returns every device
3. reinstall the app, which clears the stored choice
