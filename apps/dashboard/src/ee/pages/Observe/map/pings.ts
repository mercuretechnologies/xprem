// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import type { MapPlace } from './clusters';

export type MapPing = {
  lng: number;
  lat: number;
  // When the ripple starts, on the render loop's clock. Pings from one poll are
  // scheduled across the window they describe rather than all fired at once,
  // which is what makes a busy city read as a stream instead of a flashbulb.
  at: number;
};

// How long a ripple is drawn, and how long it keeps feeding its cluster's halo
// after it has faded. The halo outlives the ripple so a city's amplitude
// reflects its recent rate rather than flickering with each individual arrival.
export const rippleMs = 1900;

// How far a ripple reaches from its origin, in screen pixels: the arc grows to
// 3 + 30 over its life, plus a little slack. The overlay uses it to decide
// which ripples are close enough to the viewport to be worth drawing.
export const rippleReach = 60;
export const energyMs = 6000;

// The feed counts every check-in in the window, which on a large fleet is tens
// of thousands. Ripples are a visual language, not a tally: past a handful per
// city and past a couple of hundred on screen, more of them carry no extra
// information and only cost frames. The count itself is never lost, it is what
// the cluster's size and its label already say.
export const ripplesPerCity = 5;
export const ripplesPerPoll = 150;

// Scales the requested ripples down to the frame budget. The remainder is spent
// probabilistically rather than floored away, so a city with one check-in still
// pings now and then instead of being silently rounded out of existence by a
// busier neighbour.
export const scheduleRipples = (
  cities: MapPlace[],
  windowMs: number,
  now: number,
  random: () => number = Math.random
): MapPing[] => {
  const wanted = cities.map(city => Math.min(ripplesPerCity, Math.max(1, city.deviceCount)));
  const total = wanted.reduce((sum, count) => sum + count, 0);
  if (total === 0) return [];
  const factor = total > ripplesPerPoll ? ripplesPerPoll / total : 1;
  // Spread across the whole window the counts describe, so arrivals keep
  // trickling in until the next poll lands rather than firing as one burst
  // followed by seconds of a dead map. Windows overlapping slightly at the
  // seam is what makes the stream continuous, not a problem to avoid.
  const spread = Math.max(0, windowMs);

  const pings: MapPing[] = [];
  for (const [index, city] of cities.entries()) {
    const exact = wanted[index] * factor;
    let count = Math.floor(exact);
    if (random() < exact - count) count += 1;
    for (let ripple = 0; ripple < count; ripple += 1) {
      pings.push({ lng: city.lng, lat: city.lat, at: now + random() * spread });
    }
  }
  return pings;
};

// Drops what has burnt out, oldest first. The cap is a backstop for the case
// the tab was throttled and several polls landed at once.
export const pruneRipples = (pings: MapPing[], now: number) => {
  const live = pings.filter(ping => now - ping.at < energyMs);
  return live.length > ripplesPerPoll * 2 ? live.slice(live.length - ripplesPerPoll * 2) : live;
};
