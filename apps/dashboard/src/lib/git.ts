export type GitProvider = 'github' | 'gitlab' | 'bitbucket' | 'cursor' | 'generic';

export type GitRepository = {
  provider: GitProvider;
  label: string;
  commitUrl: (commitHash: string) => string;
  review?: {
    marker: '#' | '!';
    url: (number: string) => string;
  };
};

export const gitRepositoryFor = (gitUrl: string | undefined): GitRepository | null => {
  if (!gitUrl) return null;

  try {
    const repositoryUrl = new URL(gitUrl);
    if (repositoryUrl.protocol !== 'https:' && repositoryUrl.protocol !== 'http:') return null;
    if (repositoryUrl.username || repositoryUrl.password) return null;

    repositoryUrl.pathname = repositoryUrl.pathname.replace(/\/+$/, '').replace(/\.git$/i, '');
    if (!repositoryUrl.pathname || repositoryUrl.pathname === '/') return null;
    repositoryUrl.search = '';
    repositoryUrl.hash = '';
    const baseUrl = repositoryUrl.toString();
    const hostname = repositoryUrl.hostname.toLowerCase();

    let cursorUrl: string | undefined;
    if (hostname === 'origin.cursor.com') {
      const pathSegments = repositoryUrl.pathname.split('/').filter(Boolean);
      const repositorySegments = pathSegments[0] === 'git' ? pathSegments.slice(1) : pathSegments;
      if (repositorySegments.length !== 2) return null;
      cursorUrl = `https://cursor.com/codebase/${repositorySegments.join('/')}`;
    } else if (hostname === 'cursor.com' || hostname === 'www.cursor.com') {
      const pathSegments = repositoryUrl.pathname.split('/').filter(Boolean);
      if (pathSegments.length !== 3 || pathSegments[0] !== 'codebase') return null;
      cursorUrl = `https://cursor.com/${pathSegments.join('/')}`;
    }
    if (cursorUrl) {
      return {
        provider: 'cursor',
        label: 'Cursor',
        commitUrl: hash => `${cursorUrl}/commit/${encodeURIComponent(hash)}`,
        review: {
          marker: '#',
          url: number => `${cursorUrl}/pull/${encodeURIComponent(number)}`,
        },
      };
    }
    if (hostname === 'github.com' || hostname === 'www.github.com') {
      return {
        provider: 'github',
        label: 'GitHub',
        commitUrl: hash => `${baseUrl}/commit/${encodeURIComponent(hash)}`,
        review: {
          marker: '#',
          url: number => `${baseUrl}/pull/${encodeURIComponent(number)}`,
        },
      };
    }
    if (hostname === 'gitlab.com' || hostname === 'www.gitlab.com') {
      return {
        provider: 'gitlab',
        label: 'GitLab',
        commitUrl: hash => `${baseUrl}/-/commit/${encodeURIComponent(hash)}`,
        review: {
          marker: '!',
          url: number => `${baseUrl}/-/merge_requests/${encodeURIComponent(number)}`,
        },
      };
    }
    if (hostname === 'bitbucket.org' || hostname === 'www.bitbucket.org') {
      return {
        provider: 'bitbucket',
        label: 'Bitbucket',
        commitUrl: hash => `${baseUrl}/commits/${encodeURIComponent(hash)}`,
        review: {
          marker: '#',
          url: number => `${baseUrl}/pull-requests/${encodeURIComponent(number)}`,
        },
      };
    }
    return {
      provider: 'generic',
      label: 'Git',
      commitUrl: hash => `${baseUrl}/commit/${encodeURIComponent(hash)}`,
    };
  } catch {
    return null;
  }
};
