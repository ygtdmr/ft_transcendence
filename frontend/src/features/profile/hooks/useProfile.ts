/**
 * The profile the Profile pop-up shows: the signed-in user's own (`GET /api/v1/users/me`) or
 * another player's public one (`GET /api/v1/users/{user_id}`).
 *
 * The two payloads differ: only the public one carries `is_online`, and only the own one
 * carries `active_room_id`. The pop-up reads one shape, `ProfileView`, where the own
 * profile counts as online because the user is looking at it through a live session.
 *
 * Each user is read fresh when the pop-up opens it; nothing from another user can show
 * while it loads, because the pop-up is mounted once per user. A reconnect reads it again
 * quietly, so presence and stats that changed while the connection was down catch up.
 */

import { useCallback, useEffect, useRef, useState } from 'react';

import type { ApiError } from '@shared/api';
import { useOnReconnect } from '@shared/hooks';

import { getOwnProfile, getPublicProfile } from '../api';

export interface ProfileView {
  user_id: number;
  username: string;
  avatar_url: string;
  level: number;
  wins: number;
  losses: number;
  matches_played: number;
  is_online: boolean;
}

export type ProfileStatus = 'LOADING' | 'READY' | 'NOT_FOUND' | 'ERROR';

async function fetchProfile(userId: number, isSelf: boolean): Promise<ProfileView> {
  if (!isSelf) return getPublicProfile(userId);
  const own = await getOwnProfile();
  return {
    user_id: own.user_id,
    username: own.username,
    avatar_url: own.avatar_url,
    level: own.level,
    wins: own.wins,
    losses: own.losses,
    matches_played: own.matches_played,
    is_online: true,
  };
}

export function useProfile(userId: number, isSelf: boolean) {
  const [profile, setProfile] = useState<ProfileView | null>(null);
  const [status, setStatus] = useState<ProfileStatus>('LOADING');
  const mounted = useRef(true);

  const load = useCallback(
    async (quiet = false) => {
      if (!quiet) setStatus('LOADING');
      try {
        const next = await fetchProfile(userId, isSelf);
        if (!mounted.current) return;
        setProfile(next);
        setStatus('READY');
      } catch (cause) {
        if (!mounted.current) return;
        if ((cause as ApiError).code === 'USER_NOT_FOUND') {
          setProfile(null);
          setStatus('NOT_FOUND');
        } else if (!quiet) {
          setStatus('ERROR');
        }
      }
    },
    [isSelf, userId],
  );

  useEffect(() => {
    mounted.current = true;
    void load();
    return () => {
      mounted.current = false;
    };
  }, [load]);

  useOnReconnect(() => void load(true));

  return {
    profile,
    status,
    retry: useCallback(() => void load(), [load]),
    /** Read it again without the loading state, when something shown may be stale. */
    refresh: useCallback(() => void load(true), [load]),
  };
}
