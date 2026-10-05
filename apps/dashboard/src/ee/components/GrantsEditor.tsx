// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Plus, Trash2 } from 'lucide-react';
import { api } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { Button } from '@/components/ui/button';
import { Switch } from '@/components/ui/switch';
import { Combobox } from '@/components/Combobox';
import { PERMISSION_GROUPS } from '@/ee/lib/permissionCatalog';
import { RoleFormDialog } from '@/ee/components/RoleFormDialog';

export type DraftGrant = {
  appId: string;
  roleId: string | null;
  extraPermissions: string[];
};

// Per-app access editor shared by the user Roles panel and the creation
// modal. The primary flow is "pick a role per app"; direct permissions are
// deliberately tucked behind a per-app "custom permissions" toggle so they
// read as the advanced path, not as the way roles are assigned.
export const GrantsEditor = ({
  draft,
  onChange,
  disabled,
}: {
  draft: DraftGrant[];
  onChange: (next: DraftGrant[]) => void;
  disabled?: boolean;
}) => {
  const { apps } = useSelectedApp();
  const queryClient = useQueryClient();
  const rolesQuery = useQuery({
    queryKey: ['roles'],
    queryFn: () => api.getRoles(),
  });

  // The inline "New role" flow: which app card asked for it, so the created
  // role lands selected on that grant. 'none' marks the empty-state button,
  // which has no grant to select the role on.
  const [roleDialogForApp, setRoleDialogForApp] = useState<string | null>(null);

  // Cards whose custom section was opened by hand; cards carrying saved
  // extras start open so nothing granted is ever hidden.
  const [customizedApps, setCustomizedApps] = useState<Set<string>>(new Set());
  const isCustomized = (grant: DraftGrant) =>
    customizedApps.has(grant.appId) || grant.extraPermissions.length > 0;

  const appName = (appId: string) => {
    const app = apps.find(candidate => candidate.id === appId);
    return app?.name || appId;
  };
  const grantedAppIds = new Set(draft.map(grant => grant.appId));
  const availableApps = apps.filter(app => !grantedAppIds.has(app.id));

  // The Combobox renders its `label` prop for an empty value, so "no role"
  // needs a real sentinel value to display as a proper selection.
  const NO_ROLE_VALUE = 'none';
  const roleOptions = [
    { value: NO_ROLE_VALUE, label: 'No role (read-only)' },
    ...(rolesQuery.data ?? []).map(role => ({ value: role.id, label: role.name })),
  ];

  const updateGrant = (appId: string, patch: Partial<DraftGrant>) => {
    onChange(draft.map(grant => (grant.appId === appId ? { ...grant, ...patch } : grant)));
  };

  const setCustomized = (appId: string, next: boolean) => {
    setCustomizedApps(previous => {
      const draftSet = new Set(previous);
      if (next) {
        draftSet.add(appId);
      } else {
        draftSet.delete(appId);
      }
      return draftSet;
    });
    if (!next) {
      // Switching the custom section off means "role only": drop the extras
      // instead of keeping them active but invisible.
      updateGrant(appId, { extraPermissions: [] });
    }
  };

  const toggleExtra = (appId: string, permission: string, next: boolean) => {
    const grant = draft.find(candidate => candidate.appId === appId);
    if (!grant) return;
    const extras = new Set(grant.extraPermissions);
    if (next) {
      extras.add(permission);
    } else {
      extras.delete(permission);
    }
    updateGrant(appId, { extraPermissions: Array.from(extras) });
  };

  return (
    <div className="space-y-4">
      {(rolesQuery.data ?? []).length === 0 && !rolesQuery.isLoading && (
        <div className="flex items-center justify-between gap-3 rounded-lg border bg-muted/30 px-3 py-2.5 text-xs text-muted-foreground">
          <span>No roles yet. Create one, e.g. Release manager, then assign it here.</span>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={disabled}
            onClick={() => setRoleDialogForApp('none')}
            className="h-7 shrink-0 gap-1">
            <Plus className="h-3.5 w-3.5" /> Create role
          </Button>
        </div>
      )}

      {draft.map(grant => (
        <div key={grant.appId} className="rounded-xl border p-4">
          <div className="flex items-center justify-between gap-3">
            <p className="text-sm font-medium">{appName(grant.appId)}</p>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              disabled={disabled}
              onClick={() => onChange(draft.filter(g => g.appId !== grant.appId))}
              className="h-8 w-8 p-0 text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
              title="Remove access to this app">
              <Trash2 />
            </Button>
          </div>

          <div className="mt-3 space-y-1.5">
            <p className="text-xs font-medium text-muted-foreground">Role on this app</p>
            <Combobox
              modal
              options={roleOptions}
              disabled={disabled}
              value={grant.roleId ?? NO_ROLE_VALUE}
              onChange={value =>
                updateGrant(grant.appId, {
                  // Re-selecting the current option makes the Combobox emit
                  // '': both spellings mean "no role".
                  roleId: value === NO_ROLE_VALUE || value === '' ? null : value,
                })
              }
              loading={rolesQuery.isLoading}
              label="role"
              className="w-full"
              action={{
                label: 'New role',
                icon: <Plus className="h-3.5 w-3.5" />,
                onSelect: () => setRoleDialogForApp(grant.appId),
              }}
            />
          </div>

          <div className="mt-3.5 border-t pt-3">
            <label className="flex cursor-pointer items-center justify-between gap-3 text-sm">
              <span className="text-muted-foreground">
                Custom permissions for this app
                <span className="block text-xs text-muted-foreground/70">
                  Granted on top of the role, for one-off needs.
                </span>
              </span>
              <Switch
                checked={isCustomized(grant)}
                disabled={disabled}
                onCheckedChange={next => setCustomized(grant.appId, next)}
                aria-label="Custom permissions for this app"
              />
            </label>
            {isCustomized(grant) && (
              <div className="mt-3 grid grid-cols-1 gap-x-4 gap-y-2 sm:grid-cols-2">
                {PERMISSION_GROUPS.flatMap(group => group.permissions).map(permission => (
                  <label
                    key={permission.value}
                    className="flex cursor-pointer items-center justify-between gap-2 text-sm"
                    title={permission.description}>
                    <span className="truncate">{permission.label}</span>
                    <Switch
                      checked={grant.extraPermissions.includes(permission.value)}
                      disabled={disabled}
                      onCheckedChange={next => toggleExtra(grant.appId, permission.value, next)}
                      aria-label={permission.label}
                    />
                  </label>
                ))}
              </div>
            )}
          </div>
        </div>
      ))}

      {availableApps.length > 0 && (
        <div className="space-y-1.5">
          <p className="text-xs font-medium text-muted-foreground">Grant access to</p>
          <Combobox
            modal
            options={availableApps.map(app => ({ value: app.id, label: app.name || app.id }))}
            disabled={disabled}
            value=""
            onChange={appId => {
              if (appId) {
                onChange([...draft, { appId, roleId: null, extraPermissions: [] }]);
              }
            }}
            label="Select an app…"
            className="w-full"
          />
        </div>
      )}

      <RoleFormDialog
        open={roleDialogForApp !== null}
        role={null}
        onClose={() => setRoleDialogForApp(null)}
        onSaved={created => {
          queryClient.invalidateQueries({ queryKey: ['roles'] });
          if (created && roleDialogForApp && roleDialogForApp !== 'none') {
            updateGrant(roleDialogForApp, { roleId: created.id });
          }
        }}
      />
    </div>
  );
};
