// Links an update's commit hash and the `#123` in its message back to GitHub,
// using the repository the server reports per app (EXPO_APP_REPOSITORY_URL).
// Only github.com: the /commit/<sha> and /pull/<n> shapes are host-specific, and
// guessing them elsewhere yields 404s, so anything else returns null and the
// caller renders plain text.
export type RepoLinks = {
  // Null for a rollback, which stores no commit hash.
  commitUrl: (sha: string) => string | null;
  pullUrl: (pullNumber: string) => string;
};

const GITHUB_HOSTS = new Set(['github.com', 'www.github.com']);

export const repoLinksFor = (repositoryUrl: string | undefined): RepoLinks | null => {
  if (!repositoryUrl) return null;

  let parsed: URL;
  try {
    parsed = new URL(repositoryUrl);
  } catch {
    return null;
  }
  if (!GITHUB_HOSTS.has(parsed.hostname.toLowerCase())) return null;

  // Exactly owner + repo: a deeper path links into the repository, not to it.
  const segments = parsed.pathname.split('/').filter(Boolean);
  if (segments.length !== 2) return null;

  const [owner, repoSegment] = segments;
  // `git remote get-url` hands out the .git suffix; the web UI does not use it.
  const repo = repoSegment.replace(/\.git$/i, '');
  if (!repo) return null;

  const base = `https://github.com/${owner}/${repo}`;
  return {
    commitUrl: sha => (sha ? `${base}/commit/${encodeURIComponent(sha)}` : null),
    // GitHub redirects /pull/<n> to the issue when that number is one.
    pullUrl: pullNumber => `${base}/pull/${encodeURIComponent(pullNumber)}`,
  };
};
