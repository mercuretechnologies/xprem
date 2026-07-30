<p align="center">
  <img src=".github/img/cover.svg" alt="xprem" />
</p>

<h3 align="center">The complete control plane for Expo apps. Self-hosted.</h3>

<p align="center">
  Publish, roll out, roll back, and watch every update you ship.<br/>
  Health, adoption, events and logs come back from every device that received it,<br/>
  into your servers, your storage and your ClickHouse, over the official <a href="https://docs.expo.dev/technical-specs/expo-updates-1/">Expo Updates protocol</a>.
</p>

<p align="center">
  <a href="https://github.com/mercuretechnologies/xprem/releases"><img src="https://img.shields.io/github/v/release/mercuretechnologies/xprem?label=release" alt="Latest release" /></a>
  <a href="https://www.npmjs.com/package/eoas"><img src="https://img.shields.io/npm/v/eoas?label=eoas%20CLI" alt="eoas on npm" /></a>
  <a href="https://github.com/mercuretechnologies/xprem/actions"><img src="https://img.shields.io/github/actions/workflow/status/mercuretechnologies/xprem/push.yml?label=CI" alt="CI status" /></a>
  <a href="./LICENSE.md"><img src="https://img.shields.io/badge/license-MIT%20%2B%20Enterprise-blue" alt="License" /></a>
</p>

<p align="center">
  <a href="https://mercure-technologies.gitbook.io/xprem">Documentation</a> · <a href="#quick-start">Quick start</a> · <a href="https://github.com/mercuretechnologies/xprem/issues">Issues</a> · <a href="mailto:contact@xprem.dev">Contact</a>
</p>

<p align="center">
  <a href="https://cursor.com/en/install-mcp?name=xprem-docs&config=eyJ1cmwiOiJodHRwczovL21lcmN1cmUtdGVjaG5vbG9naWVzLmdpdGJvb2suaW8veHByZW0vfmdpdGJvb2svbWNwIn0%3D"><img src="https://cursor.com/deeplink/mcp-install-dark.svg" alt="Install the docs MCP server in Cursor" height="28" /></a>
  <a href="https://insiders.vscode.dev/redirect/mcp/install?name=xprem-docs&config=%7B%22type%22%3A%22http%22%2C%22url%22%3A%22https%3A%2F%2Fmercure-technologies.gitbook.io%2Fxprem%2F~gitbook%2Fmcp%22%7D"><img src="https://img.shields.io/badge/VS_Code-Install_docs_MCP-0098FF?logo=githubcopilot&logoColor=white" alt="Install the docs MCP server in VS Code" height="28" /></a>
</p>
<p align="center">
  <sub>The documentation is exposed as an <a href="#ask-the-docs-from-your-ai-assistant">MCP server</a>: plug it into Cursor, VS Code, Claude Code or any MCP client. And your xprem deployment is <a href="#talk-to-your-control-plane">one too</a>.</sub>
</p>


<p align="center">
  <img src=".github/img/dashboard-rollout.jpg" alt="The xprem dashboard showing a production branch with a progressive rollout in progress at 25%" />
</p>

> **Battle-tested in production.** xprem has been serving over-the-air updates in production since early 2025, to apps totaling more than a million monthly active users. Coming from EAS Update? `npx eoas init` migrates your app in about 30 seconds.

## Manage your OTA updates

xprem implements the official Expo Updates protocol, so your app keeps the standard `expo-updates` runtime. Publish, roll back and roll out progressively, with every bundle in your own bucket and every download URL pointing at infrastructure you own.

```bash
npx eoas publish --branch production --rollout-percentage 10
```

**Manage updates from CLI and CI.** Publish, roll back and republish are [eoas](https://www.npmjs.com/package/eoas) commands. Run them by hand or script them in your pipeline.

**Channels decide who gets what.** Each build ships with a channel baked in. Point the channel at a branch, and that's what those builds receive. Remap to roll out, remap back to roll back. No rebuild, no store review.

<p align="center">
  <img src=".github/img/dashboard-channels.jpg" alt="Channels page mapping release channels to branches, with a progressive branch rollout in progress" />
</p>

**Progressive rollouts.** Ship to a slice of the branch, watch the health chart, raise it. Deterministic, no per-device state.

<p align="center">
  <img src=".github/img/dashboard-manage-rollout.jpg" alt="Manage rollout dialog with traffic split presets and promote or revert actions" />
</p>

**A/B testing.** A channel can serve two branches at once, with devices split deterministically between them. Test two variants in production, promote the winner.

**Multi-app.** One server hosts all your Expo apps. Each app gets its own branches, channels, API tokens and update history, and your whole team manages everything from a single dashboard. No Expo account required.

## Catch the regression while it's small

Every crash, metric, event and log carries the exact update that produced it. A regression shows up while the rollout is still on a slice of the branch, and a republish reverts it.

**Native and JS crashes.** Both land tied to the update that shipped them, with the device count for each, so you see within minutes when a release breaks something.

**Split health by device, OS, region and screen.** Every metric breaks down by device model, OS, region, app version and screen, straight from react-navigation.

**Send your own events and logs.** Ship structured events and logs from the app, and read them next to the delivery data, filtered by the same update, branch and channel.

**Your ClickHouse, your tools.** The data lives in a database you own. Put Grafana, PostHog or Datadog on it, or query it raw.

## Assets download from your own infrastructure

When a device checks for an update, xprem answers with a manifest: the list of every asset the device has to download, each with a URL. xprem decides what those URLs are. Point them at your CDN, or sign them into a private bucket that never has to be made public.

The download traffic never touches the update server, which only has to answer a small JSON check. Millions of devices checking in never turn into millions of downloads hitting it.

| | |
|---|---|
| **Storage** | <img src=".github/img/logos/aws-s3.svg" height="16" alt="" /> AWS S3 &nbsp;·&nbsp; <img src=".github/img/logos/google-cloud.svg" height="14" alt="" /> Google Cloud Storage &nbsp;·&nbsp; <img src=".github/img/logos/azure.svg" height="14" alt="" /> Azure Blob Storage &nbsp;·&nbsp; <img src=".github/img/logos/cloudflare.svg" height="14" alt="" /> Cloudflare R2 &nbsp;·&nbsp; <img src=".github/img/logos/minio.svg" height="14" alt="" /> MinIO &nbsp;·&nbsp; <img src=".github/img/logos/digital-ocean.svg" height="14" alt="" /> DigitalOcean Spaces &nbsp;·&nbsp; <img src=".github/img/logos/supabase.svg" height="14" alt="" /> Supabase Storage &nbsp;·&nbsp; any S3-compatible storage &nbsp;·&nbsp; local file system |
| **Delivery** | <img src=".github/img/logos/aws-cloudfront.svg" height="16" alt="" /> CloudFront &nbsp;·&nbsp; custom CDN domain &nbsp;·&nbsp; <img src=".github/img/logos/aws-s3.svg" height="16" alt="" /> S3 presigned URLs &nbsp;·&nbsp; <img src=".github/img/logos/google-cloud.svg" height="14" alt="" /> GCS signed URLs &nbsp;·&nbsp; <img src=".github/img/logos/azure.svg" height="14" alt="" /> Azure SAS URLs &nbsp;·&nbsp; direct serving |
| **Cache** | <img src=".github/img/logos/redis.svg" height="14" alt="" /> Redis &nbsp;·&nbsp; in-memory |
| **Key store** | <img src=".github/img/logos/aws-secrets-manager.svg" height="16" alt="" /> AWS Secrets Manager &nbsp;·&nbsp; environment variables &nbsp;·&nbsp; local key files &nbsp;·&nbsp; sealed in the database |

Plus expo-updates code signing, and Hermes source maps for Sentry or PostHog.

## Talk to your control plane

xprem ships an MCP server, so agents reach the same control plane you do: branches, channels, updates, rollouts and the whole Observe dataset. Agents sign in with OAuth as dashboard users, and every call runs with that user's per-app permissions. Nothing more.

> "Which screens had the worst time to interactive since 3.4.2 rolled out?"
>
> "Create a hotfix branch and point the beta channel at it."
>
> "Something looks wrong since the last release. Roll production back to the previous update."

**Your permissions, app by app.** Agents sign in over OAuth as a dashboard user, and inherit exactly what that user can do on each app. No API keys.

**Operate releases from chat.** List branches, channels and updates, check a rollout, then roll back or republish, the same operations the dashboard exposes.

**Ask your data anything.** Plain-language questions over metrics, events and logs. The agent answers straight from Observe.

## Deploy everywhere

xprem is one Go process. It holds no session state, so you run as many replicas as you need behind your load balancer and they stay consistent on their own. Update checks are read-heavy and cheap; assets never pass through the process, so a replica only ever handles small JSON.

Run it with Docker, the Helm chart, or a single static binary under systemd. And you choose the shape at deploy time: if the only thing you want is to ship updates from your own bucket, stateless mode needs no database at all; plug in PostgreSQL when you want the multi-app dashboard, rollouts and the control plane.

## Quick start

[![Deploy on Railway](https://railway.com/button.svg)](https://railway.com/deploy/xprem?referralCode=OEHlEK&utm_medium=integration&utm_source=template&utm_campaign=generic)

1. Deploy the server with the Railway button above, Docker or the Helm chart.
2. Run `npx eoas init` in your Expo project to wire it to your server.
3. Publish your first update with `npx eoas publish --branch production`.

The full walkthrough for both modes is in the documentation: [stateless mode](https://mercure-technologies.gitbook.io/xprem/stateless-mode/getting-started) and [control plane mode](https://mercure-technologies.gitbook.io/xprem/controle-plane-mode/getting-started). Coming from v2? Follow the [migration guide](https://mercure-technologies.gitbook.io/xprem/changelog-and-migrations/migrate-from-v2-to-v3).

### Ask the docs from your AI assistant

The documentation is exposed as an MCP server, so your AI tools can answer questions about xprem with the docs as their source:

```
https://mercure-technologies.gitbook.io/xprem/~gitbook/mcp
```

Use the install buttons at the top of this page for Cursor and VS Code. For Claude Code:

```bash
claude mcp add --transport http xprem-docs https://mercure-technologies.gitbook.io/xprem/~gitbook/mcp
```

Any other MCP-compatible client (ChatGPT connectors included) can be pointed at the same URL.

## Why teams run their own

EAS Update is the fastest way to get OTA updates running on a small app. These are the reasons teams move to self-hosted infrastructure instead.

**No per-user pricing.** Nothing is metered. Unlimited devices and unlimited updates, for the cost of the server, the database and the bucket you already run.

**You own the data.** Update history in your Postgres, runtime signals in your ClickHouse, bundles in your bucket. Retention, residency, access rules and audit trail are yours to configure.

**Runs on a private or offline network.** Air-gapped deployments, internal apps and regulated environments. No outbound connection, no telemetry, and Enterprise licence keys verify offline.

**You control the scaling.** Stateless replicas behind your own load balancer, your CDN and the regions you choose. Capacity and rate limits are set by your infrastructure.

**No intermediary in the path.** Device to your server to your edge, over private links and internal DNS, inside the regions you already operate in.

**If we disappear, it keeps running.** MIT core, readable and forkable. Your release path never depends on a vendor staying alive.

## Fully open source

Publishing, branches, channels, rollbacks, progressive rollouts, every storage backend, every CDN integration, the dashboard and the Prometheus metrics are MIT. Everything you need to run OTA updates in production, free. A feature released under MIT never moves behind the commercial licence.

### Four things need a licence

They live in [`ee/`](./ee) directories in the same repository and ship in the same binary, so you can read the code before you buy. The key is verified offline: the server never contacts us, sends no telemetry, and keeps working in an air-gapped network.

- **RBAC**: roles instead of one shared admin account. Decide who can publish, who can move a channel to another branch, and who can only look.
- **SSO**: sign in through Microsoft Entra ID, Okta, Google Workspace, Keycloak or any OpenID Connect issuer, so access follows the accounts you already manage and revoke.
- **Branch protection**: mark the branches that reach real users as protected, then decide per API key whether it may publish to them. A sandbox CI job or a developer testing on staging gets a token that cannot touch production, instead of the one token that can do everything.
- **Custom device attributes**: attach your own attributes to logs, metrics and events, then slice by them. Your plan tier, your tenant, your feature flag, whatever your app knows about the device.

<table>
  <tr>
    <td><img src=".github/img/dashboard-token-restrictions.jpg" alt="API token access restrictions with protected branches and an IP allowlist" /></td>
    <td><img src=".github/img/dashboard-sso.jpg" alt="Single sign-on configuration page supporting any OIDC identity provider" /></td>
  </tr>
</table>

For a license, [contact us](mailto:contact@xprem.dev).

## Contributing

Contributions are welcome! For anything beyond a small fix, please open an issue before writing code. xprem is an open-core project and some advanced features are reserved for the commercial edition; the boundary is documented in [CONTRIBUTING.md](./CONTRIBUTING.md).

## Disclaimer

xprem is **not officially supported or affiliated with [Expo](https://expo.dev/)**. This is an independent open-source project.

## License

The core is MIT and will stay MIT. Enterprise features live in `ee/` directories and are covered by a commercial license (see [ee/LICENSE](./ee/LICENSE)); everything else is MIT (see [LICENSE](./LICENSE.md)).

## Contact

[contact@xprem.dev](mailto:contact@xprem.dev)
