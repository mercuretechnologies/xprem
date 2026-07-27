// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { useCallback, useEffect, useRef, useState } from 'react';
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

  // Which question the state belongs to. A page already in flight when the
  // filters change would otherwise land on the reset state: its rows appended
  // to the new list, and its cursor walked from there, so the reader gets the
  // previous question's records and pages deeper into the wrong stream.
  const generation = useRef(0);

  useEffect(() => {
    generation.current += 1;
    setRecords([]);
    setCursor(null);
    setHasMore(true);
    setFailed(false);
    setLoading(false);
  }, [signature]);

  const loadMore = useCallback(async () => {
    if (loading || !hasMore || !head?.available) return;
    const next = cursor ?? head.nextCursor;
    if (!next) {
      setHasMore(false);
      return;
    }
    const asked = generation.current;
    setLoading(true);
    try {
      const page = await api.getObserveLogs({ ...query, cursor: next });
      if (asked !== generation.current) return;
      setRecords(current => [...current, ...page.logs]);
      setCursor(page.nextCursor ?? '');
      setHasMore(Boolean(page.nextCursor));
      setFailed(false);
    } catch {
      if (asked !== generation.current) return;
      // Stop the scroll from asking again: the caller's effect fires on every
      // refetch, so an unhandled failure here would retry forever in silence.
      setFailed(true);
      setHasMore(false);
    } finally {
      // Only the current question's request may clear the flag: a superseded
      // one finishing late would otherwise unlock a fetch already running.
      if (asked === generation.current) setLoading(false);
    }
  }, [cursor, hasMore, head, loading, query]);

  const retry = useCallback(() => {
    setFailed(false);
    setHasMore(true);
  }, []);

  return { records, loading, hasMore, failed, loadMore, retry };
};
