/**
 * The Players Online list in Room Discovery: the user's friends who are online now.
 *
 * `GET /api/v1/friends` carries live presence, read fresh on every call, every page of it; the
 * server does not push presence changes, so the list is as current as its last load. It
 * loads when Room Discovery opens, again on Try again after a failure, and again, without
 * the loading placeholders, when the realtime connection comes back after a drop: whatever
 * changed while it was down is read fresh rather than trusted from before.
 */

import { useCallback, useEffect, useRef, useState } from 'react';

import { useConnectionStore } from '@shared/stores';
import type { Friend } from '@shared/types';

import { listAllFriends } from '../api';
import { useFriendsStore } from '../store/friendsStore';

export type OnlineFriendsStatus = 'LOADING' | 'READY' | 'ERROR';

export function useOnlineFriends() {
  const friends = useFriendsStore((state) => state.friends);
  const setFriends = useFriendsStore((state) => state.setFriends);
  const [status, setStatus] = useState<OnlineFriendsStatus>('LOADING');
  const mounted = useRef(true);

  const load = useCallback(
    async (quiet = false) => {
      if (!quiet) setStatus('LOADING');
      try {
        const { friends: list, friendCount } = await listAllFriends();
        if (!mounted.current) return;
        setFriends(list, friendCount);
        setStatus('READY');
      } catch {
        if (mounted.current && !quiet) setStatus('ERROR');
      }
    },
    [setFriends],
  );

  useEffect(() => {
    mounted.current = true;
    void load();
    return () => {
      mounted.current = false;
    };
  }, [load]);

  useEffect(
    () =>
      useConnectionStore.subscribe((state, previous) => {
        if (state.chat.status === 'OPEN' && previous.chat.status === 'RECONNECTING') {
          void load(true);
        }
      }),
    [load],
  );

  const online: Friend[] = friends.filter((friend) => friend.is_online);

  return { online, status, retry: useCallback(() => load(), [load]) };
}
