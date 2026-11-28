/**
 * Central runtime configuration.
 *
 * The deployment model is single-origin: one reverse proxy serves the SPA and
 * forwards `/api` and `/ws` to the Gateway. That is what lets the browser attach the
 * HttpOnly `ft_session` cookie to
 * REST calls and to the WebSocket upgrade without any cross-origin configuration.
 *
 * Consequently: paths are relative, no production domain is hardcoded, and no secret
 * belongs in this file or in any `VITE_*` variable (they are inlined into the bundle).
 */

/** Same-origin REST prefix. Every REST call is issued against this path. */
export const API_BASE_PATH = import.meta.env.VITE_API_BASE_PATH ?? '/api';

/** Same-origin WebSocket prefix. */
export const WS_BASE_PATH = import.meta.env.VITE_WS_BASE_PATH ?? '/ws';

/**
 * Version segment of the shared v1 contract: Auth (`/api/v1/auth/*`), profiles, avatars,
 * search, reports, blocks, friends and match history. The refresh cookie is scoped to
 * `/api/v1/auth/refresh`, so these calls must keep the version in their path.
 */
export const CORE_API_PATH = '/v1';

/**
 * Game v2 version segment. Rooms and games use one contract as a pair: REST at
 * `/api/v2/rooms` with the room socket at `/ws/v2/rooms/{room_id}`. A session never mixes
 * it with the v1 room contract.
 */
export const GAME_API_VERSION = 'v2';

/** Room REST prefix, relative to `API_BASE_PATH`. */
export const ROOM_API_PATH = `/${GAME_API_VERSION}/rooms`;

/** Per-room socket carrying both `room.*` and `game.*` events. */
export const wsRoomPath = (roomId: number): string =>
  `${WS_BASE_PATH}/${GAME_API_VERSION}/rooms/${roomId}`;

/**
 * Chat v2 version segment. Chat is its own versioned contract (`chatApiVersion`): its
 * REST calls go to `/api/v2/channels` and its socket to Chat Gateway at `/ws/v2/channels`.
 */
export const CHAT_API_VERSION = 'v2';

/** Chat REST prefix, relative to `API_BASE_PATH`. */
export const CHAT_API_PATH = `/${CHAT_API_VERSION}/channels`;

/** Per-user Chat Gateway socket, independent of any room. */
export const WS_CHAT_PATH = `${WS_BASE_PATH}/${CHAT_API_VERSION}/channels`;

/** Absolute `ws(s)://` URL for a same-origin socket path. */
export function toWebSocketUrl(path: string): string {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${protocol}//${window.location.host}${path}`;
}

export const IS_DEV = import.meta.env.DEV;

/** Development-only switches. Keep these off by default in production builds. */
export const devFlags = {
  /** Log every inbound/outbound WebSocket frame. */
  logWebSocketTraffic: IS_DEV,
  /** Log normalized REST failures. */
  logApiErrors: IS_DEV,
} as const;

/**
 * Reconnect backoff bounds. The server-side grace period is 30s in
 * `WAITING`/`COUNTDOWN` and 60s `IN_GAME`, so retries must stay well inside that.
 */
export const RECONNECT = {
  initialDelayMs: 500,
  maxDelayMs: 5_000,
  /** `null` means keep retrying; the grace period decides the real deadline. */
  maxAttempts: null as number | null,
  /** An open room socket that sends no `room.state` within this time is reconnected. */
  snapshotTimeoutMs: 10_000,
  /**
   * While the room socket retries, at most one REST room snapshot per interval checks
   * what a failed handshake cannot report: a room that is gone, a membership that ended
   * or a session that needs a refresh.
   */
  probeIntervalMs: 3_000,
  /** How long a first connection may keep failing before the room offers Try again. */
  firstConnectTimeoutMs: 20_000,
  /** A drop shorter than this recovers without flashing the Reconnecting overlay. */
  overlayDelayMs: 400,
} as const;

/**
 * The seat the server holds for a dropped player: 30s by default in `WAITING`/`COUNTDOWN`
 * and 60s `IN_GAME`. After it the server removes the member (and, during a match, checks
 * whether both teams are still staffed), so the client stops trying and reports the room
 * as lost. The client only counts from its own drop; the server's timer is the one that
 * decides. A result screen has no such seat: its decision deadline runs on regardless.
 */
export const GRACE_PERIOD_MS = {
  room: 30_000,
  game: 60_000,
} as const;
