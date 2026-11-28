/**
 * Deterministic seed data for the mock REST API.
 *
 * Ids and names follow the Bruno examples (`player_one` is user 42, `red_agent` 43 …)
 * so mock traffic reads like the contract documentation. Every value is typed with
 * the real contract types.
 */

import type { AvatarPreset, MatchHistoryEntry, OwnProfile, PublicProfile } from '@shared/types';

/** A mock account: the public profile plus the private fields only the server knows. */
export interface MockAccount extends Omit<PublicProfile, 'relationship' | 'friend_request_id'> {
  email: string;
  password: string;
  created_at: string;
}

/**
 * The mock serves the avatars bundled with the app, so mock profiles show real pictures
 * without a server behind them. The real server returns its own same-origin URLs.
 */
const BUNDLED_AVATARS = import.meta.glob<string>('../../../assets/avatars/*-avatar.svg', {
  eager: true,
  query: '?url',
  import: 'default',
});

const avatar = (presetId: string): string =>
  BUNDLED_AVATARS[`../../../assets/avatars/${presetId}-avatar.svg`] ?? '';

/** The catalog is fixed: 11 entries, fixed order. */
export const AVATAR_PRESET_IDS = [
  'alien',
  'astronaut',
  'aviator-fox',
  'blond-sunglasses',
  'brain-robot',
  'cyborg-cat',
  'glasses-boy',
  'headphones-girl',
  'orc',
  'punk',
  'scholar-owl',
] as const;

export const AVATAR_PRESETS: AvatarPreset[] = AVATAR_PRESET_IDS.map((preset_id) => ({
  preset_id,
  avatar_url: avatar(preset_id),
}));

/** Password shared by every seeded account. */
export const MOCK_PASSWORD = 'codenames42';

export const SELF_USER_ID = 42;

export function seedAccounts(): MockAccount[] {
  const account = (
    user_id: number,
    username: string,
    preset: string,
    stats: { level: number; wins: number; losses: number },
    is_online: boolean,
    created_at: string,
  ): MockAccount => ({
    user_id,
    username,
    email: `${username.replace(/_/g, '.')}@example.com`,
    password: MOCK_PASSWORD,
    avatar_url: avatar(preset),
    level: stats.level,
    wins: stats.wins,
    losses: stats.losses,
    matches_played: stats.wins + stats.losses,
    is_online,
    created_at,
  });

  return [
    account(
      42,
      'player_one',
      'scholar-owl',
      { level: 7, wins: 18, losses: 11 },
      true,
      '2026-09-01T10:00:00Z',
    ),
    account(
      43,
      'red_agent',
      'aviator-fox',
      { level: 5, wins: 12, losses: 9 },
      true,
      '2026-09-02T09:15:00Z',
    ),
    account(
      44,
      'blue_master',
      'brain-robot',
      { level: 9, wins: 27, losses: 14 },
      true,
      '2026-09-02T11:40:00Z',
    ),
    account(
      45,
      'blue_agent',
      'cyborg-cat',
      { level: 3, wins: 6, losses: 8 },
      false,
      '2026-09-03T17:05:00Z',
    ),
    account(
      46,
      'extra_red',
      'punk',
      { level: 2, wins: 2, losses: 5 },
      true,
      '2026-09-05T20:30:00Z',
    ),
    account(
      47,
      'night_owl',
      'headphones-girl',
      { level: 11, wins: 41, losses: 22 },
      false,
      '2026-09-06T23:55:00Z',
    ),
    account(
      48,
      'word_wizard',
      'glasses-boy',
      { level: 14, wins: 58, losses: 19 },
      true,
      '2026-08-20T08:00:00Z',
    ),
    account(
      49,
      'redacted_rita',
      'blond-sunglasses',
      { level: 6, wins: 15, losses: 15 },
      false,
      '2026-08-28T14:20:00Z',
    ),
    account(
      50,
      'clue_crafter',
      'alien',
      { level: 8, wins: 22, losses: 17 },
      true,
      '2026-08-11T12:00:00Z',
    ),
    account(
      51,
      'silent_scout',
      'orc',
      { level: 4, wins: 9, losses: 10 },
      true,
      '2026-08-14T18:45:00Z',
    ),
    account(
      52,
      'lucky_guess',
      'astronaut',
      { level: 10, wins: 33, losses: 21 },
      true,
      '2026-08-02T07:30:00Z',
    ),
    account(
      53,
      'card_shark',
      'cyborg-cat',
      { level: 12, wins: 47, losses: 25 },
      true,
      '2026-07-29T21:10:00Z',
    ),
    account(
      54,
      'mind_reader',
      'scholar-owl',
      { level: 5, wins: 11, losses: 12 },
      true,
      '2026-08-07T16:00:00Z',
    ),
    account(55, 'echo_ops', 'punk', { level: 3, wins: 5, losses: 9 }, true, '2026-08-25T10:25:00Z'),
  ];
}

/**
 * Who already sits in each seeded room. Every account belongs to at most one room, and
 * none of `player_one`'s friends is in one.
 */
export const SEED_ROOM_MEMBERS = {
  waiting: [48, 49],
  full: [46, 47, 50, 51],
  playing: [52, 53, 54, 55],
} as const;

/** Initial friends of `player_one`: two online, one offline. */
export const SEED_FRIEND_IDS: number[] = [43, 44, 45];

/** Users with a pending friend request to `player_one`, waiting for Accept or Decline. */
export const SEED_INCOMING_REQUEST_IDS: number[] = [50];

export function toOwnProfile(account: MockAccount, activeRoomId: number | null): OwnProfile {
  return {
    user_id: account.user_id,
    username: account.username,
    email: account.email,
    avatar_url: account.avatar_url,
    level: account.level,
    wins: account.wins,
    losses: account.losses,
    matches_played: account.matches_played,
    penalty_points: 0,
    active_room_id: activeRoomId,
    active_room_api_version: activeRoomId === null ? null : 'v2',
    created_at: account.created_at,
  };
}

export function toPublicProfile(
  account: MockAccount,
  relationship: Pick<PublicProfile, 'relationship' | 'friend_request_id'>,
): PublicProfile {
  return {
    ...relationship,
    user_id: account.user_id,
    username: account.username,
    avatar_url: account.avatar_url,
    level: account.level,
    wins: account.wins,
    losses: account.losses,
    matches_played: account.matches_played,
    is_online: account.is_online,
  };
}

/** `player_one`'s finished matches, newest first. */
export const SEED_MATCHES: MatchHistoryEntry[] = [
  {
    game_id: 20051,
    room_id: 2005,
    team: 'RED',
    role: 'SPYMASTER',
    opponents: [
      { user_id: 44, username: 'blue_master' },
      { user_id: 45, username: 'blue_agent' },
    ],
    result: 'WIN',
    end_reason: 'ALL_TEAM_CARDS_REVEALED',
    score: { red: 9, blue: 6 },
    finished_at: '2026-09-24T21:12:00Z',
  },
  {
    game_id: 20041,
    room_id: 2004,
    team: 'BLUE',
    role: 'OPERATIVE',
    opponents: [
      { user_id: 48, username: 'word_wizard' },
      { user_id: 43, username: 'red_agent' },
    ],
    result: 'LOSS',
    end_reason: 'ASSASSIN_REVEALED',
    score: { red: 4, blue: 3 },
    finished_at: '2026-09-23T19:47:00Z',
  },
  {
    game_id: 20031,
    room_id: 2003,
    team: 'RED',
    role: 'OPERATIVE',
    opponents: [
      { user_id: 47, username: 'night_owl' },
      { user_id: 49, username: 'redacted_rita' },
    ],
    result: 'WIN',
    end_reason: 'ASSASSIN_REVEALED',
    score: { red: 5, blue: 6 },
    finished_at: '2026-09-21T16:03:00Z',
  },
  {
    game_id: 20021,
    room_id: 2002,
    team: 'BLUE',
    role: 'SPYMASTER',
    opponents: [
      { user_id: 43, username: 'red_agent' },
      { user_id: 46, username: 'extra_red' },
    ],
    result: 'WIN',
    end_reason: 'PLAYER_FORFEIT',
    score: { red: 2, blue: 4 },
    finished_at: '2026-09-18T22:30:00Z',
  },
  {
    game_id: 20011,
    room_id: 2001,
    team: 'RED',
    role: 'OPERATIVE',
    opponents: [
      { user_id: 44, username: 'blue_master' },
      { user_id: 48, username: 'word_wizard' },
    ],
    result: 'LOSS',
    end_reason: 'ALL_TEAM_CARDS_REVEALED',
    score: { red: 6, blue: 9 },
    finished_at: '2026-09-15T20:10:00Z',
  },
];
