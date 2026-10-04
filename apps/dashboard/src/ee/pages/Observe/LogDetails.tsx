// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { ObserveLog } from '@/lib/api';
import { deviceName } from './deviceNames';
import { JsonView, type Json } from './JsonView';
import { parseJsonDocument, updateLabel } from './logRecords';
import { parseStackTrace, type StackTrace } from './stackTrace';
import { StackTraceView } from './StackTraceView';

const Detail = ({ label, value }: { label: string; value: string }) => (
  <div className="min-w-0">
    <dt className="text-[10px] text-muted-foreground">{label}</dt>
    <dd className="mt-1 break-all font-mono text-xs text-foreground">{value || '-'}</dd>
  </div>
);

// The attributes holding the exception's own trace, the one its group symbolicates.
const exceptionTraceKeys = new Set(['exception.stacktrace', 'stack']);

// Splits the attributes into the stack traces they hold and everything else,
// so a trace shows as frames rather than as one unreadable string.
const splitStackTraces = (attributes: Json | null) => {
  const traces: Array<{ key: string; trace: StackTrace; raw: string }> = [];
  if (attributes === null || typeof attributes !== 'object' || Array.isArray(attributes)) {
    return { traces, rest: attributes };
  }
  const rest: Record<string, Json> = {};
  for (const [key, value] of Object.entries(attributes)) {
    const trace =
      typeof value === 'string'
        ? parseStackTrace(value, exceptionTraceKeys.has(key) ? 1 : 2)
        : null;
    if (trace && typeof value === 'string') traces.push({ key, trace, raw: value });
    else rest[key] = value;
  }
  return { traces, rest };
};

export const LogDetails = ({ log }: { log: ObserveLog }) => {
  const body = log.body.trim();
  const bodyTrace = body ? parseStackTrace(body) : null;
  const bodyDocument = body && !bodyTrace ? parseJsonDocument(body) : null;
  const { traces, rest: attributes } = splitStackTraces(
    log.attributes.trim() ? parseJsonDocument(log.attributes) : null
  );
  const hasAttributes =
    attributes !== null &&
    typeof attributes === 'object' &&
    (Array.isArray(attributes) || Object.keys(attributes).length > 0);
  return (
    <div className="border-t bg-muted/30 px-6 py-4">
      <dl className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Detail label="Device" value={log.easClientId} />
        <Detail label="Session" value={log.sessionId} />
        <Detail label="Update" value={updateLabel(log.updateId)} />
        <Detail label="Runtime" value={log.runtimeVersion} />
        <Detail label="Branch" value={log.branch} />
        <Detail label="Channel" value={log.channel} />
        <Detail label="Environment" value={log.environment} />
        <Detail label="App version" value={log.appVersion} />
        <Detail label="Build number" value={log.appBuildNumber} />
        <Detail label="EAS build" value={log.easBuildId} />
        <Detail
          label="Device model"
          value={log.deviceModel ? deviceName(log.deviceModel).label : ''}
        />
        <Detail label="Country" value={log.countryCode} />
        <Detail label="OS" value={`${log.osName} ${log.osVersion}`.trim()} />
      </dl>
      {traces.map(({ key, trace, raw }) => (
        <div key={key} className="mt-4">
          <StackTraceView
            title={exceptionTraceKeys.has(key) ? 'Stack trace' : key}
            trace={trace}
            raw={raw}
            // The group symbolicates the exception's own trace, not a component stack.
            errorGroup={
              exceptionTraceKeys.has(key) && log.errorFingerprint
                ? { updateId: log.updateId, fingerprint: log.errorFingerprint }
                : undefined
            }
          />
        </div>
      ))}
      {body && (
        <div className="mt-4">
          <div className="mb-1.5 text-[10px] text-muted-foreground">Message</div>
          {bodyTrace ? (
            <StackTraceView
              title="Stack trace"
              trace={bodyTrace}
              raw={body}
              errorGroup={
                log.errorFingerprint
                  ? { updateId: log.updateId, fingerprint: log.errorFingerprint }
                  : undefined
              }
            />
          ) : bodyDocument !== null ? (
            <JsonView value={bodyDocument} />
          ) : (
            <pre className="overflow-x-auto whitespace-pre-wrap break-words rounded-lg border bg-card p-3 font-mono text-[11px] leading-relaxed text-foreground">
              {body}
            </pre>
          )}
        </div>
      )}
      {hasAttributes && (
        <div className="mt-4">
          <div className="mb-1.5 text-[10px] text-muted-foreground">Attributes</div>
          <JsonView value={attributes} />
        </div>
      )}
    </div>
  );
};
