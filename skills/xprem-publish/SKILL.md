---
name: xprem-publish
description: Publish an OTA update with eoas (not EAS). Use when the user has EOO_TOKEN, needs `npx eoas publish --branch …`, or must map a dashboard channel to that branch.
---

# xprem-publish

## Before you start

Read the live docs. Do not invent flags and do not confuse this with `eas update`.

1. Query the **xprem-docs** MCP (`https://mercure-technologies.gitbook.io/xprem/~gitbook/mcp`), or
2. Fetch [Publish an update](https://mercure-technologies.gitbook.io/xprem/eoas/publish-an-update.md).

Use **eoas** only. Never run `eas update` / EAS Update unless the user explicitly wants Expo's hosted service instead of xprem.

## When to use

- The Expo app is already wired (certificate, App ID, `eoas init`) — otherwise **xprem-configure-expo**
- The user wants to publish, set `EOO_TOKEN`, pick a branch, or point a channel at that branch

## Auth

`EOO_TOKEN` is the app-scoped API key created in the dashboard. Set it in the **process environment**. EOAS does **not** load `.env` files (`expo export` runs with `EXPO_NO_DOTENV=1`).

Without `EOO_TOKEN`, EOAS silently falls back to Expo authentication and the xprem server rejects the publish.

## Publish

From the Expo project (example from the live quickstart / publish page):

```bash
export RELEASE_CHANNEL=production
export EOO_TOKEN=eoo_your_api_key
npx eoas publish --branch production
```

- `--branch` is required and is the **only** thing that selects the branch.
- `RELEASE_CHANNEL` does not select the branch; it is passed into app config / `expo export` so the JS bundle is built for that channel.
- Runtime version must match the native build exactly or the update is silently not delivered. Prefer a fixed `runtimeVersion` in app config. Re-read the live page before recommending Expo's fingerprint policy.

Useful flags documented on the live page (re-read before using): `--platform`, `--message` / `-m`, `--rollout-percentage`, `--nonInteractive` (required in CI), `--dumpSourcemap`.

CI shape from the docs:

```bash
EOO_TOKEN=$EOO_TOKEN RELEASE_CHANNEL=production \
  npx eoas publish --branch production --nonInteractive
```

`eoas publish` refuses a dirty git tree (untracked files count). `--nonInteractive` aborts with `Commit all changes. Aborting...`. Escape hatches on the live page: `--disableRepositoryCheck` or `EAS_NO_VCS`.

## Map channel → branch

A build is bound to a **channel**; an update is published to a **branch**. In the dashboard: Channels → Create Channel and attach it to the branch you just published. A fresh channel serves that one branch. Use the channel baked into the Expo build, not a guessed default.

Do **not** invent auto-rollback. Progressive rollouts use `--rollout-percentage` plus dashboard controls; if asked, read [Progressive rollouts](https://mercure-technologies.gitbook.io/xprem/eoas/progressive-rollouts.md).
