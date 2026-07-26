import type { ObserveBreakdownDimension } from '@/lib/api';
import type { FilterKey } from './filters';
import { deviceName, osLabel } from './deviceNames';

// The dimensions a metric can be split by. Ordered by how often the answer
// lives there: an app is usually slow on a device generation or an OS version
// long before it is slow on a channel.
export const dimensionCatalog: Array<{
  value: ObserveBreakdownDimension;
  label: string;
  // How a raw column value reads to a human. `context` is the qualifier the
  // API sends alongside, used only where the value means nothing on its own.
  labelFor: (value: string, context?: string) => string;
  // Which filters isolate this segment when drilling into it.
  filtersFor: (value: string, context?: string) => Partial<Record<FilterKey, string>>;
  // Read from the params a client attaches to a timing rather than from a
  // column of its own. Grouped apart in the picker because they only answer on
  // the metrics that carry those params.
  condition?: true;
}> = [
  {
    value: 'deviceModel',
    label: 'Device',
    labelFor: value => (value ? deviceName(value).label : 'Unknown device'),
    filtersFor: value => ({ deviceModel: value }),
  },
  {
    value: 'osVersion',
    label: 'OS version',
    labelFor: (value, context) => osLabel(context ?? '', value),
    filtersFor: (value, context) => ({ osVersion: value, osName: context ?? '' }),
  },
  {
    value: 'country',
    label: 'Country',
    // Empty means the row predates the country column, or no GeoIP database
    // was configured. Saying so beats showing a blank cell.
    labelFor: value => value || 'Unknown country',
    filtersFor: value => ({ countryCode: value }),
  },
  {
    value: 'route',
    label: 'Screen',
    // Only the navigation timings carry a route. On an app-wide metric every
    // sample lands in the same bucket, and saying so beats an empty cell.
    labelFor: value => value || 'App-wide',
    filtersFor: () => ({}),
  },
  {
    // The publish, which is what people mean by "my last release". Grouping by
    // update instead splits every publish into its per-platform halves.
    value: 'updateGroup',
    label: 'Update group',
    labelFor: value =>
      value === '00000000-0000-0000-0000-000000000000'
        ? 'Ungrouped'
        : value.slice(0, 8) || 'Ungrouped',
    filtersFor: value =>
      value === '00000000-0000-0000-0000-000000000000'
        ? {}
        : { updateGroupId: value, updateId: '' },
  },
  {
    value: 'appVersion',
    label: 'App version',
    labelFor: value => value || 'Unknown',
    filtersFor: value => ({ appVersion: value }),
  },
  {
    value: 'platform',
    label: 'Platform',
    labelFor: value => (value === 'ios' ? 'iOS' : value === 'android' ? 'Android' : value),
    filtersFor: value => ({ platform: value }),
  },
  // These narrow the timings and only the timings: a thermal state belongs to
  // one measurement, not to a device, so adoption and the device list have no
  // way to honor it. The filter bar refuses to offer them anywhere else.
  {
    value: 'thermalState',
    label: 'Thermal state',
    // The states the OS reports, capitalised. iOS names them nominal to
    // critical; Android reports the same scale through its own API.
    labelFor: value => value.charAt(0).toUpperCase() + value.slice(1),
    filtersFor: value => ({ thermalState: value }),
    condition: true,
  },
  {
    value: 'lowPowerMode',
    label: 'Low power',
    labelFor: value => value,
    filtersFor: value => ({ lowPowerMode: value }),
    condition: true,
  },
  {
    value: 'networkType',
    label: 'Network',
    labelFor: value => (value === 'wifi' ? 'Wi-Fi' : value.charAt(0).toUpperCase() + value.slice(1)),
    filtersFor: value => ({ networkType: value }),
    condition: true,
  },
  {
    value: 'frozenFrames',
    label: 'Frozen frames',
    labelFor: value => value,
    filtersFor: value => ({ frozenFrames: value }),
    condition: true,
  },
  {
    value: 'networkBytes',
    label: 'Data pulled',
    labelFor: value => value,
    filtersFor: value => ({ networkBytes: value }),
    condition: true,
  },
];

export const isDimension = (value: string): value is ObserveBreakdownDimension =>
  dimensionCatalog.some(entry => entry.value === value);

export const dimensionSpec = (value: ObserveBreakdownDimension) =>
  dimensionCatalog.find(entry => entry.value === value) ?? dimensionCatalog[0];

// How a segment reads to a human, given the dimension it groups on.
export const segmentLabel = (
  dimension: ObserveBreakdownDimension,
  value: string,
  context?: string,
  // Names the raw value cannot produce on its own. A breakdown row carries an
  // update id and nothing else, while the publish it belongs to has a message
  // the caller already loaded.
  names?: Map<string, string>
) => names?.get(value) ?? dimensionSpec(dimension).labelFor(value, context);

export const segmentFilters = (
  dimension: ObserveBreakdownDimension,
  value: string,
  context?: string
): Partial<Record<FilterKey, string>> => dimensionSpec(dimension).filtersFor(value, context);

// Distinct enough to tell eight overlaid series apart, and readable on both
// the light and the dark surface.
export const seriesColors = [
  '#4c8df2',
  '#e8734a',
  '#3fb27f',
  '#a874e8',
  '#e0b13a',
  '#4ab5c4',
  '#e05f8a',
  '#7f8ea3',
];
