import { useEffect, useMemo, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useVirtualizer } from '@tanstack/react-virtual';
import { api, type ObserveLogsQuery } from '@/lib/api';
import { liveInterval, type periods } from './filters';
import { useOlderRecords } from './useOlderRecords';

// The record stream the event table is built on: a live head query, older
// pages walked backwards behind it, and a virtualizer over the two joined.
// Kept apart from the table because the awkward parts are the ones nobody
// wants to read through to find a rendering bug: pausing when the reader
// scrolls away, resetting everything when the question changes, and pulling
// the next page before the scroll reaches the end.
export const useLogStream = ({
  query,
  signature,
  live,
  periodSpec,
  rowHeight = 40,
}: {
  query: ObserveLogsQuery;
  // What makes this a different question. Anything the URL carries, minus the
  // window, which slides on its own in live mode.
  signature: string;
  live: boolean;
  periodSpec: (typeof periods)[number];
  rowHeight?: number;
}) => {
  const scrollRef = useRef<HTMLDivElement>(null);
  const [paused, setPaused] = useState(false);

  useEffect(() => {
    setPaused(false);
    scrollRef.current?.scrollTo({ top: 0 });
  }, [signature]);

  const headQuery = useQuery({
    queryKey: ['observe', 'logs', api.getAppId(), query],
    queryFn: () => api.getObserveLogs(query),
    refetchInterval: paused ? false : liveInterval(live, periodSpec, true),
    placeholderData: previous => previous,
  });

  const older = useOlderRecords({ query, head: headQuery.data, signature });

  const logs = useMemo(() => {
    const seen = new Set<string>();
    return [...(headQuery.data?.logs ?? []), ...older.records].filter(log => {
      if (seen.has(log.eventKey)) return false;
      seen.add(log.eventKey);
      return true;
    });
  }, [headQuery.data?.logs, older.records]);

  const virtualizer = useVirtualizer({
    count: logs.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => rowHeight,
    overscan: 18,
    getItemKey: index => logs[index]?.eventKey ?? index,
  });

  const virtualItems = virtualizer.getVirtualItems();
  const lastVirtualIndex = virtualItems[virtualItems.length - 1]?.index ?? 0;
  useEffect(() => {
    if (logs.length > 0 && lastVirtualIndex >= logs.length - 20) {
      void older.loadMore();
    }
  }, [lastVirtualIndex, older, logs.length]);

  const resume = () => {
    setPaused(false);
    scrollRef.current?.scrollTo({ top: 0, behavior: 'smooth' });
    void headQuery.refetch();
  };

  return { scrollRef, logs, headQuery, older, paused, setPaused, resume, virtualizer };
};
