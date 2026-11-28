/**
 * Room lifecycle and membership types, from Bruno `v2/game-rest-api/00 - Rooms/` and the
 * `room.*` events of `v2/game-websocket`.
 *
 * `room_id` (internal path key) and `room_code` (human-shareable) are never
 * interchangeable — `workspace.yml` · Room Identifier Consistency.
 */

import type { Score, Team } from './common';
import type { Game } from './game';

/**
 * The two roles. There is no spectator: a member holds one of them and a team, or is
 * unseated (`team: null`, `role: null`) while choosing a seat.
 */
export type PlayingRole = 'SPYMASTER' | 'OPERATIVE';

export type RoomStatus = 'WAITING' | 'COUNTDOWN' | 'IN_GAME' | 'POST_GAME' | 'CLOSED';

/**
 * Each member's own lifecycle, separate from the room's status: after a match the room may
 * already be `WAITING` again while some members are still `POST_GAME` on the result.
 */
export type MemberState = 'IN_LOBBY' | 'IN_GAME' | 'POST_GAME';

/**
 * Room capacity bounds. `max_players` is chosen at creation (default 8) and can be changed
 * by the host while `WAITING`, never below the current `player_count`. Every member counts,
 * seated or not. The minimum equals the smallest startable room.
 */
export const ROOM_CAPACITY = { min: 4, max: 20, default: 8 } as const;

/**
 * Turn-timer choices, in seconds; `null` means no limit and is the default. Only the host
 * changes it, under the same rules as the capacity.
 */
export const TURN_TIMER_OPTIONS = [null, 60, 90, 120] as const;
export type TurnTimerSeconds = (typeof TURN_TIMER_OPTIONS)[number];

/**
 * Word-pack languages a room can use; the first is the default. This is a game setting
 * that picks the board's words, not the interface language.
 */
export const ROOM_LANGUAGES = ['en', 'tr', 'fr'] as const;
export type RoomLanguage = (typeof ROOM_LANGUAGES)[number];

/** `^[A-Z0-9]{6}$` — the one canonical shareable room-code format. */
export const ROOM_CODE_PATTERN = /^[A-Z0-9]{6}$/;

export interface RoomMember {
  user_id: number;
  username: string;
  /** Same-origin URL of the member's current avatar. */
  avatar_url: string;
  /** `null` while the member is unseated. */
  team: Team | null;
  /** Every member joins unseated (`null`) and claims a `{team, role}` pair later. */
  role: PlayingRole | null;
  ready: boolean;
  state: MemberState;
  is_host: boolean;
  /** Join order; drives deterministic host transfer. */
  joined_at: string;
}

export interface RoomCountdown {
  seconds_remaining: number;
}

/** The decision window after a match: the deadline and who has not decided yet. */
export interface PostGame {
  deadline_at: string;
  pending_user_ids: number[];
}

/** The previous match, as an `IN_LOBBY` member's lobby projection shows it. */
export interface LastGame {
  game_id: number;
  status: 'GAME_FINISHED';
  winner: Team;
  loser: Team;
  end_reason: 'ALL_TEAM_CARDS_REVEALED' | 'ASSASSIN_REVEALED' | 'INSUFFICIENT_PLAYERS';
  score: Score;
  finished_at: string;
}

/**
 * The canonical room schema, projected for one recipient. Returned by create, join and
 * snapshot over REST and carried as `payload.room` by `room.state` and `game.started`.
 */
export interface Room {
  room_id: number;
  room_code: string;
  status: RoomStatus;
  /** `null` only while the room stands empty, before it closes. */
  host_user_id: number | null;
  player_count: number;
  /** `ROOM_CAPACITY.min..ROOM_CAPACITY.max`. Join fails with `ROOM_FULL` at this count. */
  max_players: number;
  /** Per-turn time limit, one of `TURN_TIMER_OPTIONS`; `null` means no limit. */
  turn_timer_seconds: number | null;
  /** Lowercase code of the board's word-pack language, one of `ROOM_LANGUAGES`. */
  language: string;
  /** Full server-side start predicate, not merely "everyone is ready". */
  startable: boolean;
  /** Present only while `status` is `COUNTDOWN`. */
  countdown?: RoomCountdown;
  players: RoomMember[];
  /**
   * The match this recipient sees: the running game for an `IN_GAME` member, the completed
   * one for a `POST_GAME` member, `null` in the lobby.
   */
  game: Game | null;
  /** Present while any member is still `POST_GAME`. */
  post_game?: PostGame;
  /** Present in an `IN_LOBBY` member's projection after at least one completed match. */
  last_game?: LastGame;
  empty_since?: string | null;
  close_at?: string | null;
  created_at: string;
}

/** `POST /api/v2/rooms` body. Omitted `max_players` means `ROOM_CAPACITY.default`. */
export interface CreateRoomRequest {
  max_players?: number;
}

/** `GET /api/v2/rooms/lookup/{room_code}` — resolve a code before calling Join room. */
export interface RoomLookupResponse {
  room_id: number;
  room_code: string;
  status: Exclude<RoomStatus, 'CLOSED'>;
  player_count: number;
  max_players: number;
  /** `true` only for a `WAITING` room with a free seat and no member still on a result. */
  joinable: boolean;
}
