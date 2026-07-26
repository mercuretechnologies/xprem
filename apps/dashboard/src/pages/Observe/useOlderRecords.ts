import { useCallback, useEffect, useState } from 'react';
import { api, type ObserveLog, type ObserveLogsPage, type ObserveLogsQuery } from '@/lib/api';

// The backwards half of the log tail. The head is a live query that keeps
// returning the newest records; this walks the cursor the other way as the
// viewer scrolls down.
//
// Its own hook because it is a small state machine of its own (what has been
// pulled, where the cursor stopped, whether a page is in flight, whether the
// end or an error was reached) that the view never touches except to read.
// `signature` is what resets it: a new question means the pages already pulled
// answer the previous one.
export const useOlderRecords = ({
  query,
  head,
  signature,
}: {
  query: ObserveLogsQuery;
  head?: ObserveLogsPage;
  signature: string;
}) => {
  const [records, setRecords] = useState<ObserveLog[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [hasMore, setHasMore] = useState(true);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    setRecords([]);
    setCursor(null);
    setHasMore(true);
    setFailed(false);
  }, [signature]);

  const loadMore = useCallback(async () => {
    if (loading || !hasMore || !head?.available) return;
    const next = cursor ?? head.nextCursor;
    if (!next) {
      setHasMore(false);
      return;
    }
    setLoading(true);
    try {
      const page = await api.getObserveLogs({ ...query, cursor: next });
      setRecords(current => [...current, ...page.logs]);
      setCursor(page.nextCursor ?? '');
      setHasMore(Boolean(page.nextCursor));
      setFailed(false);
    } catch {
      // Stop the scroll from asking again: the caller's effect fires on every
      // refetch, so an unhandled failure here would retry forever in silence.
      setFailed(true);
      setHasMore(false);
    } finally {
      setLoading(false);
    }
  }, [cursor, hasMore, head, loading, query]);

  const retry = useCallback(() => {
    setFailed(false);
    setHasMore(true);
  }, []);

  return { records, loading, hasMore, failed, loadMore, retry };
};
