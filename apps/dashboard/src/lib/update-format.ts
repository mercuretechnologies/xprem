// CI often publishes with `git log --oneline`, so the subject arrives behind the short
// hash the Commit column already shows. Drop it only when that leading token really is
// this update's commit: a message that legitimately opens with a hex-looking word is
// something someone wrote, and a table cell is the wrong place to second-guess it.
export const updateTitle = (message: string | undefined, commitHash: string) => {
  const raw = message ?? '';
  // A dashboard rollback stores no commit hash, so there is nothing to match against.
  if (commitHash.length < 7) return raw;
  const match = /^([0-9a-f]{7,40})\s+(\S.*)$/is.exec(raw);
  if (!match) return raw;
  const [, token, rest] = match;
  return commitHash.toLowerCase().startsWith(token.toLowerCase()) ? rest : raw;
};

// A fingerprint runtime version is forty hex characters that say nothing at a glance,
// and the full value stays one hover away. A named one ("1.0.0", "exposdk:52.0.0") is
// already short and must survive intact.
export const shortRuntimeVersion = (value: string) =>
  /^[0-9a-f]{16,}$/i.test(value) ? value.slice(0, 8) : value;
