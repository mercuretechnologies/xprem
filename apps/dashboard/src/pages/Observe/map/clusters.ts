import type { GeoProjection } from 'd3-geo';
import { isFacing, meanLongitude } from './projection';
import type { ObserveOverview } from '@/lib/api';

export type MapPlace = ObserveOverview['locations'][number];

export type MapCluster = {
  key: string;
  // Screen position, the device-weighted centroid of the members.
  x: number;
  y: number;
  // Same centroid in geography, which is what a fly-to needs.
  center: [number, number];
  devices: number;
  places: MapPlace[];
  radius: number;
};

// Roughly the diameter of the largest marker plus its label, so two clusters
// that survive the merge never overlap enough to be ambiguous to click.
export const clusterCell = 48;

export const placeKey = (place: MapPlace) =>
  `${place.countryCode}:${place.city}:${place.lat}:${place.lng}`;

export const placeLabel = (place: MapPlace) =>
  place.city || place.countryCode || 'Unknown location';

// A count of zero would make the weighted centroid undefined, and the registry
// can legitimately report a city with no device left in the window.
const weightOf = (place: MapPlace) => Math.max(1, place.deviceCount);

// Marker area tracks the device count, not its radius: a city with ten times
// the installs of another must look ten times bigger, and radius alone would
// make it look a hundred.
export const clusterRadius = (devices: number, busiest: number) =>
  4.5 + 11 * Math.sqrt(Math.min(1, devices / Math.max(1, busiest)));

// Screen-space clustering, so the same fleet splits into more, smaller markers
// as the view zooms in and collapses again as it zooms out. A grid pass buckets
// the places, then a greedy merge pass folds buckets that ended up adjacent,
// which is what stops two neighbouring cities from staying apart purely because
// a cell boundary happened to run between them.
// The screen position of a cluster is the device-weighted mean of what it
// holds, so a marker sits on the city that carries it rather than in the middle
// of its quieter neighbours. Absorbing is the same arithmetic whether a place
// joins a cell or a whole cell joins its host.
const absorb = (host: MapCluster, x: number, y: number, devices: number, places: MapPlace[]) => {
  const hostWeight = host.places.reduce((total, member) => total + weightOf(member), 0);
  const weight = places.reduce((total, member) => total + weightOf(member), 0);
  const total = hostWeight + weight;
  host.x = (host.x * hostWeight + x * weight) / total;
  host.y = (host.y * hostWeight + y * weight) / total;
  host.devices += devices;
  host.places.push(...places);
};

export const buildClusters = (
  places: MapPlace[],
  projection: GeoProjection,
  width: number,
  height: number
): MapCluster[] => {
  const cells = new Map<string, MapCluster>();
  for (const place of places) {
    // Half the planet is turned away, and a place there still projects onto the
    // near disc, so it has to be dropped before it becomes a marker over the
    // wrong ocean.
    if (!isFacing(projection, [place.lng, place.lat])) continue;
    const projected = projection([place.lng, place.lat]);
    if (!projected) continue;
    const [x, y] = projected;
    if (!Number.isFinite(x) || !Number.isFinite(y)) continue;
    // A margin of one cell keeps a marker whose centre just left the viewport
    // from popping in and out while panning.
    if (x < -clusterCell || y < -clusterCell) continue;
    if (x > width + clusterCell || y > height + clusterCell) continue;

    const cellKey = `${Math.floor(x / clusterCell)}:${Math.floor(y / clusterCell)}`;
    const existing = cells.get(cellKey);
    if (!existing) {
      cells.set(cellKey, {
        key: cellKey,
        x,
        y,
        center: [place.lng, place.lat],
        devices: place.deviceCount,
        places: [place],
        radius: 0,
      });
      continue;
    }
    absorb(existing, x, y, place.deviceCount, [place]);
  }

  const merged: MapCluster[] = [];
  // Heaviest first, so a cluster is absorbed into the marker a viewer would
  // have reached for anyway rather than into whichever one came first.
  for (const cluster of [...cells.values()].sort((a, b) => b.devices - a.devices)) {
    const host = merged.find(
      candidate => Math.hypot(candidate.x - cluster.x, candidate.y - cluster.y) < clusterCell * 0.8
    );
    if (!host) {
      merged.push(cluster);
      continue;
    }
    absorb(host, cluster.x, cluster.y, cluster.devices, cluster.places);
  }

  const busiest = merged.reduce((peak, cluster) => Math.max(peak, cluster.devices), 1);
  for (const cluster of merged) {
    cluster.places.sort((a, b) => b.deviceCount - a.deviceCount);
    // Keyed on the dominant city rather than on the grid cell, so hover and
    // selection survive the view moving under them.
    cluster.key = placeKey(cluster.places[0]);
    cluster.radius = clusterRadius(cluster.devices, busiest);
    let latitude = 0;
    let weight = 0;
    const weights = cluster.places.map(weightOf);
    cluster.places.forEach((place, index) => {
      latitude += place.lat * weights[index];
      weight += weights[index];
    });
    // The longitude cannot be averaged arithmetically: a cluster holding Fiji
    // and Tonga straddles the antimeridian, and the plain mean puts its centre
    // on the opposite side of the planet. That centre drives limbFade, so the
    // marker would fade to fully transparent while sitting in plain view.
    cluster.center = [
      meanLongitude(
        cluster.places.map(place => place.lng),
        weights
      ),
      latitude / weight,
    ];
  }
  return merged;
};

// GeoLite2 resolves a metropolitan area to several centroids, so one city can
// arrive as a handful of rows with the same name and slightly different
// coordinates. That is the right granularity for placing markers and the wrong
// one for a list, where it reads as the same city repeated. Merged by name
// within a country, so two unrelated Springfields stay apart.
export const mergePlacesByCity = (places: MapPlace[]): MapPlace[] => {
  const byCity = new Map<string, MapPlace>();
  for (const place of places) {
    const key = `${place.countryCode}:${place.city}`;
    const existing = byCity.get(key);
    if (!existing) {
      byCity.set(key, { ...place });
      continue;
    }
    // Keep the coordinates of the busiest centroid: it is the one a fly-to
    // should land on.
    if (place.deviceCount > existing.deviceCount) {
      existing.lat = place.lat;
      existing.lng = place.lng;
    }
    existing.deviceCount += place.deviceCount;
  }
  return [...byCity.values()].sort((a, b) => b.deviceCount - a.deviceCount);
};

// Named after its dominant city, counting the others. Takes the merged list so
// the count is distinct cities rather than centroids, which is what someone
// reading "Paris +2" expects it to mean.
export const clusterLabel = (cities: MapPlace[]) => {
  const dominant = placeLabel(cities[0]);
  if (cities.length === 1) return dominant;
  return `${dominant} +${cities.length - 1}`;
};

// Nearest cluster to a screen point, within its own marker plus a little slack
// so small markers stay clickable.
export const clusterAt = (clusters: MapCluster[], x: number, y: number): MapCluster | null => {
  let best: MapCluster | null = null;
  let bestDistance = Infinity;
  for (const cluster of clusters) {
    const distance = Math.hypot(cluster.x - x, cluster.y - y);
    if (distance > cluster.radius + 8 || distance >= bestDistance) continue;
    best = cluster;
    bestDistance = distance;
  }
  return best;
};

// Which cities, and how many devices in each. A count and a sum would say
// "unchanged" for two different sets of cities that happen to total the same.
export const cityCountsKey = (places: MapPlace[]) =>
  places.map(place => `${place.countryCode}:${place.city}:${place.deviceCount}`).join('|');
