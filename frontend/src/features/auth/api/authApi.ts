/**
 * Auth REST bindings — Bruno `rest-api/01 - Identity/`.
 *
 * These are contract bindings, nothing more: no flow, no redirect, no state write.
 * The authentication flow (form handling, session store writes, guard behavior) is
 * feature work that builds on top of them.
 *
 * All five calls authenticate by cookie. Register/Login/Refresh set `ft_session` and
 * `ft_refresh`; nothing here reads, stores or forwards a token, because no token value
 * is ever present in a response body.
 */

import { apiRequest } from '@shared/api';
import { API_BASE_PATH, CORE_API_PATH } from '@shared/constants';
import type {
  LoginRequest,
  LoginResponse,
  RefreshResponse,
  RegisterRequest,
  RegisterResponse,
  SessionResponse,
} from '@shared/types';

export const registerAccount = (body: RegisterRequest): Promise<RegisterResponse> =>
  apiRequest(`${CORE_API_PATH}/auth/register`, { method: 'POST', body, skipRefresh: true });

export const login = (body: LoginRequest): Promise<LoginResponse> =>
  apiRequest(`${CORE_API_PATH}/auth/login`, { method: 'POST', body, skipRefresh: true });

/** Rotates `ft_refresh` and reissues both cookies. Never retried through the refresh hook. */
export const refreshSession = (): Promise<RefreshResponse> =>
  apiRequest(`${CORE_API_PATH}/auth/refresh`, { method: 'POST', skipRefresh: true });

/**
 * The route-recovery bootstrap read: `active_room_id` here is authoritative, not the URL.
 *
 * Refresh is deliberately left enabled — a reload with an expired `ft_session` but a
 * live `ft_refresh` must recover the session rather than drop the user to Login.
 */
export const fetchSession = (): Promise<SessionResponse> =>
  apiRequest(`${CORE_API_PATH}/auth/session`);

/**
 * Where "Continue with Google" sends the browser. It is a top-level navigation, not a
 * `fetch`: the server answers with a redirect to Google, and Google's callback sets the
 * session cookies and redirects back to `returnTo`. A failure comes back to Landing as
 * `?oauth_error=<code>`. The frontend never holds a Google token or client secret.
 */
export const googleSignInUrl = (returnTo: string): string =>
  `${API_BASE_PATH}${CORE_API_PATH}/auth/oauth/google/authorize?${new URLSearchParams({ return_to: returnTo })}`;

/**
 * Revokes both credentials. Legal in every room status; during a running match it counts
 * as the player leaving it.
 */
export const endSession = (): Promise<void> =>
  apiRequest(`${CORE_API_PATH}/auth/session`, { method: 'DELETE' });
