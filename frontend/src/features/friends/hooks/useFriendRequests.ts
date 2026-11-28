/**
 * The signed-in user's pending friend requests, both ways: `GET /api/v1/friends/requests`.
 *
 * Read when the hook mounts — quietly when an earlier screen already loaded them — and
 * again after a reconnect, since the socket replays no social event missed while it was
 * down. Between reads the social events keep the store current.
 */

import { useCallback, useEffect, useRef, useState } from 'react';

import { useOnReconnect } from '@shared/hooks';

import { listFriendRequests } from '../api';
import { useFriendsStore } from '../store/friendsStore';

export type FriendRequestsStatus = 'LOADING' | 'READY' | 'ERROR';

export async function refreshFriendRequests(): Promise<void> {
  const { incoming, outgoing } = await listFriendRequests();
  useFriendsStore.getState().setRequests(incoming, outgoing);
}

export function useFriendRequests() {
  const incoming = useFriendsStore((state) => state.incoming);
  const outgoing = useFriendsStore((state) => state.outgoing);
  const [status, setStatus] = useState<FriendRequestsStatus>(() =>
    useFriendsStore.getState().requestsLoaded ? 'READY' : 'LOADING',
  );
  const mounted = useRef(true);

  const load = useCallback(async (quiet: boolean) => {
    if (!quiet) setStatus('LOADING');
    try {
      await refreshFriendRequests();
      if (mounted.current) setStatus('READY');
    } catch {
      if (mounted.current && !quiet) setStatus('ERROR');
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    void load(useFriendsStore.getState().requestsLoaded);
    return () => {
      mounted.current = false;
    };
  }, [load]);

  useOnReconnect(() => void load(true));

  return { incoming, outgoing, status, retry: useCallback(() => void load(false), [load]) };
}
