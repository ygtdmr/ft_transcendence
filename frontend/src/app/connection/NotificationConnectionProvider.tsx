import { useEffect } from 'react';

import { applySocialEvent } from '@features/friends/service/socialEvents';
import { setPlayerAvatar } from '@features/profile/hooks/usePlayerAvatars';
import { useOwnProfileStore } from '@features/profile/store/ownProfileStore';
import { useConnectionStore, useSessionStore } from '@shared/stores';
import type { SocialEvent } from '@shared/types';
import { NotificationConnection } from '@shared/websocket';

/**
 * Friend and request changes go to the friends feature. A user's new username or avatar
 * also goes to every other place that shows them: the avatars of room members, and, when
 * it is the signed-in user's own change made in another tab, their own profile and session.
 */
function applySocial(event: SocialEvent): void {
  applySocialEvent(event);
  if (event.type !== 'user.profile.updated') return;
  const { user_id: userId, username, avatar_url: avatarUrl } = event.payload;
  setPlayerAvatar(userId, avatarUrl);
  if (userId === useSessionStore.getState().user?.user_id) {
    useSessionStore.getState().setUsername(username);
    const own = useOwnProfileStore.getState();
    own.setUsername(username);
    own.setAvatar(avatarUrl);
  }
}

/**
 * Owner of the per-user notification socket (`/ws/v2/notifications`).
 *
 * Separate from the room socket by contract, and open for every signed-in screen, so the
 * user counts as online in the Lobby as well as in a room. Social events go to the friends
 * feature and to every view of the user they change. After a reconnect, the views that
 * use `useOnReconnect` read the friend list and requests again, since nothing is replayed.
 */
export function NotificationConnectionProvider() {
  const status = useSessionStore((state) => state.status);
  const userId = useSessionStore((state) => state.user?.user_id);
  const setNotificationStatus = useConnectionStore((state) => state.setNotificationStatus);
  const signedIn = status === 'AUTHENTICATED' && userId !== undefined;

  useEffect(() => {
    if (!signedIn) return undefined;
    const connection = new NotificationConnection({
      onStatusChange: setNotificationStatus,
      onSocial: applySocial,
    });
    connection.connect();
    return () => {
      connection.disconnect();
      setNotificationStatus('IDLE');
    };
  }, [setNotificationStatus, signedIn, userId]);

  return null;
}
