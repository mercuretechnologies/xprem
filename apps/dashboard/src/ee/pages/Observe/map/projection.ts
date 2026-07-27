import { geoDistance, geoOrthographic, type GeoProjection } from 'd3-geo';

// A view is a zoom multiplier over "the whole globe fits the viewport" plus the
// geography facing the viewer. On an orthographic projection the centre is the
// rotation, so moving the view turns the planet rather than sliding a picture,
// and a fly-to is the Earth rotating the target into view. Expressed this way
// rather than as a pixel transform, a view also survives a resize.
export type MapView = {
  zoom: number;
  center: [number, number];
};

export const minZoom = 1;
export const maxZoom = 24;

// Facing the Atlantic and tilted north, so the opening frame holds the
// Americas, Europe and Africa at once, which is where the devices are.
export const worldView: MapView = { zoom: 1, center: [-15, 22] };

const sphere = { type: 'Sphere' } as const;
const framePadding = 10;
// Leaves room around the limb for the atmosphere, which is drawn outside the
// globe and would otherwise be clipped by the frame.
const globeFill = 0.88;

export const wrapLongitude = (longitude: number) => ((((longitude + 180) % 360) + 360) % 360) - 180;

// The mean of a set of longitudes, taken the only way that survives the
// antimeridian: 178 and -178 are two degrees apart, and averaging them
// arithmetically answers 0, which is the Gulf of Guinea. Everything is unrolled
// relative to the first value, averaged there, then wrapped back. Weights are
// optional and default to one.
export const meanLongitude = (longitudes: number[], weights?: number[]) => {
  if (longitudes.length === 0) return 0;
  const anchor = longitudes[0];
  let offset = 0;
  let total = 0;
  longitudes.forEach((longitude, index) => {
    const weight = weights?.[index] ?? 1;
    offset += wrapLongitude(longitude - anchor) * weight;
    total += weight;
  });
  return total === 0 ? anchor : wrapLongitude(anchor + offset / total);
};

// A globe can be turned freely at any zoom, so unlike a flat map there is no
// latitude the view has to be kept away from. The poles are still fenced off by
// a couple of degrees: spinning exactly onto one makes the rotation ambiguous
// and the graticule degenerate.
export const clampView = (view: MapView): MapView => ({
  zoom: Math.min(maxZoom, Math.max(minZoom, view.zoom)),
  center: [wrapLongitude(view.center[0]), Math.min(88, Math.max(-88, view.center[1]))],
});

export const createProjection = (width: number, height: number, view: MapView): GeoProjection => {
  // Rotating by the negated centre is what brings that point to face the
  // viewer; the third angle stays zero so north stays up.
  const projection = geoOrthographic().rotate([-view.center[0], -view.center[1], 0]);
  projection.fitExtent(
    [
      [framePadding, framePadding],
      [width - framePadding, height - framePadding],
    ],
    sphere
  );
  projection.scale(projection.scale() * globeFill * view.zoom);
  // The globe always projects to a disc centred on the translate, so unlike a
  // flat projection there is nothing to correct after scaling: putting the
  // translate on the middle of the viewport is exact.
  projection.translate([width / 2, height / 2]);
  return projection;
};

// How squarely a point faces the viewer: 1 dead centre, 0 at the limb, negative
// on the far side. d3 clips the paths on its own, but projecting a point behind
// the globe still returns coordinates, mirrored onto the near disc, so every
// marker and every ping has to be tested or a city in Australia shows up over
// the Atlantic.
export const facingAmount = (projection: GeoProjection, point: [number, number]) => {
  const [longitude, latitude] = projection.rotate();
  return Math.cos(geoDistance(point, [-longitude, -latitude]));
};

export const isFacing = (projection: GeoProjection, point: [number, number]) =>
  facingAmount(projection, point) > 0;

// Markers within a few degrees of the limb are half behind the planet. Fading
// them over that band is what keeps a marker from being sliced in two by the
// edge, and makes them dissolve rather than pop while the globe turns.
export const limbFade = (projection: GeoProjection, point: [number, number]) =>
  Math.max(0, Math.min(1, facingAmount(projection, point) / 0.09));

// Dragging by (dx, dy) spins the globe so that whatever used to sit (dx, dy)
// from the middle now faces the viewer. Reading the answer back through the
// projection rather than converting pixels to degrees is what makes the drag
// track the cursor at every zoom and every latitude.
export const panBy = (
  projection: GeoProjection,
  view: MapView,
  width: number,
  height: number,
  dx: number,
  dy: number
): MapView => {
  const center = projection.invert?.([width / 2 - dx, height / 2 - dy]);
  if (!center || !Number.isFinite(center[0]) || !Number.isFinite(center[1])) return view;
  return clampView({ zoom: view.zoom, center: center as [number, number] });
};

// Zooming under the cursor: the place the pointer is on must not move. Rebuild
// at the new zoom around the old centre, measure how far that place drifted,
// then move the centre by the same pixel offset.
export const zoomAround = (
  projection: GeoProjection,
  view: MapView,
  width: number,
  height: number,
  factor: number,
  anchor: [number, number]
): MapView => {
  const zoom = Math.min(maxZoom, Math.max(minZoom, view.zoom * factor));
  const place = projection.invert?.(anchor);
  if (!place) return clampView({ zoom, center: view.center });
  const zoomed = createProjection(width, height, { zoom, center: view.center });
  const moved = zoomed(place as [number, number]);
  if (!moved) return clampView({ zoom, center: view.center });
  const center = zoomed.invert?.([
    width / 2 + moved[0] - anchor[0],
    height / 2 + moved[1] - anchor[1],
  ]);
  if (!center || !Number.isFinite(center[0]) || !Number.isFinite(center[1])) {
    return clampView({ zoom, center: view.center });
  }
  return clampView({ zoom, center: center as [number, number] });
};

// Zoom interpolates geometrically, so a flight from 1x to 16x spends as long
// going 1x to 4x as 4x to 16x. Linear zoom reads as an abrupt stop at the end.
export const interpolateView = (from: MapView, to: MapView, t: number): MapView => {
  let deltaLongitude = to.center[0] - from.center[0];
  if (deltaLongitude > 180) deltaLongitude -= 360;
  if (deltaLongitude < -180) deltaLongitude += 360;
  return {
    zoom: from.zoom * Math.pow(to.zoom / from.zoom, t),
    center: [
      wrapLongitude(from.center[0] + deltaLongitude * t),
      from.center[1] + (to.center[1] - from.center[1]) * t,
    ],
  };
};

export const easeInOutCubic = (t: number) =>
  t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2;

// The view that frames a set of places. One place has no extent to fit, so it
// gets a fixed city-level zoom instead.
export const fitView = (
  places: Array<[number, number]>,
  width: number,
  height: number,
  soloZoom: number
): MapView => {
  if (places.length === 0) return worldView;
  const latitudes = places.map(place => place[1]);
  const center: [number, number] = [
    meanLongitude(places.map(place => place[0])),
    (Math.min(...latitudes) + Math.max(...latitudes)) / 2,
  ];
  if (places.length === 1) return clampView({ zoom: soloZoom, center });

  const projection = createProjection(width, height, { zoom: 1, center });
  let spanX = 0;
  let spanY = 0;
  for (const place of places) {
    // A place on the far side projects onto the near disc, so measuring its
    // span would frame something that is not even in view.
    if (!isFacing(projection, place)) continue;
    const projected = projection(place);
    if (!projected) continue;
    spanX = Math.max(spanX, Math.abs(projected[0] - width / 2) * 2);
    spanY = Math.max(spanY, Math.abs(projected[1] - height / 2) * 2);
  }
  // Two thirds of the frame: enough margin that the outermost markers are not
  // clipped by their own halo.
  const zoom = Math.min(
    spanX > 1 ? (width * 0.66) / spanX : soloZoom,
    spanY > 1 ? (height * 0.66) / spanY : soloZoom,
    soloZoom
  );
  return clampView({ zoom: Math.max(minZoom, zoom), center });
};
