# Enterprise Edition (`ee/`)

This directory — and every other directory named `ee/` in this repository, such
as `apps/dashboard/src/ee/` — contains the source code of xprem
Enterprise Edition.

## License

The code here is **not** MIT. It is governed by the commercial license in
[`LICENSE`](./LICENSE): you may read it, and copy or modify it for development
and testing purposes, but using it in production requires a valid Enterprise
license key.

Everything outside `ee/` directories is and will remain MIT.

## How gating works

Enterprise features are compiled into the regular server binary but stay
dormant until a valid license key is activated. Keys are checked and attached
from the dashboard against the Mercure Technologies license server
(`https://api.xprem.dev`); the resulting activation is persisted in the
database and re-validated every 15 minutes. When the server refuses or cannot
be reached, enterprise features stay on for a 7-day grace window (the
dashboard warns to contact support@xprem.dev) before the license drops back
to community edition.

- `ee/licensing` — license server client (check/attach/validate), grace
  window handling, and runtime activation state (`licensing.IsEnterprise()`).

## Conventions

Every source file in an `ee/` directory starts with this header:

```
// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.
```

## Contributions

External pull requests touching `ee/` directories are not accepted for now
(accepting them would require a contributor license agreement).
