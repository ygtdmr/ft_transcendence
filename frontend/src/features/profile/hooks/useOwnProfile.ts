/**
 * The signed-in user's own profile, from the shared own-profile store.
 *
 * The first view that asks reads `GET /api/v1/users/me`; every other view shares that read
 * and every later change Settings makes. Until the profile arrives, or if it fails, the
 * session's username is still known, so the profile menu never goes blank.
 */

import { useCallback, useEffect } from 'react';
import { useShallow } from 'zustand/react/shallow';

import { useSessionStore } from '@shared/stores';

import { useOwnProfileStore } from '../store/ownProfileStore';

export function useOwnProfile() {
  const userId = useSessionStore((state) => state.user?.user_id);
  const { ownerId, profile, status, load } = useOwnProfileStore(
    useShallow((state) => ({
      ownerId: state.ownerId,
      profile: state.profile,
      status: state.status,
      load: state.load,
    })),
  );

  const current = userId !== undefined && ownerId === userId;

  useEffect(() => {
    if (userId !== undefined && (!current || status === 'IDLE')) void load(userId);
  }, [current, load, status, userId]);

  /** Read it again without the loading state, when what is shown may be stale. */
  const refresh = useCallback(() => {
    if (userId !== undefined) void load(userId, true);
  }, [load, userId]);

  return {
    profile: current ? profile : null,
    status: current && status !== 'IDLE' ? status : 'LOADING',
    refresh,
  } as const;
}
