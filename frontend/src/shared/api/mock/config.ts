/**
 * Central controls for the mock REST API.
 *
 * Change the defaults here, or change them at runtime from the browser console through
 * `mockApi.config`:
 *
 *   mockApi.config.latencyMs = 800
 *   mockApi.config.outcomes.createRoom = 'conflict'
 *   mockApi.config.outcomes.login = 'server-error'
 *   mockApi.reset()                      // back to the seed data and these defaults
 */

/**
 * Forced result of one endpoint. Every error outcome answers with the error the Bruno
 * contract documents for that endpoint; an outcome the endpoint does not document is
 * rejected with a console warning instead of being invented.
 */
export type MockOutcome =
  | 'success'
  | 'validation-error'
  | 'unauthorized'
  | 'forbidden'
  | 'not-found'
  | 'conflict'
  | 'rate-limited'
  | 'server-error'
  | 'network-error';

/** One key per REST binding in `features/*\/api`. */
export type MockEndpoint =
  | 'health'
  | 'register'
  | 'login'
  | 'refresh'
  | 'session'
  | 'endSession'
  | 'getOwnProfile'
  | 'updateOwnProfile'
  | 'deleteOwnAccount'
  | 'reportUser'
  | 'getPublicProfile'
  | 'uploadAvatar'
  | 'listAvatarPresets'
  | 'selectAvatarPreset'
  | 'matchHistory'
  | 'listFriends'
  | 'sendFriendRequest'
  | 'listFriendRequests'
  | 'acceptFriendRequest'
  | 'declineFriendRequest'
  | 'cancelFriendRequest'
  | 'removeFriend'
  | 'blockUser'
  | 'unblockUser'
  | 'searchUsers'
  | 'createRoom'
  | 'lookupRoom'
  | 'joinRoom'
  | 'getRoom'
  | 'leaveRoom';

/**
 * - `logged-in`: `player_one` has a live session.
 * - `logged-out`: no session; Login/Register work with the seeded accounts.
 * - `in-room`: logged in and already a member of the waiting room `QWER12`, which
 *   exercises route recovery from `active_room_id`.
 * - `session-expired`: the access cookie is dead but refresh works once.
 */
export type MockAuthState = 'logged-in' | 'logged-out' | 'in-room' | 'session-expired';

export interface MockApiConfig {
  /** Delay added to every response, in milliseconds. */
  latencyMs: number;
  auth: MockAuthState;
  outcomes: Partial<Record<MockEndpoint, MockOutcome>>;
}

const AUTH_STATES: readonly MockAuthState[] = [
  'logged-in',
  'logged-out',
  'in-room',
  'session-expired',
];

function initialAuth(): MockAuthState {
  const value = import.meta.env.VITE_MOCK_AUTH;
  return AUTH_STATES.find((state) => state === value) ?? 'logged-in';
}

export function defaultMockApiConfig(): MockApiConfig {
  return {
    latencyMs: 250,
    auth: initialAuth(),
    outcomes: {},
  };
}
