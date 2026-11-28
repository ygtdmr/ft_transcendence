/**
 * WebSocket envelopes, commands and events, from Bruno `v2/game-websocket` (the room
 * socket) and `v2/notification-websocket` (the notification socket).
 *
 * Both sockets authenticate by the HttpOnly `ft_session` cookie attached automatically
 * on a same-origin upgrade. Nothing here carries a credential, by design.
 */

import type {
  FriendRemovedPayload,
  FriendRequestReceivedPayload,
  FriendRequestResolvedPayload,
  FriendRestoredPayload,
  NotificationsReadyPayload,
  UserProfileUpdatedPayload,
} from './notification';
import type { Score, Team } from './common';
import type { CardColor, Clue, CurrentTurn, GameEndReason, HistoryEntry, Staffing } from './game';
import type { MemberState, PlayingRole, Room, RoomMember, RoomStatus } from './room';

/* -------------------------------------------------------------------------- */
/* Client to server                                                            */
/* -------------------------------------------------------------------------- */

export type RoomCommandType =
  | 'room.settings.update'
  | 'room.member.kick'
  | 'room.team.select'
  | 'room.role.select'
  | 'room.ready.set'
  | 'room.lobby.return'
  | 'game.clue.submit'
  | 'game.card.guess'
  | 'game.turn.pass';

/**
 * Client envelope. `request_id` is a client-generated unique string of 1..64 visible
 * ASCII characters; the server caches its result per session for the room lifetime,
 * so an identical retry replays the original ack rather than repeating the mutation.
 */
export interface ClientEnvelope<TType extends string, TPayload> {
  type: TType;
  request_id: string;
  payload: TPayload;
}

/** The room settings. A command carries at least one; an omitted one keeps its value. */
export interface RoomSettings {
  max_players: number;
  turn_timer_seconds: number | null;
  language: string;
}

export type UpdateRoomSettingsCommand = ClientEnvelope<
  'room.settings.update',
  Partial<RoomSettings>
>;
export type KickMemberCommand = ClientEnvelope<'room.member.kick', { user_id: number }>;
/** Changes a seated member's team; an unseated member claims a seat with `room.role.select`. */
export type SelectTeamCommand = ClientEnvelope<'room.team.select', { team: Team }>;
/** A role always comes with its team. */
export type SelectRoleCommand = ClientEnvelope<
  'room.role.select',
  { role: PlayingRole; team: Team }
>;
export type SetReadyCommand = ClientEnvelope<'room.ready.set', { ready: boolean }>;
export type ReturnToLobbyCommand = ClientEnvelope<'room.lobby.return', Record<string, never>>;
export type SubmitClueCommand = ClientEnvelope<
  'game.clue.submit',
  { word: string; number: number }
>;
export type GuessCardCommand = ClientEnvelope<'game.card.guess', { card_id: number }>;
export type PassTurnCommand = ClientEnvelope<'game.turn.pass', Record<string, never>>;

export type RoomCommand =
  | UpdateRoomSettingsCommand
  | KickMemberCommand
  | SelectTeamCommand
  | SelectRoleCommand
  | SetReadyCommand
  | ReturnToLobbyCommand
  | SubmitClueCommand
  | GuessCardCommand
  | PassTurnCommand;

/* -------------------------------------------------------------------------- */
/* Server to client                                                            */
/* -------------------------------------------------------------------------- */

/**
 * Room-stream event envelope. `event_id` is unique and monotonically ordered within
 * one room stream; a detected gap is recovered by taking a fresh `room.state`
 * snapshot, never by replaying history.
 */
export interface ServerEventEnvelope<TType extends string, TPayload> {
  type: TType;
  event_id: string;
  room_id: number;
  sent_at: string;
  payload: TPayload;
}

/**
 * An `ack` is not a room event: it omits `event_id`, `room_id` and `sent_at`. It
 * confirms the command was accepted — the paired server event carries the same
 * authoritative values, so applying both would double-apply the change.
 */
export interface AckMessage<TPayload = Record<string, unknown>> {
  type: 'ack';
  request_id: string;
  ok: true;
  payload: TPayload;
}

export type WsErrorCode =
  | 'INVALID_EVENT'
  | 'INVALID_PAYLOAD'
  | 'UNAUTHORIZED'
  | 'ROOM_NOT_FOUND'
  | 'NOT_ROOM_MEMBER'
  | 'TARGET_NOT_ROOM_MEMBER'
  | 'NOT_HOST'
  | 'CANNOT_KICK_SELF'
  | 'INVALID_ROOM_STATE'
  | 'POST_GAME_PENDING'
  | 'POST_GAME_DEADLINE_EXPIRED'
  | 'TEAM_REQUIRED'
  | 'ROLE_REQUIRED'
  | 'ROLE_CONFLICT'
  | 'NOT_YOUR_TURN'
  | 'ROLE_FORBIDDEN'
  | 'INVALID_CLUE'
  | 'INVALID_CLUE_NUMBER'
  | 'INVALID_CARD'
  | 'CARD_ALREADY_REVEALED'
  | 'NO_GUESSES_REMAINING'
  | 'GAME_ALREADY_FINISHED'
  | 'NOT_PERMITTED'
  | 'USER_NOT_FOUND'
  | 'SERVICE_UNAVAILABLE';

/** Action errors keep the connection open and never mutate room/game state. */
export interface WsErrorMessage {
  type: 'error';
  /** `null` when the request ID was absent or unparseable. */
  request_id: string | null;
  error: {
    code: WsErrorCode;
    message: string;
    details: Record<string, unknown>;
  };
}

/* ----------------------------- room events -------------------------------- */

export type RoomMemberChangedField = 'team' | 'role' | 'ready' | 'username' | 'avatar_url';

export type CountdownCancelReason =
  | 'PLAYER_UNREADY'
  | 'PLAYER_JOINED'
  | 'PLAYER_LEFT'
  | 'PLAYER_KICKED'
  | 'TEAM_CHANGED'
  | 'ROLE_CHANGED';

/** Why a membership ended. Logout (`SESSION_ENDED`) is distinct from a lost connection. */
export type MemberLeftReason =
  | 'EXITED'
  | 'SESSION_ENDED'
  | 'DISCONNECTED'
  | 'POST_GAME_TIMEOUT'
  | 'KICKED_BY_HOST'
  | 'ACCOUNT_DELETED'
  /** An unseated member when the paused match they joined resumed without them. */
  | 'SEAT_UNAVAILABLE';

export type RoomStateEvent = ServerEventEnvelope<'room.state', { room: Room }>;

export interface RoomMembershipPayload {
  player: RoomMember;
  /** `null` only when the room stands empty. */
  host_user_id: number | null;
  player_count: number;
  room_status: RoomStatus;
}

export type RoomPlayerJoinedEvent = ServerEventEnvelope<
  'room.player.joined',
  RoomMembershipPayload
>;
export type RoomPlayerLeftEvent = ServerEventEnvelope<
  'room.player.left',
  RoomMembershipPayload & {
    reason: MemberLeftReason;
    /** Present for `KICKED_BY_HOST`. */
    kicked_by_user_id?: number;
  }
>;

export type RoomPlayerUpdatedEvent = ServerEventEnvelope<
  'room.player.updated',
  {
    player: RoomMember;
    changed_fields: RoomMemberChangedField[];
    room_status: RoomStatus;
    startable: boolean;
  }
>;

export type RoomSettingsUpdatedEvent = ServerEventEnvelope<
  'room.settings.updated',
  RoomSettings & { player_count: number; changed_by_user_id: number }
>;

export type RoomPlayerReturnedEvent = ServerEventEnvelope<
  'room.player.returned_to_lobby',
  { user_id: number; previous_game_id: number; room_status: RoomStatus; member_state: MemberState }
>;

export type RoomPostGameStartedEvent = ServerEventEnvelope<
  'room.post_game.started',
  { game_id: number; deadline_at: string; pending_user_ids: number[] }
>;

export type RoomPostGameCompletedEvent = ServerEventEnvelope<
  'room.post_game.completed',
  { room_status: RoomStatus; remaining_member_ids: number[] }
>;

export type RoomCountdownStartedEvent = ServerEventEnvelope<
  'room.countdown.started',
  { seconds_remaining: number }
>;
export type RoomCountdownTickEvent = ServerEventEnvelope<
  'room.countdown.tick',
  { seconds_remaining: number }
>;
export type RoomCountdownCancelledEvent = ServerEventEnvelope<
  'room.countdown.cancelled',
  { reason: CountdownCancelReason; changed_by_user_id: number }
>;

/** The room shut down while members remained; every room socket then closes with `4404`. */
export type RoomClosedEvent = ServerEventEnvelope<
  'room.closed',
  { reason: 'INSUFFICIENT_PLAYERS'; game_id: number; deficient_team: Team; closed_at: string }
>;

/* ----------------------------- game events -------------------------------- */

/** A turn as a game event reports it: `team` and `phase` always, the rest when changed. */
export type TurnUpdate = Pick<CurrentTurn, 'team' | 'phase'> & Partial<CurrentTurn>;

export type GameStartedEvent = ServerEventEnvelope<'game.started', { room: Room }>;

export type GameClueSubmittedEvent = ServerEventEnvelope<
  'game.clue.submitted',
  {
    game_id: number;
    submitted_by_user_id: number;
    team: Team;
    clue: Clue;
    guesses_remaining: number;
    /** May carry only `team` and `phase`; the clue and count are the fields beside it. */
    current_turn: TurnUpdate;
  }
>;

export type RevealedCard = { card_id: number; word: string; revealed: true; color: CardColor };

export type GameCardRevealedEvent = ServerEventEnvelope<
  'game.card.revealed',
  {
    game_id: number;
    /** A revealed card's color is public, so it is never `null` here. */
    card: RevealedCard;
    guessed_by_user_id: number;
    guessing_team: Team;
    score: Score;
    /** The turn after the reveal; `clue` may be left out. */
    current_turn: TurnUpdate;
    winner: Team | null;
    end_reason: GameEndReason | null;
  }
>;

export type GameScoreUpdatedEvent = ServerEventEnvelope<
  'game.score.updated',
  { game_id: number; score: Score; changed_color: Team }
>;

export type GameTurnChangeReason =
  | 'PASSED'
  | 'NEUTRAL_CARD_REVEALED'
  | 'OPPONENT_CARD_REVEALED'
  | 'GUESSES_EXHAUSTED'
  | 'TURN_TIMER_EXPIRED';

export type GameTurnChangedEvent = ServerEventEnvelope<
  'game.turn.changed',
  {
    game_id: number;
    previous_team: Team;
    reason: GameTurnChangeReason;
    current_turn: CurrentTurn;
    score: Score;
  }
>;

/** Terminal for the match. No `game.turn.changed` follows it; `room.post_game.started` does. */
export type GameEndedEvent = ServerEventEnvelope<
  'game.ended',
  {
    game_id: number;
    winner: Team;
    loser: Team;
    end_reason: 'ALL_TEAM_CARDS_REVEALED' | 'ASSASSIN_REVEALED';
    revealed_card: RevealedCard;
    score: Score;
    room_status: 'POST_GAME';
    game_status: 'GAME_FINISHED';
    phase: 'GAME_OVER';
    post_game_deadline_at: string;
    finished_at: string;
  }
>;

/** The member whose removal left a team short, and why. */
export interface StaffingDeparture {
  user_id: number;
  username: string;
  team: Team;
  role: PlayingRole;
  reason: Exclude<MemberLeftReason, 'POST_GAME_TIMEOUT' | 'SEAT_UNAVAILABLE'>;
}

/** A team lost its only Spymaster or its last Operative: the shutdown countdown starts. */
export type GameStaffingRequiredEvent = ServerEventEnvelope<
  'game.staffing.required',
  {
    game_id: number;
    phase: 'PAUSED_FOR_PLAYERS';
    staffing: Staffing;
    departure: StaffingDeparture;
  }
>;

export type GameStaffingRestoredEvent = ServerEventEnvelope<
  'game.staffing.restored',
  { game_id: number; restored_teams: Team[]; current_turn: CurrentTurn }
>;

/** A staffing deadline expired: no winner, no loser, no recorded result. */
export type GameCancelledEvent = ServerEventEnvelope<
  'game.cancelled',
  {
    game_id: number;
    winner: null;
    loser: null;
    end_reason: 'INSUFFICIENT_PLAYERS';
    deficient_team: Team;
    revealed_card?: null;
    score: Score;
    room_status: 'CLOSED';
    game_status: 'GAME_CANCELLED';
    phase: 'GAME_OVER';
    finished_at: string;
  }
>;

/** One new entry of the match's action log, right after the events of the action it records. */
export type GameHistoryAppendedEvent = ServerEventEnvelope<
  'game.history.appended',
  { game_id: number; entry: HistoryEntry }
>;

export type RoomServerEvent =
  | RoomStateEvent
  | RoomPlayerJoinedEvent
  | RoomPlayerLeftEvent
  | RoomPlayerUpdatedEvent
  | RoomSettingsUpdatedEvent
  | RoomPlayerReturnedEvent
  | RoomPostGameStartedEvent
  | RoomPostGameCompletedEvent
  | RoomCountdownStartedEvent
  | RoomCountdownTickEvent
  | RoomCountdownCancelledEvent
  | RoomClosedEvent
  | GameStartedEvent
  | GameClueSubmittedEvent
  | GameCardRevealedEvent
  | GameScoreUpdatedEvent
  | GameTurnChangedEvent
  | GameEndedEvent
  | GameStaffingRequiredEvent
  | GameStaffingRestoredEvent
  | GameCancelledEvent
  | GameHistoryAppendedEvent;

/** Anything the room socket can deliver. */
export type RoomServerMessage = RoomServerEvent | AckMessage | WsErrorMessage;

/* ------------------------- notification messages -------------------------- */

/** The notification envelope has no `room_id`: the socket is per user, not per room. */
export interface NotificationEventEnvelope<TType extends string, TPayload> {
  type: TType;
  event_id: string;
  sent_at: string;
  payload: TPayload;
}

export type NotificationsReadyEvent = {
  type: 'notifications.ready';
  sent_at: string;
  payload: NotificationsReadyPayload;
};

export type FriendRequestReceivedEvent = NotificationEventEnvelope<
  'friend.request.received',
  FriendRequestReceivedPayload
>;
export type FriendRequestResolvedEvent = NotificationEventEnvelope<
  'friend.request.resolved',
  FriendRequestResolvedPayload
>;
export type FriendRemovedEvent = NotificationEventEnvelope<'friend.removed', FriendRemovedPayload>;
export type FriendRestoredEvent = NotificationEventEnvelope<
  'friend.restored',
  FriendRestoredPayload
>;
export type UserProfileUpdatedEvent = NotificationEventEnvelope<
  'user.profile.updated',
  UserProfileUpdatedPayload
>;

/** Friend request, friendship and profile changes, delivered live on the notification socket. */
export type SocialEvent =
  | FriendRequestReceivedEvent
  | FriendRequestResolvedEvent
  | FriendRemovedEvent
  | FriendRestoredEvent
  | UserProfileUpdatedEvent;

/** The notification socket's reply to any client frame; the socket stays open. */
export interface NotificationErrorMessage {
  type: 'error';
  payload: { code: string };
}

export type NotificationServerMessage =
  NotificationsReadyEvent | SocialEvent | NotificationErrorMessage;

/**
 * Close codes the Gateway uses. `4401` means the session became explicitly invalid
 * after connection (logout, refresh-token-family revocation, account deletion) — ordinary
 * access-token expiry does not by itself close an open socket. `4403` ends one member's
 * membership (a kick, the post-game timeout); `4404` follows `room.closed`.
 */
export const WS_CLOSE_UNAUTHORIZED = 4401;
export const WS_CLOSE_ROOM_NOT_FOUND = 4404;
export const WS_CLOSE_NOT_ROOM_MEMBER = 4403;
