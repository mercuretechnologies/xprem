import type { GeoProjection } from 'd3-geo';
import { isFacing, limbFade } from './projection';
import { clusterCell, type MapCluster } from './clusters';
import { energyMs, rippleMs, rippleReach, type MapPing } from './pings';
import type { MapPalette } from './palette';

// Everything the overlay canvas needs for one frame. Values, not refs: the
// caller reads its refs once at the call site, which is what keeps this whole
// module free of React and testable without a DOM tree.
export type OverlayFrame = {
  projection: GeoProjection;
  palette: MapPalette;
  halo: HTMLCanvasElement;
  width: number;
  height: number;
  clusters: MapCluster[];
  pings: MapPing[];
  time: number;
  // Read once per frame by the caller rather than through matchMedia in here.
  prefersReducedMotion: boolean;
  hoveredKey: string | null;
  selectedKey: string | null;
};

const compact = new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 });
const markerFont = '600 10px "Inter Variable", ui-sans-serif, system-ui, sans-serif';
// Past a handful of pings a cluster is already saturated; the curve maps a
// plausible range of arrival rates onto a halo that grows without ever
// blowing out.
const amplitudeScale = 2.5;

// Draws the live layer over the basemap: the halos whose size follows arrivals,
// the ripples of individual check-ins, and the markers with their labels.
export const drawOverlay = (context: CanvasRenderingContext2D, frame: OverlayFrame) => {
  const {
    projection,
    palette,
    halo,
    width,
    height,
    clusters,
    pings,
    time,
    prefersReducedMotion,
    hoveredKey,
    selectedKey,
  } = frame;
  context.clearRect(0, 0, width, height);

  // Pings are stored in geography and projected per frame, so a ripple stays
  // pinned to its city through a pan or a fly-to. Their energy is folded into
  // whichever cluster currently owns that spot, which is what makes the halo
  // amplitude follow the clustering as it splits and merges.
  const energies = new Float64Array(clusters.length);
  const ripples: Array<{ x: number; y: number; age: number; fade: number }> = [];
  for (const ping of pings) {
    const age = time - ping.at;
    if (age < 0) continue;
    if (!isFacing(projection, [ping.lng, ping.lat])) continue;
    const projected = projection([ping.lng, ping.lat]);
    if (!projected) continue;
    const [x, y] = projected;
    // Brute force over the clusters actually on screen. At a hundred-odd
    // clusters and a couple hundred live pings this is a fraction of a
    // millisecond, and it keeps the assignment exact.
    let nearest = -1;
    let nearestDistance = clusterCell;
    for (let index = 0; index < clusters.length; index += 1) {
      const distance = Math.hypot(clusters[index].x - x, clusters[index].y - y);
      if (distance >= nearestDistance) continue;
      nearest = index;
      nearestDistance = distance;
    }
    if (nearest >= 0) energies[nearest] += 1 - age / energyMs;
    if (
      age < rippleMs &&
      x > -rippleReach &&
      y > -rippleReach &&
      x < width + rippleReach &&
      y < height + rippleReach
    ) {
      ripples.push({ x, y, age, fade: limbFade(projection, [ping.lng, ping.lat]) });
    }
  }

  for (const [index, cluster] of clusters.entries()) {
    const amplitude = 1 - Math.exp(-energies[index] / amplitudeScale);
    if (amplitude <= 0.01) continue;
    // A busy cluster beats bigger and faster than a quiet one, so the map
    // reads at a glance without anyone having to hover.
    const beat = prefersReducedMotion ? 1 : 0.5 + 0.5 * Math.sin(time / (420 - 160 * amplitude));
    const radius = cluster.radius * (1.9 + amplitude * (1.4 + 0.9 * beat));
    context.globalAlpha =
      Math.min(1, 0.25 + amplitude * 0.75) * limbFade(projection, cluster.center);
    context.drawImage(halo, cluster.x - radius, cluster.y - radius, radius * 2, radius * 2);
  }
  context.globalAlpha = 1;

  if (!prefersReducedMotion) {
    for (const ripple of ripples) {
      const progress = ripple.age / rippleMs;
      const eased = 1 - Math.pow(1 - progress, 3);
      context.beginPath();
      context.arc(ripple.x, ripple.y, 3 + eased * 30, 0, Math.PI * 2);
      context.strokeStyle = palette.accent(0.7 * (1 - eased) * ripple.fade);
      context.lineWidth = 1.6 * (1 - eased) + 0.35;
      context.stroke();
      if (progress >= 0.22) continue;
      context.beginPath();
      context.arc(ripple.x, ripple.y, 2.4 * (1 - progress / 0.22) + 0.9, 0, Math.PI * 2);
      context.globalAlpha = ripple.fade;
      context.fillStyle = palette.flash;
      context.fill();
      // Restored inside the loop: the next ripple carries its own fade in its
      // stroke colour and would otherwise be dimmed by this one's.
      context.globalAlpha = 1;
    }
  }

  context.font = markerFont;
  context.textAlign = 'center';
  context.textBaseline = 'middle';
  context.globalAlpha = 1;
  for (const cluster of clusters) {
    const { x, y, radius } = cluster;
    // Dissolve into the limb rather than being sliced in half by it.
    context.globalAlpha = limbFade(projection, cluster.center);
    context.beginPath();
    context.arc(x, y, radius, 0, Math.PI * 2);
    context.fillStyle = palette.markerFill;
    context.fill();
    context.strokeStyle = palette.markerStroke;
    context.lineWidth = 1.1;
    context.stroke();

    // Big enough to hold a number, and the number is more useful than a dot.
    if (radius >= 9) {
      context.fillStyle = palette.label;
      context.fillText(compact.format(cluster.devices), x, y + 0.5);
    } else {
      context.beginPath();
      context.arc(x, y, Math.max(1.5, radius * 0.3), 0, Math.PI * 2);
      context.fillStyle = palette.core;
      context.fill();
    }

    if (cluster.key !== hoveredKey && cluster.key !== selectedKey) {
      continue;
    }
    context.beginPath();
    context.arc(x, y, radius + 4.5, 0, Math.PI * 2);
    context.strokeStyle = palette.ring;
    context.lineWidth = 1.3;
    context.stroke();
  }
  context.globalAlpha = 1;
};
