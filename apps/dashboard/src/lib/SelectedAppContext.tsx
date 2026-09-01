import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  ReactNode,
} from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { api, type AppDescriptor } from '@/lib/api';
import { isAuthenticated } from '@/lib/auth.ts';

const STORAGE_KEY = 'eoota.selectedAppId';

type SelectedAppContextValue = {
  apps: AppDescriptor[];
  selectedAppId: string | null;
  setSelectedAppId: (appId: string) => void;
  isLoading: boolean;
  error: Error | null;
  refreshApps: () => Promise<void>;
};

const SelectedAppContext = createContext<SelectedAppContextValue | undefined>(undefined);

// SelectedAppProvider is the single source of truth for the currently-viewed
// app in the dashboard. It fetches the list from /api/settings once, picks an
// initial app (localStorage if still valid, first app otherwise), and keeps
// the ApiClient's appId in sync so every call site that uses `api.getX()`
// automatically scopes to the right app. Changing the selection invalidates
// every react-query cache entry so tables re-fetch against the new app.
export function SelectedAppProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();

  const refreshApps = useCallback(async () => {
    await queryClient.invalidateQueries({ queryKey: ['apps'] });
  }, [queryClient]);

  // Gated on auth: /api/settings requires a token, and the provider is
  // mounted above the router so it renders even on /login. Firing the
  // query unauthenticated either spams console errors or, with a stale
  // refreshToken, drives the api refresh→logout fallback before the
  // user has a chance to type their password.
  const appsQuery = useQuery({
    queryKey: ['apps'],
    queryFn: () => api.getApps(),
    enabled: isAuthenticated(),
  });

  const apps = useMemo(() => appsQuery.data ?? [], [appsQuery.data]);
  const [selectedAppId, setSelectedAppIdState] = useState<string | null>(null);

  const setSelectedAppId = useCallback(
    (appId: string) => {
      setSelectedAppIdState(appId);
      api.setAppId(appId);
      localStorage.setItem(STORAGE_KEY, appId);
      // Invalidate every per-app query so tables re-fetch under the new app
      // immediately. Pages that read non-app data (settings, login) are
      // scoped to their own keys and untouched.
      queryClient.invalidateQueries({
        predicate: q => {
          const key = q.queryKey[0];
          return key !== 'settings' && key !== 'apps';
        },
      });
    },
    [queryClient]
  );

  // Initial resolution: pick the stored app if it's still in the list,
  // otherwise default to the first one. Runs whenever `apps` changes (e.g.,
  // someone re-deployed with a new app list while the dashboard was open).
  // Routing through setSelectedAppId here (not a direct state write) is
  // important: it invalidates any per-app query that was already fired
  // before settings resolved, so those queries, which threw "No app
  // selected" from api.appScope(), actually retry with an appId set
  // instead of waiting for react-query's default retry budget to kick in.
  useEffect(() => {
    if (!apps.length) {
      if (selectedAppId !== null) {
        setSelectedAppIdState(null);
        api.setAppId(null);
      }
      return;
    }
    const appIds = apps.map(a => a.id);
    const stored = localStorage.getItem(STORAGE_KEY);
    const initial = stored && appIds.includes(stored) ? stored : appIds[0];
    if (initial !== selectedAppId) {
      setSelectedAppId(initial);
    }
    // `selectedAppId` intentionally omitted from deps: we only want this to
    // resolve on the (settings data, apps array) change, not on every user
    // selection (which is handled by setSelectedAppId itself).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [apps, setSelectedAppId]);

  const value = useMemo<SelectedAppContextValue>(
    () => ({
      apps,
      selectedAppId,
      setSelectedAppId,
      refreshApps,
      isLoading: appsQuery.isLoading,
      error: appsQuery.error,
    }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [apps, selectedAppId, setSelectedAppId, refreshApps, appsQuery.isLoading, appsQuery.error]
  );

  return <SelectedAppContext.Provider value={value}>{children}</SelectedAppContext.Provider>;
}

// useSelectedApp is the hook every page/component uses to read or change the
// current app. Throws if called outside the provider so missing wiring is
// caught at render time, not by a silent "undefined appId" network error.
export function useSelectedApp(): SelectedAppContextValue {
  const ctx = useContext(SelectedAppContext);
  if (!ctx) {
    throw new Error('useSelectedApp must be used inside a <SelectedAppProvider>');
  }
  return ctx;
}
