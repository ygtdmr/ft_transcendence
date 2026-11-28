/**
 * Username search for friend discovery: `GET /api/v1/users/search`.
 *
 * The term is trimmed and searched once typing pauses, so a burst of keystrokes sends one
 * request. A term outside the contract's 1..20 characters is never sent: an empty one
 * simply means "not searching". Answers for an older term are dropped (and their request
 * aborted), so the list always matches what is in the field. More matches are read with
 * the next `offset` on demand.
 */

import { useCallback, useEffect, useRef, useState } from 'react';

import { USER_SEARCH_QUERY } from '@shared/types';
import type { UserSearchResult } from '@shared/types';

import { searchUsers } from '../api';

/** Pause after the last keystroke before the term is searched. */
export const SEARCH_DEBOUNCE_MS = 300;

/** Matches per page. */
const SEARCH_PAGE_SIZE = 20;

export type UserSearchStatus = 'IDLE' | 'SEARCHING' | 'READY' | 'ERROR';

/** The term as it is sent: trimmed, or `null` when there is nothing to search for. */
export function searchTerm(input: string): string | null {
  const term = input.trim();
  return term.length >= USER_SEARCH_QUERY.minLength && term.length <= USER_SEARCH_QUERY.maxLength
    ? term
    : null;
}

export function useUserSearch() {
  const [input, setInput] = useState('');
  const [results, setResults] = useState<UserSearchResult[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [status, setStatus] = useState<UserSearchStatus>('IDLE');
  const [loadingMore, setLoadingMore] = useState(false);
  /** The term the shown results belong to. */
  const [searched, setSearched] = useState<string | null>(null);
  const controller = useRef<AbortController | null>(null);

  const term = searchTerm(input);

  const run = useCallback(async (query: string, offset: number) => {
    controller.current?.abort();
    const current = new AbortController();
    controller.current = current;
    if (offset === 0) setStatus('SEARCHING');
    else setLoadingMore(true);
    try {
      const page = await searchUsers({ q: query, limit: SEARCH_PAGE_SIZE, offset }, current.signal);
      if (current.signal.aborted) return;
      setResults((shown) => (offset === 0 ? page.results : [...shown, ...page.results]));
      setHasMore(page.has_more && page.results.length > 0);
      setSearched(query);
      setStatus('READY');
    } catch {
      if (current.signal.aborted) return;
      // A failed first page is the search's error; a failed next page keeps what is shown.
      if (offset === 0) setStatus('ERROR');
    } finally {
      if (!current.signal.aborted) setLoadingMore(false);
    }
  }, []);

  useEffect(() => {
    if (term === null) {
      controller.current?.abort();
      setStatus('IDLE');
      setResults([]);
      setHasMore(false);
      setSearched(null);
      return;
    }
    const timer = setTimeout(() => void run(term, 0), SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [run, term]);

  useEffect(() => () => controller.current?.abort(), []);

  return {
    input,
    setInput,
    /** The trimmed term being searched for, or `null` when the field holds none. */
    term,
    results,
    /** The term the shown results belong to. */
    searched,
    status,
    hasMore,
    loadingMore,
    loadMore: useCallback(() => {
      if (searched !== null && hasMore && !loadingMore) void run(searched, results.length);
    }, [hasMore, loadingMore, results.length, run, searched]),
    retry: useCallback(() => {
      if (term !== null) void run(term, 0);
    }, [run, term]),
  };
}
