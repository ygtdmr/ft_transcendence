/**
 * Friends store — the signed-in user's persistent social state, read over REST and kept
 * current by the social events of the notification socket.
 *
 * It holds the friend list, the pending friend requests both ways, and what this session
 * knows about the user's own blocks. Every list is the server's answer or an event the
 * server sent; nothing here decides a relationship. `is_online` is live presence
 * maintained by the backend; the frontend displays it and never infers it from its own
 * socket state.
 */

import { create } from 'zustand';

import type { Friend, FriendRequest, Relationship } from '@shared/types';

interface FriendsState {
  friends: Friend[];
  friendCount: number;
  /** The list has been read from the server at least once this session. */
  loaded: boolean;
  /** Pending requests sent to the signed-in user. */
  incoming: FriendRequest[];
  /** Pending requests the signed-in user sent. */
  outgoing: FriendRequest[];
  /** The request lists have been read from the server at least once this session. */
  requestsLoaded: boolean;
  /**
   * The user's own blocks this session has seen: `true` blocked, `false` not blocked. The
   * contract has no block list; what is unknown is absent.
   */
  blocks: Record<number, boolean>;

  setFriends: (friends: Friend[], friendCount: number) => void;
  /** Record a friendship the server just confirmed or restored. */
  addFriend: (friend: Friend) => void;
  /** Drop a friendship the server just ended, suspended, or reported as absent. */
  removeFriend: (userId: number) => void;
  setRequests: (incoming: FriendRequest[], outgoing: FriendRequest[]) => void;
  /** File a new pending request under incoming or outgoing, by who sent it. */
  addRequest: (request: FriendRequest, selfId: number) => void;
  /** A request is no longer pending: accepted, declined or cancelled. */
  removeRequest: (requestId: number) => void;
  setBlocked: (userId: number, blocked: boolean) => void;
  /** Show a user's new username and avatar in the friend list and the requests. */
  updateUser: (userId: number, username: string, avatarUrl: string) => void;
  clear: () => void;
}

const initial = {
  friends: [],
  friendCount: 0,
  loaded: false,
  incoming: [],
  outgoing: [],
  requestsLoaded: false,
  blocks: {},
};

export const useFriendsStore = create<FriendsState>((set) => ({
  ...initial,

  setFriends: (friends, friendCount) => set({ friends, friendCount, loaded: true }),
  addFriend: (friend) =>
    set((state) => {
      if (state.friends.some((f) => f.user_id === friend.user_id)) return state;
      return { friends: [...state.friends, friend], friendCount: state.friendCount + 1 };
    }),
  removeFriend: (userId) =>
    set((state) => {
      if (!state.friends.some((f) => f.user_id === userId)) return state;
      return {
        friends: state.friends.filter((f) => f.user_id !== userId),
        friendCount: Math.max(0, state.friendCount - 1),
      };
    }),
  setRequests: (incoming, outgoing) => set({ incoming, outgoing, requestsLoaded: true }),
  addRequest: (request, selfId) =>
    set((state) => {
      const known = [...state.incoming, ...state.outgoing];
      if (known.some((r) => r.request_id === request.request_id)) return state;
      return request.from_user.user_id === selfId
        ? { outgoing: [request, ...state.outgoing] }
        : { incoming: [request, ...state.incoming] };
    }),
  removeRequest: (requestId) =>
    set((state) => ({
      incoming: state.incoming.filter((r) => r.request_id !== requestId),
      outgoing: state.outgoing.filter((r) => r.request_id !== requestId),
    })),
  setBlocked: (userId, blocked) =>
    set((state) => ({ blocks: { ...state.blocks, [userId]: blocked } })),
  updateUser: (userId, username, avatarUrl) =>
    set((state) => {
      const user = <T extends { user_id: number }>(u: T): T =>
        u.user_id === userId ? { ...u, username, avatar_url: avatarUrl } : u;
      const request = (r: FriendRequest): FriendRequest => ({
        ...r,
        from_user: user(r.from_user),
        to_user: user(r.to_user),
      });
      return {
        friends: state.friends.map(user),
        incoming: state.incoming.map(request),
        outgoing: state.outgoing.map(request),
      };
    }),
  clear: () => set(initial),
}));

/** The pending request between the signed-in user and `userId`, either way. */
export function pendingRequestWith(
  state: Pick<FriendsState, 'incoming' | 'outgoing'>,
  userId: number,
): { request: FriendRequest; direction: 'SENT' | 'RECEIVED' } | null {
  const sent = state.outgoing.find((r) => r.to_user.user_id === userId);
  if (sent) return { request: sent, direction: 'SENT' };
  const received = state.incoming.find((r) => r.from_user.user_id === userId);
  return received ? { request: received, direction: 'RECEIVED' } : null;
}

/**
 * The relationship to `userId` from what this session knows, falling back to the server's
 * answer (`reported`, from a profile or a search result) for what it cannot know: a block
 * this session has not seen, or lists not read yet. The store is newer than any one
 * answer, since every change the server makes reaches it as an event.
 */
export function relationshipOf(
  state: Pick<
    FriendsState,
    'friends' | 'loaded' | 'incoming' | 'outgoing' | 'requestsLoaded' | 'blocks'
  >,
  userId: number,
  reported: Relationship | null,
): Relationship | null {
  const blocked = state.blocks[userId];
  if (blocked === true) return 'BLOCKED';
  if (state.friends.some((f) => f.user_id === userId)) return 'FRIENDS';
  const pending = pendingRequestWith(state, userId);
  if (pending) return pending.direction === 'SENT' ? 'REQUEST_SENT' : 'REQUEST_RECEIVED';
  if (reported === 'BLOCKED' && blocked === undefined) return 'BLOCKED';
  if (state.loaded && state.requestsLoaded) return 'NONE';
  return reported;
}
