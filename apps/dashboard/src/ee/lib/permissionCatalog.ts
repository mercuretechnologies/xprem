// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { Permission } from '@/ee/lib/PermissionsContext';

// Display metadata for the permission catalog, grouped the way the role and
// grant editors lay their toggles out. The values mirror ee/rbac/permissions.go.
export type PermissionOption = {
  value: Permission;
  label: string;
  description: string;
};

export type PermissionGroup = {
  label: string;
  permissions: PermissionOption[];
};

export const PERMISSION_GROUPS: PermissionGroup[] = [
  {
    label: 'App',
    permissions: [
      {
        value: 'app:rename',
        label: 'Rename the app',
        description: 'Change the display name of the app.',
      },
      {
        value: 'app:delete',
        label: 'Delete the app',
        description: 'Remove the app with all of its branches, channels and updates.',
      },
      {
        value: 'certificate:read',
        label: 'Download the signing certificate',
        description: 'Key material used to verify update signatures.',
      },
    ],
  },
  {
    label: 'Branches',
    permissions: [
      {
        value: 'branch:create',
        label: 'Create branches',
        description: 'Add new update branches to the app.',
      },
      {
        value: 'branch:delete',
        label: 'Delete branches',
        description: 'Remove an update branch from the app.',
      },
      {
        value: 'branch:protect',
        label: 'Protect branches',
        description: 'Turn branch protection on and off. A protected branch cannot be deleted.',
      },
    ],
  },
  {
    label: 'Channels',
    permissions: [
      {
        value: 'channel:create',
        label: 'Create channels',
        description: 'Add new release channels to the app.',
      },
      {
        value: 'channel:delete',
        label: 'Delete channels',
        description: 'Builds configured with a deleted channel stop receiving updates.',
      },
      {
        value: 'channel:edit-branch',
        label: 'Change the channel branch',
        description: 'Point a release channel at a different branch.',
      },
      {
        value: 'channel:branch-surfing',
        label: 'Manage branch surfing',
        description:
          'Let devices on a channel pick which branch they are served, and set which branches are exposed.',
      },
    ],
  },
  {
    label: 'Rollouts',
    permissions: [
      {
        value: 'channel-rollout:manage',
        label: 'Manage channel rollouts',
        description: 'Start, adjust, promote or revert a progressive branch rollout.',
      },
      {
        value: 'update-rollout:manage',
        label: 'Manage update rollouts',
        description: 'Set or revert the rollout percentage of a single update.',
      },
    ],
  },
  {
    label: 'Updates',
    permissions: [
      {
        value: 'update:publish',
        label: 'Republish and roll back',
        description:
          'Put a past update back at the head of its branch, or send a branch back to the bundle embedded in the app. Both change what every device runs at its next update check.',
      },
    ],
  },
  {
    label: 'API tokens',
    permissions: [
      {
        value: 'apikeys:manage',
        label: 'Manage API tokens',
        description: 'Create and revoke publishing tokens and edit their restrictions.',
      },
    ],
  },
  {
    label: 'Identity',
    permissions: [
      {
        value: 'identity:read',
        label: 'Browse devices',
        description:
          'Read the device registry: the device list, one device in detail, the values of a metadata key and the live count. That is per-device data, including the metadata your app attached and the location.',
      },
      {
        value: 'identity:manage',
        label: 'Manage the identity allowlist',
        description: 'Choose which device metadata keys are accepted and their types.',
      },
    ],
  },
  {
    label: 'Observe',
    permissions: [
      {
        value: 'observe:read',
        label: 'Read telemetry',
        description:
          'Open the Observe explorer: the overview, events, metrics, breakdowns and the live map, and the raw log feed. A log record carries the client id, the session id and whatever body your app wrote.',
      },
    ],
  },
];
