/**
 * In-memory REST server speaking the Bruno `rest-api` contract, with rooms on the Game v2
 * contract (`v2/game-rest-api`).
 *
 * It answers every binding in `features/*\/api` with the documented status code and
 * the documented `{ data }` / `{ error }` envelope, applies the documented validation
 * and state rules, and keeps its state (session, profile, friends, rooms) between
 * calls. Rooms live in the shared mock room registry, so a room created here is the
 * room the mock room socket connects to.
 */

import { REPORT_DETAILS_MAX, REPORT_REASONS, ROOM_CAPACITY } from '@shared/types';
import type {
  ApiErrorBody,
  BlockResponse,
  ReportUserResponse,
  Friend,
  FriendListResponse,
  FriendRequest,
  FriendRequestListResponse,
  HealthResponse,
  InviteFriendResponse,
  LoginResponse,
  MatchHistoryResponse,
  OwnProfile,
  PublicProfile,
  RefreshResponse,
  Relationship,
  RegisterResponse,
  RestErrorCode,
  Room,
  RoomLookupResponse,
  SessionResponse,
  UploadAvatarResponse,
  AvatarPresetListResponse,
  UserSearchResponse,
} from '@shared/types';
import { CHAT_API_PATH, ROOM_API_PATH } from '@shared/constants';
import {
  MockActionError,
  MockChannelError,
  MockRoomServer,
  mockChat,
  mockDirectory,
  mockRooms,
  mockSession,
} from '@shared/websocket/mock';
import type { MockPlayer } from '@shared/websocket/mock';

import type { ApiRequestOptions } from '../client';
import type { MockApiConfig, MockEndpoint, MockOutcome } from './config';
import {
  AVATAR_PRESETS,
  SEED_FRIEND_IDS,
  SEED_INCOMING_REQUEST_IDS,
  SEED_MATCHES,
  SEED_ROOM_MEMBERS,
  SELF_USER_ID,
  seedAccounts,
  toOwnProfile,
  toPublicProfile,
} from './fixtures';
import type { MockAccount } from './fixtures';

const USERNAME_PATTERN = /^[A-Za-z0-9_]{3,20}$/;
const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
const LOOKUP_CODE_PATTERN = /^[A-Za-z0-9]{6}$/;
const AVATAR_TYPES = ['image/jpeg', 'image/png', 'image/webp'];
const AVATAR_MAX_BYTES = 2 * 1024 * 1024;
const ACCESS_TTL_MS = 15 * 60 * 1000;
/** Window in which a repeat report of the same user is refused (24 hours in Bruno examples). */
const REPORT_WINDOW_MS = 24 * 60 * 60 * 1000;

/** A contract error response. Thrown by handlers, turned into an `{ error }` envelope. */
class HttpError extends Error {
  readonly status: number;
  readonly code: RestErrorCode;
  readonly details: Record<string, unknown>;

  constructor(
    status: number,
    code: RestErrorCode,
    message: string,
    details: Record<string, unknown> = {},
  ) {
    super(message);
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

const unauthorized = () =>
  new HttpError(401, 'UNAUTHORIZED', 'A valid active session is required.');
const roomNotFound = (roomId: number) =>
  new HttpError(404, 'ROOM_NOT_FOUND', `Room ${roomId} was not found.`, { room_id: roomId });
const notMember = (roomId: number, userId: number) =>
  new HttpError(403, 'NOT_ROOM_MEMBER', `User ${userId} is not a member of room ${roomId}.`, {
    room_id: roomId,
    user_id: userId,
  });
const validation = (message: string, field: string, status = 422) =>
  new HttpError(status, 'VALIDATION_ERROR', message, { field });
const roomFull = (room: Room) =>
  new HttpError(
    409,
    'ROOM_FULL',
    `Room ${room.room_id} is full (${room.player_count} of ${room.max_players} players).`,
    { room_id: room.room_id, player_count: room.player_count, max_players: room.max_players },
  );
const notJoinable = (room: Room) =>
  new HttpError(
    409,
    'ROOM_NOT_JOINABLE',
    `Room ${room.room_id} is not joinable while status is ${room.status}.`,
    { room_id: room.room_id, status: room.status },
  );

interface MockResult {
  status: number;
  /** Omitted for `204 No Content`. */
  data?: unknown;
}

interface RequestContext {
  params: string[];
  options: ApiRequestOptions;
}

interface Route {
  endpoint: MockEndpoint;
  method: NonNullable<ApiRequestOptions['method']>;
  pattern: RegExp;
  /** Requires a live `ft_session`; answers `401 UNAUTHORIZED` otherwise. */
  auth: boolean;
  handle(ctx: RequestContext): MockResult;
  /** The documented error for each forced outcome this endpoint supports. */
  forced: Partial<Record<MockOutcome, () => HttpError>>;
}

interface MockRoomEntry {
  code: string;
  server: MockRoomServer;
}

const ok = (data: unknown): MockResult => ({ status: 200, data });
const created = (data: unknown): MockResult => ({ status: 201, data });
const accepted = (data: unknown): MockResult => ({ status: 202, data });
const noContent: MockResult = { status: 204 };

const bodyOf = <T>(options: ApiRequestOptions): Partial<T> =>
  typeof options.body === 'object' && options.body !== null ? (options.body as Partial<T>) : {};

/** A Chat REST v2 path under `CHAT_API_PATH`, e.g. `/v2/channels/7001/messages`. */
const chatPattern = (suffix: string): RegExp =>
  new RegExp(`^${CHAT_API_PATH.replace(/\//g, '\\/')}${suffix}$`);

/** A Game REST v2 path under `ROOM_API_PATH`, e.g. `/v2/rooms/1001/members`. */
const roomPattern = (suffix: string): RegExp =>
  new RegExp(`^${ROOM_API_PATH.replace(/\//g, '\\/')}${suffix}$`);

const isPositiveInt = (value: unknown): value is number =>
  typeof value === 'number' && Number.isInteger(value) && value > 0;

const blockedError = (userId: number) =>
  new HttpError(403, 'RELATIONSHIP_BLOCKED', 'You cannot send a friend request to this user.', {
    user_id: userId,
  });
const requestNotFound = (requestId: number) =>
  new HttpError(404, 'REQUEST_NOT_FOUND', `Friend request ${requestId} is not pending for you.`, {
    request_id: requestId,
  });

/** A pending request between the signed-in user and another user. */
interface MockFriendRequest {
  request_id: number;
  from_user_id: number;
  to_user_id: number;
  created_at: string;
}

export class MockApiServer {
  private readonly config: MockApiConfig;
  private accounts: MockAccount[] = [];
  /**
   * Friendships of the signed-in user, active or suspended by a block. Only an active one
   * (no block either way) counts as a friend anywhere.
   */
  private friendIds = new Set<number>();
  /** Users the signed-in user blocks. */
  private blockedIds = new Set<number>();
  /** Users who block the signed-in user; never revealed, only enforced. */
  private blockedByIds = new Set<number>();
  private requests = new Map<number, MockFriendRequest>();
  private nextRequestId = 701;
  private sessionUserId: number | null = null;
  /** `false` models an expired `ft_session` whose `ft_refresh` still works. */
  private accessValid = true;
  private refreshValid = true;
  private rooms = new Map<number, MockRoomEntry>();
  private nextRoomId = 1001;
  /** When the signed-in user last reported each user, by reported user ID. */
  private reports = new Map<number, number>();
  private nextReportId = 9001;
  private nextInviteId = 10;
  private readonly routes: Route[];

  constructor(config: MockApiConfig) {
    this.config = config;
    this.routes = this.buildRoutes();
    this.reset();
    mockSession.authorized = () => this.isAuthenticated();
    // The mock Channel Service asks this server, as the real one asks User/Auth.
    mockDirectory.selfId = () => this.sessionUserId ?? SELF_USER_ID;
    mockDirectory.isFriend = (userId) => this.isActiveFriend(userId);
    mockDirectory.user = (userId) => {
      const account = this.accounts.find((a) => a.user_id === userId);
      return account
        ? {
            user_id: account.user_id,
            username: account.username,
            avatar_url: account.avatar_url,
            is_online: account.is_online,
          }
        : null;
    };
    mockDirectory.roomCode = (roomId) => this.rooms.get(roomId)?.code ?? null;
  }

  /** The access cookie expires; the next `401` refreshes it, as it would in production. */
  expireAccess(): void {
    this.accessValid = false;
  }

  /** Both cookies stop working, as after a revocation elsewhere: only Log In helps. */
  revokeSession(): void {
    this.accessValid = false;
    this.refreshValid = false;
  }

  /** Restore the seed data and apply `config.auth`. */
  reset(): void {
    this.accounts = seedAccounts();
    this.friendIds = new Set(SEED_FRIEND_IDS);
    this.blockedIds = new Set();
    this.blockedByIds = new Set();
    this.requests = new Map();
    this.nextRequestId = 701;
    for (const fromId of SEED_INCOMING_REQUEST_IDS) {
      this.addRequest(fromId, SELF_USER_ID, new Date(Date.now() - 3_600_000).toISOString());
    }
    this.rooms.forEach((_entry, id) => mockRooms.delete(id));
    this.rooms = new Map();
    this.nextRoomId = 1001;
    this.reports = new Map();
    this.nextReportId = 9001;
    this.seedRooms();

    const auth = this.config.auth;
    this.sessionUserId = auth === 'logged-out' ? null : SELF_USER_ID;
    this.accessValid = auth !== 'session-expired';
    this.refreshValid = auth !== 'logged-out';
    if (auth === 'in-room') this.roomByCode('QWER12')?.server.playerJoin(this.selfPlayer());
  }

  /** Answer one request the way the Gateway would. */
  handle(path: string, options: ApiRequestOptions): Response {
    const method = options.method ?? 'GET';
    for (const route of this.routes) {
      if (route.method !== method) continue;
      const match = route.pattern.exec(path);
      if (!match) continue;
      try {
        this.applyForcedOutcome(route);
        if (route.auth && !this.isAuthenticated()) throw unauthorized();
        const response = this.respond(route.handle({ params: match.slice(1), options }));
        // Room changes made over REST reach the mock Channel Service's lifecycle projection.
        if (method !== 'GET') mockChat.syncRooms();
        return response;
      } catch (error) {
        if (error instanceof HttpError) return this.respondError(error);
        if (error instanceof MockChannelError) {
          return this.respondError(
            new HttpError(error.status, error.code as RestErrorCode, error.message, error.details),
          );
        }
        throw error;
      }
    }
    console.warn(`[mock api] no mock for ${method} ${path} — it is not in the REST contract.`);
    return this.respondError(
      new HttpError(404, 'VALIDATION_ERROR', `No route for ${method} ${path}.`),
    );
  }

  /* ------------------------------ other users act -------------------------- */

  /** `userId` sends the signed-in user a friend request. */
  friendRequestFrom(userId: number): void {
    const me = this.sessionUserId ?? SELF_USER_ID;
    if (this.relationshipTo(userId) !== 'NONE' || this.blockedByIds.has(userId)) {
      console.warn(
        `[mock api] ${userId} cannot send a request now (${this.relationshipTo(userId)}).`,
      );
      return;
    }
    const request = this.addRequest(userId, me);
    mockChat.notify('friend.request.received', { request: this.toRequest(request) });
  }

  /** `userId` accepts the signed-in user's pending request. */
  acceptFriendRequestBy(userId: number): void {
    const request = this.pendingBetween(this.sessionUserId ?? SELF_USER_ID, userId);
    if (!request) {
      console.warn(`[mock api] no pending request to ${userId}.`);
      return;
    }
    this.resolveAccepted(request);
  }

  /** `userId` declines the signed-in user's pending request. */
  declineFriendRequestBy(userId: number): void {
    const request = this.pendingBetween(this.sessionUserId ?? SELF_USER_ID, userId);
    if (!request) {
      console.warn(`[mock api] no pending request to ${userId}.`);
      return;
    }
    this.resolve(request, 'DECLINED');
  }

  /** `userId` blocks (or, with `false`, unblocks) the signed-in user. */
  blockedBy(userId: number, blocking = true): void {
    if (blocking)
      this.applyBlock(userId, () => this.blockedByIds.add(userId), 'FRIENDSHIP_REMOVED');
    else this.liftBlock(userId, () => this.blockedByIds.delete(userId));
  }

  /** `userId` changes their username or avatar, as `Update own profile` would. */
  profileUpdateBy(userId: number, profile: { username?: string; avatar_url?: string }): void {
    const account = this.accounts.find((a) => a.user_id === userId);
    if (!account) {
      console.warn(`[mock api] no account ${userId}.`);
      return;
    }
    if (profile.username !== undefined) account.username = profile.username;
    if (profile.avatar_url !== undefined) account.avatar_url = profile.avatar_url;
    this.profileChanged(account, profile);
  }

  /**
   * A confirmed username or avatar change reaches every view of the user: the members of
   * their active room through `room.player.updated`, and on the chat socket the user's own
   * sessions, their friends and the other side of a pending request.
   */
  private profileChanged(
    account: MockAccount,
    changed: { username?: string; avatar_url?: string },
  ): void {
    const roomId = this.activeRoomId(account.user_id);
    if (roomId !== null) mockRooms.get(roomId)?.updateMemberProfile(account.user_id, changed);
    const me = this.sessionUserId ?? SELF_USER_ID;
    const relationship = account.user_id === me ? null : this.relationshipTo(account.user_id);
    if (
      relationship === null ||
      ['FRIENDS', 'REQUEST_SENT', 'REQUEST_RECEIVED'].includes(relationship)
    ) {
      mockChat.notify('user.profile.updated', {
        user_id: account.user_id,
        username: account.username,
        avatar_url: account.avatar_url,
      });
    }
  }

  /* ------------------------------ relationships ----------------------------- */

  private isActiveFriend(userId: number): boolean {
    return (
      this.friendIds.has(userId) && !this.blockedIds.has(userId) && !this.blockedByIds.has(userId)
    );
  }

  /** The signed-in user's view; a block by the other user reads as `NONE`. */
  private relationshipTo(userId: number): Relationship {
    if (this.blockedIds.has(userId)) return 'BLOCKED';
    if (this.isActiveFriend(userId)) return 'FRIENDS';
    if (this.blockedByIds.has(userId)) return 'NONE';
    const me = this.sessionUserId ?? SELF_USER_ID;
    const request = this.pendingBetween(me, userId) ?? this.pendingBetween(userId, me);
    if (!request) return 'NONE';
    return request.from_user_id === me ? 'REQUEST_SENT' : 'REQUEST_RECEIVED';
  }

  private relationshipFields(userId: number): {
    relationship: Relationship;
    friend_request_id: number | null;
  } {
    const relationship = this.relationshipTo(userId);
    const me = this.sessionUserId ?? SELF_USER_ID;
    const request =
      relationship === 'REQUEST_SENT' || relationship === 'REQUEST_RECEIVED'
        ? (this.pendingBetween(me, userId) ?? this.pendingBetween(userId, me))
        : undefined;
    return { relationship, friend_request_id: request?.request_id ?? null };
  }

  private pendingBetween(fromId: number, toId: number): MockFriendRequest | undefined {
    return [...this.requests.values()].find(
      (r) => r.from_user_id === fromId && r.to_user_id === toId,
    );
  }

  private addRequest(fromId: number, toId: number, createdAt = new Date().toISOString()) {
    const request: MockFriendRequest = {
      request_id: this.nextRequestId++,
      from_user_id: fromId,
      to_user_id: toId,
      created_at: createdAt,
    };
    this.requests.set(request.request_id, request);
    return request;
  }

  private userOf(userId: number) {
    const account = this.accounts.find((a) => a.user_id === userId);
    return {
      user_id: userId,
      username: account?.username ?? `user_${userId}`,
      avatar_url: account?.avatar_url ?? '',
    };
  }

  private toRequest(request: MockFriendRequest): FriendRequest {
    return {
      request_id: request.request_id,
      from_user: this.userOf(request.from_user_id),
      to_user: this.userOf(request.to_user_id),
      status: 'PENDING',
      created_at: request.created_at,
    };
  }

  private toFriend(userId: number): Friend {
    const account = this.accounts.find((a) => a.user_id === userId);
    return { ...this.userOf(userId), is_online: account?.is_online ?? false };
  }

  /** The other user of a request, from the signed-in user's side. */
  private otherOf(request: MockFriendRequest): number {
    const me = this.sessionUserId ?? SELF_USER_ID;
    return request.from_user_id === me ? request.to_user_id : request.from_user_id;
  }

  /** `userId` withdraws the request they sent the signed-in user. */
  cancelFriendRequestBy(userId: number): void {
    const request = this.pendingBetween(userId, this.sessionUserId ?? SELF_USER_ID);
    if (!request) {
      console.warn(`[mock api] no pending request from ${userId}.`);
      return;
    }
    this.resolve(request, 'CANCELLED');
  }

  private resolve(request: MockFriendRequest, status: 'DECLINED' | 'CANCELLED'): void {
    this.requests.delete(request.request_id);
    mockChat.notify('friend.request.resolved', {
      request_id: request.request_id,
      from_user_id: request.from_user_id,
      to_user_id: request.to_user_id,
      status,
    });
  }

  /** Resolving the request and adding the friendship are one step, so it happens once. */
  private resolveAccepted(request: MockFriendRequest): Friend {
    this.requests.delete(request.request_id);
    const other = this.otherOf(request);
    this.friendIds.add(other);
    const friend = this.toFriend(other);
    mockChat.notify('friend.request.resolved', {
      request_id: request.request_id,
      from_user_id: request.from_user_id,
      to_user_id: request.to_user_id,
      status: 'ACCEPTED',
      friend,
    });
    mockChat.friendshipChanged(other, true);
    return friend;
  }

  /**
   * A block, either way: pending requests between the two users are cancelled and their
   * friendship is suspended, all in one step.
   */
  private applyBlock(
    userId: number,
    add: () => void,
    reason: 'USER_BLOCKED' | 'FRIENDSHIP_REMOVED',
  ): void {
    const wasFriend = this.isActiveFriend(userId);
    add();
    for (const request of [...this.requests.values()]) {
      if (this.otherOf(request) === userId) this.resolve(request, 'CANCELLED');
    }
    if (wasFriend) {
      mockChat.notify('friend.removed', { user_id: userId });
      mockChat.friendshipChanged(userId, false, reason);
    }
  }

  /** Lifting a block restores a suspended friendship once no block is left; nothing else. */
  private liftBlock(userId: number, remove: () => void): void {
    const wasFriend = this.isActiveFriend(userId);
    remove();
    if (!wasFriend && this.isActiveFriend(userId)) {
      mockChat.notify('friend.restored', { friend: this.toFriend(userId) });
      mockChat.friendshipChanged(userId, true);
    }
  }

  private requireUser(userId: unknown, field = 'user_id'): MockAccount {
    if (!isPositiveInt(userId)) throw validation(`${field} must be a positive integer.`, field);
    const account = this.accounts.find((a) => a.user_id === userId);
    if (!account) {
      throw new HttpError(404, 'USER_NOT_FOUND', `User ${userId} was not found.`, {
        user_id: userId,
      });
    }
    return account;
  }

  /* ------------------------------ forced outcomes --------------------------- */

  private applyForcedOutcome(route: Route): void {
    const outcome = this.config.outcomes[route.endpoint];
    if (!outcome || outcome === 'success') return;
    if (outcome === 'network-error') throw new TypeError('Failed to fetch (mock network error)');
    if (outcome === 'server-error') {
      throw new HttpError(503, 'SERVICE_UNAVAILABLE', 'The service is temporarily unavailable.');
    }
    if (outcome === 'rate-limited') {
      throw new HttpError(429, 'RATE_LIMITED', 'Too many requests; retry after 60 seconds.', {
        retry_after_seconds: 60,
      });
    }
    if (outcome === 'unauthorized' && route.auth) throw unauthorized();
    const forced = route.forced[outcome];
    if (forced) throw forced();
    console.warn(
      `[mock api] ${route.endpoint} documents no '${outcome}' error; answering normally.`,
    );
  }

  /* --------------------------------- session -------------------------------- */

  private isAuthenticated(): boolean {
    return this.sessionUserId !== null && this.accessValid;
  }

  private self(): MockAccount {
    const account = this.accounts.find((a) => a.user_id === this.sessionUserId);
    if (!account) throw unauthorized();
    return account;
  }

  private selfPlayer(): MockPlayer {
    const account = this.accounts.find((a) => a.user_id === (this.sessionUserId ?? SELF_USER_ID));
    return {
      user_id: account?.user_id ?? SELF_USER_ID,
      username: account?.username ?? 'player_one',
      ...(account ? { avatar_url: account.avatar_url } : {}),
    };
  }

  private startSession(userId: number): string {
    this.sessionUserId = userId;
    this.accessValid = true;
    this.refreshValid = true;
    return this.expiry();
  }

  private expiry(): string {
    return new Date(Date.now() + ACCESS_TTL_MS).toISOString();
  }

  /* ---------------------------------- rooms --------------------------------- */

  private seedRooms(): void {
    const players = (ids: readonly number[]): MockPlayer[] =>
      ids.map((id) => {
        const account = this.accounts.find((a) => a.user_id === id);
        return {
          user_id: id,
          username: account?.username ?? `user_${id}`,
          ...(account ? { avatar_url: account.avatar_url } : {}),
        };
      });
    const [wizard, rita] = SEED_ROOM_MEMBERS.waiting;
    const [shark, reader, lucky, echo] = SEED_ROOM_MEMBERS.playing;

    // A waiting room with free seats, reachable by code `QWER12`.
    const waiting = this.addRoom('QWER12', 8, players(SEED_ROOM_MEMBERS.waiting));
    waiting.selectRole(wizard, 'SPYMASTER', 'BLUE');
    waiting.selectRole(rita, 'OPERATIVE', 'RED');

    // Full: 4 of 4 players.
    this.addRoom('FULL44', 4, players(SEED_ROOM_MEMBERS.full));

    // Already playing: not joinable while its match runs.
    const playing = this.addRoom('BUSY77', 8, players(SEED_ROOM_MEMBERS.playing));
    const seats = [
      [shark, 'RED', 'SPYMASTER'],
      [reader, 'RED', 'OPERATIVE'],
      [lucky, 'BLUE', 'SPYMASTER'],
      [echo, 'BLUE', 'OPERATIVE'],
    ] as const;
    for (const [userId, team, role] of seats) playing.selectRole(userId, role, team);
    playing.startGame('BLUE');
  }

  private addRoom(code: string, maxPlayers: number, members: MockPlayer[]): MockRoomServer {
    const roomId = this.nextRoomId++;
    const server = new MockRoomServer(roomId, this.selfPlayer(), maxPlayers, members, code);
    this.rooms.set(roomId, { code, server });
    mockRooms.set(roomId, server);
    return server;
  }

  private roomByCode(code: string): MockRoomEntry | undefined {
    const upper = code.toUpperCase();
    return [...this.rooms.values()].find((entry) => entry.code === upper);
  }

  private requireRoom(roomId: number): MockRoomServer {
    const server = mockRooms.get(roomId);
    if (!server || server.snapshot().status === 'CLOSED') throw roomNotFound(roomId);
    return server;
  }

  /** The room this user belongs to, from the shared registry (sockets may have changed it). */
  private activeRoomId(userId: number): number | null {
    for (const [roomId, server] of mockRooms) {
      if (server.snapshot().status !== 'CLOSED' && server.hasMember(userId)) return roomId;
    }
    return null;
  }

  private newRoomCode(): string {
    const alphabet = 'ABCDEFGHJKLMNPQRSTUVWXYZ23456789';
    let code: string;
    do {
      code = Array.from(
        { length: 6 },
        (_v, i) => alphabet[(this.nextRoomId * 7 + i * 13) % alphabet.length],
      ).join('');
    } while (this.roomByCode(code));
    return code;
  }

  private parseRoomId(raw: string | undefined): number {
    const roomId = Number(raw);
    if (!Number.isInteger(roomId) || roomId <= 0) {
      throw validation('room_id must be a positive integer.', 'room_id');
    }
    return roomId;
  }

  /* ---------------------------------- routes -------------------------------- */

  private respond(result: MockResult): Response {
    if (result.status === 204) return new Response(null, { status: 204 });
    return Response.json({ data: result.data }, { status: result.status });
  }

  private respondError(error: HttpError): Response {
    const body: ApiErrorBody = {
      error: { code: error.code, message: error.message, details: error.details },
    };
    return Response.json(body, { status: error.status });
  }

  private buildRoutes(): Route[] {
    const route = (
      endpoint: MockEndpoint,
      method: Route['method'],
      pattern: RegExp,
      auth: boolean,
      handle: Route['handle'],
      forced: Route['forced'] = {},
    ): Route => ({ endpoint, method, pattern, auth, handle, forced });

    const selfId = () => this.self().user_id;

    return [
      route('health', 'GET', /^\/v1\/health$/, false, () =>
        ok({
          status: 'OK',
          contract_version: '1.2.0-dev',
          server_time: new Date().toISOString(),
        } satisfies HealthResponse),
      ),

      /* ------------------------------- identity ------------------------------ */

      route(
        'register',
        'POST',
        /^\/v1\/auth\/register$/,
        false,
        ({ options }) => {
          const body = bodyOf<{ email: string; username: string; password: string }>(options);
          const email = typeof body.email === 'string' ? body.email.trim() : '';
          const username = typeof body.username === 'string' ? body.username.trim() : '';
          const password = typeof body.password === 'string' ? body.password : '';
          if (!EMAIL_PATTERN.test(email)) {
            throw new HttpError(422, 'INVALID_EMAIL', 'email must be a valid email address.', {
              field: 'email',
            });
          }
          if (!USERNAME_PATTERN.test(username)) {
            throw new HttpError(
              422,
              'INVALID_USERNAME',
              'Username must match ^[A-Za-z0-9_]{3,20}$.',
              {
                field: 'username',
              },
            );
          }
          if (password.length < 8 || new TextEncoder().encode(password).length > 72) {
            throw new HttpError(
              422,
              'INVALID_PASSWORD',
              'password must be at least 8 characters.',
              {
                field: 'password',
              },
            );
          }
          if (this.accounts.some((a) => a.email.toLowerCase() === email.toLowerCase())) {
            throw new HttpError(409, 'EMAIL_TAKEN', `Email '${email}' is already registered.`, {
              field: 'email',
            });
          }
          if (this.accounts.some((a) => a.username.toLowerCase() === username.toLowerCase())) {
            throw new HttpError(
              409,
              'USERNAME_TAKEN',
              `Username '${username}' is already registered.`,
              {
                field: 'username',
              },
            );
          }
          const createdAt = new Date().toISOString();
          const account: MockAccount = {
            user_id: Math.max(...this.accounts.map((a) => a.user_id)) + 1,
            username,
            email,
            password,
            avatar_url: AVATAR_PRESETS[0]?.avatar_url ?? '',
            level: 1,
            wins: 0,
            losses: 0,
            matches_played: 0,
            is_online: true,
            created_at: createdAt,
          };
          this.accounts.push(account);
          return created({
            user: { user_id: account.user_id, username, email, created_at: createdAt },
            access_token_expires_at: this.startSession(account.user_id),
          } satisfies RegisterResponse);
        },
        {
          'validation-error': () =>
            new HttpError(422, 'INVALID_USERNAME', 'Username must match ^[A-Za-z0-9_]{3,20}$.', {
              field: 'username',
            }),
          conflict: () =>
            new HttpError(
              409,
              'EMAIL_TAKEN',
              "Email 'player.one@example.com' is already registered.",
              {
                field: 'email',
              },
            ),
        },
      ),

      route(
        'login',
        'POST',
        /^\/v1\/auth\/login$/,
        false,
        ({ options }) => {
          const body = bodyOf<{ email: string; password: string }>(options);
          const email = typeof body.email === 'string' ? body.email.trim().toLowerCase() : '';
          const account = this.accounts.find((a) => a.email.toLowerCase() === email);
          if (!account || account.password !== body.password) {
            throw new HttpError(401, 'INVALID_CREDENTIALS', 'Email or password is incorrect.');
          }
          return ok({
            user: { user_id: account.user_id, username: account.username, email: account.email },
            access_token_expires_at: this.startSession(account.user_id),
          } satisfies LoginResponse);
        },
        {
          unauthorized: () =>
            new HttpError(401, 'INVALID_CREDENTIALS', 'Email or password is incorrect.'),
          'validation-error': () =>
            new HttpError(400, 'VALIDATION_ERROR', 'Request body must be valid JSON.', {}),
        },
      ),

      route(
        'refresh',
        'POST',
        /^\/v1\/auth\/refresh$/,
        false,
        () => {
          if (this.sessionUserId === null || !this.refreshValid) {
            throw new HttpError(
              401,
              'SESSION_EXPIRED',
              'The refresh token is invalid or expired. Log in again.',
            );
          }
          this.accessValid = true;
          return ok({ access_token_expires_at: this.expiry() } satisfies RefreshResponse);
        },
        {
          unauthorized: () =>
            new HttpError(
              401,
              'SESSION_EXPIRED',
              'The refresh token is invalid or expired. Log in again.',
            ),
        },
      ),

      route('session', 'GET', /^\/v1\/auth\/session$/, true, () => {
        const account = this.self();
        return ok({
          user: { user_id: account.user_id, username: account.username },
          active_room_id: this.activeRoomId(account.user_id),
          active_room_api_version: this.activeRoomId(account.user_id) === null ? null : 'v2',
          session_expires_at: this.expiry(),
        } satisfies SessionResponse);
      }),

      route('endSession', 'DELETE', /^\/v1\/auth\/session$/, true, () => {
        const userId = selfId();
        const roomId = this.activeRoomId(userId);
        // Logout leaves the room; during a match it counts as leaving it.
        if (roomId !== null) mockRooms.get(roomId)?.playerLeave(userId, 'SESSION_ENDED');
        this.sessionUserId = null;
        this.refreshValid = false;
        return noContent;
      }),

      /* --------------------------- users and profile ------------------------- */

      route('getOwnProfile', 'GET', /^\/v1\/users\/me$/, true, () => {
        const account = this.self();
        return ok(toOwnProfile(account, this.activeRoomId(account.user_id)) satisfies OwnProfile);
      }),

      route(
        'updateOwnProfile',
        'PATCH',
        /^\/v1\/users\/me$/,
        true,
        ({ options }) => {
          const account = this.self();
          const { username } = bodyOf<{ username: string }>(options);
          if (username !== undefined) {
            const next = typeof username === 'string' ? username.trim() : '';
            if (!USERNAME_PATTERN.test(next)) {
              throw new HttpError(
                422,
                'INVALID_USERNAME',
                'Username must match ^[A-Za-z0-9_]{3,20}$.',
                {
                  field: 'username',
                },
              );
            }
            const taken = this.accounts.some(
              (a) =>
                a.user_id !== account.user_id && a.username.toLowerCase() === next.toLowerCase(),
            );
            if (taken) {
              throw new HttpError(
                409,
                'USERNAME_TAKEN',
                `Username '${next}' is already registered.`,
                {
                  field: 'username',
                },
              );
            }
            if (next !== account.username) {
              account.username = next;
              this.profileChanged(account, { username: next });
            }
          }
          return ok(toOwnProfile(account, this.activeRoomId(account.user_id)));
        },
        {
          'validation-error': () =>
            new HttpError(422, 'INVALID_USERNAME', 'Username must match ^[A-Za-z0-9_]{3,20}$.', {
              field: 'username',
            }),
          conflict: () =>
            new HttpError(409, 'USERNAME_TAKEN', "Username 'red_agent' is already registered.", {
              field: 'username',
            }),
        },
      ),

      route(
        'deleteOwnAccount',
        'DELETE',
        /^\/v1\/users\/me$/,
        true,
        ({ options }) => {
          const account = this.self();
          const { confirm_username: typed } = bodyOf<{ confirm_username: string }>(options);
          if (
            typeof typed !== 'string' ||
            typed.trim().toLowerCase() !== account.username.toLowerCase()
          ) {
            throw new HttpError(
              422,
              'CONFIRMATION_MISMATCH',
              'confirm_username must match your current username.',
              { field: 'confirm_username' },
            );
          }
          // Leaving the room first, as the server does, so the others see the departure.
          const roomId = this.activeRoomId(account.user_id);
          if (roomId !== null)
            mockRooms.get(roomId)?.playerLeave(account.user_id, 'ACCOUNT_DELETED');
          this.accounts = this.accounts.filter((a) => a.user_id !== account.user_id);
          this.friendIds = new Set();
          this.blockedIds = new Set();
          this.blockedByIds = new Set();
          this.requests = new Map();
          this.sessionUserId = null;
          this.refreshValid = false;
          return noContent;
        },
        {
          'validation-error': () =>
            new HttpError(
              422,
              'CONFIRMATION_MISMATCH',
              'confirm_username must match your current username.',
              { field: 'confirm_username' },
            ),
        },
      ),

      route(
        'reportUser',
        'POST',
        /^\/v1\/users\/(\d+)\/reports$/,
        true,
        ({ params, options }) => {
          const reporterId = selfId();
          const targetId = Number(params[0]);
          if (!this.accounts.some((a) => a.user_id === targetId)) {
            throw new HttpError(404, 'USER_NOT_FOUND', `User ${targetId} was not found.`, {
              user_id: targetId,
            });
          }
          if (targetId === reporterId) {
            throw new HttpError(422, 'CANNOT_REPORT_SELF', 'You cannot report yourself.', {
              user_id: targetId,
            });
          }
          const body = bodyOf<{ reason: string; details: string; room_id: number }>(options);
          const reason = REPORT_REASONS.find((value) => value === body.reason);
          if (!reason)
            throw validation(`reason must be one of ${REPORT_REASONS.join(', ')}.`, 'reason');
          if (body.details !== undefined) {
            if (
              typeof body.details !== 'string' ||
              body.details.trim().length > REPORT_DETAILS_MAX
            ) {
              throw validation(
                `details must be at most ${REPORT_DETAILS_MAX} characters.`,
                'details',
              );
            }
          }
          if (body.room_id !== undefined && !isPositiveInt(body.room_id)) {
            throw validation('room_id must be a positive integer.', 'room_id');
          }
          const last = this.reports.get(targetId);
          if (last !== undefined && Date.now() - last < REPORT_WINDOW_MS) {
            throw new HttpError(
              409,
              'ALREADY_REPORTED',
              `You already reported user ${targetId} recently.`,
              {
                user_id: targetId,
                retry_after_seconds: Math.ceil((REPORT_WINDOW_MS - (Date.now() - last)) / 1000),
              },
            );
          }
          const createdAt = new Date();
          this.reports.set(targetId, createdAt.getTime());
          return created({
            report_id: this.nextReportId++,
            reported_user_id: targetId,
            reason,
            created_at: createdAt.toISOString(),
          } satisfies ReportUserResponse);
        },
        {
          'not-found': () =>
            new HttpError(404, 'USER_NOT_FOUND', 'User 9999 was not found.', { user_id: 9999 }),
          conflict: () =>
            new HttpError(409, 'ALREADY_REPORTED', 'You already reported user 44 recently.', {
              user_id: 44,
              retry_after_seconds: 82800,
            }),
          'validation-error': () =>
            new HttpError(422, 'CANNOT_REPORT_SELF', 'You cannot report yourself.', {
              user_id: 42,
            }),
        },
      ),

      route('searchUsers', 'GET', /^\/v1\/users\/search$/, true, ({ options }) => {
        const q = String(options.query?.q ?? '').trim();
        if (q.length < 1 || q.length > 20) throw validation('q must be 1..20 characters.', 'q');
        const limit = Number(options.query?.limit ?? 20);
        const offset = Number(options.query?.offset ?? 0);
        if (!Number.isInteger(limit) || limit < 1 || limit > 50) {
          throw validation('limit must be an integer from 1 through 50.', 'limit');
        }
        if (!Number.isInteger(offset) || offset < 0) {
          throw validation('offset must be a non-negative integer.', 'offset');
        }
        const me = selfId();
        const needle = q.toLowerCase();
        const matches = this.accounts
          .filter((a) => a.user_id !== me && a.username.toLowerCase().includes(needle))
          .sort((a, b) => {
            const prefix =
              Number(!a.username.toLowerCase().startsWith(needle)) -
              Number(!b.username.toLowerCase().startsWith(needle));
            return (
              prefix ||
              a.username.toLowerCase().localeCompare(b.username.toLowerCase()) ||
              a.user_id - b.user_id
            );
          });
        const results = matches.slice(offset, offset + limit).map((a) => ({
          user_id: a.user_id,
          username: a.username,
          avatar_url: a.avatar_url,
          ...this.relationshipFields(a.user_id),
        }));
        return ok({
          results,
          limit,
          offset,
          has_more: offset + results.length < matches.length,
        } satisfies UserSearchResponse);
      }),

      route('matchHistory', 'GET', /^\/v1\/users\/me\/matches$/, true, ({ options }) => {
        const limit = Number(options.query?.limit ?? 20);
        if (!Number.isInteger(limit) || limit < 1 || limit > 50) {
          throw validation('limit must be an integer from 1 through 50.', 'limit');
        }
        const before = options.query?.before;
        const matches = SEED_MATCHES.filter(
          (m) => before === undefined || m.finished_at < String(before),
        ).slice(0, limit);
        return ok({ matches } satisfies MatchHistoryResponse);
      }),

      route(
        'getPublicProfile',
        'GET',
        /^\/v1\/users\/([^/]+)$/,
        true,
        ({ params }) => {
          const userId = Number(params[0]);
          if (!Number.isInteger(userId) || userId <= 0) {
            throw validation('user_id must be a positive integer.', 'user_id');
          }
          const account = this.accounts.find((a) => a.user_id === userId);
          if (!account) {
            throw new HttpError(404, 'USER_NOT_FOUND', `User ${userId} was not found.`, {
              user_id: userId,
            });
          }
          return ok(
            toPublicProfile(account, this.relationshipFields(userId)) satisfies PublicProfile,
          );
        },
        {
          'not-found': () =>
            new HttpError(404, 'USER_NOT_FOUND', 'User 9999 was not found.', { user_id: 9999 }),
          'validation-error': () => validation('user_id must be a positive integer.', 'user_id'),
        },
      ),

      route(
        'uploadAvatar',
        'POST',
        /^\/v1\/users\/me\/avatar$/,
        true,
        ({ options }) => {
          const file = options.formData?.get('avatar');
          if (
            !(file instanceof Blob) ||
            !AVATAR_TYPES.includes(file.type) ||
            file.size > AVATAR_MAX_BYTES
          ) {
            throw new HttpError(
              422,
              'INVALID_IMAGE',
              'Avatar must be image/jpeg, image/png, or image/webp under 2 MiB.',
            );
          }
          const account = this.self();
          account.avatar_url = URL.createObjectURL(file);
          this.profileChanged(account, { avatar_url: account.avatar_url });
          return ok({ avatar_url: account.avatar_url } satisfies UploadAvatarResponse);
        },
        {
          'validation-error': () =>
            new HttpError(
              422,
              'INVALID_IMAGE',
              'Avatar must be image/jpeg, image/png, or image/webp under 2 MiB.',
            ),
        },
      ),

      route('listAvatarPresets', 'GET', /^\/v1\/avatars\/presets$/, true, () =>
        ok({ presets: AVATAR_PRESETS } satisfies AvatarPresetListResponse),
      ),

      route(
        'selectAvatarPreset',
        'PUT',
        /^\/v1\/users\/me\/avatar$/,
        true,
        ({ options }) => {
          const { preset_id: presetId } = bodyOf<{ preset_id: string }>(options);
          const preset = AVATAR_PRESETS.find((p) => p.preset_id === presetId);
          if (!preset) {
            throw new HttpError(
              422,
              'INVALID_AVATAR_PRESET',
              `Avatar preset '${String(presetId)}' does not exist.`,
              { field: 'preset_id' },
            );
          }
          const account = this.self();
          account.avatar_url = preset.avatar_url;
          this.profileChanged(account, { avatar_url: preset.avatar_url });
          return ok({ avatar_url: preset.avatar_url } satisfies UploadAvatarResponse);
        },
        {
          'validation-error': () =>
            new HttpError(422, 'INVALID_AVATAR_PRESET', "Avatar preset 'dragon' does not exist.", {
              field: 'preset_id',
            }),
        },
      ),

      /* --------------------------------- friends ----------------------------- */

      route('listFriends', 'GET', /^\/v1\/friends$/, true, ({ options }) => {
        const limit = Number(options.query?.limit ?? 50);
        const offset = Number(options.query?.offset ?? 0);
        if (!Number.isInteger(limit) || limit < 1 || limit > 100) {
          throw validation('limit must be an integer from 1 through 100.', 'limit');
        }
        if (!Number.isInteger(offset) || offset < 0) {
          throw validation('offset must be a non-negative integer.', 'offset');
        }
        const all: Friend[] = this.accounts
          .filter((a) => this.isActiveFriend(a.user_id))
          .sort(
            (a, b) =>
              a.username.toLowerCase().localeCompare(b.username.toLowerCase()) ||
              a.user_id - b.user_id,
          )
          .map((a) => ({
            user_id: a.user_id,
            username: a.username,
            avatar_url: a.avatar_url,
            is_online: a.is_online,
          }));
        return ok({
          friend_count: all.length,
          limit,
          offset,
          has_more: offset + limit < all.length,
          friends: all.slice(offset, offset + limit),
        } satisfies FriendListResponse);
      }),

      route('listFriendRequests', 'GET', /^\/v1\/friends\/requests$/, true, () => {
        const me = selfId();
        const newest = (a: MockFriendRequest, b: MockFriendRequest) =>
          b.created_at.localeCompare(a.created_at) || b.request_id - a.request_id;
        const all = [...this.requests.values()].sort(newest);
        return ok({
          incoming: all.filter((r) => r.to_user_id === me).map((r) => this.toRequest(r)),
          outgoing: all.filter((r) => r.from_user_id === me).map((r) => this.toRequest(r)),
        } satisfies FriendRequestListResponse);
      }),

      route(
        'sendFriendRequest',
        'POST',
        /^\/v1\/friends\/requests$/,
        true,
        ({ options }) => {
          const me = selfId();
          const { user_id: rawId } = bodyOf<{ user_id: number }>(options);
          if (rawId === me)
            throw validation('You cannot send a friend request to yourself.', 'user_id');
          const userId = this.requireUser(rawId).user_id;
          if (this.blockedIds.has(userId) || this.blockedByIds.has(userId))
            throw blockedError(userId);
          if (this.isActiveFriend(userId)) {
            throw new HttpError(409, 'ALREADY_FRIENDS', `User ${userId} is already a friend.`, {
              user_id: userId,
            });
          }
          const sent = this.pendingBetween(me, userId);
          if (sent) {
            throw new HttpError(
              409,
              'REQUEST_ALREADY_SENT',
              `A friend request to user ${userId} is already pending.`,
              { user_id: userId, request_id: sent.request_id },
            );
          }
          const received = this.pendingBetween(userId, me);
          if (received) {
            throw new HttpError(
              409,
              'REQUEST_ALREADY_RECEIVED',
              `User ${userId} already sent you a friend request.`,
              { user_id: userId, request_id: received.request_id },
            );
          }
          const request = this.toRequest(this.addRequest(me, userId));
          mockChat.notify('friend.request.received', { request });
          return created(request);
        },
        {
          forbidden: () => blockedError(45),
          'not-found': () =>
            new HttpError(404, 'USER_NOT_FOUND', 'User 9999 was not found.', { user_id: 9999 }),
          conflict: () =>
            new HttpError(
              409,
              'REQUEST_ALREADY_SENT',
              'A friend request to user 43 is already pending.',
              {
                user_id: 43,
                request_id: 701,
              },
            ),
          'validation-error': () =>
            validation('You cannot send a friend request to yourself.', 'user_id'),
        },
      ),

      route(
        'acceptFriendRequest',
        'POST',
        /^\/v1\/friends\/requests\/([^/]+)\/accept$/,
        true,
        ({ params }) => {
          const requestId = Number(params[0]);
          if (!isPositiveInt(requestId))
            throw validation('request_id must be a positive integer.', 'request_id');
          const request = this.requests.get(requestId);
          if (!request || request.to_user_id !== selfId()) throw requestNotFound(requestId);
          const other = request.from_user_id;
          if (this.blockedIds.has(other) || this.blockedByIds.has(other)) throw blockedError(other);
          return ok(this.resolveAccepted(request) satisfies Friend);
        },
        { 'not-found': () => requestNotFound(702) },
      ),

      route(
        'declineFriendRequest',
        'POST',
        /^\/v1\/friends\/requests\/([^/]+)\/decline$/,
        true,
        ({ params }) => {
          const requestId = Number(params[0]);
          if (!isPositiveInt(requestId))
            throw validation('request_id must be a positive integer.', 'request_id');
          const request = this.requests.get(requestId);
          if (!request || request.to_user_id !== selfId()) throw requestNotFound(requestId);
          this.resolve(request, 'DECLINED');
          return noContent;
        },
        { 'not-found': () => requestNotFound(702) },
      ),

      route(
        'cancelFriendRequest',
        'DELETE',
        /^\/v1\/friends\/requests\/([^/]+)$/,
        true,
        ({ params }) => {
          const requestId = Number(params[0]);
          if (!isPositiveInt(requestId))
            throw validation('request_id must be a positive integer.', 'request_id');
          const request = this.requests.get(requestId);
          if (!request || request.from_user_id !== selfId()) {
            throw new HttpError(
              404,
              'REQUEST_NOT_FOUND',
              `Friend request ${requestId} is not pending from you.`,
              { request_id: requestId },
            );
          }
          this.resolve(request, 'CANCELLED');
          return noContent;
        },
        { 'not-found': () => requestNotFound(701) },
      ),

      route(
        'removeFriend',
        'DELETE',
        /^\/v1\/friends\/([^/]+)$/,
        true,
        ({ params }) => {
          const userId = Number(params[0]);
          if (!this.isActiveFriend(userId)) {
            throw new HttpError(404, 'NOT_FRIENDS', `User ${params[0]} is not a friend.`, {
              user_id: userId,
            });
          }
          this.friendIds.delete(userId);
          mockChat.notify('friend.removed', { user_id: userId });
          mockChat.friendshipChanged(userId, false);
          return noContent;
        },
        {
          'not-found': () =>
            new HttpError(404, 'NOT_FRIENDS', 'User 9999 is not a friend.', { user_id: 9999 }),
        },
      ),

      /* -------------------------------- blocking ----------------------------- */

      route(
        'blockUser',
        'PUT',
        /^\/v1\/users\/([^/]+)\/block$/,
        true,
        ({ params }) => {
          const userId = Number(params[0]);
          if (userId === selfId()) throw validation('You cannot block yourself.', 'user_id');
          this.requireUser(userId);
          if (!this.blockedIds.has(userId)) {
            this.applyBlock(userId, () => this.blockedIds.add(userId), 'USER_BLOCKED');
          }
          return ok({
            user_id: userId,
            blocked_at: new Date().toISOString(),
          } satisfies BlockResponse);
        },
        {
          'not-found': () =>
            new HttpError(404, 'USER_NOT_FOUND', 'User 9999 was not found.', { user_id: 9999 }),
          'validation-error': () => validation('You cannot block yourself.', 'user_id'),
        },
      ),

      route(
        'unblockUser',
        'DELETE',
        /^\/v1\/users\/([^/]+)\/block$/,
        true,
        ({ params }) => {
          const userId = Number(params[0]);
          if (userId === selfId()) throw validation('You cannot unblock yourself.', 'user_id');
          this.requireUser(userId);
          this.liftBlock(userId, () => this.blockedIds.delete(userId));
          return noContent;
        },
        {
          'not-found': () =>
            new HttpError(404, 'USER_NOT_FOUND', 'User 9999 was not found.', { user_id: 9999 }),
        },
      ),

      /* ---------------------------------- rooms ------------------------------ */

      route(
        'createRoom',
        'POST',
        roomPattern(''),
        true,
        ({ options }) => {
          const me = selfId();
          const { max_players: requested } = bodyOf<{ max_players: number }>(options);
          const maxPlayers = requested ?? ROOM_CAPACITY.default;
          if (
            !Number.isInteger(maxPlayers) ||
            maxPlayers < ROOM_CAPACITY.min ||
            maxPlayers > ROOM_CAPACITY.max
          ) {
            throw validation(
              `max_players must be an integer from ${ROOM_CAPACITY.min} through ${ROOM_CAPACITY.max}.`,
              'max_players',
            );
          }
          const active = this.activeRoomId(me);
          if (active !== null) {
            throw new HttpError(
              409,
              'ALREADY_IN_ROOM',
              `User ${me} already belongs to active room ${active}.`,
              {
                room_id: active,
              },
            );
          }
          const code = this.newRoomCode();
          const server = this.addRoom(code, maxPlayers, [this.selfPlayer()]);
          return created(server.snapshot() satisfies Room);
        },
        {
          conflict: () =>
            new HttpError(409, 'ALREADY_IN_ROOM', 'User 42 already belongs to active room 1001.', {
              room_id: 1001,
            }),
          'validation-error': () =>
            validation('max_players must be an integer from 4 through 20.', 'max_players'),
        },
      ),

      route(
        'lookupRoom',
        'GET',
        roomPattern('/lookup/([^/]+)'),
        true,
        ({ params }) => {
          const code = decodeURIComponent(params[0] ?? '');
          if (!LOOKUP_CODE_PATTERN.test(code)) {
            throw validation('room_code must match ^[A-Za-z0-9]{6}$.', 'room_code');
          }
          const entry = this.roomByCode(code);
          const room = entry?.server.state();
          if (!entry || !room || room.status === 'CLOSED') {
            throw new HttpError(404, 'ROOM_NOT_FOUND', `No active room matches code ${code}.`, {
              room_code: code,
            });
          }
          const full = room.player_count >= room.max_players;
          return ok({
            room_id: room.room_id,
            room_code: entry.code,
            status: room.status,
            player_count: room.player_count,
            max_players: room.max_players,
            joinable: !full && entry.server.joinable(),
          } satisfies RoomLookupResponse);
        },
        {
          'not-found': () =>
            new HttpError(404, 'ROOM_NOT_FOUND', 'No active room matches code ZZZZZZ.', {
              room_code: 'ZZZZZZ',
            }),
          'validation-error': () =>
            validation('room_code must match ^[A-Za-z0-9]{6}$.', 'room_code'),
        },
      ),

      route(
        'joinRoom',
        'POST',
        roomPattern('/([^/]+)/members'),
        true,
        ({ params }) => {
          const roomId = this.parseRoomId(params[0]);
          const server = this.requireRoom(roomId);
          const me = this.selfPlayer();
          const active = this.activeRoomId(me.user_id);
          if (active !== null) {
            throw new HttpError(
              409,
              'ALREADY_IN_ROOM',
              `User ${me.user_id} already belongs to active room ${active}.`,
              { room_id: active },
            );
          }
          const room = server.state();
          if (!server.joinable()) throw notJoinable(room);
          if (room.player_count >= room.max_players) throw roomFull(room);
          try {
            server.playerJoin(me);
          } catch (error) {
            if (error instanceof MockActionError && error.code === 'ROOM_FULL')
              throw roomFull(room);
            if (error instanceof MockActionError) throw notJoinable(room);
            throw error;
          }
          return created(server.snapshot() satisfies Room);
        },
        {
          'not-found': () => roomNotFound(9999),
          conflict: () =>
            new HttpError(409, 'ROOM_FULL', 'Room 1001 is full (8 of 8 players).', {
              room_id: 1001,
              player_count: 8,
              max_players: 8,
            }),
          'validation-error': () => validation('room_id must be a positive integer.', 'room_id'),
        },
      ),

      route(
        'getRoom',
        'GET',
        roomPattern('/([^/]+)'),
        true,
        ({ params }) => {
          const roomId = this.parseRoomId(params[0]);
          const server = this.requireRoom(roomId);
          if (!server.hasMember(selfId())) throw notMember(roomId, selfId());
          return ok(server.snapshot() satisfies Room);
        },
        {
          'not-found': () => roomNotFound(9999),
          forbidden: () => notMember(1001, 47),
          'validation-error': () => validation('room_id must be a positive integer.', 'room_id'),
        },
      ),

      route(
        'leaveRoom',
        'DELETE',
        roomPattern('/([^/]+)/members/me'),
        true,
        ({ params }) => {
          const roomId = this.parseRoomId(params[0]);
          const server = this.requireRoom(roomId);
          if (!server.hasMember(selfId())) throw notMember(roomId, selfId());
          // IN_GAME is not an error: the server rechecks both teams' staffing.
          server.playerLeave(selfId(), 'EXITED');
          return noContent;
        },
        {
          'not-found': () => roomNotFound(9999),
          forbidden: () => notMember(1001, 47),
        },
      ),

      route(
        'inviteFriend',
        'POST',
        roomPattern('/([^/]+)/invites'),
        true,
        ({ params, options }) => {
          const roomId = this.parseRoomId(params[0]);
          const me = selfId();
          const { user_id: userId } = bodyOf<{ user_id: number }>(options);
          if (!isPositiveInt(userId) || userId === me) {
            throw validation('user_id must be a positive integer other than your own.', 'user_id');
          }
          const server = this.requireRoom(roomId);
          if (!server.hasMember(me)) throw notMember(roomId, me);
          const friend = this.accounts.find((a) => a.user_id === userId);
          if (!friend) {
            throw new HttpError(404, 'USER_NOT_FOUND', `User ${userId} was not found.`, {
              user_id: userId,
            });
          }
          if (!this.isActiveFriend(userId)) {
            throw new HttpError(404, 'NOT_FRIENDS', `User ${userId} is not a friend.`, {
              user_id: userId,
            });
          }
          const room = server.state();
          if (room.status !== 'WAITING' || !server.joinable()) throw notJoinable(room);
          if (room.player_count >= room.max_players) throw roomFull(room);
          if (!friend.is_online) {
            throw new HttpError(
              409,
              'USER_OFFLINE',
              `User ${userId} has no active Chat Gateway v2 connection.`,
              { user_id: userId },
            );
          }
          if (this.activeRoomId(userId) !== null) {
            throw new HttpError(
              409,
              'USER_IN_ROOM',
              `User ${userId} already belongs to an active room.`,
              {
                user_id: userId,
              },
            );
          }
          const entry = this.rooms.get(roomId);
          const acceptedAt = new Date();
          return accepted({
            invite_id: `inv_${String(this.nextInviteId++).padStart(4, '0')}`,
            room_id: roomId,
            room_code: entry?.code ?? room.room_code,
            to_user_id: userId,
            accepted_at: acceptedAt.toISOString(),
            expires_at: new Date(acceptedAt.getTime() + 30_000).toISOString(),
          } satisfies InviteFriendResponse);
        },
        {
          forbidden: () => notMember(1001, 47),
          'not-found': () =>
            new HttpError(404, 'NOT_FRIENDS', 'User 9999 is not a friend.', { user_id: 9999 }),
          conflict: () =>
            new HttpError(
              409,
              'USER_OFFLINE',
              'User 43 has no active Chat Gateway v2 connection.',
              {
                user_id: 43,
              },
            ),
          'validation-error': () => validation('user_id must be a positive integer.', 'user_id'),
        },
      ),

      /* ------------------------------ Chat REST v2 --------------------------- */

      route('listChannels', 'GET', chatPattern(''), true, ({ options }) =>
        ok(mockChat.listChannels({ limit: options.query?.limit, after: options.query?.after })),
      ),

      route(
        'openDirectChannel',
        'POST',
        chatPattern('/direct'),
        true,
        ({ options }) => {
          const { peer_user_id: peerUserId } = bodyOf<{ peer_user_id: number }>(options);
          const { created: isNew, data } = mockChat.openDirect(peerUserId);
          return isNew ? created(data) : ok(data);
        },
        {
          forbidden: () =>
            new HttpError(403, 'NOT_PERMITTED', 'Direct channels are available to friends only.', {
              peer_user_id: 99,
            }),
          'not-found': () =>
            new HttpError(404, 'USER_NOT_FOUND', 'User 9999 was not found.', { user_id: 9999 }),
          'validation-error': () =>
            validation('peer_user_id must be a positive integer.', 'peer_user_id'),
        },
      ),

      route(
        'messageHistory',
        'GET',
        chatPattern('/([^/]+)/messages'),
        true,
        ({ params, options }) => {
          const channelId = Number(params[0]);
          if (!Number.isInteger(channelId) || channelId <= 0) {
            throw validation('channel_id must be a positive integer.', 'channel_id');
          }
          return ok(
            mockChat.history(channelId, {
              limit: options.query?.limit,
              before: options.query?.before,
            }),
          );
        },
        {
          forbidden: () =>
            new HttpError(403, 'CHANNEL_ACCESS_REVOKED', 'Access to channel 7001 has ended.', {
              channel_id: 7001,
            }),
          'not-found': () =>
            new HttpError(404, 'CHANNEL_NOT_FOUND', 'Channel 9999 was not found.', {
              channel_id: 9999,
            }),
        },
      ),
    ];
  }
}
