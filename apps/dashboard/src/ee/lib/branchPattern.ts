// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE at the repository root); it is NOT covered by the MIT
// license of this repository.

// Mirrors matchBranchPattern in ee/apikeyrestrictions/acl.go: "*" stands for
// any run of characters, empty included, and nothing else is special. Kept in
// sync so the preview under the field says what the server will actually
// decide, rather than an approximation of it.
export const matchBranchPattern = (pattern: string, name: string): boolean => {
  if (!pattern.includes('*')) return pattern === name;
  const segments = pattern.split('*');
  const prefix = segments[0];
  const suffix = segments[segments.length - 1];
  if (!name.startsWith(prefix) || !name.endsWith(suffix)) return false;
  // The two anchors must not overlap: "ab*ba" does not match "aba".
  if (prefix.length + suffix.length > name.length) return false;
  let rest = name.slice(prefix.length, name.length - suffix.length);
  for (const segment of segments.slice(1, -1)) {
    const index = rest.indexOf(segment);
    if (index < 0) return false;
    rest = rest.slice(index + segment.length);
  }
  return true;
};
