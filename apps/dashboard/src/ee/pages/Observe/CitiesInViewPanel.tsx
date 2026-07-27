import { Link, useSearchParams } from 'react-router';
import { ChartNoAxesCombined, ChevronLeft } from 'lucide-react';
import { placeKey, placeLabel, type MapPlace } from './map/clusters';
import { filterParam } from './filters';

const compact = new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 });

const regionNames = new Intl.DisplayNames(undefined, { type: 'region' });
// Intl throws on anything that is not a well formed region code, and the
// registry can hold whatever GeoLite2 returned.
const countryName = (code: string) => {
  if (!/^[A-Za-z]{2}$/.test(code)) return code;
  try {
    return regionNames.of(code.toUpperCase()) ?? code;
  } catch {
    return code;
  }
};

// The list beside the globe: the busiest cities currently on screen, or the
// cities of the marker that was selected. Its own file because it is the one
// piece of the map with concerns of its own (routing, the metrics link, the
// keyboard path into the data) and none of the animation loop's.
//
// It is also the accessible half of the map. The canvas takes pointer input
// only, so this list is the sole way to reach a city with a keyboard or a
// screen reader, which is why it stays mounted on small screens.
export const CitiesInViewPanel = ({
  title,
  places,
  onZoomTo,
  onClearSelection,
}: {
  title: string;
  places: MapPlace[];
  onZoomTo: (place: MapPlace) => void;
  // Absent when nothing is selected, which is also when there is no way back.
  onClearSelection?: () => void;
}) => {
  const [searchParams] = useSearchParams();
  // Carries whatever is already filtered, plus the country of the place that
  // was clicked. Country and not city: telemetry rows are stamped with the
  // country at ingestion, so it is the finest place the metrics can honour.
  const metricsHref = (place: MapPlace) => {
    const params = new URLSearchParams(searchParams);
    params.set(filterParam('countryCode'), place.countryCode);
    return `/observe/metrics?${params.toString()}`;
  };

  if (places.length === 0) return null;

  return (
    <div className="absolute bottom-3 left-3 z-10 w-56 rounded-lg border bg-popover/90 p-2 shadow-lg backdrop-blur sm:w-64">
      <div className="flex items-center justify-between gap-2 px-1 pb-1.5">
        <span className="text-[11px] font-medium text-muted-foreground">{title}</span>
        {onClearSelection && (
          <button
            type="button"
            onClick={onClearSelection}
            className="inline-flex items-center gap-0.5 rounded text-[11px] text-muted-foreground transition-colors hover:text-foreground">
            <ChevronLeft className="h-3 w-3" />
            All
          </button>
        )}
      </div>
      <ul>
        {places.slice(0, 6).map(place => (
          <li key={placeKey(place)} className="flex items-center gap-0.5">
            <button
              type="button"
              onClick={() => onZoomTo(place)}
              title={`Zoom to ${placeLabel(place)}`}
              className="flex min-w-0 flex-1 items-center justify-between gap-2 rounded px-1 py-1 text-left text-xs transition-colors hover:bg-accent">
              <span className="truncate">{placeLabel(place)}</span>
              <span className="shrink-0 font-mono text-[11px] text-muted-foreground">
                {compact.format(place.deviceCount)}
              </span>
            </button>
            {/* Named after the country, not the city, because that is what the
                link actually filters on. A row that said "metrics for Paris"
                and delivered France would be a lie. */}
            {place.countryCode && (
              <Link
                to={metricsHref(place)}
                title={`Metrics for ${countryName(place.countryCode)}`}
                aria-label={`Metrics for ${countryName(place.countryCode)}`}
                className="shrink-0 rounded p-1 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground">
                <ChartNoAxesCombined className="h-3.5 w-3.5" />
              </Link>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
};
