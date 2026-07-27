import { useEffect, useRef } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api, type ObserveCheckInFeed } from '@/lib/api';
import type { ObserveQuery } from '@/lib/api';

// The feed's cadence is a property of the map, not of the selected period: the
// ripples say what is happening now, whether the charts above show an hour or a
// month. Ten seconds also stays well inside the lookback the server allows, so
// consecutive windows join up instead of leaving gaps.
export const checkInPollMs = 10_000;

export const useCheckInFeed = (
  enabled: boolean,
  filter: ObserveQuery,
  onFeed: (feed: ObserveCheckInFeed) => void
) => {
  // Server-issued, never computed here: the browser clock has no say in where
  // the window starts.
  const cursorRef = useRef<string | undefined>(undefined);
  const handlerRef = useRef(onFeed);

  const query = useQuery({
    queryKey: ['observe', 'check-ins', api.getAppId(), filter],
    queryFn: () => api.getObserveCheckIns(cursorRef.current, filter),
    enabled,
    refetchInterval: checkInPollMs,
    // A tail, never a cache. Replaying a stored window on mount would fire
    // ripples for arrivals that are already minutes old.
    staleTime: 0,
    gcTime: 0,
    retry: 1,
  });

  useEffect(() => {
    handlerRef.current = onFeed;
  });

  const feed = query.data;
  useEffect(() => {
    if (!feed) return;
    cursorRef.current = feed.cursor;
    handlerRef.current(feed);
  }, [feed]);

  return {
    truncated: feed?.truncated ?? false,
    // Rate rather than a raw count: the window is whatever the server decided,
    // so "412 check-ins" would mean something different on every poll.
    perMinute:
      feed && feed.windowSeconds > 0
        ? (feed.cities.reduce((total, city) => total + city.deviceCount, 0) * 60) /
          feed.windowSeconds
        : 0,
    failed: query.isError,
  };
};
