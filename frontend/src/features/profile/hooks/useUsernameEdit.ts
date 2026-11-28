/**
 * USERNAME in Settings: a draft that becomes the username only when the server says so.
 *
 *   draft -> (Save) -> check the rule -> PATCH /api/v1/users/me -> the server's profile
 *
 * The rule is checked here first so input that could never succeed is not sent, and the
 * server checks it again along with uniqueness, which only it can know. While the request
 * runs, Save does nothing more, so one click sends one request. A refusal keeps the draft
 * and says why under the field; the username shown elsewhere changes only with the
 * server's answer.
 */

import { useCallback, useRef, useState } from 'react';

import { ApiError } from '@shared/api';
import { USERNAME_PATTERN } from '@shared/types';
import type { OwnProfile } from '@shared/types';

import { updateOwnProfile } from '../api';

/** Why a username was not saved; each is a `settings.username.error.*` message. */
export type UsernameError = 'INVALID_USERNAME' | 'USERNAME_TAKEN' | 'FAILED';

export function useUsernameEdit(current: string, onSaved: (profile: OwnProfile) => void) {
  const [draft, setDraft] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<UsernameError | null>(null);
  const busy = useRef(false);

  // Until the user types, the field follows the current username, including a newer one.
  const value = draft ?? current;
  const changed = value.trim() !== current;

  const change = useCallback((next: string) => {
    setDraft(next);
    setError(null);
  }, []);

  const save = useCallback(async () => {
    if (busy.current) return;
    const username = value.trim();
    if (username === current) return;
    if (!USERNAME_PATTERN.test(username)) {
      setError('INVALID_USERNAME');
      return;
    }
    busy.current = true;
    setSaving(true);
    setError(null);
    try {
      const profile = await updateOwnProfile({ username });
      setDraft(null);
      onSaved(profile);
    } catch (cause) {
      const code = cause instanceof ApiError ? cause.code : null;
      setError(code === 'USERNAME_TAKEN' || code === 'INVALID_USERNAME' ? code : 'FAILED');
    } finally {
      busy.current = false;
      setSaving(false);
    }
  }, [current, onSaved, value]);

  return { value, changed, saving, error, change, save } as const;
}
