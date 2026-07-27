// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { geoGraticule10, geoPath, type GeoProjection } from 'd3-geo';
import { feature, mesh } from 'topojson-client';
import type { GeometryCollection, Topology } from 'topojson-specification';
import type { FeatureCollection, Geometry, MultiLineString } from 'geojson';
import world from 'world-atlas/countries-110m.json';
import type { MapPalette } from './palette';

const topology = world as unknown as Topology<{ countries: GeometryCollection }>;

const countries = feature(topology, topology.objects.countries) as FeatureCollection<Geometry>;
// Coastlines and inland frontiers are separate strokes because they carry
// different weight: the coast is the edge of the landmass and reads first, the
// frontier is detail inside it. Stroking every country polygon instead would
// draw each shared border twice and make the whole map look furry.
const borders = mesh(topology, topology.objects.countries, (a, b) => a !== b) as MultiLineString;
const coast = mesh(topology, topology.objects.countries, (a, b) => a === b) as MultiLineString;
const graticule = geoGraticule10();
const sphere = { type: 'Sphere' } as const;

export const drawBasemap = (
  context: CanvasRenderingContext2D,
  projection: GeoProjection,
  palette: MapPalette,
  width: number,
  height: number
) => {
  const path = geoPath(projection, context);
  context.clearRect(0, 0, width, height);

  // The globe is a disc centred on the translate, with the scale as its radius.
  // Every light effect below is placed off that, not off the viewport, so the
  // volume stays consistent while the planet is turned or zoomed.
  const [centreX, centreY] = projection.translate();
  const radius = projection.scale();
  // One fixed light, up and to the left. What makes a circle read as a sphere
  // is that the shading does not follow the rotation.
  const lightX = centreX - radius * 0.42;
  const lightY = centreY - radius * 0.46;

  const atmosphere = context.createRadialGradient(
    centreX,
    centreY,
    radius * 1.0,
    centreX,
    centreY,
    radius * 1.14
  );
  atmosphere.addColorStop(0, palette.atmosphere(0.5));
  atmosphere.addColorStop(0.28, palette.atmosphere(0.13));
  atmosphere.addColorStop(1, palette.atmosphere(0));
  context.beginPath();
  context.arc(centreX, centreY, radius * 1.14, 0, Math.PI * 2);
  context.fillStyle = atmosphere;
  context.fill();

  context.beginPath();
  path(sphere);
  const ocean = context.createRadialGradient(
    lightX,
    lightY,
    radius * 0.04,
    centreX,
    centreY,
    radius * 1.12
  );
  ocean.addColorStop(0, palette.oceanInner);
  ocean.addColorStop(1, palette.oceanOuter);
  context.fillStyle = ocean;
  context.fill();

  // Everything else is clipped to the sphere, so a graticule line or a coast
  // never bleeds past the edge of the world when the view is zoomed out.
  context.save();
  context.clip();

  context.beginPath();
  path(graticule);
  context.strokeStyle = palette.graticule;
  context.lineWidth = 0.5;
  context.stroke();

  context.beginPath();
  path(countries);
  const land = context.createRadialGradient(
    lightX,
    lightY,
    radius * 0.04,
    centreX,
    centreY,
    radius * 1.12
  );
  land.addColorStop(0, palette.landTop);
  land.addColorStop(1, palette.landBottom);
  context.fillStyle = land;
  context.fill();

  context.beginPath();
  path(borders);
  context.strokeStyle = palette.border;
  context.lineWidth = 0.5;
  context.stroke();

  context.beginPath();
  path(coast);
  context.strokeStyle = palette.coast;
  context.lineWidth = 0.9;
  context.stroke();

  // Limb darkening, over the land and the sea alike: the ground curving away
  // from the light is what tells the eye this is a ball and not a disc with a
  // map painted on it.
  const shade = context.createRadialGradient(
    lightX,
    lightY,
    radius * 0.32,
    centreX,
    centreY,
    radius * 1.02
  );
  shade.addColorStop(0, palette.shade(0));
  shade.addColorStop(0.62, palette.shade(0.1));
  shade.addColorStop(1, palette.shade(0.62));
  context.fillStyle = shade;
  context.fillRect(0, 0, width, height);

  context.restore();

  context.beginPath();
  path(sphere);
  context.strokeStyle = palette.outline;
  context.lineWidth = 1;
  context.stroke();
};
