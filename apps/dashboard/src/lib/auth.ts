export const isAuthenticated = () => {
  return !!localStorage.getItem('token') && !!localStorage.getItem('refreshToken');
};

export const getToken = () => {
  return localStorage.getItem('token');
};

export const getRefreshToken = () => {
  return localStorage.getItem('refreshToken');
};

export const setTokens = (token: string, refreshToken: string) => {
  localStorage.setItem('token', token);
  localStorage.setItem('refreshToken', refreshToken);
};

export const logout = () => {
  localStorage.removeItem('token');
  localStorage.removeItem('refreshToken');
};

const RETURN_TO_KEY = 'returnTo';
const RETURN_TO_TTL_MS = 10 * 60 * 1000;

// Where the next successful sign-in should land instead of the home page.
// sessionStorage rather than router state: it must survive the SSO round-trip
// through the identity provider. Writing is idempotent, so it is render-safe.
export const saveReturnTo = (path: string) => {
  sessionStorage.setItem(RETURN_TO_KEY, JSON.stringify({ path, at: Date.now() }));
};

// peekReturnTo is deliberately read-only so it can be called during render
// (StrictMode renders twice); the entry is dropped by clearReturnTo once the
// destination page mounts, or by the TTL for abandoned flows.
export const peekReturnTo = (): string | null => {
  const raw = sessionStorage.getItem(RETURN_TO_KEY);
  if (!raw) {
    return null;
  }
  try {
    const { path, at } = JSON.parse(raw) as { path?: unknown; at?: unknown };
    if (typeof path !== 'string' || typeof at !== 'number' || Date.now() - at > RETURN_TO_TTL_MS) {
      return null;
    }
    // Router-internal paths only: anything else could bounce a fresh session
    // to another origin.
    if (!path.startsWith('/') || path.startsWith('//')) {
      return null;
    }
    return path;
  } catch {
    return null;
  }
};

export const clearReturnTo = () => {
  sessionStorage.removeItem(RETURN_TO_KEY);
};
