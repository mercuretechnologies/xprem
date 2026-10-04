import { isZonedAbsolute, parseTimeExpression, type TimeRange } from '@/lib/timeRange';

const dateOnly = /^\d{4}-\d{2}-\d{2}$/;

// Date-only URLs predate the time picker: the API interprets them as UTC
// calendar days, with `to` including the entire day. Keep that spelling on
// the wire so neither the browser's timezone nor Date's millisecond precision
// changes the filter. The picker receives the corresponding zoned bounds.
export const resolvePublishedFilter = (from: string, to: string, now: number) => {
  const start = from.trim();
  const end = to.trim();
  const queryBound = (expression: string, upper: boolean) => {
    if (!expression || (upper && expression === 'now')) return '';
    if (dateOnly.test(expression) || isZonedAbsolute(expression)) return expression;
    // Preserve malformed bounds for the API's validation rather than widening
    // the query to all history when a pasted filter cannot be resolved.
    return parseTimeExpression(expression, now)?.toISOString() ?? expression;
  };
  const pickerBound = (expression: string, upper: boolean) =>
    dateOnly.test(expression)
      ? `${expression}T${upper ? '23:59:59.999999999' : '00:00:00'}Z`
      : expression;
  const range: TimeRange | null =
    start || end
      ? {
          from: pickerBound(start, false),
          to: pickerBound(end, true),
        }
      : null;
  return { from: queryBound(start, false), to: queryBound(end, true), range };
};
