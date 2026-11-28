/**
 * The signed-in user's relationship to one other user, for a profile: the relationship
 * itself, the pending request when there is one, and the actions it offers.
 *
 * The server's answer (`GET /api/v1/users/{user_id}`), the friend list and the pending
 * requests are read fresh when the hook mounts and again after a reconnect. Until they
 * land the status is `LOADING`, so a profile never offers an action from a stale state.
 * After that the friends store, kept current by the server's answers and social events,
 * says what the relationship is now.
 */

import { useCallback, useEffect, useRef, useState } from 'react';

import { useOnReconnect } from '@shared/hooks';
import type { PublicProfile } from '@shared/types';

import { getRelationship, listAllFriends } from '../api';
import { pendingRequestWith, relationshipOf, useFriendsStore } from '../store/friendsStore';
import { refreshFriendRequests } from './useFriendRequests';
import { useSocialActions } from './useSocialActions';

export type RelationshipStatus = 'LOADING' | 'READY' | 'ERROR';

export function useRelationship(userId: number) {
  const [reported, setReported] =
    useState<Pick<PublicProfile, 'relationship' | 'friend_request_id'>>();
  const [status, setStatus] = useState<RelationshipStatus>('LOADING');
  const relationship = useFriendsStore((state) =>
    relationshipOf(state, userId, reported?.relationship ?? null),
  );
  const pendingId = useFriendsStore(
    (state) => pendingRequestWith(state, userId)?.request.request_id ?? null,
  );
  const actions = useSocialActions();
  const mounted = useRef(true);

  const load = useCallback(
    async (quiet = false) => {
      if (!quiet) setStatus('LOADING');
      try {
        const [answer, list] = await Promise.all([
          getRelationship(userId),
          listAllFriends(),
          refreshFriendRequests(),
        ]);
        if (!mounted.current) return;
        useFriendsStore.getState().setFriends(list.friends, list.friendCount);
        // What the server says about the user's own block replaces what this session knew.
        useFriendsStore.getState().setBlocked(userId, answer.relationship === 'BLOCKED');
        setReported(answer);
        setStatus('READY');
      } catch {
        if (mounted.current && !quiet) setStatus('ERROR');
      }
    },
    [userId],
  );

  useEffect(() => {
    mounted.current = true;
    void load();
    return () => {
      mounted.current = false;
    };
  }, [load]);

  useOnReconnect(() => void load(true));

  const requestId = pendingId ?? reported?.friend_request_id ?? null;

  return {
    status,
    /** Meaningful only once `status` is `READY`. */
    relationship: status === 'READY' ? relationship : null,
    /** The action in flight, if any; another one is ignored until it settles. */
    change: actions.busy.get(userId) ?? null,
    /** Translation key of the last refusal. */
    failure: actions.failed.get(userId) ?? null,
    sendRequest: useCallback(() => void actions.sendRequest(userId), [actions, userId]),
    accept: useCallback(() => {
      if (requestId !== null) void actions.accept(userId, requestId);
    }, [actions, requestId, userId]),
    decline: useCallback(() => {
      if (requestId !== null) void actions.decline(userId, requestId);
    }, [actions, requestId, userId]),
    cancel: useCallback(() => {
      if (requestId !== null) void actions.cancel(userId, requestId);
    }, [actions, requestId, userId]),
    remove: useCallback(() => void actions.remove(userId), [actions, userId]),
    block: useCallback(() => void actions.block(userId), [actions, userId]),
    unblock: useCallback(() => void actions.unblock(userId), [actions, userId]),
    retry: useCallback(() => void load(), [load]),
  };
}
