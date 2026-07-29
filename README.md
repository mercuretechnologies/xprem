<p align="center">
  <img src=".github/img/cover.svg" alt="xprem" />
</p>

<h3 align="center">Self-hosted over-the-air updates for Expo apps, built for production at scale.</h3>

<p align="center">
  An open-source Go server implementing the <a href="https://docs.expo.dev/technical-specs/expo-updates-1/">Expo Updates protocol</a>,<br/>
  with a web dashboard, multi-app support, progressive rollouts, instant rollbacks and one-command publishing.
</p>

<p align="center">
  <a href="https://github.com/mercuretechnologies/xprem/releases"><img src="https://img.shields.io/github/v/release/mercuretechnologies/xprem?label=release" alt="Latest release" /></a>
  <a href="https://www.npmjs.com/package/eoas"><img src="https://img.shields.io/npm/v/eoas?label=eoas%20CLI" alt="eoas on npm" /></a>
  <a href="https://github.com/mercuretechnologies/xprem/actions"><img src="https://img.shields.io/github/actions/workflow/status/mercuretechnologies/xprem/push.yml?label=CI" alt="CI status" /></a>
  <a href="./LICENSE.md"><img src="https://img.shields.io/badge/license-MIT%20%2B%20Enterprise-blue" alt="License" /></a>
</p>

<p align="center">
  <a href="https://mercure-technologies.gitbook.io/expo-open-ota">Documentation</a> · <a href="#quick-start">Quick start</a> · <a href="https://github.com/mercuretechnologies/xprem/issues">Issues</a> · <a href="mailto:contact@mercuretechnologies.com">Contact</a>
</p>

<p align="center">
  <a href="https://cursor.com/en/install-mcp?name=xprem-docs&config=eyJ1cmwiOiJodHRwczovL21lcmN1cmUtdGVjaG5vbG9naWVzLmdpdGJvb2suaW8vZXhwby1vcGVuLW90YS9%2BZ2l0Ym9vay9tY3AifQ%3D%3D"><img src="https://cursor.com/deeplink/mcp-install-dark.svg" alt="Install the docs MCP server in Cursor" height="28" /></a>
  <a href="https://insiders.vscode.dev/redirect/mcp/install?name=xprem-docs&config=%7B%22type%22%3A%22http%22%2C%22url%22%3A%22https%3A%2F%2Fmercure-technologies.gitbook.io%2Fexpo-open-ota%2F~gitbook%2Fmcp%22%7D"><img src="https://img.shields.io/badge/VS_Code-Install_docs_MCP-0098FF?logo=githubcopilot&logoColor=white" alt="Install the docs MCP server in VS Code" height="28" /></a>
</p>
<p align="center">
  <sub>The documentation is exposed as an <a href="#ask-the-docs-from-your-ai-assistant">MCP server</a>: plug it into Cursor, VS Code, Claude Code or any MCP client.</sub>
</p>

<p align="center">
  <sub>xprem is an independent open-source project. It is not affiliated with, endorsed or supported by <a href="https://expo.dev/">Expo</a>.</sub>
</p>

<p align="center">
  <img src=".github/img/dashboard-rollout.jpg" alt="The xprem dashboard showing a production branch with a progressive rollout in progress at 25%" />
</p>

> **Battle-tested in production.** xprem has been serving over-the-air updates in production since early 2025, to apps totaling more than a million monthly active users.

## Why self-host your OTA updates?

**Cut costs.** EAS Update pricing scales with your monthly active users. Self-hosting serves unlimited updates to unlimited devices for the price of your infrastructure.

**Own your infrastructure.** Updates live in your bucket, are served by your server and travel through your CDN, behind your security policies.

**No vendor lock-in.** The standard Expo Updates protocol on top of standard storage. Switch clouds anytime.

## Built for production and scale

**Your CDN does the heavy lifting.** Devices download updates from your CDN, straight out of your bucket. The server only answers lightweight update checks, so millions of devices checking in never turn into millions of downloads hitting it.

**It scales horizontally.** The update server is stateless: run as many replicas as your traffic needs, they stay consistent with each other out of the box.

**It plugs into your monitoring.** Metrics, dashboards and health checks come built in, ready for whatever observability stack you already run.

## Features

### Multi-app support

One server hosts all your Expo apps. Each app gets its own branches, channels, API tokens and update history, and your whole team manages everything from a single dashboard. No Expo account required.

### One-command publishing

Ship an update from your terminal or CI with the [eoas](https://www.npmjs.com/package/eoas) CLI. Rollbacks and republishing are one command too.

```bash
npx eoas publish --branch production --rollout-percentage 10
```

### Release channels & branches

Your app is built once and asks for a channel. You decide which branch of updates that channel serves: remap to roll out, remap back to roll back. No rebuild, no store review.

<p align="center">
  <img src=".github/img/dashboard-channels.jpg" alt="Channels page mapping release channels to branches, with a progressive branch rollout in progress" />
</p>

### Progressive rollouts

Ship to 10% of devices, watch your metrics, then increase, finish or revert in one click from the dashboard.

<p align="center">
  <img src=".github/img/dashboard-manage-rollout.jpg" alt="Manage rollout dialog with traffic split presets and promote or revert actions" />
</p>

### A/B testing

A channel can serve two branches at once, with devices split deterministically between them. Test two variants in production, promote the winner.

### Stateless mode

Start with nothing but a bucket and a few environment variables, no database. When you want the multi-app dashboard, plug in PostgreSQL and the server migrates itself into control plane mode automatically.

## Integrations

| | |
|---|---|
| **Storage** | <img src=".github/img/logos/aws-s3.svg" height="16" alt="" /> AWS S3 &nbsp;·&nbsp; <img src=".github/img/logos/google-cloud.svg" height="14" alt="" /> Google Cloud Storage &nbsp;·&nbsp; <img src=".github/img/logos/azure.svg" height="14" alt="" /> Azure Blob Storage &nbsp;·&nbsp; <img src=".github/img/logos/cloudflare.svg" height="14" alt="" /> Cloudflare R2 &nbsp;·&nbsp; <img src=".github/img/logos/minio.svg" height="14" alt="" /> MinIO &nbsp;·&nbsp; <img src=".github/img/logos/digital-ocean.svg" height="14" alt="" /> DigitalOcean Spaces &nbsp;·&nbsp; <img src=".github/img/logos/supabase.svg" height="14" alt="" /> Supabase Storage &nbsp;·&nbsp; any S3-compatible storage &nbsp;·&nbsp; local file system |
| **CDN** | <img src=".github/img/logos/aws-cloudfront.svg" height="16" alt="" /> CloudFront &nbsp;·&nbsp; <img src=".github/img/logos/google-cloud.svg" height="14" alt="" /> GCS signed URLs &nbsp;·&nbsp; <img src=".github/img/logos/azure.svg" height="14" alt="" /> Azure SAS URLs &nbsp;·&nbsp; custom CDN domain &nbsp;·&nbsp; direct serving |
| **Cache** | <img src=".github/img/logos/redis.svg" height="14" alt="" /> Redis &nbsp;·&nbsp; in-memory |
| **Key store** | <img src=".github/img/logos/aws-secrets-manager.svg" height="16" alt="" /> AWS Secrets Manager &nbsp;·&nbsp; environment variables &nbsp;·&nbsp; local key files &nbsp;·&nbsp; sealed in the database |

Plus expo-updates code signing, and Hermes source maps for Sentry or PostHog.

## Quick start

[![Deploy on Railway](https://railway.com/button.svg)](https://railway.com/deploy/expo-open-ota?referralCode=OEHlEK&utm_medium=integration&utm_source=template&utm_campaign=generic)

1. Deploy the server with the Railway button above, Docker or the Helm chart.
2. Run `npx eoas init` in your Expo project to wire it to your server.
3. Publish your first update with `npx eoas publish --branch production`.

The full walkthrough for both modes is in the documentation: [stateless mode](https://mercure-technologies.gitbook.io/expo-open-ota/stateless-mode/getting-started) and [control plane mode](https://mercure-technologies.gitbook.io/expo-open-ota/controle-plane-mode/getting-started). Coming from v2? Follow the [migration guide](https://mercure-technologies.gitbook.io/expo-open-ota/changelog-and-migrations/migrate-from-v2-to-v3).

### Ask the docs from your AI assistant

The documentation is exposed as an MCP server, so your AI tools can answer questions about xprem with the docs as their source:

```
https://mercure-technologies.gitbook.io/expo-open-ota/~gitbook/mcp
```

Use the install buttons at the top of this page for Cursor and VS Code. For Claude Code:

```bash
claude mcp add --transport http xprem-docs https://mercure-technologies.gitbook.io/expo-open-ota/~gitbook/mcp
```

Any other MCP-compatible client (ChatGPT connectors included) can be pointed at the same URL.

## Enterprise

For teams that need tighter control, the [Enterprise edition](https://mercure-technologies.gitbook.io/expo-open-ota/open-core-and-licensing) adds:

- **Single sign-on (OIDC)**: let your team sign in through Microsoft Entra ID, Okta, Google Workspace, Keycloak or any OpenID Connect issuer.
- **Protected branches**: once a branch is protected, only API tokens you explicitly allow can publish, roll back or republish on it. A token handed to a developer for staging can never ship to production.
- **IP allowlists**: restrict each API token to your CI runners' addresses, per address or CIDR range.
- **Audit logs**: every publish, rollback, login, role change and configuration edit is recorded with who did it, from which IP, and whether it succeeded. Browse the trail from the dashboard or export it as NDJSON for your compliance tooling.

<table>
  <tr>
    <td><img src=".github/img/dashboard-token-restrictions.jpg" alt="API token access restrictions with protected branches and an IP allowlist" /></td>
    <td><img src=".github/img/dashboard-sso.jpg" alt="Single sign-on configuration page supporting any OIDC identity provider" /></td>
  </tr>
</table>

The enterprise code lives in public `ee/` directories you can read before you buy, and licenses are verified fully offline against an embedded key: your production servers never phone home. Everything else on this page is MIT. For a license, [contact us](mailto:contact@mercuretechnologies.com).

## Contributing

Contributions are welcome! For anything beyond a small fix, please open an issue before writing code. xprem is an open-core project and some advanced features are reserved for the commercial edition; the boundary is documented in [CONTRIBUTING.md](./CONTRIBUTING.md).

## Disclaimer

xprem is **not officially supported or affiliated with [Expo](https://expo.dev/)**. This is an independent open-source project.

## License

The core is MIT and will stay MIT. Enterprise features live in `ee/` directories and are covered by a commercial license (see [ee/LICENSE](./ee/LICENSE)); everything else is MIT (see [LICENSE](./LICENSE.md)).

## Contact

[contact@mercuretechnologies.com](mailto:contact@mercuretechnologies.com)
