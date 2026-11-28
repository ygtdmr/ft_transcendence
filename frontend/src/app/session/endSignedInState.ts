import { useFriendsStore } from '@features/friends/store/friendsStore';
import { useGameStore } from '@features/game/store/gameStore';
import { useOwnProfileStore } from '@features/profile/store/ownProfileStore';
import { useRoomStore } from '@features/room/store/roomStore';
import { useConnectionStore, useModalStore, useSessionStore } from '@shared/stores';

/** Why the signed-in state ends: the user logged out, deleted the account, or lost the session. */
export type SessionEnd = 'LOGGED_OUT' | 'ACCOUNT_DELETED' | 'EXPIRED';

/**
 * Lets go of the signed-in user once the session is over: every store holding the user's
 * private data is cleared, any open modal closes, and the session becomes anonymous.
 * Leaving the private routes closes the room and notification sockets, and the room route lets
 * the user go without its leave prompt, since the membership is no longer this client's
 * to give up.
 *
 * Log Out and account deletion are deliberate exits, so the route guard takes the user to
 * Landing. An expired session (the server would not renew it) takes them to Log In, which
 * says why and returns them to where they were once they sign in again.
 *
 * It lives at app level because it clears the stores of several features.
 */
export function endSignedInState(end: SessionEnd = 'LOGGED_OUT') {
  const session = useSessionStore.getState();
  useModalStore.getState().close();
  useRoomStore.getState().clear();
  useGameStore.getState().clear();
  useFriendsStore.getState().clear();
  useOwnProfileStore.getState().clear();
  useConnectionStore.getState().setRoomLost(false);
  if (end === 'EXPIRED') session.setAnonymous({ sessionExpired: true });
  else session.setAnonymous({ loggedOut: true, accountDeleted: end === 'ACCOUNT_DELETED' });
}
