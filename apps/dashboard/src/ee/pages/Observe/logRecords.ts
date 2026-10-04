// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { ObserveLog } from '@/lib/api';
import type { Json } from './JsonView';

// What a log record looks like once it is read rather than stored, shared by
// the event table and the details panel it expands into.

// A log row's date, down to the millisecond, as Datadog shows it.
export const logTime = new Intl.DateTimeFormat(undefined, {
  month: 'short',
  day: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  fractionalSecondDigits: 3,
  hour12: false,
});

// The first block of a UUID, enough to tell rows apart; '-' for the zero UUID.
export const shortUUID = (value: string) =>
  !value || /^[0-]+$/.test(value) ? '-' : value.slice(0, 8);

// The server's sentinel for a device running the bundle compiled into its binary.
const EMBEDDED_UPDATE_ID = '00000000-0000-0000-0000-000000000000';

export const updateLabel = (updateId: string, short = false) =>
  updateId === EMBEDDED_UPDATE_ID ? 'Embedded bundle' : short ? shortUUID(updateId) : updateId;

export const severityDot = (log: ObserveLog) => {
  if (log.isFatal) return 'bg-rose-500';
  if (log.severityNumber >= 17) return 'bg-red-400';
  if (log.severityNumber >= 13) return 'bg-amber-400';
  if (log.severityNumber >= 9) return 'bg-sky-400';
  return 'bg-muted-foreground';
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
  const document = parseJsonDocument(log.attributes);
  const attributes =
    document && typeof document === 'object' && !Array.isArray(document) ? document : {};
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

// Parses a stored payload; null when it is not a JSON object or array.
export const parseJsonDocument = (value: string): Json | null => {
  try {
    const parsed: unknown = JSON.parse(value);
    return parsed && typeof parsed === 'object' ? (parsed as Json) : null;
  } catch {
    return null;
  }
};
