/**
 * Avatars of the players in a room, from their public profiles (`GET /api/v1/users/{id}`).
 *
 * Room members carry no avatar of their own, so each member's avatar comes from the public
 * profile, fetched once per user and shared by every screen that asks for it. A profile that
 * fails to load leaves that player on the placeholder face and is asked for again the next
 * time the member list changes. When the signed-in user changes their own avatar in
 * Settings, `setPlayerAvatar` puts the server's new URL in place for every screen at once.
 */

import { useCallback, useEffect, useState } from 'react';

import { getPublicProfile } from '../api';

const avatars = new Map<number, string | null>();
const inflight = new Map<number, Promise<void>>();
const listeners = new Set<() => void>();

/** Record an avatar the server confirmed, and show it wherever that player appears. */
export function setPlayerAvatar(userId: number, avatarUrl: string): void {
  avatars.set(userId, avatarUrl || null);
  listeners.forEach((listener) => listener());
}

function load(userId: number): Promise<void> {
  const current = inflight.get(userId);
  if (current) return current;
  const request = getPublicProfile(userId)
    .then(
      (profile) => {
        avatars.set(userId, profile.avatar_url || null);
      },
      () => {
        // Keep the placeholder; the next member-list change asks again.
      },
    )
    .finally(() => inflight.delete(userId));
  inflight.set(userId, request);
  return request;
}

/** Returns a lookup from `user_id` to avatar URL, `null` while unknown. */
export function usePlayerAvatars(userIds: readonly number[]): (userId: number) => string | null {
  const key = [...new Set(userIds)].sort((a, b) => a - b).join(',');
  const [known, setKnown] = useState<ReadonlyMap<number, string | null>>(() => new Map(avatars));

  useEffect(() => {
    let active = true;
    const missing = key
      .split(',')
      .filter(Boolean)
      .map(Number)
      .filter((id) => !avatars.has(id));
    if (missing.length === 0) return;
    void Promise.all(missing.map(load)).then(() => {
      if (active) setKnown(new Map(avatars));
    });
    return () => {
      active = false;
    };
  }, [key]);

  useEffect(() => {
    const listener = () => setKnown(new Map(avatars));
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  }, []);

  // `known` re-renders the caller when avatars arrive; the shared map covers ones loaded elsewhere.
  return useCallback((userId: number) => known.get(userId) ?? avatars.get(userId) ?? null, [known]);
}
