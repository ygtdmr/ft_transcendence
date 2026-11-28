import { useCallback, useRef, useState } from 'react';

import { endSession } from '@features/auth/api';
import { ApiError } from '@shared/api';

import { endSignedInState } from './endSignedInState';

export type LogOutStatus = 'IDLE' | 'PENDING' | 'FAILED';

/**
 * Log Out, from Settings.
 *
 *   DELETE /api/v1/auth/session -> clear private state -> anonymous session -> Landing
 *
 * The server revokes both session cookies, closes this session's sockets and, during a
 * match the user plays in, counts it as leaving the match (a leave penalty); the account,
 * friends and statistics stay. Only once it
 * has answered does the client let go (`endSignedInState`). A session the server no longer
 * knows (`401`) is already logged out. Any other failure keeps the user signed in and says
 * so. One press sends one request.
 */
export function useLogOut() {
  const [status, setStatus] = useState<LogOutStatus>('IDLE');
  const busy = useRef(false);

  const logOut = useCallback(async () => {
    if (busy.current) return;
    busy.current = true;
    setStatus('PENDING');
    try {
      await endSession();
    } catch (cause) {
      if (!(cause instanceof ApiError && cause.status === 401)) {
        busy.current = false;
        setStatus('FAILED');
        return;
      }
    }
    endSignedInState();
  }, []);

  return { status, logOut };
}
