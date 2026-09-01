import { clsx, type ClassValue } from 'clsx';
import { twMerge } from 'tailwind-merge';

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

export function formatTimestamp(
  dateString: string | null | undefined,
  showSeconds: boolean = false
): string | null {
  if (!dateString) return null;
  const d = new Date(dateString);
  if (isNaN(d.getTime())) return null;
  const dateStr = d.toLocaleDateString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
  });
  const timeStr = d.toLocaleTimeString(undefined, {
    hour: '2-digit',
    minute: '2-digit',
    ...(showSeconds && { second: '2-digit' }),
  });
  return `${dateStr} at ${timeStr}`;
}

// A feed is read against now: on the day something shipped, the date says nothing that
// "Today" does not. Older rows get a fixed dd/mm/yyyy, hand-formatted rather than
// localised, because toLocaleDateString hands a US browser mm/dd/yyyy instead.
export function formatCompactTimestamp(
  dateString: string | null | undefined,
  showSeconds: boolean = false
): string | null {
  if (!dateString) return null;
  const d = new Date(dateString);
  if (isNaN(d.getTime())) return null;
  const pad = (n: number) => String(n).padStart(2, '0');
  const time = `${pad(d.getHours())}:${pad(d.getMinutes())}${
    showSeconds ? `:${pad(d.getSeconds())}` : ''
  }`;
  const now = new Date();
  const isToday =
    d.getDate() === now.getDate() &&
    d.getMonth() === now.getMonth() &&
    d.getFullYear() === now.getFullYear();
  return isToday
    ? `Today ${time}`
    : `${pad(d.getDate())}/${pad(d.getMonth() + 1)}/${d.getFullYear()} ${time}`;
}
