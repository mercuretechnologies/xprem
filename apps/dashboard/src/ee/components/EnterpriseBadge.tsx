// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE at the repository root); it is NOT covered by the MIT
// license of this repository.

import { useQuery } from '@tanstack/react-query';
import { BadgeCheck } from 'lucide-react';
import { api } from '@/lib/api';
import { useSettings } from '@/lib/SettingsContext';

// Sidebar marker of an active Enterprise license. Renders nothing in
// stateless mode, while loading, or when no valid license is active; the
// community sidebar stays untouched. Shares the ['license'] query with the
// License page, so activating or removing a key updates it immediately.
export const EnterpriseBadge = () => {
  const { CONTROL_PLANE_ENABLED } = useSettings();

  const licenseQuery = useQuery({
    queryKey: ['license'],
    queryFn: () => api.getLicense(),
    enabled: CONTROL_PLANE_ENABLED,
  });

  const license = licenseQuery.data;
  if (!license?.valid) {
    return null;
  }

  return (
    <div className="mx-3 mt-3 flex items-center gap-2 rounded-lg border border-emerald-400/20 bg-emerald-400/[0.07] px-2.5 py-1.5 shadow-card">
      <BadgeCheck
        className="h-4 w-4 shrink-0 text-emerald-700 dark:text-emerald-300"
        strokeWidth={2}
      />
      <div className="min-w-0 leading-tight">
        <div className="text-[11px] font-semibold text-emerald-800 dark:text-emerald-200">
          Enterprise
        </div>
        <div
          className="truncate font-mono text-[10px] text-emerald-700/70 dark:text-emerald-300/60"
          title={license.licenseId}>
          {license.licenseId?.split('-')[0]}
        </div>
      </div>
    </div>
  );
};
