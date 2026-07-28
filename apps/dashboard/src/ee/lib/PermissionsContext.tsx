// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { createContext, useContext, useMemo, ReactNode } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { useCurrentUser } from '@/lib/CurrentUserContext';
import { useSelectedApp } from '@/lib/SelectedAppContext';

// Mirrors the server catalog (ee/rbac/permissions.go). An unknown string
// would simply never match, so drift fails closed.
export type Permission =
  | 'app:delete'
  | 'app:rename'
  | 'certificate:read'
  | 'branch:create'
  | 'branch:delete'
  | 'branch:protect'
  | 'channel:create'
  | 'channel:delete'
  | 'channel:edit-branch'
  | 'channel-rollout:manage'
  | 'update-rollout:manage'
  | 'update:publish'
  | 'update:publish-protected'
  | 'apikeys:manage'
  | 'identity:manage'
  | 'identity:read'
  | 'observe:read';

// What a member gets when roles are NOT enforced, mirroring rbac.Fallback on
// the server. It is declared at each call site rather than attached to the
// permission, because the server declares it per route: the same permission
// could gate one route that stays open to members without a license and
// another that does not, and guessing here would show buttons the server
// refuses or hide pages it allows.
export type Fallback = 'admin-only' | 'any-member';

type PermissionsContextValue = {
  // rbacEnabled reports whether fine-grained roles are enforced right now
  // (control plane + valid enterprise license). Deliberately not called
  // "enabled": accounts carry an enabled flag of their own and the two mean
  // nothing alike, a disabled account never gets a session in the first place.
  rbacEnabled: boolean;
  isAdmin: boolean;
  // can answers "may the current account do this on this app". Admins can do
  // everything; enforced members follow their grants; without enforcement the
  // call site's fallback decides, exactly as the route's does on the server.
  // Display gating only: the server re-checks every call.
  can: (appId: string | null | undefined, permission: Permission, fallback: Fallback) => boolean;
};

const PermissionsContext = createContext<PermissionsContextValue | null>(null);

export function PermissionsProvider({ children }: { children: ReactNode }) {
  // isAdmin from /api/me keeps the UI correct while the permission map is
  // still loading (an admin never flickers, a member starts read-only and
  // gains buttons when their grants arrive).
  const { isAdmin: sessionIsAdmin } = useCurrentUser();
  const { data } = useQuery({
    queryKey: ['me', 'permissions'],
    queryFn: () => api.getMyPermissions(),
  });

  const value = useMemo<PermissionsContextValue>(() => {
    const isAdmin = data ? data.isAdmin : sessionIsAdmin;
    const rbacEnabled = data?.rbacEnabled ?? false;
    const apps = data?.apps ?? null;
    return {
      rbacEnabled,
      isAdmin,
      can: (appId, permission, fallback) => {
        if (isAdmin) {
          return true;
        }
        if (!rbacEnabled) {
          return fallback === 'any-member';
        }
        if (!appId) {
          return false;
        }
        return apps?.[appId]?.includes(permission) ?? false;
      },
    };
  }, [data, sessionIsAdmin]);

  return <PermissionsContext.Provider value={value}>{children}</PermissionsContext.Provider>;
}

export function usePermissions(): PermissionsContextValue {
  const context = useContext(PermissionsContext);
  if (!context) {
    throw new Error('usePermissions must be used within a PermissionsProvider');
  }
  return context;
}

// useAppPermission is the one-liner for the common case: "may I do this on
// the app currently selected in the dashboard". The fallback is required, not
// defaulted, so a new call site has to answer the same question its route
// answered rather than inherit an assumption.
export function useAppPermission(permission: Permission, fallback: Fallback): boolean {
  const { selectedAppId } = useSelectedApp();
  const { can } = usePermissions();
  return can(selectedAppId, permission, fallback);
}
