// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router';
import type { ObserveQuery } from '@/lib/api';
import type { FilterScope } from './navigation';

export type ObservePeriod = '1h' | '24h' | '7d' | '14d' | '30d';

// snapMs rounds the window start down to a stable boundary. Without it every
// render computes a new `from` and react-query treats it as a brand new query,
// so nothing is ever served from cache. `to` stays unset so the head of the
// window keeps sliding to now on each refetch.
export const periods: Array<{
  value: ObservePeriod;
  label: string;
  windowMs: number;
  snapMs: number;
  liveMs: number;
}> = [
  { value: '1h', label: 'Last hour', windowMs: 3_600_000, snapMs: 60_000, liveMs: 5_000 },
  { value: '24h', label: 'Last 24 hours', windowMs: 86_400_000, snapMs: 300_000, liveMs: 15_000 },
  { value: '7d', label: 'Last 7 days', windowMs: 604_800_000, snapMs: 3_600_000, liveMs: 60_000 },
  {
    value: '14d',
    label: 'Last 14 days',
    windowMs: 1_209_600_000,
    snapMs: 3_600_000,
    liveMs: 60_000,
  },
  {
    value: '30d',
    label: 'Last 30 days',
    windowMs: 2_592_000_000,
    snapMs: 3_600_000,
    liveMs: 60_000,
  },
];

export const defaultPeriod: ObservePeriod = '24h';

export type FilterKey =
  | 'platform'
  | 'channel'
  | 'branch'
  | 'runtimeVersion'
  | 'updateId'
  | 'updateGroupId'
  | 'easClientId'
  | 'appVersion'
  | 'appBuildNumber'
  | 'easBuildId'
  | 'environment'
  | 'osName'
  | 'osVersion'
  | 'deviceModel'
  | 'countryCode'
  | 'attributes'
  // The state the device reported for a measurement. Reachable by clicking a
  // row of the matching split, never typed: their values are bucket names the
  // server chose, and nobody knows them by heart.
  | 'thermalState'
  | 'lowPowerMode'
  | 'networkType'
  | 'frozenFrames'
  | 'networkBytes';

// One table drives the URL codec, the chips and the scope rules, so a new
// filter cannot end up applied but unlabelled (or labelled but never sent).
// Every ObserveQuery field but `platform` is a plain optional string, so a
// descriptor can write into the query through its key with no cast.
type TextQueryKey = Exclude<keyof ObserveQuery, 'platform' | 'from' | 'to'>;

const descriptors: Array<{
  key: FilterKey;
  // Short URL spelling: these end up in links people paste to each other.
  param: string;
  label: string;
  // Where the value lands in the API query. Absent for `platform`, which is
  // the one field that is not a free string.
  queryKey?: TextQueryKey;
  // Which pages can honor this filter. The device registry knows the update a
  // device runs (hence its branch, runtime and platform, joined from the
  // update) plus the hardware it reported, but never the build dimensions,
  // which exist only in telemetry.
  scopes: FilterScope[];
  uuid?: boolean;
}> = [
  {
    key: 'platform',
    param: 'platform',
    label: 'Platform',
    scopes: ['telemetry', 'updateGroups', 'devices'],
  },
  {
    key: 'channel',
    param: 'channel',
    label: 'Channel',
    queryKey: 'channel',
    scopes: ['telemetry'],
  },
  {
    key: 'branch',
    param: 'branch',
    label: 'Branch',
    queryKey: 'branch',
    scopes: ['telemetry', 'updateGroups', 'devices'],
  },
  {
    key: 'runtimeVersion',
    param: 'runtime',
    label: 'Runtime',
    queryKey: 'runtimeVersion',
    scopes: ['telemetry', 'updateGroups', 'devices'],
  },
  {
    key: 'updateId',
    param: 'update',
    label: 'Update',
    queryKey: 'updateId',
    scopes: ['telemetry', 'updateGroups', 'devices'],
    uuid: true,
  },
  {
    key: 'updateGroupId',
    param: 'group',
    label: 'Update group',
    queryKey: 'updateGroupId',
    // The registry reaches a publish through the update each device runs, so
    // this narrows the inventory like any other release dimension.
    scopes: ['telemetry', 'updateGroups', 'devices'],
    uuid: true,
  },
  {
    key: 'easClientId',
    param: 'device',
    label: 'Device',
    queryKey: 'easClientId',
    scopes: ['telemetry', 'devices'],
    uuid: true,
  },
  {
    key: 'appVersion',
    param: 'appVersion',
    label: 'App version',
    queryKey: 'appVersion',
    scopes: ['telemetry'],
  },
  {
    key: 'appBuildNumber',
    param: 'buildNumber',
    label: 'Build number',
    queryKey: 'appBuildNumber',
    scopes: ['telemetry'],
  },
  {
    key: 'easBuildId',
    param: 'easBuild',
    label: 'EAS build',
    queryKey: 'easBuildId',
    scopes: ['telemetry'],
    uuid: true,
  },
  {
    key: 'environment',
    param: 'env',
    label: 'Environment',
    queryKey: 'environment',
    scopes: ['telemetry'],
  },
  // Hardware and OS are not offered as dropdowns in the bar on purpose: you
  // reach them by clicking a segment in a breakdown, which is the only place
  // where their values are both known and worth picking.
  { key: 'osName', param: 'os', label: 'OS', queryKey: 'osName', scopes: ['telemetry', 'devices'] },
  {
    key: 'osVersion',
    param: 'osVersion',
    label: 'OS version',
    queryKey: 'osVersion',
    scopes: ['telemetry', 'devices'],
  },
  {
    key: 'deviceModel',
    param: 'model',
    label: 'Model',
    queryKey: 'deviceModel',
    scopes: ['telemetry', 'devices'],
  },
  {
    key: 'countryCode',
    param: 'country',
    label: 'Country',
    queryKey: 'countryCode',
    scopes: ['telemetry', 'devices'],
  },
  {
    // One filter holding `key:value` pairs rather than a key field and a value
    // field: several attributes narrow together, and each can carry several
    // values, which two paired fields have no way to spell.
    key: 'attributes',
    param: 'attr',
    label: 'Attribute',
    queryKey: 'attr',
    scopes: ['telemetry', 'devices'],
  },
  // Conditions. They travel under their dimension name so the split a row came
  // from and the filter clicking it applies are spelled the same, and they are
  // scoped to the timings alone: a thermal state is a property of one
  // measurement, so there is no coherent way to narrow a device list or an
  // adoption curve on it.
  {
    key: 'thermalState',
    param: 'thermalState',
    label: 'Thermal state',
    queryKey: 'thermalState',
    scopes: ['timings'],
  },
  {
    key: 'lowPowerMode',
    param: 'lowPowerMode',
    label: 'Power',
    queryKey: 'lowPowerMode',
    scopes: ['timings'],
  },
  {
    key: 'networkType',
    param: 'networkType',
    label: 'Network',
    queryKey: 'networkType',
    scopes: ['timings'],
  },
  {
    key: 'frozenFrames',
    param: 'frozenFrames',
    label: 'Frozen frames',
    queryKey: 'frozenFrames',
    scopes: ['timings'],
  },
  {
    key: 'networkBytes',
    param: 'networkBytes',
    label: 'Data pulled',
    queryKey: 'networkBytes',
    scopes: ['timings'],
  },
];

// The param a filter travels under in the URL. Exported so a link can carry a
// filter across to another page without restating the codec.
export const filterParam = (key: FilterKey) =>
  descriptors.find(descriptor => descriptor.key === key)?.param ?? key;

export const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export type FilterState = Record<FilterKey, string[]>;

// An attribute filter travels as `key:value`. Keys cannot contain a colon, so
// the first one splits the pair and values keep theirs.
export const attributePair = (key: string, value: string) => `${key}:${value}`;

export const splitPair = (pair: string): [string, string] => {
  const at = pair.indexOf(':');
  return at < 0 ? [pair, ''] : [pair.slice(0, at), pair.slice(at + 1)];
};

export type FilterChip = {
  key: FilterKey;
  label: string;
  value: string;
  // What removing the chip toggles off: the whole `key:value` pair for an
  // attribute, the value itself everywhere else.
  raw: string;
  // False when the current page cannot honor it. The chip stays visible and
  // muted rather than silently doing nothing: an invisible ignored filter is
  // how people end up misreading a number.
  applied: boolean;
};

const emptyState = (): FilterState =>
  descriptors.reduce((state, descriptor) => {
    state[descriptor.key] = [];
    return state;
  }, {} as FilterState);

// The API query for one set of scopes. Taken as an argument rather than read
// from the page, because a page can hold a panel served by another source: the
// Overview draws telemetry, but its presence tile comes from the device
// registry, which honors a narrower set of filters.
const queryForScopes = (state: FilterState, scopes: FilterScope[]): ObserveQuery => {
  const applied: ObserveQuery = {};
  for (const descriptor of descriptors) {
    const values = state[descriptor.key];
    if (values.length === 0) continue;
    if (!scopes.some(scope => scope !== 'none' && descriptor.scopes.includes(scope))) continue;
    // A half-typed UUID is a 400 from the API, so it waits until complete
    // instead of blanking the page under the user's cursor.
    const usable = descriptor.uuid ? values.filter(value => uuidPattern.test(value)) : values;
    if (usable.length === 0) continue;
    if (descriptor.key === 'platform') {
      applied.platform = usable.filter(value => value === 'ios' || value === 'android');
    } else if (descriptor.queryKey) {
      // Every dimension is a list on the wire; platform is the one field
      // that is not a plain string, and it is handled above.
      (applied as Record<string, string[]>)[descriptor.queryKey] = usable;
    }
  }
  return applied;
};

const isPeriod = (value: string | null): value is ObservePeriod =>
  periods.some(period => period.value === value);

export const useObserveFilters = (scopes: FilterScope[]) => {
  const [searchParams, setSearchParams] = useSearchParams();

  const period: ObservePeriod = isPeriod(searchParams.get('period'))
    ? (searchParams.get('period') as ObservePeriod)
    : defaultPeriod;
  // isPeriod already guarantees the find succeeds; the fallback only exists to
  // satisfy the type, and it resolves through defaultPeriod rather than through
  // an index whose correctness would depend on the order of the table.
  const periodSpec =
    periods.find(entry => entry.value === period) ??
    periods.find(entry => entry.value === defaultPeriod)!;
  // Live is on by default on the short windows people watch during a rollout,
  // and off on the long ones where polling only costs ClickHouse time.
  const liveParam = searchParams.get('live');
  const live = liveParam == null ? periodSpec.windowMs <= 86_400_000 : liveParam === '1';

  // The window start is computed once and reused, so on its own it would stay
  // pinned to the moment the page opened and "last hour" would quietly grow
  // into "last three hours". This advances it one snap boundary at a time
  // while live, and freezes it when paused, which is what paused should mean.
  const [windowTick, setWindowTick] = useState(() => Date.now());
  useEffect(() => {
    if (!live) return;
    setWindowTick(Date.now());
    const timer = window.setInterval(() => setWindowTick(Date.now()), periodSpec.snapMs);
    return () => window.clearInterval(timer);
  }, [live, periodSpec.snapMs]);

  const state = useMemo(() => {
    const next = emptyState();
    for (const descriptor of descriptors) {
      next[descriptor.key] = searchParams
        .getAll(descriptor.param)
        .map(value => value.trim())
        .filter(Boolean);
    }
    return next;
  }, [searchParams]);

  const applies = useCallback(
    (key: FilterKey) => {
      const descriptor = descriptors.find(entry => entry.key === key);
      if (!descriptor) return false;
      return scopes.some(scope => scope !== 'none' && descriptor.scopes.includes(scope));
    },
    [scopes]
  );

  // replace: filter edits are not navigation steps. Pushing them would make
  // the back button walk through every keystroke instead of leaving the page.
  const write = useCallback(
    (mutate: (params: URLSearchParams) => void) => {
      setSearchParams(
        current => {
          const next = new URLSearchParams(current);
          mutate(next);
          return next;
        },
        { replace: true }
      );
    },
    [setSearchParams]
  );

  // Replaces the whole set of values for each named filter. Passing a single
  // string is still allowed, since most call sites set exactly one.
  const setFilters = useCallback(
    (patch: Partial<Record<FilterKey, string | string[]>>) => {
      write(params => {
        for (const [key, value] of Object.entries(patch)) {
          const descriptor = descriptors.find(entry => entry.key === key);
          if (!descriptor) continue;
          const values = (Array.isArray(value) ? value : [value ?? ''])
            .map(entry => entry.trim())
            .filter(Boolean);
          params.delete(descriptor.param);
          for (const entry of values) params.append(descriptor.param, entry);
        }
      });
    },
    [write]
  );

  // Adds or removes one value, which is what a multi-select toggle needs.
  const toggleFilter = useCallback(
    (key: FilterKey, value: string) => {
      const descriptor = descriptors.find(entry => entry.key === key);
      if (!descriptor || !value) return;
      write(params => {
        const current = params.getAll(descriptor.param);
        const next = current.includes(value)
          ? current.filter(entry => entry !== value)
          : [...current, value];
        params.delete(descriptor.param);
        for (const entry of next) params.append(descriptor.param, entry);
      });
    },
    [write]
  );

  const clearFilters = useCallback(() => {
    write(params => {
      for (const descriptor of descriptors) params.delete(descriptor.param);
    });
  }, [write]);

  const setPeriod = useCallback(
    (value: ObservePeriod) => {
      write(params => {
        if (value === defaultPeriod) params.delete('period');
        else params.set('period', value);
        // The live default follows the window length, so an explicit choice
        // made for another window must not stick to the new one.
        params.delete('live');
      });
    },
    [write]
  );

  // The dimension is not a filter: it does not narrow anything, it splits what
  // is already selected into overlaid series. Same URL, separate parameter.
  //
  // Exactly one, or none. Splitting on two at once multiplies the series into a
  // chart nobody can read, and it answers a question nobody asked: narrowing to
  // one device model and then splitting by OS version says the same thing, one
  // legible chart at a time. Old links carrying a comma-separated list keep
  // working, on their first dimension.
  const dimension = useMemo(() => {
    const raw = searchParams.get('by') ?? '';
    return raw.split(',')[0]?.trim() || '';
  }, [searchParams]);

  const setDimension = useCallback(
    (next: string) => {
      write(params => {
        if (next) params.set('by', next);
        else params.delete('by');
      });
    },
    [write]
  );

  const setLive = useCallback(
    (value: boolean) => {
      write(params => params.set('live', value ? '1' : '0'));
    },
    [write]
  );

  const query = useMemo<ObserveQuery>(() => {
    const from = new Date(
      Math.floor((windowTick - periodSpec.windowMs) / periodSpec.snapMs) * periodSpec.snapMs
    ).toISOString();
    return { from, ...queryForScopes(state, scopes) };
  }, [periodSpec.snapMs, periodSpec.windowMs, scopes, state, windowTick]);

  // What the Postgres device registry can honor of the current selection, for
  // a panel served by it on a page that reads from somewhere else. Carries no
  // window: the registry answers "right now" on its own presence window, not
  // on the period picker. A filter the page already shows as ignored stays
  // ignored here too, so the chips keep telling the truth.
  const registryQuery = useMemo(
    () =>
      queryForScopes(
        Object.fromEntries(
          descriptors.map(descriptor => [
            descriptor.key,
            applies(descriptor.key) ? state[descriptor.key] : [],
          ])
        ) as FilterState,
        ['devices']
      ),
    [applies, state]
  );

  // True when the selection holds a filter the registry cannot honor, so a
  // panel served by it can say its number is wider than the rest of the page
  // instead of quietly contradicting them.
  const registryHonorsAll = useMemo(
    () =>
      descriptors.every(
        descriptor =>
          state[descriptor.key].length === 0 ||
          !applies(descriptor.key) ||
          descriptor.scopes.includes('devices')
      ),
    [applies, state]
  );

  const chips = useMemo<FilterChip[]>(
    () =>
      descriptors.flatMap(descriptor =>
        // One chip per value: removing one narrows the comparison instead of
        // dropping the whole filter. An attribute carries its own key as the
        // label, so "plan pro" reads as the pair it is.
        state[descriptor.key].map(value => {
          const [key, attributeValue] = splitPair(value);
          const isAttribute = descriptor.key === 'attributes';
          return {
            key: descriptor.key,
            label: isAttribute ? key : descriptor.label,
            value: isAttribute ? attributeValue : value,
            raw: value,
            applied: applies(descriptor.key),
          };
        })
      ),
    [applies, state]
  );

  return {
    state,
    setFilters,
    toggleFilter,
    clearFilters,
    chips,
    query,
    registryQuery,
    registryHonorsAll,
    period,
    periodSpec,
    setPeriod,
    live,
    setLive,
    applies,
    dimension,
    setDimension,
  };
};

// What every Observe view receives: the applied API query plus the controls
// that produced it, so a view can offer a drill-down that writes back a filter.
export type ObserveFilters = ReturnType<typeof useObserveFilters>;

// Poll interval for a view, in ms, or false when live is off. Logs get their
// own cadence: a tail that lags 15s behind reads as broken.
export const liveInterval = (
  live: boolean,
  periodSpec: (typeof periods)[number],
  fast = false
): number | false => {
  if (!live) return false;
  return fast ? Math.min(periodSpec.liveMs, 2_000) : periodSpec.liveMs;
};
