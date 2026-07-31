// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE at the repository root); it is NOT covered by the MIT
// license of this repository.

import { ReactNode } from 'react';
import { Link } from 'react-router';
import { ArrowUpRight, Lock, Sparkles } from 'lucide-react';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';

// Links to the enterprise page with the dashboard URL and the locked feature
// as query params, so we know where the click came from.
const enterpriseUrl = (feature?: string) => {
  const url = new URL('https://xprem.dev/enterprise');
  url.searchParams.set('referrer', window.location.href);
  if (feature) url.searchParams.set('feature', feature);
  return url.toString();
};

// The upsell dialog shown when someone reaches for an enterprise feature
// without a valid license. Used by EnterpriseFeatureGate for masked blocks,
// and directly by inline actions (like the branch protection toggle) where a
// masking overlay would not fit.
//
// Pass `feature` to explain what the specific feature does, on top of the
// generic enterprise pitch; the masked-block gate can omit it since the
// surrounding UI already shows what is locked.
export const EnterpriseExplainerDialog = ({
  open,
  onOpenChange,
  feature,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  feature?: { name: string; description: ReactNode };
}) => (
  <Dialog open={open} onOpenChange={onOpenChange}>
    <DialogContent>
      <DialogHeader>
        <div className="mb-1 flex h-11 w-11 items-center justify-center rounded-lg border border-emerald-400/25 bg-emerald-400/10 shadow-card">
          <Lock className="h-5 w-5 text-emerald-700 dark:text-white" strokeWidth={2.2} />
        </div>
        <DialogTitle>{feature ? feature.name : 'Unlock Enterprise features'}</DialogTitle>
        <DialogDescription>
          This feature is part of the Enterprise edition of xprem. Your deployment currently
          runs the community edition, so it stays visible here but locked.
        </DialogDescription>
      </DialogHeader>
      <div className="space-y-3 text-sm text-muted-foreground">
        {feature && (
          <div className="flex gap-2.5 rounded-lg border border-emerald-400/25 bg-emerald-400/[0.07] p-3 text-foreground">
            <Sparkles className="mt-0.5 h-4 w-4 shrink-0 text-emerald-700 dark:text-emerald-300" />
            <p className="text-xs leading-relaxed">{feature.description}</p>
          </div>
        )}
        <p>
          Want to know more or get a license? Everything is on{' '}
          <a
            href={enterpriseUrl(feature?.name)}
            target="_blank"
            rel="noreferrer"
            className="font-medium text-link hover:underline">
            xprem.dev/enterprise
          </a>
          .
        </p>
        <p>
          Already have a key? Activate it on the{' '}
          <Link
            to="/license"
            onClick={() => onOpenChange(false)}
            className="font-medium text-link hover:underline">
            License page
          </Link>
          .
        </p>
      </div>
      <DialogFooter>
        <Button variant="outline" onClick={() => onOpenChange(false)}>
          Close
        </Button>
        <Button asChild>
          <a href={enterpriseUrl(feature?.name)} target="_blank" rel="noreferrer">
            <ArrowUpRight className="h-4 w-4" />
            Discover Enterprise
          </a>
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
);
