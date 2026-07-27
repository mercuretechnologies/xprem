import { useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { ChevronRight, Info, ServerCrash } from 'lucide-react';
import { api, ObserveBreakdownDimension, ObserveMetric } from '@/lib/api';
import { Skeleton } from '@/components/ui/skeleton';
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip';
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { TimeSeriesChart, type TimeSeriesChartProps } from '@/components/charts/TimeSeriesChart';
import { UpdateHealthHistory } from '@/pages/Updates/components/UpdateHealthHistory';
import { HealthBySegment } from './HealthBySegment';
import { HealthPlaceholder } from './HealthPlaceholder';
import { liveInterval, type ObserveFilters } from './filters';
import { ObserveNotice } from './ObserveNotice';
import { TelemetryUnavailable } from './TelemetryUnavailable';
import {
  duration,
  exactNumber,
  formatChange,
  relativeChange,
  withoutPartialBucket,
} from './format';
import {
  dimensionSpec,
  isDimension,
  seriesColors,
  segmentFilters,
  segmentLabel,
} from './dimensions';
import {
  branchesByUpdateId,
  buildUpdateGroups,
  groupContext,
  groupTitle,
  platformLabel,
  subjectLine,
  titlesByUpdateId,
  updateGroupFilter,
  type UpdateGroup,
} from './updateGroups';

const publishedAt = new Intl.DateTimeFormat(undefined, {
  day: 'numeric',
  month: 'short',
  hour: '2-digit',
  minute: '2-digit',
});

// A segment carried by a handful of devices cannot be ranked against one
// carried by hundreds: a single unlucky device would produce a +600% sitting
// above a regression hitting a quarter of the fleet.
const MIN_DEVICES_TO_RANK = 5;
// Rows shown inline. Enough to see the shape, few enough that six metrics can
// share one page; everything else lives behind "See all".
const INLINE_SEGMENTS = 6;
// Shared by the header and every row so the columns cannot drift apart, the
// same reason EventsView keeps its own template in one place.
const segmentGrid = 'grid-cols-[minmax(0,1fr)_62px_70px_70px_86px_20px]';

const useUpdateGroups = (filters: ObserveFilters) => {
  const { query } = filters;
  const updatesQuery = useQuery({
    queryKey: ['observe', 'update-groups', api.getAppId(), query],
    queryFn: () =>
      api.getUpdateFeed({
        // One value per filter on this endpoint: with several selected we ask
        // wide and narrow below, so a comparison still lists its own groups.
        branch: query.branch?.length === 1 ? query.branch[0] : undefined,
        runtimeVersion: query.runtimeVersion?.length === 1 ? query.runtimeVersion[0] : undefined,
        platform: query.platform?.length === 1 ? query.platform[0] : undefined,
        uuid: query.updateId?.length === 1 ? query.updateId[0] : undefined,
        groupId: query.updateGroupId?.length === 1 ? query.updateGroupId[0] : undefined,
        // Wide enough to still hold the newest group of every branch when
        // several channels are live at once.
        limit: 60,
      }),
    placeholderData: previous => previous,
  });
  // A channel is not a dimension of an update, it is a pointer to a branch. So
  // narrowing the health of a channel means narrowing to what that channel
  // serves: its branch, and the branch a rollout is sending a share to while
  // one is running.
  const channelsQuery = useQuery({
    queryKey: ['channels', api.getAppId()],
    queryFn: () => api.getChannels(),
    enabled: (query.channel?.length ?? 0) > 0,
  });
  return useMemo(() => {
    const groups = buildUpdateGroups(updatesQuery.data?.items ?? []);
    const served = new Set<string>();
    for (const channel of channelsQuery.data ?? []) {
      if (!query.channel?.includes(channel.releaseChannelName)) continue;
      if (channel.branchName) served.add(channel.branchName);
      if (channel.rollout?.rolloutBranchName) served.add(channel.rollout.rolloutBranchName);
    }
    // Only needed when the endpoint could not filter for us, which is exactly
    // when more than one value is selected.
    const narrow = (values: string[] | undefined) =>
      !values || values.length < 2 ? null : new Set(values);
    const branches = narrow(query.branch);
    const runtimes = narrow(query.runtimeVersion);
    // A publish is named by its group when it has one and by its update ids
    // when it does not, and the bar writes whichever applies. So the two are
    // one selection asked twice, and a group matching either is in it.
    const groupIds = narrow(query.updateGroupId);
    const updateIds = narrow(query.updateId);
    const picked = (group: UpdateGroup) => {
      if (!groupIds && !updateIds) return true;
      if (groupIds && group.publishGroup && groupIds.has(group.publishGroup)) return true;
      return Boolean(updateIds && group.updateUUIDs.some(id => updateIds.has(id)));
    };
    return groups.filter(
      group =>
        (!branches || branches.has(group.branch)) &&
        (served.size === 0 || served.has(group.branch)) &&
        (!runtimes || runtimes.has(group.runtimeVersion)) &&
        picked(group)
    );
  }, [
    channelsQuery.data,
    query.branch,
    query.channel,
    query.runtimeVersion,
    query.updateGroupId,
    query.updateId,
    updatesQuery.data?.items,
  ]);
};

type RankedSegment = {
  // Stable identity: the raw column values, never the display label, since two
  // hardware identifiers can map to the same commercial name.
  id: string;
  label: string;
  // The heading this row sits under, when the dimension has one. Only the
  // release dimensions do: a device model belongs to no branch.
  group?: string;
  value: string;
  context?: string;
  devices: number;
  p50: number;
  p90: number;
  ranked: boolean;
  change: number | null;
};

const SegmentRow = ({
  segment,
  color,
  onSelect,
  onHover,
}: {
  segment: RankedSegment;
  color?: string;
  // Absent when the split has no filter to drill into (a screen is a property
  // of a navigation timing, not something the rest of the page can be narrowed
  // to). The row then reads as data rather than offering an action that would
  // change nothing.
  onSelect?: () => void;
  // Only the rows that have a curve report a hover: the ones past the plotted
  // few would dim every line and point at nothing.
  onHover?: (key: string | null) => void;
}) => {
  const slower = segment.change != null && segment.change > 0.01;
  const faster = segment.change != null && segment.change < -0.01;
  const Row = onSelect ? 'button' : 'div';
  // The row is a CSS grid of spans, so assistive tech reads its cells as one
  // run of numbers with no column names attached. Spelling it out is what makes
  // it say which number is which.
  const spoken = [
    segment.label,
    `${exactNumber.format(segment.devices)} devices`,
    `p50 ${duration(segment.p50)}`,
    `p90 ${duration(segment.p90)}`,
    segment.ranked ? `${formatChange(segment.change)} vs baseline` : 'too few devices to rank',
  ].join(', ');
  return (
    <Row
      type={onSelect ? 'button' : undefined}
      onClick={onSelect}
      onMouseEnter={() => onHover?.(color ? segment.id : null)}
      onMouseLeave={() => onHover?.(null)}
      onFocus={() => onHover?.(color ? segment.id : null)}
      onBlur={() => onHover?.(null)}
      title={onSelect ? `Filter on ${segment.label}` : undefined}
      aria-label={onSelect ? `Filter on ${spoken}` : spoken}
      className={`grid w-full ${segmentGrid} items-center border-b px-5 py-2 text-left last:border-0 ${
        onSelect ? 'hover:bg-accent/50' : ''
      }`}>
      <span className="flex min-w-0 items-center gap-2">
        <i
          className="h-2 w-2 shrink-0 rounded-full"
          style={{ backgroundColor: color ?? 'transparent' }}
        />
        <span className="truncate text-[13px]">{segment.label}</span>
      </span>
      <span className="text-right font-mono text-[13px] tabular-nums text-muted-foreground">
        {exactNumber.format(segment.devices)}
      </span>
      <span className="text-right font-mono text-[13px] tabular-nums">{duration(segment.p50)}</span>
      <span className="text-right font-mono text-[13px] tabular-nums text-muted-foreground">
        {duration(segment.p90)}
      </span>
      <span
        className={`text-right font-mono text-[13px] tabular-nums ${
          slower
            ? 'text-rose-600 dark:text-rose-400'
            : faster
              ? 'text-emerald-600 dark:text-emerald-400'
              : 'text-muted-foreground'
        }`}>
        {segment.ranked ? (
          formatChange(segment.change)
        ) : (
          <span
            className="text-muted-foreground/70"
            title={`Under ${MIN_DEVICES_TO_RANK} devices, a median is noise rather than a measurement.`}>
            too few
          </span>
        )}
      </span>
      {onSelect ? <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" /> : <span />}
    </Row>
  );
};

// One section per metric, all of them stacked on the page. Behind tabs they
// would hide the one thing a comparison needs: whether what moved moved
// everywhere or in a single place.
const MetricSection = ({
  metric,
  filters,
  dimension,
  annotations,
  renderAnnotationDetails,
  updateTitles,
  branchOfUpdate,
  branchReach,
}: {
  metric: ObserveMetric;
  filters: ObserveFilters;
  dimension: ObserveBreakdownDimension | undefined;
  annotations: Array<{ key: string; label: string; timestamp: Date }>;
  renderAnnotationDetails: TimeSeriesChartProps['renderAnnotationDetails'];
  // Publish messages, keyed by the ids a breakdown row carries.
  updateTitles: Map<string, string>;
  // The branch each of those ids belongs to, and how many devices each branch
  // carries, which is the order the headings come in.
  branchOfUpdate: Map<string, string>;
  branchReach: Map<string, number>;
}) => {
  const [showAll, setShowAll] = useState(false);
  const [highlighted, setHighlighted] = useState<string | null>(null);
  const breakdownQuery = useQuery({
    queryKey: ['observe', 'breakdown', api.getAppId(), metric.id, dimension, filters.query],
    queryFn: () =>
      api.getObserveBreakdown(metric.id, dimension!, filters.query, { points: true }),
    // No condition gate here: the view no longer renders a card that cannot
    // answer the current split, so a card that exists is one worth asking.
    enabled: dimension != null,
    refetchInterval: liveInterval(filters.live, filters.periodSpec),
    // Keeping the previous answer on screen while the next one loads is what
    // stops the page blinking on every filter change. But only while it still
    // answers the same question: carried across a change of dimension it lists
    // screens under a heading that reads Network, and a card whose split has
    // no answer would keep the rows of the one before it forever.
    placeholderData: (previous, previousQuery) =>
      previousQuery?.queryKey?.[4] === dimension ? previous : undefined,
  });

  const baseline = breakdownQuery.data?.overall;
  const baselineP50 = baseline?.p50 ?? metric.stats.median;

  const segments = useMemo<RankedSegment[]>(
    () =>
      [...(breakdownQuery.data?.segments ?? [])]
        .map(segment => {
          const ranked = segment.devices >= MIN_DEVICES_TO_RANK;
          return {
            id: `${segment.value}\u0000${segment.context ?? ''}`,
            value: segment.value,
            context: segment.context,
            devices: segment.devices,
            p50: segment.p50,
            p90: segment.p90,
            points: segment.points,
            label: segmentLabel(dimension!, segment.value, segment.context, updateTitles),
            group: branchOfUpdate.get(segment.value),
            ranked,
            change: ranked ? relativeChange(segment.p50, baselineP50) : null,
            impact: ranked ? segment.devices * Math.max(0, segment.p50 - baselineP50) : -1,
          };
        })
        .sort(
          (left, right) =>
            (branchReach.get(right.group ?? '') ?? 0) - (branchReach.get(left.group ?? '') ?? 0) ||
            Number(right.ranked) - Number(left.ranked) ||
            right.impact - left.impact ||
            right.devices - left.devices
        ),
    [
      baselineP50,
      breakdownQuery.data?.segments,
      dimension,
      branchOfUpdate,
      updateTitles,
      branchReach,
    ]
  );

  const series = useMemo(() => {
    if (dimension) {
      return segments
        .filter(segment => segment.ranked)
        .slice(0, INLINE_SEGMENTS)
        .map((segment, index) => ({
          key: segment.id,
          label: segment.label,
          color: seriesColors[index % seriesColors.length],
          points: withoutPartialBucket(
            (
              (segment as RankedSegment & { points?: Array<{ timestamp: string; value: number }> })
                .points ?? []
            ).map(point => ({
              timestamp: new Date(point.timestamp),
              value: point.value,
            }))
          ),
        }))
        .filter(entry => entry.points.length > 0);
    }
    return [
      {
        key: metric.id,
        label: metric.label,
        color: seriesColors[0],
        points: withoutPartialBucket(
          metric.points.map(point => ({
            timestamp: new Date(point.timestamp),
            value: point.value,
          }))
        ),
      },
    ];
  }, [dimension, metric, segments]);

  const colorOf = (id: string, index: number) =>
    index < INLINE_SEGMENTS && series.some(entry => entry.key === id)
      ? seriesColors[index % seriesColors.length]
      : undefined;

  // A split whose dimension carries no drill-in filter (Screen) is not
  // selectable: applying it would set nothing and, in the dialog, closing on
  // top of that would look like it had.
  const filtersOf = (segment: RankedSegment) =>
    dimension ? segmentFilters(dimension, segment.value, segment.context) : {};
  const selectable = segments.length > 0 && Object.keys(filtersOf(segments[0])).length > 0;
  const applySegment = (segment: RankedSegment) => {
    filters.setFilters(filtersOf(segment));
    setShowAll(false);
  };

  return (
    <section className="overflow-hidden rounded-xl border bg-card shadow-card">
      <div className="flex flex-wrap items-end justify-between gap-4 border-b px-5 py-3.5">
        <div>
          <h2 className="flex items-center gap-1.5 text-sm font-medium">
            {metric.label}
            {metric.description && (
              <Tooltip>
                <TooltipTrigger
                  // A button, not a bare icon: the explanation has to be
                  // reachable without a pointer, and a tooltip on a focusable
                  // trigger is the only shape that is.
                  type="button"
                  aria-label={`What ${metric.label} measures`}
                  className="text-muted-foreground/60 transition hover:text-foreground focus-visible:text-foreground">
                  <Info className="h-3.5 w-3.5" />
                </TooltipTrigger>
                <TooltipContent
                  side="right"
                  className="max-w-xs text-xs font-normal leading-relaxed">
                  {metric.description}
                </TooltipContent>
              </Tooltip>
            )}
          </h2>
          {/* Values on one baseline with their label underneath, rather than
              alternating value/label along a line: a percentile and its number
              read as one thing, and the three stay comparable. */}
          <div className="mt-1.5 flex items-baseline gap-6">
            <span className="flex flex-col">
              <span className="font-mono text-xl font-semibold leading-tight">
                {duration(baselineP50)}
              </span>
              <span className="text-[10px] text-muted-foreground">p50</span>
            </span>
            <span className="flex flex-col">
              <span className="font-mono text-sm font-medium leading-tight text-muted-foreground">
                {duration(baseline?.p90 ?? metric.stats.p90)}
              </span>
              <span className="text-[10px] text-muted-foreground">p90</span>
            </span>
            <span className="flex flex-col">
              <span className="font-mono text-sm font-medium leading-tight text-muted-foreground">
                {duration(metric.stats.p99)}
              </span>
              <span className="text-[10px] text-muted-foreground">p99</span>
            </span>
          </div>
        </div>
        <p className="text-xs text-muted-foreground">
          {exactNumber.format(baseline?.devices ?? metric.stats.devices)} devices ·{' '}
          {exactNumber.format(baseline?.samples ?? metric.stats.count)} samples
        </p>
      </div>

      <div className="px-3 py-2">
        {series.some(entry => entry.points.length > 0) ? (
          <TimeSeriesChart
            series={series}
            annotations={annotations}
            annotationNoun="update groups"
            renderAnnotationDetails={renderAnnotationDetails}
            formatValue={duration}
            formatAxisValue={duration}
            // Durations: nothing starts at zero and one cold start at twelve
            // seconds would flatten every other curve against the axis.
            frameToData
            highlightedKey={highlighted}
            ariaLabel={`${metric.label} over time`}
            height={200}
          />
        ) : (
          <div className="flex h-[200px] flex-col items-center justify-center px-6 text-center">
            {breakdownQuery.isLoading ? (
              <p className="text-sm text-muted-foreground">Loading…</p>
            ) : (
              <p className="text-sm text-muted-foreground">Not enough points to draw a trend</p>
            )}
          </div>
        )}
      </div>

      {dimension && segments.length > 0 && (
        <>
          <div
            className={`grid ${segmentGrid} border-y bg-muted/20 px-5 py-1.5 text-[11px] text-muted-foreground`}>
            <span>{dimensionSpec(dimension).label}</span>
            <span className="text-right">Devices</span>
            <span className="text-right">p50</span>
            <span className="text-right">p90</span>
            <span className="text-right">vs baseline</span>
            <span />
          </div>
          {segments.slice(0, INLINE_SEGMENTS).map((segment, index) => {
            const heading =
              segment.group && segment.group !== segments[index - 1]?.group ? segment.group : null;
            const row = (
              <SegmentRow
                key={segment.id}
                segment={segment}
                color={colorOf(segment.id, index)}
                onSelect={selectable ? () => applySegment(segment) : undefined}
                onHover={setHighlighted}
              />
            );
            if (!heading) return row;
            return (
              <div key={`${heading}:${segment.id}`}>
                <div className="border-b bg-muted/10 px-5 py-1 text-[11px] font-medium text-muted-foreground">
                  {heading}
                </div>
                {row}
              </div>
            );
          })}
          {segments.length > INLINE_SEGMENTS && (
            <button
              type="button"
              onClick={() => setShowAll(true)}
              className="w-full border-t px-5 py-2 text-center text-xs text-muted-foreground hover:bg-accent/50 hover:text-foreground">
              See all {segments.length} values
            </button>
          )}

          <Dialog open={showAll} onOpenChange={setShowAll}>
            <DialogContent className="max-w-3xl">
              <DialogHeader>
                <DialogTitle>
                  {metric.label} by {dimensionSpec(dimension).label.toLowerCase()}
                </DialogTitle>
              </DialogHeader>
              <div
                className={`grid ${segmentGrid} border-y bg-muted/20 px-5 py-1.5 text-[11px] text-muted-foreground`}>
                <span>Value</span>
                <span className="text-right">Devices</span>
                <span className="text-right">p50</span>
                <span className="text-right">p90</span>
                <span className="text-right">vs baseline</span>
                <span />
              </div>
              <div className="max-h-[60vh] overflow-y-auto">
                {segments.map((segment, index) => (
                  <SegmentRow
                    key={segment.id}
                    segment={segment}
                    color={colorOf(segment.id, index)}
                    onSelect={selectable ? () => applySegment(segment) : undefined}
                  />
                ))}
              </div>
            </DialogContent>
          </Dialog>
        </>
      )}
    </section>
  );
};

// What a marker reading "12 updates" folds together, listed under it. Picking
// one is the natural next move, so the row filters the whole page on it.
const PublishedHere = ({
  groups,
  onSelect,
}: {
  groups: UpdateGroup[];
  onSelect: (group: UpdateGroup) => void;
}) => (
  <>
    <div className="border-b bg-muted/30 px-3 py-2 text-[11px] text-muted-foreground">
      {groups.length === 1 ? 'Published here' : `${groups.length} update groups published here`}
    </div>
    <div className="max-h-64 overflow-y-auto">
      {groups.map(group => (
        <button
          key={group.key}
          type="button"
          onClick={() => onSelect(group)}
          title="Filter everything on this update group"
          className="block w-full border-b px-3 py-2 text-left last:border-0 hover:bg-accent/50">
          <span className="flex items-baseline justify-between gap-2">
            <code className="font-mono text-xs font-medium text-foreground">{group.shortId}</code>
            <span className="shrink-0 text-[11px] text-muted-foreground">
              {publishedAt.format(group.createdAt)}
            </span>
          </span>
          <span className="mt-0.5 block truncate text-[11px] text-muted-foreground">
            {platformLabel(group.platforms)} · {group.branch} · runtime {group.runtimeVersion}
          </span>
          {group.message && (
            <span className="mt-0.5 block truncate text-[11px] text-foreground">
              {subjectLine(group.message)}
            </span>
          )}
        </button>
      ))}
    </div>
  </>
);

export const MetricsView = ({ filters }: { filters: ObserveFilters }) => {
  const updateGroups = useUpdateGroups(filters);
  // undefined when nothing is split, or when the URL names something this
  // build does not know: a stale link must land on the unsplit view rather
  // than on a request the server refuses.
  const dimension = isDimension(filters.dimension)
    ? (filters.dimension as ObserveBreakdownDimension)
    : undefined;

  const overviewQuery = useQuery({
    queryKey: ['observe', 'overview', api.getAppId(), filters.query],
    queryFn: () => api.getObserveOverview(filters.query),
    refetchInterval: liveInterval(filters.live, filters.periodSpec),
    placeholderData: previous => previous,
  });
  const overview = overviewQuery.data;
  // expo.unknown.* are measurements the SDK dispatches with no OTel name (iOS
  // session duration, memory snapshots). Also in seconds, they would sit next
  // to a cold launch as though comparable.
  // Which conditions are read off the session is the server's answer: those
  // reach every timing, the others only the one that reports them.
  const conditionsQuery = useQuery({
    queryKey: ['observe', 'conditions', api.getAppId()],
    queryFn: () => api.getObserveConditions(),
    staleTime: Infinity,
  });
  const splitsOnMeasuredCondition =
    dimension != null &&
    dimensionSpec(dimension).condition &&
    !conditionsQuery.data?.find(entry => entry.name === dimension)?.sessionScoped;
  const reported = useMemo(
    () =>
      (overview?.metrics ?? []).filter(
        metric => metric.stats.count > 0 && !metric.name.startsWith('expo.unknown.')
      ),
    [overview?.metrics]
  );
  // Only one timing records the state the device was in, so a split by one of
  // those leaves every other card with nothing to say. Showing the one that
  // answers beats showing nine, eight of them apologising.
  const metrics = splitsOnMeasuredCondition
    ? reported.filter(metric => metric.stats.reportsConditions ?? true)
    : reported;
  const withheld = reported.length - metrics.length;

  // Health over time needs a scope. Without one, every update of the period
  // would collapse into a single curve mixing what is served today with what
  // was served last week, which answers nothing.
  // Lengths, not truthiness: these are always arrays, and an empty array is
  // truthy, so testing the values themselves makes this constantly true and
  // the guard never fires.
  const scoped =
    filters.state.branch.length > 0 ||
    filters.state.channel.length > 0 ||
    filters.state.updateId.length > 0 ||
    filters.state.updateGroupId.length > 0;
  // Health comes from update_health_snapshots, which the server aggregates per
  // update: the manifest poll it is built from carries the update, its branch
  // and the platform, and nothing about the hardware. So the split drives this
  // chart for the dimensions the snapshots actually hold, and says so for the
  // rest rather than silently ignoring the choice.
  // Update group needs nothing here: the per-group curves are what this chart
  // draws by default, so splitting by it is already the unsplit view.
  const segmentSplit = (
    ['deviceModel', 'osVersion', 'country', 'appVersion', 'platform'] as string[]
  ).includes(dimension ?? '')
    ? (dimension as ObserveBreakdownDimension)
    : undefined;
  // Screen is the one split the health events cannot follow: a route belongs to
  // a navigation timing, and an adoption or a launch failure has none.
  const unsupportedSplit = dimension === 'route' ? 'route' : undefined;

  const healthSeries = useMemo(() => {
    if (!scoped) return [];
    // The history endpoint takes at most 20 update ids. Asking for every group
    // of the period earns a 400 and an empty chart, and would be meaningless
    // anyway: adoption is a question about what shipped recently, so the
    // newest publishes are the ones that get plotted.
    const recent = updateGroups.slice(0, 8);
    const withinRequestBudget = <T extends { updateUUIDs: string[] }>(entries: T[]) => {
      const kept: T[] = [];
      let ids = 0;
      for (const entry of entries) {
        if (kept.length >= seriesColors.length) break;
        const trimmed = { ...entry, updateUUIDs: entry.updateUUIDs.slice(0, 20 - ids) };
        if (trimmed.updateUUIDs.length === 0) break;
        ids += trimmed.updateUUIDs.length;
        kept.push(trimmed);
      }
      return kept;
    };
    // What a publish is, in the words that let you recognise it: which branch
    // it went out on, which runtime it needs, and when it shipped. The platform
    // is deliberately absent, a group is both platforms by construction.
    const describe = (group: UpdateGroup) =>
      groupContext(group, (date: Date) => publishedAt.format(date), { branch: false });
    const colored = (
      entries: Array<{
        key: string;
        label: string;
        detail?: string;
        group?: string;
        updateUUIDs: string[];
      }>
    ) =>
      withinRequestBudget(entries).map((entry, index) => ({
        ...entry,
        color: seriesColors[index % seriesColors.length],
      }));

    // One curve per publish, so a rollout and the control it runs against stay
    // apart. The history endpoint takes at most 20 update ids, which is what
    // bounds how many publishes can be compared at once.
    return colored(
      recent.map(group => ({
        key: group.key,
        label: groupTitle(group),
        detail: describe(group),
        group: group.branch,
        updateUUIDs: group.updateUUIDs,
      }))
    );
  }, [scoped, updateGroups]);

  // Publish markers, restricted to the window on screen: one off the left edge
  // would pin itself to the axis and read as a publish that never happened.
  const windowStart = filters.query.from ? new Date(filters.query.from).getTime() : 0;
  const updateGroupMarkers = useMemo(
    () =>
      updateGroups
        .filter(group => group.createdAt.getTime() >= windowStart)
        .map(group => ({ key: group.key, label: groupTitle(group), timestamp: group.createdAt })),
    [updateGroups, windowStart]
  );

  const updateNames = useMemo(() => titlesByUpdateId(updateGroups), [updateGroups]);
  const { branchOfUpdate, branchReach } = useMemo(
    () => branchesByUpdateId(updateGroups),
    [updateGroups]
  );

  // A row of the health table narrows the page to what it names, the same move
  // the segment table offers. Splitting by update plots one curve per platform
  // row, so there the key is the update itself rather than its group.
  const selectHealthSeries = (key: string) => {
    const group = updateGroups.find(entry => entry.key === key);
    if (group) filters.setFilters(updateGroupFilter(group));
  };

  const renderMarkedGroups: TimeSeriesChartProps['renderAnnotationDetails'] = (cluster, close) => (
    <PublishedHere
      groups={updateGroups.filter(group =>
        cluster.members.some(member => member.key === group.key)
      )}
      onSelect={group => {
        filters.setFilters(updateGroupFilter(group));
        close();
      }}
    />
  );

  if (overviewQuery.isLoading) {
    return (
      <div className="space-y-5">
        <Skeleton className="h-28 rounded-xl" />
        <Skeleton className="h-72 rounded-xl" />
        <Skeleton className="h-72 rounded-xl" />
      </div>
    );
  }
  if (overviewQuery.isError) {
    return (
      <ObserveNotice
        icon={ServerCrash}
        tone="error"
        title="Metrics could not be loaded"
        detail="Check the server and ClickHouse logs."
      />
    );
  }

  return (
    <div className="space-y-5">
      {overview?.available === false && <TelemetryUnavailable />}

      {scoped ? (
        healthSeries.length > 0 &&
        (unsupportedSplit ? (
          <HealthPlaceholder
            title="Health cannot be split by screen"
            detail="A route belongs to a navigation timing. Adoption and launch failures come from the manifest polls every client makes, which know the update, its branch, the platform and the device, but never which screen was open. The split still applies to the timings below."
          />
        ) : segmentSplit ? (
          <HealthBySegment
            filters={filters}
            updateUUIDs={healthSeries.flatMap(entry => entry.updateUUIDs).slice(0, 20)}
            dimension={segmentSplit}
            annotations={updateGroupMarkers}
            renderAnnotationDetails={renderMarkedGroups}
          />
        ) : (
          <UpdateHealthHistory
            series={healthSeries}
            annotations={updateGroupMarkers}
            annotationNoun="update groups"
            renderAnnotationDetails={renderMarkedGroups}
            breakdownLabel="Update group"
            onBreakdownSelect={selectHealthSeries}
            from={filters.query.from}
            live={filters.live}
          />
        ))
      ) : (
        <HealthPlaceholder
          title="Pick what you want the health of"
          detail="Choose a branch, a channel or an update group above, and adoption and launch failures appear here over time. Without a scope, every update of the period would collapse into a single curve mixing what ships today with what shipped last week."
        />
      )}

      {overview?.available && metrics.length === 0 && (
        <div className="flex h-56 flex-col items-center justify-center rounded-xl border border-dashed bg-card text-center">
          <p className="text-sm font-medium">No timing reported in this period</p>
          <p className="mt-1 max-w-md text-xs text-muted-foreground">
            Startup and update timings need expo-observe on SDK 55 or later. Navigation timings also
            need a router integration enabled in <code>configure()</code>.
          </p>
        </div>
      )}

      {/* Said once, above the cards that remain, rather than repeated inside
          each of the ones that are gone. */}
      {withheld > 0 && (
        <p className="rounded-lg border border-dashed px-4 py-2.5 text-xs text-muted-foreground">
          {withheld} other {withheld === 1 ? 'timing is' : 'timings are'} hidden while you split by
          a device condition. Your app records what the phone was doing (network, battery, dropped
          frames) at the moment it becomes interactive, and attaches it to that one measurement, so
          it is the only one this split can answer for. Clear the split to see them all again.
        </p>
      )}

      {/* Its own provider: the app mounts none, and the sidebar's covers only
          the sidebar. Radix is happy with one per subtree. */}
      <TooltipProvider delayDuration={150}>
        <div className="grid gap-5 2xl:grid-cols-2">
          {metrics.map(metric => (
            <MetricSection
              key={metric.id}
              metric={metric}
              filters={filters}
              dimension={dimension}
              annotations={updateGroupMarkers}
              renderAnnotationDetails={renderMarkedGroups}
              updateTitles={updateNames}
              branchOfUpdate={branchOfUpdate}
              branchReach={branchReach}
            />
          ))}
        </div>
      </TooltipProvider>
    </div>
  );
};
