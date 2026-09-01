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
