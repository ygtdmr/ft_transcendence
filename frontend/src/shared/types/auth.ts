/**
 * Identity and session types, from Bruno `rest-api/01 - Identity/`.
 *
 * Both session credentials (`ft_session`, `ft_refresh`) are HttpOnly cookies. No token
 * value ever appears in a response body, so no token type exists here on purpose.
 */

/** `POST /api/v1/auth/register` request body. */
export interface RegisterRequest {
  email: string;
  username: string;
  password: string;
}

/** `POST /api/v1/auth/login` request body. */
export interface LoginRequest {
  email: string;
  password: string;
}

/** The `data.user` object returned by Register. */
export interface RegisteredUser {
  user_id: number;
  username: string;
  email: string;
  created_at: string;
}

/** The `data.user` object returned by Login (no `created_at`). */
export interface AuthenticatedUser {
  user_id: number;
  username: string;
  email: string;
}

export interface RegisterResponse {
  user: RegisteredUser;
  /** `ft_session` expiry, for client-side UX timers only. */
  access_token_expires_at: string;
}

export interface LoginResponse {
  user: AuthenticatedUser;
  access_token_expires_at: string;
}

/** `POST /api/v1/auth/refresh` — reads the `ft_refresh` cookie, reissues both cookies. */
export interface RefreshResponse {
  access_token_expires_at: string;
}

/** The room contract versions a membership can belong to. */
export type RoomApiVersion = 'v1' | 'v2';

/** `GET /api/v1/auth/session` — the route-recovery bootstrap call. */
export interface SessionResponse {
  user: {
    user_id: number;
    username: string;
  };
  /** Derived live from Game Session's membership index; `null` when not in a room. */
  active_room_id: number | null;
  /**
   * The room contract the active room speaks, so recovery pairs the right REST and socket
   * versions. `null` exactly when `active_room_id` is `null`.
   */
  active_room_api_version: RoomApiVersion | null;
  session_expires_at: string;
}
