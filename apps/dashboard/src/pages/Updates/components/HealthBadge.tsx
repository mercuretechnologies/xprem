import { UpdateHealthRecord } from '@/lib/api.ts';

// The health score of an update: successful devices over successful + faulty
// devices. Rendered as a quiet status dot + percentage (the column header
// already says "Health"); the tooltip carries the raw counts.
export const HealthBadge = ({ health }: { health?: UpdateHealthRecord }) => {
  if (!health || health.healthPercent == null) {
    return <span className="whitespace-nowrap text-xs text-muted-foreground/60">No data</span>;
  }
  const percent = health.healthPercent;
  const dot = percent >= 98 ? 'bg-emerald-500' : percent >= 90 ? 'bg-amber-500' : 'bg-red-500';
  // Floor, never round up: "100%" must mean zero failures.
  const label = percent === 100 ? '100%' : `${(Math.floor(percent * 10) / 10).toFixed(1)}%`;
  const detail = `${health.successfulDevices.toLocaleString()} successful · ${health.faultyDevices.toLocaleString()} crashed`;
  return (
    <span
      className={`inline-flex items-center gap-1.5 whitespace-nowrap text-sm tabular-nums ${
        percent < 90 ? 'font-medium text-red-600' : ''
      }`}
      title={detail}>
      <span className={`h-2 w-2 shrink-0 rounded-full ${dot}`} />
      {label}
    </span>
  );
};
