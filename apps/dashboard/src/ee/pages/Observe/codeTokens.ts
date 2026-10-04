// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

// Colors a line of JavaScript or TypeScript, enough for the few lines of
// source shown around a frame. One line at a time: a comment or a template
// string that spans lines is colored on the line it starts.

export type CodeToken = {
  kind: 'comment' | 'string' | 'keyword' | 'number' | 'call' | 'type' | 'punctuation' | 'plain';
  text: string;
};

const keywords = new Set(
  `async await break case catch class const continue default delete do else enum export extends false finally for from function if import in instanceof interface let new null of return static super switch this throw true try type typeof undefined var void while yield as declare readonly satisfies`.split(
    ' '
  )
);

// Tried in order at the current position; the first match wins.
const patterns: Array<{ kind: CodeToken['kind']; pattern: RegExp }> = [
  { kind: 'comment', pattern: /^(\/\/.*|\/\*.*?(\*\/|$))/ },
  { kind: 'string', pattern: /^('(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*"|`(?:[^`\\]|\\.)*`?)/ },
  { kind: 'number', pattern: /^(0x[0-9a-fA-F]+|\d+(?:\.\d+)?)\b/ },
  { kind: 'plain', pattern: /^[A-Za-z_$][\w$]*/ },
  { kind: 'punctuation', pattern: /^[{}()[\].,;:<>=+\-*/%!&|?^~]+/ },
  { kind: 'plain', pattern: /^\s+|^./ },
];

export const tokenizeCode = (line: string): CodeToken[] => {
  const tokens: CodeToken[] = [];
  let rest = line;
  while (rest) {
    for (const { kind, pattern } of patterns) {
      const match = pattern.exec(rest);
      if (!match) continue;
      const text = match[0];
      tokens.push({
        kind: kind === 'plain' ? wordKind(text, rest.slice(text.length)) : kind,
        text,
      });
      rest = rest.slice(text.length);
      break;
    }
  }
  return tokens;
};

// A word is a keyword, a call when "(" follows, a type when it is capitalized.
const wordKind = (word: string, after: string): CodeToken['kind'] => {
  if (!/^[A-Za-z_$]/.test(word)) return 'plain';
  if (keywords.has(word)) return 'keyword';
  if (/^\s*\(/.test(after)) return 'call';
  if (/^[A-Z]/.test(word)) return 'type';
  return 'plain';
};
