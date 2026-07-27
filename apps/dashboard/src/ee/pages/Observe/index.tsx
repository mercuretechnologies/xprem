// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { lazy, Suspense } from 'react';
import { Navigate, useLocation, useParams } from 'react-router';
import { Skeleton } from '@/components/ui/skeleton';
import { FilterBar } from './FilterBar';
import { useObserveFilters } from './filters';
import { isObservePage, observePage } from './navigation';

const OverviewView = lazy(() =>
  import('./OverviewView').then(module => ({ default: module.OverviewView }))
);
const MetricsView = lazy(() =>
  import('./MetricsView').then(module => ({ default: module.MetricsView }))
);
const EventsView = lazy(() =>
  import('./EventsView').then(module => ({ default: module.EventsView }))
);
const DevicesView = lazy(() =>
  import('./DevicesView').then(module => ({ default: module.DevicesView }))
);
const IdentityAttributes = lazy(() =>
  import('./IdentityAttributes').then(module => ({ default: module.IdentityAttributes }))
);

export const Observe = () => {
  const { page: requested } = useParams<{ page: string }>();
  const { search } = useLocation();
  const page = observePage(requested);
  const filters = useObserveFilters(page.scopes);

  // One canonical URL per page, so the sidebar can highlight on an exact
  // match and a pasted link always points at a page that exists. The query
  // string carries the filters and must survive both redirects.
  if (requested === undefined) return <Navigate to={`/observe/overview${search}`} replace />;
  // The log tail is part of the event table now, and links to it are already
  // out there.
  if (requested === 'logs') return <Navigate to={`/observe/events${search}`} replace />;
  if (!isObservePage(requested)) return <Navigate to={`/observe/overview${search}`} replace />;

  return (
    <div className="space-y-5">
      <header>
        <div className="flex items-center gap-2.5">
          <h1 className="font-display text-[26px] font-semibold tracking-tight">{page.label}</h1>
          {page.minimumSdk && (
            <span
              title={`Needs the expo-observe SDK ${page.minimumSdk} or later in your app`}
              className="rounded-full border border-primary/20 bg-primary/[0.07] px-2 py-0.5 font-mono text-[10px] text-primary">
              SDK {page.minimumSdk}+
            </span>
          )}
        </div>
        <p className="mt-1 text-sm text-muted-foreground">{page.question}</p>
      </header>

      {!page.scopes.includes('none') && (
        <FilterBar filters={filters} showDimensions={page.value === 'metrics'} />
      )}

      <Suspense fallback={<Skeleton className="h-[520px] rounded-xl" />}>
        {page.value === 'overview' && <OverviewView filters={filters} />}
        {page.value === 'metrics' && <MetricsView filters={filters} />}
        {page.value === 'events' && <EventsView filters={filters} />}
        {page.value === 'devices' && <DevicesView filters={filters} />}
        {page.value === 'attributes' && <IdentityAttributes />}
      </Suspense>
    </div>
  );
};
