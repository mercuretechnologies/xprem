// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE at the repository root); it is NOT covered by the MIT
// license of this repository.

import { ReactNode, useState } from 'react';
import { Link } from 'react-router';
import { useQuery } from '@tanstack/react-query';
import { Sparkles } from 'lucide-react';
import { api } from '@/lib/api';
import { Button } from '@/components/ui/button';
import { EnterpriseExplainerDialog } from '@/ee/components/EnterpriseExplainerDialog';
import { EnterpriseFeature } from '@/ee/lib/enterpriseFeatures';

// Wraps an enterprise-only block. With a valid license the children render
// untouched; without one `fallback`, or a panel naming the feature, takes their place.
export const EnterpriseFeatureGate = ({
  children,
  feature,
  fallback,
}: {
  children: ReactNode;
  feature: EnterpriseFeature;
  fallback?: ReactNode;
}) => {
  const [isExplainerOpen, setIsExplainerOpen] = useState(false);

  const licenseQuery = useQuery({
    queryKey: ['license'],
    queryFn: () => api.getLicense(),
  });

  if (licenseQuery.isPending) {
    return null;
  }
  if (licenseQuery.data?.valid) {
    return <>{children}</>;
  }
  if (fallback) {
    return <>{fallback}</>;
  }

  return (
    <>
      <div className="flex flex-wrap items-center gap-x-6 gap-y-4 rounded-xl border border-emerald-400/20 bg-card px-6 py-5 shadow-card">
        <div className="flex h-11 w-11 shrink-0 items-center justify-center rounded-lg border border-emerald-400/25 bg-emerald-400/10 shadow-card">
          <feature.icon className="h-5 w-5 text-emerald-700 dark:text-white" strokeWidth={2} />
        </div>
        <div className="min-w-[240px] flex-1">
          <p className="text-[11px] font-medium text-emerald-700 dark:text-emerald-300">
            Enterprise
          </p>
          <p className="text-[15px] font-semibold">{feature.name}</p>
          <p className="mt-1 text-sm leading-relaxed text-muted-foreground">
            {feature.description}
          </p>
        </div>
        <div className="flex shrink-0 flex-wrap items-center gap-x-4 gap-y-2">
          <Button size="sm" onClick={() => setIsExplainerOpen(true)}>
            <Sparkles className="h-3.5 w-3.5" />
            Discover Enterprise
          </Button>
          <p className="text-xs text-muted-foreground">
            Have a key?{' '}
            <Link to="/license" className="font-medium text-link hover:underline">
              Activate it
            </Link>
          </p>
        </div>
      </div>

      <EnterpriseExplainerDialog
        open={isExplainerOpen}
        onOpenChange={setIsExplainerOpen}
        feature={feature}
      />
    </>
  );
};
