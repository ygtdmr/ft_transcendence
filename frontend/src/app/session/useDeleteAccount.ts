import { useCallback, useRef, useState } from 'react';

import { deleteOwnAccount } from '@features/profile/api';
import { ApiError } from '@shared/api';

import { endSignedInState } from './endSignedInState';

export type DeleteAccountStatus = 'IDLE' | 'PENDING' | 'FAILED';

/** Why the last attempt failed, as the confirmation dialog explains it. */
export type DeleteAccountFailure = 'MISMATCH' | 'RATE_LIMITED' | 'FAILED';

/**
 * Delete Account, from Settings.
 *
 *   DELETE /api/v1/users/me { confirm_username } -> clear private state -> Landing
 *
 * The server checks the typed username, removes the user from any room, deletes the
 * account, revokes every session and closes every socket. Only once it has answered
 * `204` does the client let go of the user, with Landing told the account is gone. Any
 * failure leaves the account and the session as they were and says why. One press sends
 * one request.
 */
export function useDeleteAccount() {
  const [status, setStatus] = useState<DeleteAccountStatus>('IDLE');
  const [failure, setFailure] = useState<DeleteAccountFailure | null>(null);
  const busy = useRef(false);

  const deleteAccount = useCallback(async (confirmUsername: string) => {
    if (busy.current) return;
    busy.current = true;
    setStatus('PENDING');
    setFailure(null);
    try {
      await deleteOwnAccount({ confirm_username: confirmUsername.trim() });
    } catch (cause) {
      busy.current = false;
      setStatus('FAILED');
      const code = cause instanceof ApiError ? cause.code : null;
      setFailure(
        code === 'CONFIRMATION_MISMATCH'
          ? 'MISMATCH'
          : code === 'RATE_LIMITED'
            ? 'RATE_LIMITED'
            : 'FAILED',
      );
      return;
    }
    endSignedInState('ACCOUNT_DELETED');
  }, []);

  const reset = useCallback(() => {
    if (busy.current) return;
    setStatus('IDLE');
    setFailure(null);
  }, []);

  return { status, failure, deleteAccount, reset };
}
