// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { useState } from 'react';
import { ChevronDown, ChevronRight } from 'lucide-react';

export type Json = null | boolean | number | string | Json[] | { [key: string]: Json };

const Scalar = ({ value }: { value: Json }) => {
  if (value === null) return <span className="text-violet-600 dark:text-violet-300">null</span>;
  if (typeof value === 'boolean')
    return <span className="text-violet-600 dark:text-violet-300">{String(value)}</span>;
  if (typeof value === 'number')
    return <span className="text-amber-700 dark:text-amber-300">{value}</span>;
  // A stack trace is a string: its line breaks are kept rather than escaped.
  return (
    <span className="whitespace-pre-wrap break-words text-emerald-700 dark:text-emerald-300">
      &quot;{String(value)}&quot;
    </span>
  );
};

const Node = ({ name, value, depth }: { name?: string; value: Json; depth: number }) => {
  const isContainer = value !== null && typeof value === 'object';
  const [open, setOpen] = useState(depth < 2);
  const label = name !== undefined && (
    <span className="text-sky-700 dark:text-sky-300">&quot;{name}&quot;: </span>
  );

  if (!isContainer) {
    return (
      <div className="pl-4">
        {label}
        <Scalar value={value} />
      </div>
    );
  }

  const entries: Array<[string | undefined, Json]> = Array.isArray(value)
    ? value.map(item => [undefined, item])
    : Object.entries(value);
  const [openBracket, closeBracket] = Array.isArray(value) ? ['[', ']'] : ['{', '}'];

  return (
    <div className={depth > 0 ? 'pl-4' : undefined}>
      <button
        type="button"
        onClick={() => setOpen(!open)}
        className="-ml-4 inline-flex items-center text-left hover:text-foreground">
        {open ? (
          <ChevronDown className="h-3 w-4 shrink-0 text-muted-foreground" />
        ) : (
          <ChevronRight className="h-3 w-4 shrink-0 text-muted-foreground" />
        )}
        {label}
        <span className="text-muted-foreground">{openBracket}</span>
        {!open && (
          <span className="text-muted-foreground">
            {' '}
            {entries.length} {entries.length === 1 ? 'item' : 'items'} {closeBracket}
          </span>
        )}
      </button>
      {open && (
        <>
          {entries.map(([key, item], index) => (
            <Node key={key ?? index} name={key} value={item} depth={depth + 1} />
          ))}
          <div className="text-muted-foreground">{closeBracket}</div>
        </>
      )}
    </div>
  );
};

export const JsonView = ({ value }: { value: Json }) => (
  <div className="overflow-x-auto rounded-lg border bg-card p-3 pl-7 font-mono text-[11px] leading-relaxed text-foreground">
    <Node value={value} depth={0} />
  </div>
);
