/** Lifecycle of one managed socket, shared by the room socket and the notification socket. */
export type ConnectionStatus =
  | 'IDLE'
  | 'CONNECTING'
  | 'OPEN'
  | 'RECONNECTING'
  /** Closed deliberately, or closed in a way retrying cannot fix. */
  | 'CLOSED';

/**
 * Why a socket stopped. `SESSION_INVALID` maps to close code 4401 and means the
 * session was explicitly revoked — the client must re-authenticate, not retry.
 */
export type ConnectionCloseReason =
  | 'CLIENT_DISCONNECT'
  | 'TRANSPORT_DROP'
  | 'SESSION_INVALID'
  | 'ROOM_NOT_FOUND'
  | 'NOT_ROOM_MEMBER'
  | 'UNKNOWN';
