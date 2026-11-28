/**
 * Profile, search and friend types, from Bruno `rest-api/02 - Users & Profile/`
 * and `05 - Friends/`.
 */

import type { RoomApiVersion } from './auth';

/** `GET /api/v1/users/me`, `PATCH /api/v1/users/me`. */
export interface OwnProfile {
  user_id: number;
  username: string;
  /** Read-only; shown in Settings. */
  email: string;
  avatar_url: string;
  level: number;
  wins: number;
  losses: number;
  matches_played: number;
  /** Persistent total from leaving running matches as a participant. */
  penalty_points: number;
  active_room_id: number | null;
  active_room_api_version: RoomApiVersion | null;
  created_at: string;
}

/**
 * The signed-in user's relationship to another user, decided by the server. A block placed
 * by the other user is never revealed and reads as `NONE`.
 */
export type Relationship = 'NONE' | 'REQUEST_SENT' | 'REQUEST_RECEIVED' | 'FRIENDS' | 'BLOCKED';

/** `GET /api/v1/users/{user_id}`. */
export interface PublicProfile {
  user_id: number;
  username: string;
  avatar_url: string;
  level: number;
  wins: number;
  losses: number;
  matches_played: number;
  is_online: boolean;
  relationship: Relationship;
  /** The pending request between the two users while one is `REQUEST_SENT` or `REQUEST_RECEIVED`. */
  friend_request_id: number | null;
}

/**
 * Username rule shared by Register and `PATCH /api/v1/users/me`: trimmed, 3–20 letters,
 * digits or underscores. Uniqueness is case-insensitive and only the server can check it.
 */
export const USERNAME_PATTERN = /^[A-Za-z0-9_]{3,20}$/;

/** `PATCH /api/v1/users/me` request body — `username` omitted means unchanged. */
export interface UpdateOwnProfileRequest {
  username?: string;
}

/** `POST /api/v1/users/me/avatar` (multipart, field name `avatar`). */
export interface UploadAvatarResponse {
  avatar_url: string;
}

/** `GET /api/v1/avatars/presets` entry — a ready-made avatar. */
export interface AvatarPreset {
  preset_id: string;
  avatar_url: string;
}

export interface AvatarPresetListResponse {
  presets: AvatarPreset[];
}

/** `PUT /api/v1/users/me/avatar` body; the response is `UploadAvatarResponse`. */
export interface SelectAvatarPresetRequest {
  preset_id: string;
}

/** `DELETE /api/v1/users/me` body: the current username, typed as confirmation. */
export interface DeleteOwnAccountRequest {
  confirm_username: string;
}

/** Report categories for `POST /api/v1/users/{user_id}/reports`. */
export const REPORT_REASONS = [
  'HARASSMENT',
  'CHEATING',
  'OFFENSIVE_USERNAME',
  'SPAM',
  'OTHER',
] as const;

export type ReportReason = (typeof REPORT_REASONS)[number];

/** Longest `details` text a report accepts, after trimming. */
export const REPORT_DETAILS_MAX = 500;

/** `POST /api/v1/users/{user_id}/reports` body. */
export interface ReportUserRequest {
  reason: ReportReason;
  details?: string;
  /** The room it happened in, for context. */
  room_id?: number;
}

export interface ReportUserResponse {
  report_id: number;
  reported_user_id: number;
  reason: ReportReason;
  created_at: string;
}

/** `GET /api/v1/friends` entry, and the `Accept friend request` response body. */
export interface Friend {
  user_id: number;
  username: string;
  avatar_url: string;
  is_online: boolean;
}

/** `GET /api/v1/friends` — one page, ordered case-insensitively by `username`, then `user_id`. */
export interface FriendListResponse {
  friend_count: number;
  /** Applied page size, `1..100`. */
  limit: number;
  /** Applied offset. */
  offset: number;
  /** Whether another page follows this one. */
  has_more: boolean;
  friends: Friend[];
}

/**
 * `GET /api/v1/friends` query parameters. A type alias, like `MatchHistoryQuery`, so it can
 * be passed straight to the REST client's `query` option.
 */
export type FriendListQuery = {
  /** `1..100`, default `50`. */
  limit?: number;
  /** `>= 0`, default `0`. */
  offset?: number;
};

/** `POST /api/v1/friends/requests` body. */
export interface SendFriendRequestBody {
  user_id: number;
}

/** A user as a friend request names them. */
export interface FriendRequestUser {
  user_id: number;
  username: string;
  avatar_url: string;
}

/** A pending friend request: `POST /api/v1/friends/requests` and `GET /api/v1/friends/requests`. */
export interface FriendRequest {
  request_id: number;
  from_user: FriendRequestUser;
  to_user: FriendRequestUser;
  status: 'PENDING';
  created_at: string;
}

/** `GET /api/v1/friends/requests`, newest first in each list. */
export interface FriendRequestListResponse {
  incoming: FriendRequest[];
  outgoing: FriendRequest[];
}

/** `PUT /api/v1/users/{user_id}/block` response. */
export interface BlockResponse {
  user_id: number;
  blocked_at: string;
}

/** `GET /api/v1/users/search?q=` result entry. */
export interface UserSearchResult {
  user_id: number;
  username: string;
  avatar_url: string;
  relationship: Relationship;
  friend_request_id: number | null;
}

/**
 * `GET /api/v1/users/search` — one page of matches, ranked by username prefix, then
 * case-insensitive username, then `user_id`.
 */
export interface UserSearchResponse {
  results: UserSearchResult[];
  /** Applied page size, `1..50`. */
  limit: number;
  /** Applied offset. */
  offset: number;
  /** Whether another page follows this one. */
  has_more: boolean;
}

/** `GET /api/v1/users/search` query parameters. */
export type UserSearchQuery = {
  /** Trimmed, `1..20` characters, matched case-insensitively anywhere in the username. */
  q: string;
  /** `1..50`, default `20`. */
  limit?: number;
  /** `>= 0`, default `0`. */
  offset?: number;
};

/** The contract's bounds for a search term, after trimming. */
export const USER_SEARCH_QUERY = { minLength: 1, maxLength: 20 } as const;
