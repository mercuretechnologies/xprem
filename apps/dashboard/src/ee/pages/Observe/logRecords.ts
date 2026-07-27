// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { ObserveLog } from '@/lib/api';

// What a log record looks like once it is read rather than stored, shared by
// the event table and the details panel it expands into.

export const exactTime = new Intl.DateTimeFormat(undefined, {
  month: 'short',
  day: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
});

export const shortID = (value: string) =>
  value.length > 12 ? `${value.slice(0, 8)}…` : value || '-';

export const severityDot = (log: ObserveLog) => {
  if (log.isFatal) return 'bg-rose-500';
  if (log.severityNumber >= 17) return 'bg-red-400';
  if (log.severityNumber >= 13) return 'bg-amber-400';
  if (log.severityNumber >= 9) return 'bg-sky-400';
  return 'bg-muted-foreground';
};

const parseAttributes = (value: string): Record<string, unknown> => {
  try {
    const parsed: unknown = JSON.parse(value);
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed)
      ? (parsed as Record<string, unknown>)
      : {};
  } catch {
    return {};
  }
};

const firstText = (attributes: Record<string, unknown>, keys: string[]) => {
  for (const key of keys) {
    const value = attributes[key];
    if (typeof value === 'string' && value.trim()) return value;
  }
  return '';
};

// A record with an empty body is the norm for exceptions, where the readable
// part lives in the attributes.
export const logMessage = (log: ObserveLog) => {
  if (log.body.trim()) return log.body.trim();
  const attributes = parseAttributes(log.attributes);
  return (
    firstText(attributes, [
      'exception.message',
      'message',
      'expo.log.display_name',
      'exception.type',
    ]) ||
    log.eventName ||
    'Log record'
  );
};

export const prettyPayload = (value: string) => {
  if (!value.trim()) return '';
  try {
    return JSON.stringify(JSON.parse(value), null, 2);
  } catch {
    return value;
  }
};
