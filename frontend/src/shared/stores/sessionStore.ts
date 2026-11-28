/**
 * Session/auth state — the only store that knows who the user is.
 *
 * It holds no credential: both session cookies are HttpOnly and unreadable from JS.
 * `activeRoomId` mirrors `GET /api/v1/auth/session`, which derives it live from room
 * membership, and is what route recovery on refresh reads instead of trusting the URL.
 * This client speaks only the Game v2 room contract, so a membership the session pairs
 * with the v1 contract is not one it can open (`isOpenableRoom`).
 *
 * Actions here are state transitions only. The REST calls that produce them belong to
 * `features/auth/api`.
 */

import { create } from 'zustand';

import type { SessionResponse } from '@shared/types';

import { showToast } from './toastStore';

/**
 * Whether this client can open the session's active room. The REST and socket contracts
 * of one room are always used as a pair, and this client pairs only Game v2.
 */
export const isOpenableRoom = (session: SessionResponse): boolean =>
  session.active_room_id !== null && session.active_room_api_version !== 'v1';

/** `UNKNOWN` until bootstrap finishes — route guards must not redirect before then. */
export type SessionStatus = 'UNKNOWN' | 'AUTHENTICATED' | 'ANONYMOUS';

export interface SessionUser {
  user_id: number;
  username: string;
}

interface SessionState {
  status: SessionStatus;
  user: SessionUser | null;
  activeRoomId: number | null;
  sessionExpiresAt: string | null;
  /**
   * The user ended the session themselves with Log Out, rather than losing it. The route
   * guard then takes them to Landing instead of asking them to log in again.
   */
  loggedOut: boolean;
  /** The session ended because the user deleted their own account; Landing says so once. */
  accountDeleted: boolean;
  /**
   * A signed-in session was lost because the server would no longer renew it (a failed
   * refresh). Log In says so once, so the user knows why they were signed out.
   */
  sessionExpired: boolean;

  setSession: (session: SessionResponse) => void;
  setAnonymous: (options?: {
    loggedOut?: boolean;
    accountDeleted?: boolean;
    sessionExpired?: boolean;
  }) => void;
  /** Landing has shown the account-deleted notice. */
  acknowledgeAccountDeleted: () => void;
  /** Log In has shown the session-expired notice. */
  acknowledgeSessionExpired: () => void;
  setActiveRoomId: (roomId: number | null) => void;
  /** The username the server confirmed after the user renamed themselves. */
  setUsername: (username: string) => void;
}

export const useSessionStore = create<SessionState>((set) => ({
  status: 'UNKNOWN',
  user: null,
  activeRoomId: null,
  sessionExpiresAt: null,
  loggedOut: false,
  accountDeleted: false,
  sessionExpired: false,

  setSession: (session) => {
    // A room on the older room contract cannot be opened here; say so when it is found.
    if (session.active_room_id !== null && !isOpenableRoom(session)) {
      showToast({ message: 'room.recovery.legacy', tone: 'error', icon: 'warning' });
    }
    set({
      status: 'AUTHENTICATED',
      user: session.user,
      activeRoomId: isOpenableRoom(session) ? session.active_room_id : null,
      sessionExpiresAt: session.session_expires_at,
      loggedOut: false,
      accountDeleted: false,
      sessionExpired: false,
    });
  },

  setAnonymous: ({ loggedOut = false, accountDeleted = false, sessionExpired = false } = {}) =>
    set({
      status: 'ANONYMOUS',
      user: null,
      activeRoomId: null,
      sessionExpiresAt: null,
      loggedOut: loggedOut || accountDeleted,
      accountDeleted,
      sessionExpired,
    }),

  acknowledgeAccountDeleted: () => set({ accountDeleted: false }),
  acknowledgeSessionExpired: () => set({ sessionExpired: false }),

  setActiveRoomId: (activeRoomId) => set({ activeRoomId }),

  setUsername: (username) =>
    set((state) => (state.user ? { user: { ...state.user, username } } : state)),
}));
