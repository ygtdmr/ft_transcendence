/**
 * Match history bindings — Bruno `rest-api/02 - Users & Profile/get-match-history.yml`.
 *
 * Wins, losses and level come from the profile payloads; this endpoint backs the match
 * list. There is no leaderboard endpoint in this contract.
 */

import { apiRequest } from '@shared/api';
import { CORE_API_PATH } from '@shared/constants';
import type { MatchHistoryQuery, MatchHistoryResponse } from '@shared/types';

export const getMatchHistory = (query: MatchHistoryQuery = {}): Promise<MatchHistoryResponse> =>
  apiRequest(`${CORE_API_PATH}/users/me/matches`, { query });
