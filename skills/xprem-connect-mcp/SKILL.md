---
name: xprem-connect-mcp
description: Connect Cursor or another MCP client to a deployed xprem control plane at https://<host>/mcp. Use when the user wants an agent to list apps, branches, channels or updates, or to rollback/republish on their own server. Not for the public docs MCP (already bundled).
---

# xprem-connect-mcp

## Before you start

Read the live docs. Do not invent endpoints, OAuth paths, or a Mercure-hosted control plane.

1. Query the **xprem-docs** MCP (`https://mercure-technologies.gitbook.io/xprem/~gitbook/mcp`), or
2. Fetch [MCP server](https://mercure-technologies.gitbook.io/xprem/ai/mcp-server.md).

There is **no** Mercure-hosted instance MCP. The user brings their own host.

## When to use

- Connect Cursor / Grok Bot / Claude Code / VS Code to a running xprem
- The user has a public URL and wants `/mcp`
- Docs MCP works but instance tools (apps, branches, rollback) do not

## Requirements

- **Control plane (database).** `/mcp` exists only when the server runs with a database. A stateless deployment does **not** expose `/mcp`.
- `BASE_URL` must be the exact **public** URL clients use. It is the OAuth issuer and token audience. An internal hostname or missing path breaks discovery.
- Reverse proxy must forward `/mcp`, `/oauth/`, and `/.well-known/` on that same public host.
- Several replicas: route `/mcp` with session affinity (the MCP session lives in memory on the replica that created it).
- `JWT_SECRET` signs MCP access tokens the same way it signs dashboard sessions.

## Endpoint

```
https://<their-host>/mcp
```

Streamable HTTP. OAuth 2.1 as the **dashboard account**. The first connect opens the consent page; the user logs in and approves. After that the client stores and refreshes tokens. Sessions idle more than 30 minutes are dropped server-side; the client re-initializes.

## Configure Cursor (this plugin)

The plugin already declares an `xprem` HTTP MCP server whose URL is the user variable `XPREM_MCP_URL` (optional, not required).

1. Ask for the public control-plane URL. Do not guess a host.
2. Set **Plugins → Configure → xprem instance MCP URL** to `https://<host>/mcp` (full endpoint, including `/mcp`).
3. If they are testing a local copy of this plugin, you may also write the same URL into their Cursor MCP config:

```json
{
  "mcpServers": {
    "xprem": {
      "type": "http",
      "url": "https://updates.example.com/mcp"
    }
  }
}
```

Replace the example host with **theirs**. Never write a Mercure-owned host. Never hardcode a token.

Claude Code (from the live docs):

```bash
claude mcp add --transport http xprem https://updates.example.com/mcp
```

The **docs** MCP stays at `https://mercure-technologies.gitbook.io/xprem/~gitbook/mcp` and needs no auth. Do not replace it with the instance URL.

## Permissions

The agent acts as the connected dashboard account and cannot do more than that account.

- Community default (no Enterprise RBAC): members can use **read** tools on apps they see. Certificate download and every **write** (create/delete branches and channels, rollback, republish) is **admin** only.
- Enterprise RBAC: per-app permissions; writes can be granted app by app. The audit-log tool stays admin-only.

Destructive tools (`delete_branch`, `delete_channel`, `rollback_branch`) declare the MCP destructive annotation. **Always ask for explicit confirmation** before calling them.

Core MIT tools (confirm the current list on the live page) include `whoami`, `get_apps`, `get_branches`, `get_channels`, `get_updates`, `create_branch`, `create_channel`, `rollback_branch`, `republish_update`, and related read/write tools. Enterprise / Observe tools (`query_logs`, `get_observe_overview`, device search, audit) are **not** MIT — do not treat them as available on every deploy.

After connect, start with `whoami`.
