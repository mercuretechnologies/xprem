// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE at the repository root); it is NOT covered by the MIT
// license of this repository.

import { useQuery } from '@tanstack/react-query';
import { Link, useLocation } from 'react-router';
import { TriangleAlert } from 'lucide-react';
import { api } from '@/lib/api';
import { formatTimestamp } from '@/lib/utils';
import { useSettings } from '@/lib/SettingsContext';
import { licenseErrorMessage } from '@/ee/lib/licenseErrors';

// Deployment-wide warning shown on every page while the license server keeps
// refusing (grace window running) or after it dropped the license (grace
// exhausted). Shares the ['license'] query with the License page; the refetch
// interval keeps long-lived tabs from missing the window entirely.
export const LicenseGraceBanner = () => {
  const { CONTROL_PLANE_ENABLED } = useSettings();
  const location = useLocation();

  const licenseQuery = useQuery({
    queryKey: ['license'],
    queryFn: () => api.getLicense(),
    enabled: CONTROL_PLANE_ENABLED,
    refetchInterval: 5 * 60 * 1000,
  });

  // The License page already shows the same warning inline.
  if (location.pathname === '/license') {
    return null;
  }

  const license = licenseQuery.data;
  if (!license?.hasKey || !license.validationFailedAt) {
    return null;
  }

  const reason = licenseErrorMessage(license.validationErrorCode);
  const graceEndsAt = license.graceEndsAt ? formatTimestamp(license.graceEndsAt) : null;

  return (
    <div className="border-b border-red-800/40 bg-red-600 px-4 py-2.5 text-sm font-medium text-white dark:bg-red-700">
      <div className="mx-auto flex max-w-[1480px] flex-wrap items-center gap-x-2 gap-y-1">
        <TriangleAlert className="h-4 w-4 shrink-0" />
        {license.suspended ? (
          <span>
            Your Enterprise license has been deactivated. {reason} Contact{' '}
            <a className="underline" href="mailto:support@xprem.dev">
              support@xprem.dev
            </a>{' '}
            to restore it.
          </span>
        ) : (
          <span>
            We can&apos;t verify your Enterprise license. {reason} Enterprise features will be
            disabled {graceEndsAt ? `on ${graceEndsAt}` : 'soon'}. Contact{' '}
            <a className="underline" href="mailto:support@xprem.dev">
              support@xprem.dev
            </a>{' '}
            as soon as possible.
          </span>
        )}
        <Link to="/license" className="ml-auto shrink-0 underline">
          View license
        </Link>
      </div>
    </div>
  );
};
