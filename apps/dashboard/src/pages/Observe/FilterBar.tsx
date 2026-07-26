import { Fragment } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Check, GitBranch, Pause, Radio, SlidersHorizontal, X } from 'lucide-react';
import { api } from '@/lib/api';
import { Combobox } from '@/components/Combobox';
import { MultiSelect, MultiTextInput } from './MultiSelect';
import { AttributeFilters } from './AttributeFilters';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import apple from '@/assets/apple.svg';
import android from '@/assets/android.svg';
import { FilterKey, periods, useObserveFilters } from './filters';
import { deviceName } from './deviceNames';
import { buildUpdateGroups, groupContext, groupTitle } from './updateGroups';
import { dimensionCatalog } from './dimensions';

const updateDate = new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric' });

// UUIDs are unreadable at full length and a board name is not a phone name.
const chipText = (key: FilterKey, value: string) => {
  if (
    key === 'updateId' ||
    key === 'updateGroupId' ||
    key === 'easClientId' ||
    key === 'easBuildId'
  )
    return value.slice(0, 8);
  if (key === 'deviceModel') return deviceName(value).label;
  return value;
};

const Field = ({ label, children }: { label: string; children: React.ReactNode }) => (
  <label className="space-y-1.5">
    <span className="text-[11px] font-medium text-muted-foreground">{label}</span>
    {children}
  </label>
);

export const FilterBar = ({
  filters,
  // Splitting only means something where charts are drawn.
  showDimensions = false,
}: {
  filters: ReturnType<typeof useObserveFilters>;
  showDimensions?: boolean;
}) => {
  const {
    state,
    setFilters,
    toggleFilter,
    clearFilters,
    chips,
    period,
    setPeriod,
    live,
    setLive,
    applies,
    dimension,
    setDimension,
  } = filters;

  // Collapsed so the label is written once, whatever the chip order. Grouped on
  // the label too, since every identity attribute shares one filter key and
  // "plan" must not swallow the values of "tenant".
  //
  // By lookup rather than by adjacency: attribute pairs are appended to
  // state.attributes in toggle order, so plan, tenant, plan is an ordinary
  // sequence. Merging only neighbours would emit that as two "plan" groups,
  // which renders the label twice under one duplicated React key.
  const chipGroups = chips.reduce<
    Array<{ key: FilterKey; label: string; applied: boolean; values: typeof chips }>
  >((groups, chip) => {
    const existing = groups.find(group => group.key === chip.key && group.label === chip.label);
    if (existing) existing.values.push(chip);
    else groups.push({ key: chip.key, label: chip.label, applied: chip.applied, values: [chip] });
    return groups;
  }, []);

  const channelsQuery = useQuery({
    queryKey: ['channels', api.getAppId()],
    queryFn: () => api.getChannels(),
  });
  const branchesQuery = useQuery({
    queryKey: ['branches', api.getAppId()],
    queryFn: () => api.getBranches(),
  });
  // The published updates double as the runtime list and as the update picker,
  // so nobody has to know a UUID by heart to filter on a updateGroup.
  const updatesQuery = useQuery({
    queryKey: ['observe', 'update-options', api.getAppId(), state.branch],
    queryFn: () =>
      api.getUpdateFeed({
        branch: state.branch.length === 1 ? state.branch[0] : undefined,
        limit: 100,
      }),
    select: page =>
      state.branch.length > 1
        ? { ...page, items: page.items.filter(item => state.branch.includes(item.branch)) }
        : page,
  });
  const updates = updatesQuery.data?.items ?? [];
  const runtimeOptions = Array.from(
    new Set(updates.map(update => update.runtimeVersion).filter(Boolean))
  )
    .sort()
    .map(value => ({ value, label: value }));
  // A publish produces one update per platform. The bar offers updateGroups, never
  // raw updates: picking "the iOS half of yesterday's publish" is not a
  // question anyone asks. A updateGroup without a publish group (older CLI,
  // rollback marker) falls back to its single update, which is what
  // updateGroupFilter encodes.
  const updateGroups = buildUpdateGroups(updates);
  const updateGroupOptions = updateGroups.map(updateGroup => ({
    value: updateGroup.key,
    // The message titles the row and the rest qualifies it on its own line:
    // the short id (what a pasted URL carries), the runtime and when it
    // shipped. The branch heads the section instead, since every publish in
    // one belongs to it.
    label: groupTitle(updateGroup),
    group: updateGroup.branch,
    detail: groupContext(updateGroup, date => updateDate.format(date), { branch: false }),
    // Nobody reads a UUID, but a pasted one has to land somewhere.
    keywords: [updateGroup.key, ...updateGroup.updateUUIDs],
  }));
  const selectedGroups = updateGroups
    .filter(
      group =>
        (group.publishGroup && state.updateGroupId.includes(group.publishGroup)) ||
        group.updateUUIDs.some(id => state.updateId.includes(id))
    )
    .map(group => group.key);

  // Selecting a group means adding its own id where it has one and its update
  // ids where it does not, so both spellings stay in sync in the URL.
  const toggleUpdateGroup = (key: string) => {
    const next = selectedGroups.includes(key)
      ? selectedGroups.filter(entry => entry !== key)
      : [...selectedGroups, key];
    const chosen = updateGroups.filter(group => next.includes(group.key));
    setFilters({
      updateGroupId: chosen.flatMap(group => (group.publishGroup ? [group.publishGroup] : [])),
      updateId: chosen.flatMap(group => (group.publishGroup ? [] : group.updateUUIDs)),
    });
  };
  // The popover offers exactly what the current page can honor, so the badge
  // count never promises a filter the page would ignore. Driven by the same
  // scope table as the chips: one source of truth, no page-specific booleans.
  // No update group here: the bar picks one by message, and its search box
  // takes a pasted UUID too, so a second field for the same filter is noise.
  const advancedKeys: FilterKey[] = (
    [
      'easClientId',
      'appVersion',
      'appBuildNumber',
      'easBuildId',
      'environment',
      'osVersion',
      'deviceModel',
      'countryCode',
      'attributes',
    ] as FilterKey[]
  ).filter(applies);
  const advancedCount = advancedKeys.filter(key => state[key].length > 0).length;
  const shows = (key: FilterKey) => advancedKeys.includes(key);

  // The conditions are named by the same table that splits on them, so a
  // picker and the split it drills into always spell a value the same way.
  const conditionKeys = dimensionCatalog
    .filter(entry => entry.condition && applies(entry.value as FilterKey))
    .map(entry => entry.value as FilterKey);
  const conditionLabel = (key: FilterKey) =>
    dimensionCatalog.find(entry => entry.value === key)?.label ?? key;
  const conditionValueLabel = (key: FilterKey, value: string) =>
    dimensionCatalog.find(entry => entry.value === key)?.labelFor(value) ?? value;
  // The ranges are cut server-side, so they are asked for rather than restated
  // here. They only change when the server does, hence no refetching.
  const conditionsQuery = useQuery({
    queryKey: ['observe', 'conditions', api.getAppId()],
    queryFn: () => api.getObserveConditions(),
    enabled: conditionKeys.length > 0,
    staleTime: Infinity,
  });

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2 rounded-xl border bg-card p-2 shadow-card [&>*]:shrink-0">
        {/* The one exclusive filter, because it is the one where selecting
            nothing already means everything: picking both says exactly what
            picking neither says. So a click switches the platform instead of
            adding one, and clicking the selected one goes back to both. */}
        <div className="flex shrink-0 rounded-lg border bg-muted/40 p-1">
          {(['ios', 'android'] as const).map(option => {
            const label = option === 'ios' ? 'iOS' : 'Android';
            const selected = state.platform.includes(option);
            return (
              <button
                key={option}
                type="button"
                title={label}
                aria-pressed={selected}
                onClick={() =>
                  setFilters({
                    platform: selected && state.platform.length === 1 ? [] : [option],
                  })
                }
                className={`flex h-8 w-9 items-center justify-center rounded-md text-xs transition ${
                  selected
                    ? 'bg-card font-medium text-foreground shadow-card'
                    : 'text-muted-foreground opacity-50 hover:opacity-100'
                }`}>
                {/* Height only: the Apple mark is 814x1000, so forcing it
                    square stretches it wider than it is drawn. */}
                <img
                  src={option === 'ios' ? apple : android}
                  className="h-3.5 w-auto brightness-0 dark:invert"
                  alt=""
                />
                <span className="sr-only">{label}</span>
              </button>
            );
          })}
        </div>

        {applies('channel') && (
          <MultiSelect
            className="w-32 sm:w-36"
            label="Channel"
            loading={channelsQuery.isLoading}
            values={state.channel}
            onToggle={value => toggleFilter('channel', value)}
            onClear={() => setFilters({ channel: [] })}
            options={(channelsQuery.data ?? []).map(item => ({
              value: item.releaseChannelName,
              label: item.releaseChannelName,
            }))}
          />
        )}
        <MultiSelect
          className="w-36 sm:w-40"
          label="Branch"
          loading={branchesQuery.isLoading}
          values={state.branch}
          onToggle={value => toggleFilter('branch', value)}
          onClear={() => setFilters({ branch: [] })}
          options={(branchesQuery.data ?? []).map(item => ({
            value: item.branchName,
            label: item.branchName,
          }))}
        />
        <MultiSelect
          className="w-32 sm:w-36"
          label="Runtime"
          loading={updatesQuery.isLoading}
          values={state.runtimeVersion}
          onToggle={value => toggleFilter('runtimeVersion', value)}
          onClear={() => setFilters({ runtimeVersion: [] })}
          options={runtimeOptions}
        />
        <MultiSelect
          className="w-44 sm:w-52"
          label="Update group"
          loading={updatesQuery.isLoading}
          values={selectedGroups}
          onToggle={toggleUpdateGroup}
          onClear={() => setFilters({ updateGroupId: [], updateId: [] })}
          options={updateGroupOptions}
          groupIcon={<GitBranch className="h-3 w-3 shrink-0 opacity-70" />}
          // The trigger truncates, so the message can lead here too: a publish
          // with no --message falls back to its short id on its own.
          display={value => {
            const group = updateGroups.find(entry => entry.key === value);
            return group ? groupTitle(group) : value.slice(0, 8);
          }}
        />

        <Popover>
          <PopoverTrigger asChild>
            <Button
              variant="outline"
              className={
                advancedCount > 0 ? 'border-primary/25 bg-primary/[0.07] text-foreground' : ''
              }>
              <SlidersHorizontal className="h-3.5 w-3.5" />
              More
              {advancedCount > 0 && (
                <Badge className="h-5 min-w-5 justify-center px-1.5 text-[10px]">
                  {advancedCount}
                </Badge>
              )}
            </Button>
          </PopoverTrigger>
          <PopoverContent
            align="start"
            className="max-h-[min(70vh,40rem)] w-[min(34rem,calc(100vw-2rem))] space-y-4 overflow-y-auto">
            <div>
              <h2 className="text-sm font-semibold">Narrow the audience</h2>
              <p className="mt-0.5 text-xs text-muted-foreground">
                Every change applies immediately and is kept in the page URL.
              </p>
            </div>

            {shows('attributes') && (
              <div className="border-b pb-4">
                <AttributeFilters
                  pairs={state.attributes}
                  onChange={pairs => setFilters({ attributes: pairs })}
                />
              </div>
            )}

            <div className="grid gap-3 sm:grid-cols-2">
              {shows('easClientId') && (
                <Field label="Device">
                  <MultiTextInput
                    values={state.easClientId}
                    onChange={values => setFilters({ easClientId: values })}
                    placeholder="EAS client UUID"
                  />
                </Field>
              )}
              {shows('deviceModel') && (
                <Field label="Device model">
                  <MultiTextInput
                    values={state.deviceModel}
                    onChange={values => setFilters({ deviceModel: values })}
                    placeholder="iPhone18,2"
                  />
                </Field>
              )}
              {shows('osVersion') && (
                <Field label="OS version">
                  <MultiTextInput
                    values={state.osVersion}
                    onChange={values => setFilters({ osVersion: values })}
                    placeholder="26.1"
                  />
                </Field>
              )}
              {shows('countryCode') && (
                <Field label="Country">
                  <MultiTextInput
                    values={state.countryCode}
                    onChange={values => setFilters({ countryCode: values })}
                    placeholder="FR"
                    transform={value => value.toUpperCase()}
                  />
                </Field>
              )}
              {shows('appVersion') && (
                <Field label="App version">
                  <MultiTextInput
                    values={state.appVersion}
                    onChange={values => setFilters({ appVersion: values })}
                    placeholder="1.4.0"
                  />
                </Field>
              )}
              {shows('appBuildNumber') && (
                <Field label="Build number">
                  <MultiTextInput
                    values={state.appBuildNumber}
                    onChange={values => setFilters({ appBuildNumber: values })}
                    placeholder="421"
                  />
                </Field>
              )}
              {shows('easBuildId') && (
                <Field label="EAS build">
                  <MultiTextInput
                    values={state.easBuildId}
                    onChange={values => setFilters({ easBuildId: values })}
                    placeholder="Build UUID"
                  />
                </Field>
              )}
              {shows('environment') && (
                <Field label="Environment">
                  <MultiTextInput
                    values={state.environment}
                    onChange={values => setFilters({ environment: values })}
                    placeholder="production"
                  />
                </Field>
              )}
            </div>

            {conditionKeys.length > 0 && (
              <div className="border-t pt-4">
                <h3 className="text-sm font-semibold">The state the device was in</h3>
                <p className="mt-0.5 text-xs text-muted-foreground">
                  Narrows the timings to the measurements taken under these conditions. Adoption,
                  launch failures and the device list keep answering for the whole fleet: a thermal
                  state belongs to one measurement, not to a phone.
                </p>
                {/* No field label: a select whose trigger already reads
                    "Thermal state" until something is picked does not need the
                    same words written next to it. */}
                <div className="mt-3 grid gap-2 sm:grid-cols-2">
                  {conditionKeys.map(key => (
                    <MultiSelect
                      key={key}
                      className="w-full"
                      label={conditionLabel(key)}
                      loading={conditionsQuery.isLoading}
                      values={state[key]}
                      onToggle={value => toggleFilter(key, value)}
                      onClear={() => setFilters({ [key]: [] })}
                      display={value => conditionValueLabel(key, value)}
                      options={(
                        conditionsQuery.data?.find(entry => entry.name === key)?.values ?? []
                      ).map(value => ({ value, label: conditionValueLabel(key, value) }))}
                    />
                  ))}
                </div>
              </div>
            )}
          </PopoverContent>
        </Popover>

        <div className="ml-auto flex shrink-0 items-center gap-2">
          <Button
            variant={live ? 'ghost' : 'outline'}
            size="sm"
            aria-pressed={live}
            title={live ? 'Pause automatic refresh' : 'Refresh automatically'}
            onClick={() => setLive(!live)}
            className={live ? 'text-emerald-600 dark:text-emerald-400' : ''}>
            {live ? (
              <>
                <Radio className="h-3.5 w-3.5 animate-pulse motion-reduce:animate-none" />
                Live
              </>
            ) : (
              <>
                <Pause className="h-3.5 w-3.5" />
                Paused
              </>
            )}
          </Button>
          <Combobox
            className="w-36 sm:w-40"
            value={period}
            onChange={value => setPeriod(value as typeof period)}
            options={periods.map(option => ({ value: option.value, label: option.label }))}
          />
        </div>
      </div>

      {showDimensions && (
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="mr-1 text-[11px] font-medium text-muted-foreground">
            Split charts by
          </span>
          {dimensionCatalog.map((entry, index) => {
            const selected = dimension === entry.value;
            const chip = (
              <button
                key={entry.value}
                type="button"
                aria-pressed={selected}
                title={
                  entry.condition
                    ? `Compare the timings measured under each ${entry.label.toLowerCase()}`
                    : `Overlay one series per ${entry.label.toLowerCase()}`
                }
                // One at a time: picking another replaces it, picking the
                // current one clears the split.
                onClick={() => setDimension(selected ? '' : entry.value)}
                className={`flex items-center gap-1 rounded-full border px-2.5 py-1 text-[11px] transition ${
                  selected
                    ? 'border-primary/30 bg-primary/[0.09] font-medium text-foreground'
                    : 'border-border text-muted-foreground hover:bg-accent hover:text-foreground'
                }`}>
                {selected && <Check className="h-3 w-3" />}
                {entry.label}
              </button>
            );
            // The conditions read from a timing's own params sit behind their
            // own label: they explain a slow measurement rather than describe
            // the fleet, and they only answer on the metrics that carry them.
            if (!entry.condition || dimensionCatalog[index - 1]?.condition) return chip;
            return (
              <Fragment key={entry.value}>
                <span
                  className="ml-2 mr-1 border-l pl-3 text-[11px] font-medium text-muted-foreground"
                  title="Measured on the interactive timings, which carry the state the device was in">
                  or by conditions
                </span>
                {chip}
              </Fragment>
            );
          })}
          {dimension !== '' && (
            <button
              type="button"
              onClick={() => setDimension('')}
              className="ml-1 text-[11px] text-muted-foreground hover:text-foreground">
              Clear
            </button>
          )}
        </div>
      )}

      {chipGroups.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5">
          {chipGroups.map(group => (
            <span
              key={`${group.key}:${group.label}`}
              title={
                group.applied
                  ? undefined
                  : 'Not applied here: update-group health comes from the device registry, which does not carry this dimension.'
              }
              className={`flex items-center gap-1 rounded-full border py-1 pl-2.5 pr-1 text-[11px] ${
                group.applied
                  ? 'border-primary/20 bg-primary/[0.07] text-foreground'
                  : 'border-dashed border-border bg-transparent text-muted-foreground'
              }`}>
              {/* The label is written once even when several values are being
                  compared: "Branch production staging" reads as one filter,
                  which is what it is. */}
              <span className="mr-0.5 text-muted-foreground">{group.label}</span>
              {group.values.map(chip => (
                <span
                  key={chip.value}
                  className={`flex items-center gap-0.5 ${
                    group.applied ? '' : 'line-through decoration-muted-foreground/40'
                  }`}>
                  <span className="max-w-40 truncate font-mono">
                    {chipText(chip.key, chip.value)}
                  </span>
                  <button
                    type="button"
                    aria-label={`Remove ${group.label} ${chip.value}`}
                    // One value at a time: removing it narrows the comparison
                    // instead of dropping every value of that filter.
                    onClick={() => toggleFilter(chip.key, chip.raw)}
                    className="rounded-full p-0.5 text-muted-foreground hover:bg-accent hover:text-foreground">
                    <X className="h-3 w-3" />
                  </button>
                </span>
              ))}
            </span>
          ))}
          <button
            type="button"
            onClick={clearFilters}
            className="ml-1 text-[11px] text-muted-foreground hover:text-foreground">
            Clear all
          </button>
        </div>
      )}
    </div>
  );
};
