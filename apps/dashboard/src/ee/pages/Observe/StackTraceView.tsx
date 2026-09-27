// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { ChevronDown, ChevronRight, Loader2, Sparkles } from 'lucide-react';
import { api, type ErrorGroupStatus, type TraceOrigin } from '@/lib/api';
import { useSettings } from '@/lib/SettingsContext';
import { cn } from '@/lib/utils';
import { EnterpriseExplainerDialog } from '@/ee/components/EnterpriseExplainerDialog';
import { sourcemapFeature } from '@/ee/lib/sourcemapFeature';
import {
  entriesOfSymbolicated,
  shortFileName,
  type StackEntry,
  type StackTrace,
} from './stackTrace';
import { tokenizeCode, type CodeToken } from './codeTokens';

// How many entries show before "Show more": enough for the frames that threw
// and the component around them.
const collapsedRows = 8;

const baseName = (path: string) => path.split('/').pop() ?? path;

const tokenColors: Record<CodeToken['kind'], string> = {
  comment: 'text-muted-foreground/70 italic',
  string: 'text-emerald-700 dark:text-emerald-300',
  keyword: 'text-violet-600 dark:text-violet-300',
  number: 'text-amber-700 dark:text-amber-300',
  call: 'text-sky-700 dark:text-sky-300',
  type: 'text-amber-800 dark:text-amber-200',
  punctuation: 'text-muted-foreground',
  plain: '',
};

/**
 * Highlights a source line while preserving spacing; an empty line occupies one space.
 */
const CodeLine = ({ text }: { text: string }) => (
  <code className="whitespace-pre pr-3">
    {text
      ? tokenizeCode(text).map((token, index) => (
          <span key={index} className={tokenColors[token.kind]}>
            {token.text}
          </span>
        ))
      : ' '}
  </code>
);

/**
 * Shows numbered source context and highlights the origin line; returns null
 * when the origin has no context.
 */
const SourceContext = ({ origin }: { origin: TraceOrigin }) => {
  if (!origin.context) return null;
  return (
    <ol className="my-1.5 ml-3 mr-3 overflow-x-auto rounded border bg-muted/40 text-foreground">
      {origin.context.lines.map((text, index) => {
        const number = origin.context!.firstLine + index;
        const throws = number === origin.line;
        return (
          <li
            key={number}
            className={cn(
              'grid grid-cols-[3rem_1fr] leading-relaxed',
              throws ? 'bg-rose-500/10' : 'opacity-80'
            )}>
            <span className="select-none pr-3 text-right text-muted-foreground">{number}</span>
            <CodeLine text={text} />
          </li>
        );
      })}
    </ol>
  );
};

/**
 * Shows a frame or skipped count, with optional expandable source context.
 * showContext sets the initial expansion state.
 */
const FrameRow = ({ entry, showContext }: { entry: StackEntry; showContext: boolean }) => {
  const [contextOpen, setContextOpen] = useState(showContext);
  if (entry.type === 'skipped') {
    return (
      <li className="px-3 py-1 italic text-muted-foreground">… {entry.count} frames skipped</li>
    );
  }
  const { frame, repeat, origin } = entry;
  const native = frame.kind === 'native';
  const inApp = origin?.inApp ?? false;
  const functionName = origin?.name || frame.functionName || '<anonymous>';
  const position = origin
    ? `${baseName(origin.source)}:${origin.line}:${origin.column}`
    : native
      ? frame.file || 'native'
      : `${shortFileName(frame.file)}:${frame.line}:${frame.column}`;
  const bundlePosition = native ? '' : `${frame.file}:${frame.line}:${frame.column}`;
  const muted = native || (origin !== undefined && !inApp);
  return (
    <li className={cn(muted && 'text-muted-foreground')}>
      <div
        role={origin?.context ? 'button' : undefined}
        onClick={origin?.context ? () => setContextOpen(!contextOpen) : undefined}
        className={cn(
          'flex items-baseline gap-3 px-3 py-1',
          origin?.context && 'cursor-pointer hover:bg-accent/50'
        )}>
        <span className="min-w-0 flex-1 truncate">
          <span className={cn(!muted && 'text-foreground', inApp && 'font-medium')}>
            {functionName}
          </span>
          {repeat > 1 && (
            <span className="ml-2 rounded bg-muted px-1.5 text-[10px] text-muted-foreground">
              ×{repeat}
            </span>
          )}
        </span>
        <span
          className="shrink-0 text-muted-foreground"
          title={origin ? bundlePosition : frame.file}>
          {position}
        </span>
      </div>
      {origin && contextOpen && <SourceContext origin={origin} />}
    </li>
  );
};

const statusLabels: Partial<Record<ErrorGroupStatus, string>> = {
  waiting: 'Waiting to be processed…',
  indexing: 'Indexing the source map…',
  index_failed: 'Source map indexing failed',
  no_sourcemap: 'No source map for this update',
};

// The right side of the header: what stands between this trace and the
// app's own code, or where its processing is.
const SourceStatus = ({ status }: { status?: ErrorGroupStatus }) => {
  const { UPLOAD_SOURCEMAPS } = useSettings();
  const [explainerOpen, setExplainerOpen] = useState(false);
  const licenseQuery = useQuery({ queryKey: ['license'], queryFn: () => api.getLicense() });

  if (licenseQuery.data && !licenseQuery.data.valid) {
    return (
      <>
        <button
          type="button"
          onClick={() => setExplainerOpen(true)}
          className="inline-flex items-center gap-1.5 rounded-md px-2 py-1 font-sans text-[11px] text-emerald-700 hover:bg-emerald-400/10 dark:text-emerald-300">
          <Sparkles className="h-3 w-3" />
          See the original source with Symbolication
        </button>
        <EnterpriseExplainerDialog
          open={explainerOpen}
          onOpenChange={setExplainerOpen}
          feature={sourcemapFeature}
        />
      </>
    );
  }
  if (licenseQuery.data?.valid && !UPLOAD_SOURCEMAPS) {
    return (
      <span className="font-sans text-[11px] text-muted-foreground">
        Upload source maps at publish to see the original source
      </span>
    );
  }
  const label = status && statusLabels[status];
  if (!label) return null;
  const inProgress = status === 'waiting' || status === 'indexing';
  return (
    <span className="inline-flex items-center gap-1.5 font-sans text-[11px] text-muted-foreground">
      {inProgress && <Loader2 className="h-3 w-3 animate-spin motion-reduce:animate-none" />}
      {label}
    </span>
  );
};

/**
 * Displays parsed frames with a raw-text toggle and optional source-map results.
 * When errorGroup is supplied, polls every ten seconds while waiting or indexing;
 * parsed frames remain visible until a ready group arrives, including on query errors.
 */
export const StackTraceView = ({
  title,
  trace,
  raw,
  errorGroup,
}: {
  title: string;
  trace: StackTrace;
  raw: string;
  // Set for an exception's trace: the group holds its symbolicated frames.
  errorGroup?: { updateId: string; fingerprint: string };
}) => {
  const [open, setOpen] = useState(true);
  const [showAll, setShowAll] = useState(false);
  const [showRaw, setShowRaw] = useState(false);

  const groupQuery = useQuery({
    queryKey: [
      'observe',
      'error-group',
      api.getAppId(),
      errorGroup?.updateId,
      errorGroup?.fingerprint,
    ],
    queryFn: () => api.getErrorGroup(errorGroup!.updateId, errorGroup!.fingerprint),
    enabled: errorGroup !== undefined,
    // A group in the making shows up on its own.
    refetchInterval: query => {
      const status = query.state.data?.status;
      return status === 'waiting' || status === 'indexing' ? 10_000 : false;
    },
  });
  const group = groupQuery.data?.status === 'ready' ? groupQuery.data.group : undefined;

  const entries = group ? entriesOfSymbolicated(group.trace.frames ?? []) : trace.entries;
  const visible = showAll ? entries : entries.slice(0, collapsedRows);
  const hidden = entries.length - visible.length;
  // The first frame of the app's own code opens on its code; the others on a click.
  const firstInApp = entries.findIndex(entry => entry.type === 'frame' && entry.origin?.inApp);

  return (
    <section className="overflow-hidden rounded-lg border bg-card font-mono text-[11px]">
      <header className="flex items-center gap-2 border-b bg-muted/30 px-3 py-2">
        <button
          type="button"
          onClick={() => setOpen(!open)}
          aria-expanded={open}
          className="flex items-center gap-1.5 font-sans text-xs font-medium text-foreground">
          {open ? (
            <ChevronDown className="h-3.5 w-3.5" />
          ) : (
            <ChevronRight className="h-3.5 w-3.5" />
          )}
          {title}
          <span className="font-normal text-muted-foreground">{trace.frameCount} frames</span>
          {group?.culprit && (
            <span className="font-normal text-muted-foreground">· {group.culprit}</span>
          )}
        </button>
        <span className="ml-auto flex items-center gap-2">
          {errorGroup && <SourceStatus status={groupQuery.data?.status} />}
          <button
            type="button"
            onClick={() => setShowRaw(!showRaw)}
            aria-pressed={showRaw}
            className="rounded-md px-2 py-1 font-sans text-[11px] text-muted-foreground hover:bg-accent hover:text-foreground">
            {showRaw ? 'Frames' : 'Raw'}
          </button>
        </span>
      </header>
      {open &&
        (showRaw ? (
          <pre className="overflow-x-auto whitespace-pre-wrap break-all p-3 leading-relaxed text-muted-foreground">
            {raw}
          </pre>
        ) : (
          <>
            {trace.message && (
              <p className="whitespace-pre-wrap border-b px-3 py-2 text-foreground">
                {trace.message}
              </p>
            )}
            <ol className="py-1">
              {visible.map((entry, index) => (
                <FrameRow
                  key={`${group ? 'group' : 'raw'}-${index}`}
                  entry={entry}
                  showContext={index === firstInApp}
                />
              ))}
            </ol>
            {hidden > 0 && (
              <button
                type="button"
                onClick={() => setShowAll(true)}
                className="w-full border-t px-3 py-1.5 text-left font-sans text-[11px] text-primary hover:bg-accent">
                Show {hidden} more
              </button>
            )}
          </>
        ))}
    </section>
  );
};
