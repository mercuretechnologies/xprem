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
dormant until a valid license key is activated. Licenses are issued through
[Keygen](https://keygen.sh) using the `ED25519_SIGN` scheme and verified fully
offline by `ee/licensing` against the account's embedded Ed25519 verify key —
no network calls, no phone home.

- `ee/licensing` — signed-key parsing, signature verification, and runtime
  activation state (`licensing.IsEnterprise()`).

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
