import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from 'react';
import type { GeoProjection } from 'd3-geo';
import { Minus, Plus, Radio, Shrink } from 'lucide-react';
import type { ObserveCheckInFeed } from '@/lib/api';
import { useTheme } from '@/lib/theme';
import { cn } from '@/lib/utils';
import { type ObserveFilters } from './filters';
import { drawBasemap } from './map/basemap';
import {
  buildClusters,
  cityCountsKey,
  clusterAt,
  clusterLabel,
  mergePlacesByCity,
  type MapCluster,
  type MapPlace,
} from './map/clusters';
import { createHaloSprite, readMapPalette, type MapPalette } from './map/palette';
import { drawOverlay as drawOverlayFrame } from './map/overlay';
import { spinFrameMs, spinResumeMs, spinZoomEpsilon, spinnedView } from './map/spin';
import { pruneRipples, scheduleRipples, type MapPing } from './map/pings';
import {
  clampView,
  createProjection,
  easeInOutCubic,
  fitView,
  interpolateView,
  minZoom,
  panBy,
  worldView,
  zoomAround,
  type MapView,
} from './map/projection';
import { useCheckInFeed } from './map/useCheckInFeed';
import { CitiesInViewPanel } from './CitiesInViewPanel';

const compact = new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 });
const exact = new Intl.NumberFormat();

// Deep enough to read a city and the region around it. Fitting a single point
// has no extent to work from, so this is the answer for a solo fly-to and the
// ceiling for a multi-city one.
const cityZoom = 9;
const flightMs = 780;
type Flight = { from: MapView; to: MapView; start: number; duration: number };
type Selection = { key: string; label: string; devices: number; places: MapPlace[] };
type Hover = { key: string; label: string; devices: number; cities: number; x: number; y: number };

const reducedMotionQuery = () =>
  typeof window === 'undefined' ? null : window.matchMedia('(prefers-reduced-motion: reduce)');

export const WorldActivityMap = ({
  locations,
  filters,
}: {
  locations: MapPlace[];
  filters: ObserveFilters;
}) => {
  const { resolvedTheme } = useTheme();
  const containerRef = useRef<HTMLDivElement | null>(null);
  const baseRef = useRef<HTMLCanvasElement | null>(null);
  const overlayRef = useRef<HTMLCanvasElement | null>(null);

  // Everything the render loop touches lives in refs. Driving 60fps through
  // React state would re-render the page on every frame of a fly-to for a
  // picture that only ever lands on a canvas.
  const viewRef = useRef<MapView>(worldView);
  const flightRef = useRef<Flight | null>(null);
  const projectionRef = useRef<GeoProjection | null>(null);
  const clustersRef = useRef<MapCluster[]>([]);
  const pingsRef = useRef<MapPing[]>([]);
  const sizeRef = useRef({ width: 0, height: 0 });
  const frameRef = useRef(0);
  const timerRef = useRef(0);
  // Read from a ref rather than through matchMedia: this is consulted twice per
  // frame, and every call allocated a MediaQueryList.
  const reducedMotionRef = useRef(reducedMotionQuery()?.matches ?? false);
  const citiesInViewAtRef = useRef(0);
  const basemapDrawnForRef = useRef('');
  const overlayDrawnForRef = useRef('');
  const clustersBuiltForRef = useRef('');
  const locationsRef = useRef(locations);
  const paletteRef = useRef<MapPalette | null>(null);
  const haloRef = useRef<HTMLCanvasElement | null>(null);
  const hoveredKeyRef = useRef<string | null>(null);
  const selectedKeyRef = useRef<string | null>(null);
  const dragRef = useRef<{ pointerId: number; x: number; y: number; travelled: number } | null>(
    null
  );
  const lastInteractionRef = useRef(0);
  const spinFrameRef = useRef(0);

  const [citiesInView, setCitiesInView] = useState<MapPlace[]>([]);
  const citiesInViewKeyRef = useRef('');
  const [hovered, setHovered] = useState<Hover | null>(null);
  const [selected, setSelected] = useState<Selection | null>(null);
  const [zoomed, setZoomed] = useState(false);

  locationsRef.current = locations;
  hoveredKeyRef.current = hovered?.key ?? null;
  selectedKeyRef.current = selected?.key ?? null;

  const totalDevices = useMemo(
    () => locations.reduce((total, place) => total + place.deviceCount, 0),
    [locations]
  );

  // What the clusters are actually built from. A count and a sum would say
  // "unchanged" for two different sets of cities that happen to add up the
  // same, and the map would keep drawing the old ones until the view moved.
  const locationsKey = useMemo(() => cityCountsKey(locations), [locations]);

  // One frame at a time, and the loop stops itself the moment nothing is
  // moving: a still map with no arrivals costs nothing. Every handler below
  // wakes it through this, which stays stable by going through a ref rather
  // than closing over the frame body.
  const stepRef = useRef<(time: number) => void>(() => {});
  const markInteraction = useCallback(() => {
    lastInteractionRef.current = performance.now();
  }, []);
  // delayMs wakes the loop through a timer instead of the next repaint. Used by
  // the resting spin, which only moves the globe 24 times a second: without it
  // the loop runs at display rate (120Hz on ProMotion) to decide, most frames,
  // that there is nothing to do.
  const schedule = useCallback((delayMs = 0) => {
    if (frameRef.current || timerRef.current) return;
    if (delayMs > 4) {
      timerRef.current = window.setTimeout(() => {
        timerRef.current = 0;
        frameRef.current = requestAnimationFrame(time => stepRef.current(time));
      }, delayMs);
      return;
    }
    frameRef.current = requestAnimationFrame(time => stepRef.current(time));
  }, []);

  // Reads the refs once and hands values across: the drawing itself lives in
  // map/overlay.ts, which knows nothing about React.
  const drawOverlay = useCallback((time: number) => {
    const context = overlayRef.current?.getContext('2d');
    const projection = projectionRef.current;
    const palette = paletteRef.current;
    const halo = haloRef.current;
    if (!context || !projection || !palette || !halo) return;
    const { width, height } = sizeRef.current;
    drawOverlayFrame(context, {
      projection,
      palette,
      halo,
      width,
      height,
      clusters: clustersRef.current,
      pings: pingsRef.current,
      time,
      prefersReducedMotion: reducedMotionRef.current,
      hoveredKey: hoveredKeyRef.current,
      selectedKey: selectedKeyRef.current,
    });
  }, []);

  const step = useCallback(
    (time: number) => {
      frameRef.current = 0;
      const { width, height } = sizeRef.current;
      if (width === 0 || height === 0) return;

      const flight = flightRef.current;
      if (flight) {
        const progress =
          flight.duration <= 0 ? 1 : Math.min(1, (time - flight.start) / flight.duration);
        viewRef.current = interpolateView(flight.from, flight.to, easeInOutCubic(progress));
        if (progress >= 1) {
          viewRef.current = flight.to;
          flightRef.current = null;
        }
      }

      // Eligibility and the resume delay are separate on purpose. The loop
      // below keeps ticking while the globe is merely waiting out its delay,
      // because nothing else would come back five seconds after a drag to
      // notice the delay had passed, and the globe would stay frozen for good.
      const spinEligible =
        !flightRef.current &&
        !dragRef.current &&
        !hoveredKeyRef.current &&
        viewRef.current.zoom <= minZoom + spinZoomEpsilon &&
        !reducedMotionRef.current;
      const spinning = spinEligible && time - lastInteractionRef.current > spinResumeMs;
      if (!spinning) {
        spinFrameRef.current = 0;
      } else if (!spinFrameRef.current) {
        spinFrameRef.current = time;
      } else if (time - spinFrameRef.current >= spinFrameMs) {
        viewRef.current = spinnedView(viewRef.current, time - spinFrameRef.current);
        spinFrameRef.current = time;
      }

      const view = viewRef.current;
      const signature = [
        width,
        height,
        view.zoom.toFixed(4),
        view.center[0].toFixed(4),
        view.center[1].toFixed(4),
        resolvedTheme,
      ].join('|');

      if (signature !== basemapDrawnForRef.current) {
        const projection = createProjection(width, height, view);
        projectionRef.current = projection;
        const context = baseRef.current?.getContext('2d');
        const palette = paletteRef.current;
        // The signature only advances once the frame was actually painted, so
        // a frame that ran before the palette was ready is retried rather than
        // remembered as done.
        if (context && palette) {
          drawBasemap(context, projection, palette, width, height);
          basemapDrawnForRef.current = signature;
        }
      }

      // Clusters follow the view, but they also have to be rebuilt when the
      // overview refetches under a still map.
      const clusterSignature = `${signature}|${locationsKey}`;
      if (clusterSignature !== clustersBuiltForRef.current && projectionRef.current) {
        clustersBuiltForRef.current = clusterSignature;
        clustersRef.current = buildClusters(
          locationsRef.current,
          projectionRef.current,
          width,
          height
        );
        // The panel lists what is actually on screen, so zooming or turning the
        // globe re-answers "busiest where I am looking". Clusters are rebuilt on
        // every frame of a flight and the panel beside them does not need to be:
        // merging clones up to 500 places, builds a map and sorts, all to keep
        // six rows, and a setState per frame would re-render the page sixty
        // times a second for a list that moves twice. So it recomputes a few
        // times a second while the view moves, once more the moment it settles,
        // and publishes only when the list really changes.
        const settled = !flightRef.current;
        if (settled || time - citiesInViewAtRef.current > 250) {
          citiesInViewAtRef.current = time;
          const inView = mergePlacesByCity(
            clustersRef.current.flatMap(cluster => cluster.places)
          ).slice(0, 6);
          const inViewKey = cityCountsKey(inView);
          if (inViewKey !== citiesInViewKeyRef.current) {
            citiesInViewKeyRef.current = inViewKey;
            setCitiesInView(inView);
          }
        }
      }

      // The overlay only holds pings and the halos they feed, so with none
      // alive and a view that did not move there is nothing new to paint. The
      // resting globe would otherwise clear and redraw the full canvas on
      // every frame to produce the same image.
      const overlayMoved = pingsRef.current.length > 0 || signature !== overlayDrawnForRef.current;
      if (overlayMoved) {
        drawOverlay(time);
        overlayDrawnForRef.current = signature;
      }

      pingsRef.current = pruneRipples(pingsRef.current, time);
      if (flightRef.current || pingsRef.current.length > 0) {
        schedule();
      } else if (spinEligible) {
        // Nothing is animating except the globe's own rotation: come back for
        // its next step, not for the next repaint.
        const due =
          spinning && spinFrameRef.current
            ? spinFrameMs - (time - spinFrameRef.current)
            : spinFrameMs;
        schedule(Math.max(0, Math.min(spinFrameMs, due)));
      }
    },
    [drawOverlay, schedule, resolvedTheme, locationsKey]
  );
  stepRef.current = step;

  // Keeps the reduced-motion ref honest without asking matchMedia per frame,
  // and the change alone is enough to bring the loop back to repaint.
  useEffect(() => {
    const query = reducedMotionQuery();
    if (!query) return;
    const sync = () => {
      reducedMotionRef.current = query.matches;
      schedule();
    };
    sync();
    query.addEventListener('change', sync);
    return () => query.removeEventListener('change', sync);
  }, [schedule]);

  useEffect(() => {
    schedule();
    return () => {
      if (frameRef.current) cancelAnimationFrame(frameRef.current);
      frameRef.current = 0;
      if (timerRef.current) window.clearTimeout(timerRef.current);
      timerRef.current = 0;
    };
  }, [schedule]);

  // The reset control lights up as soon as the view left the wide shot. Its
  // epsilon is coarser than the spin one below on purpose: this one only has to
  // survive a rounding error, while that one gates an animation and must not
  // re-trigger on a view sitting a hair off the minimum.
  const syncZoomed = useCallback((zoom = viewRef.current.zoom) => {
    setZoomed(zoom > minZoom + 0.01);
  }, []);

  const flyTo = useCallback(
    (target: MapView) => {
      markInteraction();
      const destination = clampView(target);
      flightRef.current = {
        from: viewRef.current,
        to: destination,
        start: performance.now(),
        duration: reducedMotionRef.current ? 0 : flightMs,
      };
      syncZoomed(destination.zoom);
      schedule();
    },
    [markInteraction, schedule]
  );

  const flyToPlaces = useCallback(
    (places: MapPlace[]) => {
      const { width, height } = sizeRef.current;
      flyTo(
        fitView(
          places.map(place => [place.lng, place.lat] as [number, number]),
          width,
          height,
          cityZoom
        )
      );
    },
    [flyTo]
  );

  const reset = useCallback(() => {
    setSelected(null);
    flyTo(worldView);
  }, [flyTo]);

  const zoomBy = useCallback(
    (factor: number) => {
      const { width, height } = sizeRef.current;
      const projection = projectionRef.current;
      if (!projection) return;
      flyTo(
        zoomAround(projection, viewRef.current, width, height, factor, [width / 2, height / 2])
      );
    },
    [flyTo]
  );

  // Palette and halo sprite follow the theme toggle.
  useEffect(() => {
    paletteRef.current = readMapPalette(resolvedTheme);
    haloRef.current = createHaloSprite(paletteRef.current);
    basemapDrawnForRef.current = '';
    schedule();
  }, [resolvedTheme, schedule]);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    const observer = new ResizeObserver(entries => {
      const box = entries[0]?.contentRect;
      if (!box) return;
      const width = Math.round(box.width);
      const height = Math.round(box.height);
      if (width === sizeRef.current.width && height === sizeRef.current.height) return;
      sizeRef.current = { width, height };
      const ratio = Math.min(2, window.devicePixelRatio || 1);
      for (const canvas of [baseRef.current, overlayRef.current]) {
        if (!canvas) continue;
        canvas.width = Math.max(1, Math.round(width * ratio));
        canvas.height = Math.max(1, Math.round(height * ratio));
        canvas.style.width = `${width}px`;
        canvas.style.height = `${height}px`;
        canvas.getContext('2d')?.setTransform(ratio, 0, 0, ratio, 0, 0);
      }
      basemapDrawnForRef.current = '';
      schedule();
    });
    observer.observe(container);
    return () => observer.disconnect();
  }, [schedule]);

  useEffect(() => {
    schedule();
  }, [locations, hovered, selected, schedule]);

  const onFeed = useCallback(
    (feed: ObserveCheckInFeed) => {
      if (feed.cities.length === 0) return;
      const now = performance.now();
      pingsRef.current = pruneRipples(
        [...pingsRef.current, ...scheduleRipples(feed.cities, feed.windowSeconds * 1000, now)],
        now
      );
      schedule();
    },
    [schedule]
  );

  const feed = useCheckInFeed(filters.live, filters.query, onFeed);

  // Wheel has to be a native listener: React marks it passive, and a passive
  // handler cannot stop the page from scrolling underneath the zoom.
  useEffect(() => {
    const canvas = overlayRef.current;
    if (!canvas) return;
    const onWheel = (event: WheelEvent) => {
      event.preventDefault();
      markInteraction();
      const projection = projectionRef.current;
      if (!projection) return;
      const rect = canvas.getBoundingClientRect();
      const { width, height } = sizeRef.current;
      flightRef.current = null;
      viewRef.current = zoomAround(
        projection,
        viewRef.current,
        width,
        height,
        Math.exp(-event.deltaY * 0.0015),
        [event.clientX - rect.left, event.clientY - rect.top]
      );
      syncZoomed();
      schedule();
    };
    canvas.addEventListener('wheel', onWheel, { passive: false });
    return () => canvas.removeEventListener('wheel', onWheel);
  }, [markInteraction, schedule]);

  const pointerPosition = (event: ReactPointerEvent<HTMLCanvasElement>): [number, number] => {
    const rect = event.currentTarget.getBoundingClientRect();
    return [event.clientX - rect.left, event.clientY - rect.top];
  };

  // One pointer at a time. touch-none routes every finger to this canvas, so a
  // second one would take over the drag mid-gesture, and lifting the first
  // would then release the second's capture and turn its own lift into a click
  // (selection plus fly-to nobody asked for).
  const onPointerDown = (event: ReactPointerEvent<HTMLCanvasElement>) => {
    if (dragRef.current) return;
    markInteraction();
    const [x, y] = pointerPosition(event);
    dragRef.current = { pointerId: event.pointerId, x, y, travelled: 0 };
    event.currentTarget.setPointerCapture(event.pointerId);
    flightRef.current = null;
  };

  // A gesture the browser takes over (scroll handoff, a system edge swipe)
  // fires cancel and never up. Without this the drag stays open for good: the
  // globe keeps panning with no button held, and the resting rotation never
  // comes back since it waits on dragRef being clear.
  const endDrag = (event: ReactPointerEvent<HTMLCanvasElement>) => {
    const drag = dragRef.current;
    if (!drag || drag.pointerId !== event.pointerId) return;
    dragRef.current = null;
    if (event.currentTarget.hasPointerCapture(drag.pointerId)) {
      event.currentTarget.releasePointerCapture(drag.pointerId);
    }
    syncZoomed();
    schedule();
  };

  const onPointerMove = (event: ReactPointerEvent<HTMLCanvasElement>) => {
    const [x, y] = pointerPosition(event);
    const drag = dragRef.current;
    const projection = projectionRef.current;
    // A second finger reports moves too; only the one that started the drag
    // gets to pan.
    if (drag && drag.pointerId !== event.pointerId) return;
    if (drag && projection) {
      const { width, height } = sizeRef.current;
      markInteraction();
      drag.travelled += Math.hypot(x - drag.x, y - drag.y);
      viewRef.current = panBy(projection, viewRef.current, width, height, x - drag.x, y - drag.y);
      drag.x = x;
      drag.y = y;
      schedule();
      return;
    }
    const cluster = clusterAt(clustersRef.current, x, y);
    if (!cluster) {
      if (hoveredKeyRef.current) setHovered(null);
      return;
    }
    if (cluster.key === hoveredKeyRef.current) return;
    const cities = mergePlacesByCity(cluster.places);
    setHovered({
      key: cluster.key,
      label: clusterLabel(cities),
      devices: cluster.devices,
      cities: cities.length,
      x: cluster.x,
      y: cluster.y,
    });
  };

  const onPointerUp = (event: ReactPointerEvent<HTMLCanvasElement>) => {
    const drag = dragRef.current;
    // The lift of a finger that never owned the drag is not a click.
    if (drag && drag.pointerId !== event.pointerId) return;
    markInteraction();
    dragRef.current = null;
    if (drag && event.currentTarget.hasPointerCapture(drag.pointerId)) {
      event.currentTarget.releasePointerCapture(drag.pointerId);
    }
    syncZoomed();
    schedule();
    // A drag that moved a few pixels is still a click as far as anyone's hand
    // is concerned.
    if (!drag || drag.travelled > 5) return;
    const [x, y] = pointerPosition(event);
    const cluster = clusterAt(clustersRef.current, x, y);
    if (!cluster) {
      setSelected(null);
      return;
    }
    const cities = mergePlacesByCity(cluster.places);
    setSelected({
      key: cluster.key,
      label: clusterLabel(cities),
      devices: cluster.devices,
      places: cities,
    });
    flyToPlaces(cluster.places);
  };

  const panelPlaces = selected?.places ?? citiesInView;
  return (
    <section className="relative overflow-hidden rounded-xl border bg-card shadow-card">
      <header className="flex flex-wrap items-start justify-between gap-3 px-5 pt-5">
        <div>
          <h2 className="text-sm font-medium">Live activity</h2>
          <p className="mt-1 text-xs text-muted-foreground">
            Devices by city over the selected period. Ripples are check-ins as they land.
          </p>
        </div>
        <div className="flex items-center gap-2">
          {filters.live && !feed.failed && (
            <span className="inline-flex items-center gap-1.5 rounded-full border border-primary/25 bg-primary/10 px-2.5 py-1 font-mono text-[11px] text-primary">
              <Radio className="h-3 w-3" />
              {feed.perMinute >= 1
                ? `${compact.format(Math.round(feed.perMinute))}/min`
                : 'listening'}
            </span>
          )}
          <span
            className="rounded-full border bg-muted/50 px-2.5 py-1 font-mono text-[11px] text-muted-foreground"
            title={`${exact.format(totalDevices)} devices across ${exact.format(locations.length)} cities`}>
            {compact.format(locations.length)} cities
          </span>
        </div>
      </header>

      <div ref={containerRef} className="relative mt-4 h-[460px] w-full sm:h-[620px]">
        <canvas ref={baseRef} className="absolute inset-0 h-full w-full" aria-hidden />
        <canvas
          ref={overlayRef}
          className={cn(
            'absolute inset-0 h-full w-full touch-none',
            hovered ? 'cursor-pointer' : 'cursor-grab active:cursor-grabbing'
          )}
          role="img"
          aria-label={`Device activity across ${exact.format(locations.length)} cities`}
          onPointerDown={onPointerDown}
          onPointerMove={onPointerMove}
          onPointerUp={onPointerUp}
          onPointerCancel={endDrag}
          onLostPointerCapture={endDrag}
          onPointerLeave={() => {
            setHovered(null);
            schedule();
          }}
        />

        {hovered && (
          <div
            className="pointer-events-none absolute z-20 -translate-x-1/2 -translate-y-full rounded-lg border bg-popover/95 px-3 py-2 shadow-lg backdrop-blur"
            style={{ left: hovered.x, top: hovered.y - 14 }}>
            <p className="text-xs font-medium">{hovered.label}</p>
            <p className="mt-0.5 font-mono text-[11px] text-primary">
              {exact.format(hovered.devices)} devices
            </p>
            {hovered.cities > 1 && (
              <p className="mt-0.5 text-[10px] text-muted-foreground">
                {hovered.cities} cities, click to expand
              </p>
            )}
          </div>
        )}

        <div className="absolute right-3 top-3 z-10 flex flex-col gap-1.5">
          <MapButton label="Zoom in" onClick={() => zoomBy(1.8)}>
            <Plus className="h-3.5 w-3.5" />
          </MapButton>
          <MapButton label="Zoom out" onClick={() => zoomBy(1 / 1.8)}>
            <Minus className="h-3.5 w-3.5" />
          </MapButton>
          <MapButton label="Reset view" onClick={reset} disabled={!zoomed && !selected}>
            <Shrink className="h-3.5 w-3.5" />
          </MapButton>
        </div>

        <CitiesInViewPanel
          title={selected ? selected.label : 'Busiest in view'}
          places={panelPlaces}
          onZoomTo={place => flyToPlaces([place])}
          onClearSelection={selected ? reset : undefined}
        />

        {feed.truncated && (
          <p className="absolute bottom-3 right-3 z-10 rounded border bg-popover/90 px-2 py-1 text-[10px] text-muted-foreground backdrop-blur">
            Showing the {exact.format(locations.length)} busiest cities
          </p>
        )}
      </div>
    </section>
  );
};

const MapButton = ({
  label,
  onClick,
  disabled,
  children,
}: {
  label: string;
  onClick: () => void;
  disabled?: boolean;
  children: ReactNode;
}) => (
  <button
    type="button"
    onClick={onClick}
    disabled={disabled}
    aria-label={label}
    title={label}
    className="rounded-md border bg-popover/90 p-1.5 text-muted-foreground shadow-sm backdrop-blur transition-colors hover:text-foreground disabled:pointer-events-none disabled:opacity-40">
    {children}
  </button>
);
