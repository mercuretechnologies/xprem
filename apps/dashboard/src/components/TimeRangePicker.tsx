import { useMemo, useState } from 'react';
import {
  CalendarDays,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  Clock,
  Search,
  ZoomOut,
} from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { cn } from '@/lib/utils';
import {
  defaultRange,
  describeRange,
  formatAbsolute,
  parseTimeExpression,
  quickRanges,
  readRecentRanges,
  rememberRange,
  rangeLengthMs,
  sameDay,
  shiftRange,
  TimeRange,
  zoomOutRange,
} from '@/lib/timeRange';

const weekdays = ['Mo', 'Tu', 'We', 'Th', 'Fr', 'Sa', 'Su'];
const monthTitle = new Intl.DateTimeFormat(undefined, { month: 'long', year: 'numeric' });

const MonthCalendar = ({
  selected,
  onPick,
}: {
  selected: Date | null;
  onPick: (day: Date) => void;
}) => {
  const [month, setMonth] = useState(() => {
    const start = selected ?? new Date();
    return new Date(start.getFullYear(), start.getMonth(), 1);
  });
  // Weeks start on Monday: the grid opens on the Monday before the 1st.
  const offset = (month.getDay() + 6) % 7;
  const days = Array.from(
    { length: 42 },
    (_, index) => new Date(month.getFullYear(), month.getMonth(), index - offset + 1)
  );
  const today = new Date();
  return (
    <div className="mt-2 rounded-md border bg-card p-2">
      <div className="mb-1 flex items-center justify-between">
        <button
          type="button"
          aria-label="Previous month"
          onClick={() => setMonth(new Date(month.getFullYear(), month.getMonth() - 1, 1))}
          className="rounded p-1 hover:bg-accent">
          <ChevronLeft className="h-4 w-4" />
        </button>
        <span className="text-xs font-medium capitalize">{monthTitle.format(month)}</span>
        <button
          type="button"
          aria-label="Next month"
          onClick={() => setMonth(new Date(month.getFullYear(), month.getMonth() + 1, 1))}
          className="rounded p-1 hover:bg-accent">
          <ChevronRight className="h-4 w-4" />
        </button>
      </div>
      <div className="grid grid-cols-7 text-center text-[10px] text-muted-foreground">
        {weekdays.map(day => (
          <span key={day} className="py-1">
            {day}
          </span>
        ))}
      </div>
      <div className="grid grid-cols-7 text-center text-xs">
        {days.map(day => {
          const outside = day.getMonth() !== month.getMonth();
          const isSelected = selected !== null && sameDay(day, selected);
          return (
            <button
              key={day.toISOString()}
              type="button"
              disabled={day > today}
              onClick={() => onPick(day)}
              className={cn(
                'rounded py-1 hover:bg-accent disabled:pointer-events-none disabled:opacity-30',
                outside && 'text-muted-foreground/60',
                sameDay(day, today) && 'font-semibold text-primary',
                isSelected && 'bg-primary text-primary-foreground hover:bg-primary'
              )}>
              {day.getDate()}
            </button>
          );
        })}
      </div>
    </div>
  );
};

const RangeField = ({
  label,
  value,
  onChange,
  onPickDay,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  onPickDay: (day: Date) => string;
}) => {
  const [calendarOpen, setCalendarOpen] = useState(false);
  const invalid = value.trim() !== '' && parseTimeExpression(value, Date.now()) === null;
  return (
    <div>
      <span className="text-xs text-muted-foreground">{label}</span>
      <div className="mt-1 flex gap-1">
        <Input
          aria-label={label}
          value={value}
          onChange={event => onChange(event.target.value)}
          aria-invalid={invalid}
          className={cn('h-9 font-mono text-xs', invalid && 'border-destructive')}
        />
        <Button
          type="button"
          variant="outline"
          size="icon"
          aria-label={`Pick the ${label.toLowerCase()} date`}
          aria-pressed={calendarOpen}
          onClick={() => setCalendarOpen(!calendarOpen)}
          className="h-9 w-9 shrink-0">
          <CalendarDays className="h-4 w-4" />
        </Button>
      </div>
      {calendarOpen && (
        <MonthCalendar
          selected={parseTimeExpression(value, Date.now())}
          onPick={day => {
            onChange(onPickDay(day));
            setCalendarOpen(false);
          }}
        />
      )}
    </div>
  );
};

const browserZone = () => {
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const minutes = -new Date().getTimezoneOffset();
  const sign = minutes >= 0 ? '+' : '-';
  const absolute = Math.abs(minutes);
  const pad = (n: number) => String(n).padStart(2, '0');
  return { zone, offset: `UTC${sign}${pad(Math.floor(absolute / 60))}:${pad(absolute % 60)}` };
};

// Picks a time range: relative to now or absolute, typed, picked on a
// calendar or chosen from quick ranges. With allowAllTime, null means no
// bound at all.
export const TimeRangePicker = ({
  value,
  onChange,
  allowAllTime = false,
  maxRangeMs = Infinity,
  className,
  popoverClassName,
}: {
  value: TimeRange | null;
  onChange: (range: TimeRange | null) => void;
  allowAllTime?: boolean;
  // The widest range the data behind the picker can be asked for.
  maxRangeMs?: number;
  className?: string;
  popoverClassName?: string;
}) => {
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState<TimeRange>(defaultRange);
  const [search, setSearch] = useState('');
  const [recent, setRecent] = useState<TimeRange[]>([]);

  const fits = (lengthMs: number) => lengthMs > 0 && lengthMs <= maxRangeMs;
  const openPicker = (next: boolean) => {
    if (next) {
      setDraft(value ?? defaultRange);
      setSearch('');
      setRecent(readRecentRanges().filter(range => fits(rangeLengthMs(range, Date.now()))));
    }
    setOpen(next);
  };

  const choose = (range: TimeRange | null) => {
    onChange(range);
    setOpen(false);
  };

  const draftLength = rangeLengthMs(draft, Date.now());
  const draftValid = fits(draftLength);
  const apply = () => {
    if (!draftValid) return;
    rememberRange(draft);
    choose(draft);
  };

  const options = useMemo(() => {
    const needle = search.trim().toLowerCase();
    const all = [
      ...(allowAllTime ? [{ range: null, label: 'All time' }] : []),
      ...quickRanges
        .filter(range => rangeLengthMs(range, Date.now()) <= maxRangeMs)
        .map(({ label, ...range }) => ({ range, label })),
    ];
    return needle ? all.filter(option => option.label.toLowerCase().includes(needle)) : all;
  }, [allowAllTime, maxRangeMs, search]);

  const label = value ? describeRange(value) : 'All time';
  const zone = browserZone();
  const step = (next: TimeRange | null) => next && onChange(next);

  return (
    <div
      className={cn(
        'flex w-fit max-w-full items-center overflow-hidden rounded-md border border-input bg-card',
        className
      )}>
      <button
        type="button"
        aria-label="Earlier"
        disabled={!value}
        onClick={() => value && step(shiftRange(value, -1, Date.now()))}
        className="flex h-9 w-8 items-center justify-center border-r text-muted-foreground hover:bg-accent disabled:opacity-40">
        <ChevronLeft className="h-4 w-4" />
      </button>
      <Popover open={open} onOpenChange={openPicker}>
        <PopoverTrigger asChild>
          <button
            type="button"
            title={label}
            className="flex h-9 min-w-0 flex-1 items-center gap-2 px-3 text-sm hover:bg-accent">
            <Clock className="h-4 w-4 shrink-0 text-muted-foreground" />
            <span className="truncate whitespace-nowrap tabular-nums">{label}</span>
            <ChevronDown className="ml-auto h-4 w-4 shrink-0 text-muted-foreground" />
          </button>
        </PopoverTrigger>
        <PopoverContent
          align="end"
          className={cn('w-[min(600px,calc(100vw-32px))] p-0', popoverClassName)}>
          <div className="grid sm:grid-cols-2">
            <div className="space-y-3 border-b p-4 sm:border-b-0 sm:border-r">
              <p className="text-sm font-medium">Absolute time range</p>
              <RangeField
                label="From"
                value={draft.from}
                onChange={from => setDraft(current => ({ ...current, from }))}
                onPickDay={day => formatAbsolute(day)}
              />
              <RangeField
                label="To"
                value={draft.to}
                onChange={to => setDraft(current => ({ ...current, to }))}
                onPickDay={day =>
                  formatAbsolute(
                    new Date(day.getFullYear(), day.getMonth(), day.getDate(), 23, 59, 59)
                  )
                }
              />
              <Button className="w-full" disabled={!draftValid} onClick={apply}>
                Apply time range
              </Button>
              {!draftValid && (
                <p className="text-xs text-destructive">
                  {draftLength > maxRangeMs
                    ? `This view reads at most ${Math.round(maxRangeMs / 86_400_000)} days at a time.`
                    : 'Use now, now-7d, or a date like 2026-09-27 14:00, with From before To.'}
                </p>
              )}
              {recent.length > 0 && (
                <div>
                  <p className="mb-1 text-xs text-muted-foreground">Recently used</p>
                  {recent.map(range => (
                    <button
                      key={`${range.from}|${range.to}`}
                      type="button"
                      onClick={() => choose(range)}
                      className="block w-full truncate rounded px-2 py-1 text-left font-mono text-xs hover:bg-accent">
                      {range.from} → {range.to}
                    </button>
                  ))}
                </div>
              )}
            </div>
            <div className="flex max-h-96 flex-col p-2">
              <div className="relative mb-2">
                <Search className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
                <Input
                  autoFocus
                  value={search}
                  onChange={event => setSearch(event.target.value)}
                  placeholder="Search quick ranges"
                  className="h-9 pl-8 text-sm"
                />
              </div>
              <div className="min-h-0 flex-1 overflow-auto">
                {options.map(option => {
                  const selected =
                    option.range === null
                      ? value === null
                      : value?.from === option.range.from && value?.to === option.range.to;
                  return (
                    <button
                      key={option.label}
                      type="button"
                      onClick={() => choose(option.range)}
                      className={cn(
                        'block w-full rounded px-3 py-1.5 text-left text-sm hover:bg-accent',
                        selected && 'bg-primary/10 font-medium text-primary'
                      )}>
                      {option.label}
                    </button>
                  );
                })}
                {options.length === 0 && (
                  <p className="px-3 py-2 text-sm text-muted-foreground">No quick range matches.</p>
                )}
              </div>
            </div>
          </div>
          <div className="flex items-center justify-between border-t px-4 py-2 text-xs text-muted-foreground">
            <span>
              <span className="font-medium text-foreground">Browser time</span> {zone.zone}
            </span>
            <span className="font-mono">{zone.offset}</span>
          </div>
        </PopoverContent>
      </Popover>
      <button
        type="button"
        aria-label="Later"
        disabled={!value || value.to === 'now'}
        onClick={() => value && step(shiftRange(value, 1, Date.now()))}
        className="flex h-9 w-8 items-center justify-center border-l text-muted-foreground hover:bg-accent disabled:opacity-40">
        <ChevronRight className="h-4 w-4" />
      </button>
      <button
        type="button"
        aria-label="Zoom out"
        disabled={!value}
        onClick={() => value && step(zoomOutRange(value, Date.now(), maxRangeMs))}
        className="flex h-9 w-8 items-center justify-center border-l text-muted-foreground hover:bg-accent disabled:opacity-40">
        <ZoomOut className="h-4 w-4" />
      </button>
    </div>
  );
};
