/**
 * LOG IN / CREATE ACCOUNT flow.
 *
 * Both calls set the `ft_session` and `ft_refresh` cookies and return no token, so the
 * session is then read back from `GET /api/v1/auth/session`: that response is what the
 * session store holds, including the active room the account may already be in.
 *
 * `enter()` is the only place the store turns `AUTHENTICATED`, and it is called only
 * after the server has accepted the credentials and answered the session read.
 */

import { useCallback } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';

import { useSessionStore } from '@shared/stores';
import type { LoginRequest, RegisterRequest, SessionResponse } from '@shared/types';

import { fetchSession, login, registerAccount } from '../api';
import { returnPath } from '../model/returnPath';

export interface Authenticated {
  username: string;
  session: SessionResponse;
}

export function useAuthentication() {
  const navigate = useNavigate();
  const { state: locationState } = useLocation();

  const logIn = useCallback(async (credentials: LoginRequest): Promise<Authenticated> => {
    const { user } = await login(credentials);
    return { username: user.username, session: await fetchSession() };
  }, []);

  const register = useCallback(async (account: RegisterRequest): Promise<Authenticated> => {
    const { user } = await registerAccount(account);
    return { username: user.username, session: await fetchSession() };
  }, []);

  /**
   * Store the session and go on to the route the user asked for, or the Lobby. Route
   * recovery then sends a room member into their room.
   */
  const enter = useCallback(
    (session: SessionResponse) => {
      useSessionStore.getState().setSession(session);
      navigate(returnPath(locationState), { replace: true });
    },
    [locationState, navigate],
  );

  return { logIn, register, enter };
}
