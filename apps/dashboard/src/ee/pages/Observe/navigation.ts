// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import {
  Braces,
  Smartphone,
  ChartNoAxesCombined,
  Gauge,
  MousePointerClick,
} from 'lucide-react';

// Each page answers one question an app team actually asks, which is why they
// are named after the question and not after the table behind them. The order
// is the order those questions come up: "is it healthy right now", "is my last
// release fast and did it break anything", "what did the app record", "who is
// running it", then the attribute allowlist that feeds the filters.
export type ObservePage = 'overview' | 'metrics' | 'events' | 'devices' | 'attributes';

// Which filters mean anything on a page. Update-group health is answered from the
// Postgres device registry, which knows nothing about builds, environments or
// Identity cohorts: rather than letting people apply a filter and then warning
// them it was ignored, the bar only offers what the page can honor.
// 'timings' is the narrowest of them: the state a device reported for one
// measurement lives on the metric data point alone, so only the page drawing
// those timings can honor a filter on it.
export type FilterScope = 'telemetry' | 'timings' | 'updateGroups' | 'devices' | 'none';

export const observeNavigation: Array<{
  value: ObservePage;
  label: string;
  question: string;
  icon: typeof Gauge;
  scopes: FilterScope[];
  // Lowest expo-observe SDK that produces the data. Absent means the page
  // works with no SDK at all, from the manifest polls every client makes.
  minimumSdk?: number;
}> = [
  {
    value: 'overview',
    label: 'Overview',
    question: 'Is the app healthy right now?',
    icon: ChartNoAxesCombined,
    scopes: ['telemetry'],
  },
  {
    value: 'metrics',
    label: 'Metrics',
    question: 'Is the served update group healthy, and is the app fast for everyone?',
    icon: Gauge,
    scopes: ['telemetry', 'timings'],
  },
  {
    value: 'events',
    label: 'Events',
    question: 'How is the app being used, and what exactly happened on a device?',
    icon: MousePointerClick,
    scopes: ['telemetry'],
    minimumSdk: 56,
  },
  {
    value: 'devices',
    label: 'Devices',
    question: 'Which devices are out there, and what is on them?',
    icon: Smartphone,
    scopes: ['devices'],
  },
  {
    value: 'attributes',
    label: 'Attributes',
    question: 'Which device metadata can I filter on?',
    icon: Braces,
    scopes: ['none'],
  },
];

export const isObservePage = (value: string | undefined): value is ObservePage =>
  observeNavigation.some(page => page.value === value);

export const observePage = (value: string | undefined) =>
  observeNavigation.find(page => page.value === value) ?? observeNavigation[0];
