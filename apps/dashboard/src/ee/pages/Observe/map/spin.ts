// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { wrapLongitude, type MapView } from './projection';

// The resting rotation of the globe. A globe that never moves reads as a
// picture, but one that keeps turning while you are trying to click a city is a
// nuisance, so it only turns when there is nothing to aim at precisely (fully
// zoomed out) and nobody is doing anything. Four minutes for a full turn:
// unmistakably alive, and slow enough that the panel listing what is in view
// does not churn while someone is reading it. Raise the rate here if it ever
// needs to be more obvious.
const spinMsPerRevolution = 240_000;
const spinDegPerMs = 360 / spinMsPerRevolution;

// Redrawing the basemap is the expensive part of a frame, and at this speed the
// eye cannot tell 24 frames a second from 60. Throttling keeps an idle
// dashboard from holding a core busy for decoration.
export const spinFrameMs = 1000 / 24;

// The globe stops the moment anything is touched and picks up again five
// seconds later, long enough that letting go of a drag does not hand it
// straight back to the animation.
export const spinResumeMs = 5000;

// Tighter than the reset control's threshold: the rotation must not start again
// on a view that is only a rounding error away from the wide shot, or a
// released pinch would hand the globe straight back to the animation.
export const spinZoomEpsilon = 0.001;

// A backgrounded tab resumes with a huge gap since the last spin frame, which
// would teleport the globe. Capped so it always picks up where it left off.
const spinMaxStepMs = 500;

// Where the globe has turned to after elapsedMs of resting rotation. Eastward,
// the direction the planet actually turns. Only the longitude moves: the tilt
// is whatever the viewer left it at.
export const spinnedView = (view: MapView, elapsedMs: number): MapView => ({
  zoom: view.zoom,
  center: [
    wrapLongitude(view.center[0] + spinDegPerMs * Math.min(elapsedMs, spinMaxStepMs)),
    view.center[1],
  ],
});
