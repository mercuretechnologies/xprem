// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

// Client-side mirror of the audit action catalog (internal/auditlog on the
// server), grouped for the filter dropdown. Adding an action server-side
// means adding it here so it stays filterable.

export type AuditActionGroup = {
  label: string;
  actions: string[];
};

export const AUDIT_ACTION_GROUPS: AuditActionGroup[] = [
  {
    label: 'Auth & account',
    actions: ['user.login', 'user.sso_login', 'user.password_changed'],
  },
  {
    label: 'Users & privileges',
    actions: [
      'user.created',
      'user.updated',
      'user.deleted',
      'user.sso_provisioned',
      'user.sso_linked',
      'user.approved',
      'user.admin_granted',
      'user.admin_revoked',
      'role.created',
      'role.updated',
      'role.deleted',
      'user.grants_updated',
    ],
  },
  {
    label: 'Enterprise administration',
    actions: ['license.activated', 'license.removed', 'sso_config.saved', 'sso_config.deleted'],
  },
  {
    label: 'App management',
    actions: [
      'app.created',
      'app.renamed',
      'app.deleted',
      'channel.created',
      'channel.deleted',
      'channel_branch.mapped',
      'channel_branch_surfing.updated',
      'branch.created',
      'branch.deleted',
    ],
  },
  {
    label: 'Delivery',
    actions: [
      'update.published',
      'update.rollback',
      'update.republished',
      'channel_rollout.started',
      'channel_rollout.updated',
      'channel_rollout.ended',
      'update_rollout.set',
      'update_rollout.reverted',
    ],
  },
  {
    label: 'Credentials & key material',
    actions: [
      'api_key.created',
      'api_key.revoked',
      'api_key.restrictions_updated',
      'branch_protection.updated',
      'certificate.downloaded',
      'android_credentials.saved',
      'android_credentials.deleted',
    ],
  },
  {
    label: 'Access control',
    actions: ['permission.denied'],
  },
];
