/**
 * Match history and statistics, from Bruno
 * `rest-api/02 - Users & Profile/get-match-history.yml`.
 *
 * No leaderboard endpoint exists in this contract revision.
 */

import type { Score, Team } from './common';
import type { PlayingRole } from './room';

export type MatchResult = 'WIN' | 'LOSS';

/**
 * How a recorded match ended. History also holds matches from before the Game v2 room
 * flow, which a leaving player could still forfeit; a cancelled match is never recorded.
 */
export type MatchEndReason = 'ALL_TEAM_CARDS_REVEALED' | 'ASSASSIN_REVEALED' | 'PLAYER_FORFEIT';

export interface MatchOpponent {
  user_id: number;
  username: string;
}

export interface MatchHistoryEntry {
  game_id: number;
  room_id: number;
  /** This user's team in the match. */
  team: Team;
  /** This user's role in the match. */
  role: PlayingRole;
  opponents: MatchOpponent[];
  result: MatchResult;
  end_reason: MatchEndReason;
  score: Score;
  finished_at: string;
}

export interface MatchHistoryResponse {
  matches: MatchHistoryEntry[];
}

/**
 * `GET /api/v1/users/me/matches` query parameters.
 *
 * Declared as a type alias rather than an interface so it carries an implicit index
 * signature and can be passed straight to the REST client's `query` option.
 */
export type MatchHistoryQuery = {
  /** `1..50`, default `20`. */
  limit?: number;
  /** ISO 8601 UTC pagination cursor. */
  before?: string;
};
