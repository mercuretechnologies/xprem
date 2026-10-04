// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

// Reads a stack trace the way ee/symbolication/frames.go does, so the
// dashboard and the server agree on what a frame is.

import type { TraceFrame, TraceOrigin } from '@/lib/api';

export type StackFrame = {
  // Bytecode frames point at a Hermes bytecode offset, native ones at no source at all.
  kind: 'source' | 'bytecode' | 'native';
  functionName: string;
  file: string;
  line: number;
  column: number;
};

export type StackEntry =
  // A frame, and how many times in a row it appears: a recursion shows once.
  // origin is where it comes from, once the server mapped it.
  | { type: 'frame'; frame: StackFrame; repeat: number; origin?: TraceOrigin }
  // Frames the engine or the server left out.
  | { type: 'skipped'; count: number };

export type StackTrace = {
  // The lines before the first frame, usually "Error: message".
  message: string;
  entries: StackEntry[];
  frameCount: number;
};

type FrameFormat = { pattern: RegExp; frame: (match: RegExpExecArray) => StackFrame };

const native = (functionName = '', file = '', line = 0): StackFrame => ({
  kind: 'native',
  functionName,
  file,
  line,
  column: 0,
});

// Tried in order, the first match wins. Same list as frames.go.
const frameFormats: FrameFormat[] = [
  {
    // Hermes and V8: "at fn (address at file:1:2)", "at fn (file:1:2)", "at fn (native)".
    pattern: /^\s*at (.+?) \((?:(native)|(address at )?(.+):(\d+):(\d+))\)$/,
    frame: match =>
      match[2]
        ? native(match[1])
        : {
            kind: match[3] ? 'bytecode' : 'source',
            functionName: match[1],
            file: match[4],
            line: Number(match[5]),
            column: Number(match[6]),
          },
  },
  {
    // V8 without a function name: "at file:1:2".
    pattern: /^\s*at (\S.*):(\d+):(\d+)$/,
    frame: match => ({
      kind: 'source',
      functionName: '',
      file: match[1],
      line: Number(match[2]),
      column: Number(match[3]),
    }),
  },
  {
    // JavaScriptCore and Firefox: "fn@file:1:2".
    pattern: /^\s*([^@\s]*)@(.+):(\d+):(\d+)$/,
    frame: match => ({
      kind: 'source',
      functionName: match[1],
      file: match[2],
      line: Number(match[3]),
      column: Number(match[4]),
    }),
  },
  {
    // JavaScriptCore native code: "fn@[native code]".
    pattern: /^\s*(?:([^@\s]*)@)?\[native code\]$/,
    frame: match => native(match[1]),
  },
  {
    // Java, as Android renders it: "com.app.Main.run(Main.java:12)".
    pattern: /^\s*(?:at )?([\w$]+(?:\.[\w$<>]+)+)\(([^():]*)(?::(\d+))?\)$/,
    frame: match => native(match[1], match[2], Number(match[3] ?? 0)),
  },
  {
    // iOS: "symbol (Binary + 1234)", "Binary + 1234".
    pattern: /^(?:(.+) \()?([^\s()]+) \+ \d+\)?$/,
    frame: match => native(match[1], match[2]),
  },
  {
    // iOS, an address alone: "0x1a2b".
    pattern: /^0x[0-9a-fA-F]+$/,
    frame: () => native(),
  },
];

const skippedPatterns = [/^\s*\.\.\. skipping (\d+) frames$/, /^\s*… \+(\d+) more frames$/];

const maxLineLength = 4096;

const parseFrame = (line: string): StackFrame | null => {
  if (line.length > maxLineLength) return null;
  for (const format of frameFormats) {
    const match = format.pattern.exec(line);
    if (match) return format.frame(match);
  }
  return null;
};

const parseSkipped = (line: string): number | null => {
  for (const pattern of skippedPatterns) {
    const match = pattern.exec(line);
    if (match) return Number(match[1]);
  }
  return null;
};

const sameFrame = (a: StackFrame, b: StackFrame) =>
  a.kind === b.kind &&
  a.functionName === b.functionName &&
  a.file === b.file &&
  a.line === b.line &&
  a.column === b.column;

// Reads a value as a stack trace; null when it holds fewer than minFrames.
// Two frames tell a trace from any other text; a value known to be a trace,
// like exception.stacktrace, may hold a single one (an async crash has no caller).
export const parseStackTrace = (text: string, minFrames = 2): StackTrace | null => {
  const messageLines: string[] = [];
  const entries: StackEntry[] = [];
  let frameCount = 0;
  for (const line of text.split('\n')) {
    const frame = parseFrame(line);
    if (frame) {
      frameCount += 1;
      const previous = entries[entries.length - 1];
      if (previous?.type === 'frame' && sameFrame(previous.frame, frame)) {
        previous.repeat += 1;
      } else {
        entries.push({ type: 'frame', frame, repeat: 1 });
      }
      continue;
    }
    const skipped = parseSkipped(line);
    if (skipped !== null) {
      entries.push({ type: 'skipped', count: skipped });
      continue;
    }
    // Text before the first frame is the message; text between frames is dropped.
    if (frameCount === 0 && line.trim()) messageLines.push(line.trim());
  }
  if (frameCount < minFrames) return null;
  return { message: messageLines.join('\n'), entries, frameCount };
};

// The file of a frame without the folder it sits in on the device. A bundle
// is named after its hash, shortened to its first 8 characters.
export const shortFileName = (file: string) => {
  const name = file.split('/').pop() ?? file;
  return name.replace(/^([0-9a-f]{8})[0-9a-f]{24}(\.\w+)$/, '$1…$2');
};

// The entries of a trace the server symbolicated, in the shape parseStackTrace gives.
export const entriesOfSymbolicated = (frames: TraceFrame[]): StackEntry[] =>
  frames.map(frame =>
    frame.skipped
      ? { type: 'skipped', count: frame.skipped }
      : {
          type: 'frame',
          frame: {
            kind: frame.native ? 'native' : 'source',
            functionName: frame.function ?? '',
            file: frame.file ?? '',
            line: frame.line ?? 0,
            column: frame.column ?? 0,
          },
          repeat: frame.repeat ?? 1,
          origin: frame.origin,
        }
  );
