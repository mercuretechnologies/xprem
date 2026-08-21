---
name: xprem-configure-expo
description: Wire an Expo app to a running xprem server. Use when the user needs the signing certificate (certs/certificate.pem), App ID, npx eoas init, updates.url / request headers, or a release channel on a native build. Not for publishing an update (use xprem-publish).
---

# xprem-configure-expo

## Before you start

Read the live docs. Do not invent config keys, header names, or CLI prompts.

1. Query the **xprem-docs** MCP (`https://mercure-technologies.gitbook.io/xprem/~gitbook/mcp`), or
2. Fetch [Configure your application](https://mercure-technologies.gitbook.io/xprem/installation-guide/configure-your-application.md) and Quickstart steps 5–8 at [quickstart.md](https://mercure-technologies.gitbook.io/xprem/quickstart.md).

xprem is **not** affiliated with Expo. Use `eoas`, not EAS Update.

## When to use

- A control-plane server is already running (see **xprem-quickstart**) and the Expo project is not wired yet
- Missing `certs/certificate.pem`
- Need the dashboard App ID, `updates.url` (server `BASE_URL`), channel, or `npx eoas init`

## Steps (confirm every prompt on the live pages)

### 1. Create the app and copy the App ID

Open `{SERVER_URL}/dashboard`, create the app, then **App Info** and copy the App ID (UUID).

### 2. Download the signing certificate

App Info → **Download certificate**. Save it in the Expo project as `certs/certificate.pem`:

```bash
mkdir -p certs
mv ~/Downloads/app-<your-app-id>-certificate.txt certs/certificate.pem
```

Commit the certificate. The app uses it to verify that updates came from this server.

### 3. Create an API key

Dashboard → **API tokens**. Copy it now; it cannot be shown again. **xprem-publish** uses it as `EOO_TOKEN`.

### 4. `npx eoas init`

From the Expo project root:

```bash
npx eoas init
```

Prompts on the live docs:

| Prompt | Answer |
| --- | --- |
| Project id (sent as expo-app-id the in the header) | Dashboard app UUID |
| URL of your update server | Server `BASE_URL` — this is `updates.url` |
| Do you have already generated your certificates | **Yes** (downloaded above) |

`eoas init` also adds three keys to `updates.requestHeaders`: `expo-channel-name`, `expo-app-id`, and `xprem-branch`. Keep all three — `expo-updates` only sends headers declared at build time, and `xprem-branch` is what makes branch surfing possible. Keep `expo-channel-name` as a **literal string**, not `process.env`; the config is evaluated at export time and an unset variable silently drops the key.

### 5. Channel

Each native build is bound to a release channel. After the first publish, create a channel in the dashboard and attach it to the branch. Use the channel name that will be baked into the Expo build. Confirm on the live pages rather than assuming `production`.

### 6. Native rebuild

Server URL and signing certificate are embedded at **build time**. Existing binaries keep the old configuration. A new native build is required after this wiring.

Do not walk through publish here — hand off to **xprem-publish**.
