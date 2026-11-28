/**
 * The signed-in user's own profile, from `GET /api/v1/users/me`, shared by every view that
 * shows it: the profile menu on Room Discovery, the Ready Room and the Game Board, and
 * Settings.
 *
 * It is read once per signed-in user and then kept current by the server's answers to
 * the Settings actions — the updated profile from `PATCH /api/v1/users/me` and the new
 * `avatar_url` from an upload or a preset pick — so a change shows everywhere at once,
 * without a reload. It never holds anything the server has not confirmed. Nothing is
 * persisted.
 */

import { create } from 'zustand';

import type { OwnProfile } from '@shared/types';

import { getOwnProfile } from '../api';

export type OwnProfileStatus = 'IDLE' | 'LOADING' | 'READY' | 'ERROR';

interface OwnProfileState {
  /** The user this profile belongs to; another sign-in reads its own. */
  ownerId: number | null;
  profile: OwnProfile | null;
  status: OwnProfileStatus;

  /** Read the profile for `ownerId`. `quiet` keeps what is shown while it reloads. */
  load: (ownerId: number, quiet?: boolean) => Promise<void>;
  /** The server's updated profile. */
  apply: (profile: OwnProfile) => void;
  /** The server's new avatar URL. */
  setAvatar: (avatarUrl: string) => void;
  /** The server's new username. */
  setUsername: (username: string) => void;
  clear: () => void;
}

const initial = { ownerId: null, profile: null, status: 'IDLE' as OwnProfileStatus };

let inflight: { ownerId: number; request: Promise<void> } | null = null;

export const useOwnProfileStore = create<OwnProfileState>((set, get) => ({
  ...initial,

  load: (ownerId, quiet = false) => {
    if (inflight?.ownerId === ownerId) return inflight.request;
    if (get().ownerId !== ownerId) set({ ...initial, ownerId });
    if (!quiet || !get().profile) set({ status: 'LOADING' });

    const request = getOwnProfile()
      .then(
        (profile) => {
          if (get().ownerId === ownerId) set({ profile, status: 'READY' });
        },
        () => {
          // A quiet reload that fails keeps the profile already shown.
          if (get().ownerId === ownerId && !get().profile) set({ status: 'ERROR' });
        },
      )
      .finally(() => {
        if (inflight?.request === request) inflight = null;
      });
    inflight = { ownerId, request };
    return request;
  },

  apply: (profile) =>
    set((state) => (state.ownerId === profile.user_id ? { profile, status: 'READY' } : state)),

  setAvatar: (avatarUrl) =>
    set((state) =>
      state.profile ? { profile: { ...state.profile, avatar_url: avatarUrl } } : state,
    ),

  setUsername: (username) =>
    set((state) => (state.profile ? { profile: { ...state.profile, username } } : state)),

  clear: () => {
    inflight = null;
    set(initial);
  },
}));
