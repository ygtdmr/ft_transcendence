import { useEffect } from 'react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { endSignedInState } from '@app/session/endSignedInState';
import { fetchSession, refreshSession } from '@features/auth/api';
import { ApiError, setRefreshHandler } from '@shared/api';
import { useSessionStore } from '@shared/stores';
import { LoadingState } from '@shared/ui';

/**
 * Session bootstrap gate.
 *
 * Two jobs, both application-level rather than feature-level:
 *   1. install the silent-refresh handler on the shared REST client, so any `401`
 *      retries once after `POST /api/v1/auth/refresh` instead of logging the user out;
 *   2. read `GET /api/v1/auth/session` once before the router renders, so guards never
 *      redirect on a session that is only still `UNKNOWN`.
 *
 * A refresh that fails (`401 SESSION_EXPIRED`) while the user is signed in ends the
 * signed-in state as expired: private state is cleared and the route guard sends the user
 * to Log In, which explains why. During bootstrap the same failure is simply the anonymous
 * state, since there was no session to lose.
 *
 * `active_room_id` from that response is the authoritative route-recovery input — the
 * screen after a refresh is derived from server membership, never from the reloaded URL
 * (`RoomRouteGuard`, `LobbyRouteGuard`). It is paired with `active_room_api_version`: this
 * client restores only a Game v2 room, and explains a room it cannot open.
 */
export function SessionBootstrap({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const status = useSessionStore((state) => state.status);

  useEffect(() => {
    let cancelled = false;

    setRefreshHandler(async () => {
      try {
        await refreshSession();
        return true;
      } catch {
        // SESSION_EXPIRED: the refresh credential itself is dead — log in again.
        if (useSessionStore.getState().status === 'AUTHENTICATED') endSignedInState('EXPIRED');
        else useSessionStore.getState().setAnonymous();
        return false;
      }
    });

    void (async () => {
      try {
        const session = await fetchSession();
        if (!cancelled) useSessionStore.getState().setSession(session);
      } catch (error) {
        // A 401 here simply means "no usable session", which is the anonymous state.
        if (!cancelled && error instanceof ApiError) {
          useSessionStore.getState().setAnonymous();
        }
      }
    })();

    return () => {
      cancelled = true;
      setRefreshHandler(null);
    };
  }, []);

  if (status === 'UNKNOWN') {
    return <LoadingState label={t('common.loading')} className="min-h-screen" />;
  }

  return <>{children}</>;
}
