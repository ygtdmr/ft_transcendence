/**
 * Social events from the notification socket: friend requests sent and resolved, friendships
 * removed (or suspended by a block) and restored, and a user's new username or avatar. They are applied to the friends store,
 * so every list and button that reads it follows the change without a reload. Nothing is
 * replayed after a drop, so the lists are read again over REST after every reconnect.
 */

import { useSessionStore } from '@shared/stores';
import type { SocialEvent } from '@shared/types';

import { useFriendsStore } from '../store/friendsStore';

export function applySocialEvent(event: SocialEvent): void {
  const store = useFriendsStore.getState();
  const selfId = useSessionStore.getState().user?.user_id;
  switch (event.type) {
    case 'friend.request.received':
      if (selfId !== undefined) store.addRequest(event.payload.request, selfId);
      return;
    case 'friend.request.resolved':
      store.removeRequest(event.payload.request_id);
      if (event.payload.status === 'ACCEPTED' && event.payload.friend) {
        store.addFriend(event.payload.friend);
      }
      return;
    case 'friend.removed':
      store.removeFriend(event.payload.user_id);
      return;
    case 'friend.restored':
      store.addFriend(event.payload.friend);
      return;
    case 'user.profile.updated': {
      const { user_id: userId, username, avatar_url: avatarUrl } = event.payload;
      store.updateUser(userId, username, avatarUrl);
      return;
    }
  }
}
