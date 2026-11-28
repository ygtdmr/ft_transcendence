/**
 * Notifications WebSocket v2 types, from Bruno `v2/notification-websocket`.
 *
 * The notification socket is a per-user, server-push connection. It accepts no client
 * commands and carries the social and profile events below; keeping it open is what
 * makes the signed-in user count as online.
 */

import type { Friend, FriendRequest } from './user';

/** `notifications.ready`: the gateway accepted the session. Sent once per connection. */
export interface NotificationsReadyPayload {
  user_id: number;
  connected_at: string;
}

/* ------------------------------ social events ------------------------------- */

/** `friend.request.received`: a request sent to or by the signed-in user. */
export interface FriendRequestReceivedPayload {
  request: FriendRequest;
}

/** `friend.request.resolved`: a pending request was accepted, declined or cancelled. */
export interface FriendRequestResolvedPayload {
  request_id: number;
  from_user_id: number;
  to_user_id: number;
  status: 'ACCEPTED' | 'DECLINED' | 'CANCELLED';
  /** `ACCEPTED` only: the other user, as a friend list entry. */
  friend?: Friend;
}

/** `friend.removed`: the friendship with `user_id` ended or was suspended. */
export interface FriendRemovedPayload {
  user_id: number;
}

/** `friend.restored`: a suspended friendship is active again. */
export interface FriendRestoredPayload {
  friend: Friend;
}

/** `user.profile.updated`: the user's current username and avatar after a change. */
export interface UserProfileUpdatedPayload {
  user_id: number;
  username: string;
  avatar_url: string;
}
