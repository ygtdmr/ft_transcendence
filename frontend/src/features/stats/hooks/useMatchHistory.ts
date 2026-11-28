/**
 * GAME HISTORY data for the own profile, from `GET /api/v1/users/me/matches`.
 *
 * Exposes the four UI states — loading, error, empty, populated — and cursor paging
 * with `before` = the oldest loaded `finished_at`. Nothing is cached across mounts.
 */

import { useCallback, useEffect, useRef, useState } from 'react';

import type { ApiError } from '@shared/api';
import type { MatchHistoryEntry } from '@shared/types';

import { getMatchHistory } from '../api';

const PAGE_SIZE = 20;

export type MatchHistoryStatus = 'LOADING' | 'ERROR' | 'EMPTY' | 'READY';

export function useMatchHistory() {
  const [matches, setMatches] = useState<MatchHistoryEntry[]>([]);
  const [status, setStatus] = useState<MatchHistoryStatus>('LOADING');
  const [error, setError] = useState<ApiError | null>(null);
  const [hasMore, setHasMore] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const request = useRef<AbortController | null>(null);

  const load = useCallback(async (before?: string) => {
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    if (before) setLoadingMore(true);
    else setStatus('LOADING');
    try {
      const page = await getMatchHistory(
        before ? { limit: PAGE_SIZE, before } : { limit: PAGE_SIZE },
      );
      if (controller.signal.aborted) return;
      setMatches((current) => {
        const next = before ? [...current, ...page.matches] : page.matches;
        setStatus(next.length === 0 ? 'EMPTY' : 'READY');
        return next;
      });
      setHasMore(page.matches.length === PAGE_SIZE);
      setError(null);
    } catch (cause) {
      if (controller.signal.aborted) return;
      setError(cause as ApiError);
      if (!before) setStatus('ERROR');
    } finally {
      if (!controller.signal.aborted) setLoadingMore(false);
    }
  }, []);

  useEffect(() => {
    void load();
    return () => request.current?.abort();
  }, [load]);

  const loadMore = useCallback(() => {
    const oldest = matches.at(-1);
    if (oldest && hasMore && !loadingMore) void load(oldest.finished_at);
  }, [hasMore, load, loadingMore, matches]);

  return { matches, status, error, hasMore, loadingMore, retry: () => void load(), loadMore };
}
