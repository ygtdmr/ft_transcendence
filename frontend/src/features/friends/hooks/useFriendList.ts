/**
 * The signed-in user's whole friend list, from the shared friends store.
 *
 * It reads `GET /api/v1/friends` (every page) when it mounts — quietly when an earlier screen
 * already loaded the list, so what is shown stays on screen — again after a reconnect, and
 * whenever `refresh` is called. The server does not push presence, so `is_online` is as
 * fresh as the last read.
 */

import { useCallback, useEffect, useRef, useState } from 'react';

import { useOnReconnect } from '@shared/hooks';

import { listAllFriends } from '../api';
import { useFriendsStore } from '../store/friendsStore';

export type FriendListStatus = 'LOADING' | 'READY' | 'ERROR';

export function useFriendList() {
  const friends = useFriendsStore((state) => state.friends);
  const [status, setStatus] = useState<FriendListStatus>(() =>
    useFriendsStore.getState().loaded ? 'READY' : 'LOADING',
  );
  const mounted = useRef(true);

  const load = useCallback(async (quiet: boolean) => {
    if (!quiet) setStatus('LOADING');
    try {
      const { friends: list, friendCount } = await listAllFriends();
      if (!mounted.current) return;
      useFriendsStore.getState().setFriends(list, friendCount);
      setStatus('READY');
    } catch {
      if (mounted.current && !quiet) setStatus('ERROR');
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    void load(useFriendsStore.getState().loaded);
    return () => {
      mounted.current = false;
    };
  }, [load]);

  useOnReconnect(() => void load(true));

  return {
    friends,
    status,
    retry: useCallback(() => void load(false), [load]),
    refresh: useCallback(() => void load(true), [load]),
  };
}
