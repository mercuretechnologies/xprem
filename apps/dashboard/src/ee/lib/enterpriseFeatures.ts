// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE at the repository root); it is NOT covered by the MIT
// license of this repository.

import {
  Braces,
  Bug,
  Fingerprint,
  KeyRound,
  Lock,
  LucideIcon,
  ScrollText,
  ShieldCheck,
} from 'lucide-react';

// What the dashboard says about an enterprise feature wherever it is locked.
export type EnterpriseFeature = { name: string; description: string; icon: LucideIcon };

export const auditLogFeature: EnterpriseFeature = {
  name: 'Audit log',
  description:
    'Record every state-changing action on this server, with who performed it, on what, and with which outcome.',
  icon: ScrollText,
};

export const rolesFeature: EnterpriseFeature = {
  name: 'Roles',
  description:
    'Create roles with granular permissions, like Release manager, and assign them to users per app.',
  icon: ShieldCheck,
};

export const ssoFeature: EnterpriseFeature = {
  name: 'Single sign-on',
  description:
    'Sign in through an OIDC identity provider such as Okta, Entra, Google, Auth0 or Keycloak. Accounts are provisioned on first sign-in.',
  icon: Fingerprint,
};

export const identityAttributesFeature: EnterpriseFeature = {
  name: 'Identity attributes',
  description:
    'Declare custom attributes such as user_id, plan or tenant, and use them as cohorts across Observe.',
  icon: Braces,
};

export const tokenAccessFeature: EnterpriseFeature = {
  name: 'Token access',
  description:
    'Restrict each API token to a set of branches and actions, and to an IP allowlist in CIDR notation.',
  icon: KeyRound,
};

export const errorTrackingFeature: EnterpriseFeature = {
  name: 'Error tracking',
  description:
    "Resolves each stack frame to its original file, line and function from the update's source map, and groups the same error across updates.",
  icon: Bug,
};

export const branchProtectionFeature: EnterpriseFeature = {
  name: 'Branch protection',
  description:
    'Protect critical branches like production so they cannot be deleted, by anyone, until the protection is lifted. Restricting who may publish on a branch is a separate feature: it is decided per API token, on the API tokens page.',
  icon: Lock,
};
