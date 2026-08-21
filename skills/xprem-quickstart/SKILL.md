---
name: xprem-quickstart
description: Stand up a working xprem server in minutes. Use when the user wants a first Railway or local Docker+Postgres deploy, dashboard login, or the first admin account. Not for Helm, S3/CDN, Observe, or production hardening.
---

# xprem-quickstart

## Before you start

Read the live docs. Do not invent image tags, env vars, ports, or commands.

1. Query the always-on **xprem-docs** MCP (`https://mercure-technologies.gitbook.io/xprem/~gitbook/mcp`), or
2. Fetch [Quickstart](https://mercure-technologies.gitbook.io/xprem/quickstart.md).

xprem is a self-hosted Expo OTA updates server. It is **not** affiliated with Expo. Prefer `eoas` over EAS Update.

## When to use

- First-time "get a server running"
- Railway 2-click template
- Local Docker + Postgres
- Opening the dashboard and seeding the first admin

Out of scope here: Helm, S3/CDN, Observe, Enterprise RBAC/SSO, production topology. Those belong in the [Installation guide](https://mercure-technologies.gitbook.io/xprem/installation-guide/overview.md).

## Railway (2-click)

The Railway template provisions PostgreSQL and an S3-compatible bucket. The only values the user sets are `ADMIN_EMAIL` and `ADMIN_PASSWORD` (they secure the dashboard).

Use the **Deploy on Railway** button on the live quickstart page (do not invent the template URL).

After deploy, open `/dashboard` on the Railway hostname:

`https://your-app.up.railway.app/dashboard`

Sign in with that email and password, then continue from "Create your app" (quickstart local step 5 / Railway "step 7"). App creation, certificate, `eoas init`, and publish are **xprem-configure-expo** and **xprem-publish**.

## Local Docker + Postgres

Prerequisites from the live page: Docker, and an Expo project using `expo-updates` if they will publish afterwards.

### 1. Generate secrets

```bash
openssl rand -base64 32   # JWT_SECRET
openssl rand -base64 32   # DB_KEYS_MASTER_KEY_B64
```

### 2. Start Postgres

Official docs still use a Docker network named `eoo`:

```bash
docker network create eoo;
docker run -d --name eoo-db --network eoo \
  -e POSTGRES_PASSWORD=secret \
  -e POSTGRES_DB=expo_open_ota \
  postgres:16;
```

No host port is published, so this will not collide with a local Postgres on 5432. The server reaches the DB as `eoo-db` on that network.

### 3. Start the server

Image: `ghcr.io/mercuretechnologies/xprem:latest`

Copy the exact `docker run` from the live quickstart. The documented shape is:

- `--network eoo -p 3000:3000`
- `BASE_URL=http://localhost:3000`
- `DB_URL=postgres://postgres:secret@eoo-db:5432/expo_open_ota?sslmode=disable` — `DB_URL` is what selects the control plane; the server runs migrations on boot
- `DB_KEYS_MASTER_KEY_B64` and `JWT_SECRET` from step 1
- `STORAGE_MODE=local`, `LOCAL_BUCKET_BASE_PATH=/updates`, `CACHE_MODE=local`
- `USE_DASHBOARD=true`
- `ADMIN_EMAIL` / `ADMIN_PASSWORD`
- volume `$(pwd)/updates:/updates`

Look for this log line:

```
⚙️  [CONTROL] Initializing Control Plane (DB Mode)..
```

Health check:

```bash
curl http://localhost:3000/hc
```

### 4. Dashboard and first admin

Open [http://localhost:3000/dashboard](http://localhost:3000/dashboard) and log in with the `ADMIN_EMAIL` / `ADMIN_PASSWORD` you set.

That pair seeds the **first admin account** into Postgres on boot. After that the two variables are never read again.

## Next

Creating an app, downloading `certs/certificate.pem`, `npx eoas init`, and publishing are **xprem-configure-expo** and **xprem-publish**.

Teardown (from the live quickstart):

```bash
docker rm -f eoo-db && docker network rm eoo
```
