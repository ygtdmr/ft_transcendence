/**
 * The social actions on other users — send a friend request or cancel it, accept or
 * decline one,
 * remove a friend, block and unblock — with their in-flight state and failures per user.
 *
 * Each answer updates the shared friends store, so every list and profile that reads it
 * agrees at once; the social events the other user's side causes arrive on the notification
 * socket. An answer that only says this client was out of date (the request was already
 * sent, already resolved, the friendship already gone) reads the lists again instead of
 * reporting an error. Each user has at most one action in flight; a second press is
 * ignored until it settles.
 */

import { useCallback, useEffect, useRef, useState } from 'react';

import { ApiError } from '@shared/api';

import {
  acceptFriendRequest,
  blockUser,
  cancelFriendRequest,
  declineFriendRequest,
  listAllFriends,
  removeFriend,
  sendFriendRequest,
  unblockUser,
} from '../api';
import { useFriendsStore } from '../store/friendsStore';
import { refreshFriendRequests } from './useFriendRequests';

export type SocialAction =
  'REQUEST' | 'CANCEL' | 'ACCEPT' | 'DECLINE' | 'REMOVE' | 'BLOCK' | 'UNBLOCK';

const codeOf = (cause: unknown): string | null => (cause instanceof ApiError ? cause.code : null);

async function refreshFriends(): Promise<void> {
  const { friends, friendCount } = await listAllFriends();
  useFriendsStore.getState().setFriends(friends, friendCount);
}

/** Read both lists again; a failed read leaves them as they were until the next one. */
const resync = () => Promise.allSettled([refreshFriends(), refreshFriendRequests()]);

/** The translation key for a refusal that is news to the user, or `null` for a stale list. */
function failureFor(action: SocialAction, code: string | null): string | null {
  switch (code) {
    case 'RELATIONSHIP_BLOCKED':
      return action === 'ACCEPT' ? 'friends.errors.cannotAccept' : 'friends.errors.blocked';
    case 'USER_NOT_FOUND':
      return 'friends.errors.notFound';
    case 'ALREADY_FRIENDS':
    case 'REQUEST_ALREADY_SENT':
    case 'REQUEST_ALREADY_RECEIVED':
    case 'NOT_FRIENDS':
      return null;
    case 'REQUEST_NOT_FOUND':
      return 'friends.errors.requestGone';
    default:
      return `friends.errors.${action}`;
  }
}

export function useSocialActions() {
  const [busy, setBusy] = useState<ReadonlyMap<number, SocialAction>>(new Map());
  const [failed, setFailed] = useState<ReadonlyMap<number, string>>(new Map());
  const inFlight = useRef(new Map<number, SocialAction>());
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const run = useCallback(
    async (userId: number, action: SocialAction, perform: () => Promise<void>) => {
      if (inFlight.current.has(userId)) return;
      inFlight.current.set(userId, action);
      setBusy(new Map(inFlight.current));
      setFailed((current) => {
        const next = new Map(current);
        next.delete(userId);
        return next;
      });
      try {
        await perform();
      } catch (cause) {
        const failure = failureFor(action, codeOf(cause));
        if (failure === null || failure === 'friends.errors.requestGone') await resync();
        if (failure && mounted.current)
          setFailed((current) => new Map(current).set(userId, failure));
      } finally {
        inFlight.current.delete(userId);
        if (mounted.current) setBusy(new Map(inFlight.current));
      }
    },
    [],
  );

  const store = () => useFriendsStore.getState();

  return {
    /** The action in flight per user. */
    busy,
    /** Translation key of the last refusal per user. */
    failed,
    sendRequest: useCallback(
      (userId: number) =>
        run(userId, 'REQUEST', async () => {
          const request = await sendFriendRequest({ user_id: userId });
          store().addRequest(request, request.from_user.user_id);
        }),
      [run],
    ),
    accept: useCallback(
      (userId: number, requestId: number) =>
        run(userId, 'ACCEPT', async () => {
          const friend = await acceptFriendRequest(requestId);
          store().removeRequest(requestId);
          store().addFriend(friend);
        }),
      [run],
    ),
    /** Withdraw the user's own pending request; no friendship follows. */
    cancel: useCallback(
      (userId: number, requestId: number) =>
        run(userId, 'CANCEL', async () => {
          await cancelFriendRequest(requestId);
          store().removeRequest(requestId);
        }),
      [run],
    ),
    decline: useCallback(
      (userId: number, requestId: number) =>
        run(userId, 'DECLINE', async () => {
          await declineFriendRequest(requestId);
          store().removeRequest(requestId);
        }),
      [run],
    ),
    remove: useCallback(
      (userId: number) =>
        run(userId, 'REMOVE', async () => {
          try {
            await removeFriend(userId);
          } finally {
            // Removed now, or already gone: either way it is not a friendship any more.
            store().removeFriend(userId);
          }
        }),
      [run],
    ),
    block: useCallback(
      (userId: number) =>
        run(userId, 'BLOCK', async () => {
          await blockUser(userId);
          const { incoming, outgoing } = store();
          // The server cancelled every request between the two and suspended the friendship.
          [...incoming, ...outgoing]
            .filter((r) => r.from_user.user_id === userId || r.to_user.user_id === userId)
            .forEach((r) => store().removeRequest(r.request_id));
          store().removeFriend(userId);
          store().setBlocked(userId, true);
        }),
      [run],
    ),
    unblock: useCallback(
      (userId: number) =>
        run(userId, 'UNBLOCK', async () => {
          await unblockUser(userId);
          store().setBlocked(userId, false);
          // A friendship the block suspended may be active again.
          await refreshFriends().catch(() => undefined);
        }),
      [run],
    ),
  };
}

export type SocialActions = ReturnType<typeof useSocialActions>;
