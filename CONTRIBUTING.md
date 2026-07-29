# Contributing to xprem

Thank you for taking the time to contribute. This document explains how contributions work here, and — importantly — how the open-source and commercial sides of the project fit together, so you never invest effort into something that can't be merged.

## Before you write code

For small fixes — typos, documentation improvements, obvious bug fixes — feel free to open a pull request directly.

For anything significant (a new feature, a behavior change, a refactor), **please open an issue first** and wait for a maintainer's go-ahead before writing code. This is not bureaucracy: it lets us tell you early if the idea conflicts with the roadmap, overlaps with planned commercial features, or needs a different approach. A feature pull request opened without prior discussion may be closed with a pointer to this document, and nobody enjoys that outcome — least of all us.

## Open core policy

xprem is an open-core project. The complete OTA workflow is MIT-licensed and will stay that way: publishing updates, release channels, branches, rollbacks, storage backends, CDN integrations, the dashboard, Prometheus metrics, and A/B testing.

Advanced and organization-level capabilities are commercial. They live in `ee/` directories under a separate license (see [ee/LICENSE](./ee/LICENSE)) and are currently planned or built as enterprise features:

- Analytics beyond the built-in Prometheus metrics
- Advanced update targeting (regional, user-group based)
- API key scoping per release channel
- SSO / SAML authentication
- Audit logs
- Expo Observe Support
- Automatic rollbacks
- IP whitelisting
- Priority support

This list will evolve as the product grows, and new advanced capabilities may be added to it. However, **a feature already released under MIT will not be moved to the commercial edition** — the core only grows.

If you are unsure which side of the line your idea falls on, ask in an issue before building. We would much rather tell you in five minutes than after three weekends of your work.

## Enterprise code (`ee/` directories)

External pull requests touching `ee/` directories are not accepted for now: this code is not MIT-licensed, and accepting outside contributions to it would require a contributor license agreement. See [ee/README.md](./ee/README.md) for details.

## Questions

Open an issue, or reach out at [contact@mercuretechnologies.com](mailto:contact@mercuretechnologies.com).
