// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE at the repository root); it is NOT covered by the MIT
// license of this repository.

import { ReactNode, useState } from 'react';
import { Link } from 'react-router';
import { LucideIcon, Sparkles } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { EnterpriseExplainerDialog } from '@/ee/components/EnterpriseExplainerDialog';
import { EnterpriseFeature } from '@/ee/lib/enterpriseFeatures';

export type FeaturePitchPoint = { icon: LucideIcon; text: string };

export const FeaturePitch = ({
  feature,
  title,
  description,
  points,
  illustration,
}: {
  feature: EnterpriseFeature;
  title: string;
  description: string;
  points?: FeaturePitchPoint[];
  illustration: ReactNode;
}) => {
  const [isExplainerOpen, setIsExplainerOpen] = useState(false);

  return (
    <>
      <section className="overflow-hidden rounded-xl border bg-card shadow-card">
        <div className="grid items-center gap-8 p-6 lg:grid-cols-[minmax(0,5fr)_minmax(0,7fr)] lg:p-10">
          <div>
            <p className="inline-flex items-center gap-1.5 rounded-full border border-emerald-400/25 bg-emerald-400/10 px-2.5 py-0.5 text-[11px] font-medium text-emerald-700 dark:text-emerald-300">
              <Sparkles className="h-3 w-3" />
              Enterprise
            </p>
            <h2 className="mt-4 text-2xl font-semibold tracking-tight">{title}</h2>
            <p className="mt-3 text-sm leading-relaxed text-muted-foreground">{description}</p>
            {points && (
              <ul className="mt-5 space-y-3 text-sm">
                {points.map(point => (
                  <li key={point.text} className="flex gap-3 text-muted-foreground">
                    <point.icon className="mt-0.5 h-4 w-4 shrink-0 text-primary" />
                    {point.text}
                  </li>
                ))}
              </ul>
            )}
            <div className="mt-6 flex flex-wrap items-center gap-x-4 gap-y-3">
              <Button onClick={() => setIsExplainerOpen(true)}>
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
          <div aria-hidden>{illustration}</div>
        </div>
      </section>

      <EnterpriseExplainerDialog
        open={isExplainerOpen}
        onOpenChange={setIsExplainerOpen}
        feature={feature}
      />
    </>
  );
};

export const SketchWindow = ({ path, children }: { path: string; children: ReactNode }) => (
  <div className="overflow-hidden rounded-xl border bg-background shadow-elevated">
    <div className="flex items-center gap-3 border-b bg-muted/50 px-4 py-2.5">
      <span className="flex gap-1.5">
        {[0, 1, 2].map(index => (
          <span key={index} className="h-2.5 w-2.5 rounded-full bg-muted-foreground/20" />
        ))}
      </span>
      <span className="text-[11px] text-muted-foreground">
        ota.xprem.dev<span className="text-foreground">/{path}</span>
      </span>
    </div>
    {children}
  </div>
);

export const SketchBar = ({
  w,
  className = 'bg-muted-foreground/15',
  h = 'h-1.5',
}: {
  w: string;
  className?: string;
  h?: string;
}) => <span className={`block shrink-0 rounded-full ${h} ${className}`} style={{ width: w }} />;
