// CI publishes with `git log --oneline`, so the message arrives prefixed with the short
// hash the Commit column already shows. Drop that prefix only when it really is this
// update's commit, so a message that happens to start with a hex word is left alone.
export const updateTitle = (message: string | undefined, commitHash: string) => {
  const raw = message ?? '';
  // A dashboard rollback stores no commit hash, so there is nothing to match against.
  if (commitHash.length < 7) return raw;
  const match = /^([0-9a-f]{7,40})\s+(\S.*)$/is.exec(raw);
  if (!match) return raw;
  const [, token, rest] = match;
  return commitHash.toLowerCase().startsWith(token.toLowerCase()) ? rest : raw;
};

// Forty hex characters is the fingerprint `expo-updates runtimeversion:resolve` returns;
// named versions ("1.0.0", "exposdk:52.0.0") are left as they were set.
export const shortRuntimeVersion = (value: string) =>
  /^[0-9a-f]{40}$/i.test(value) ? value.slice(0, 8) : value;

// A squash merge puts the pull request number in the commit subject. Split
// rather than replace, so `updateTitle` stays a plain string for the republish
// dialog label and the `title` tooltips.
export type TitlePart = { text: string } | { pr: string };

export const splitPullRequestRefs = (title: string): TitlePart[] => {
  const parts: TitlePart[] = [];
  let cursor = 0;
  // Trailing \w guard: "#123abc" is not a reference, so it must not link the
  // "#123" prefix at some unrelated pull request.
  for (const match of title.matchAll(/#(\d+)(?!\w)/g)) {
    const start = match.index ?? 0;
    if (start > cursor) parts.push({ text: title.slice(cursor, start) });
    parts.push({ pr: match[1] });
    cursor = start + match[0].length;
  }
  if (cursor < title.length) parts.push({ text: title.slice(cursor) });
  return parts;
};
