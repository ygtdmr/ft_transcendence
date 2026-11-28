/**
 * Transport foundation shared by the room socket and the notification socket.
 *
 * Authentication is cookie-only: the HttpOnly `ft_session`
 * cookie is attached by the browser on a same-origin upgrade. This module therefore
 * never reads a cookie, never appends a query-string token, and never sets a
 * handshake header — the `WebSocket` constructor takes a path and nothing else.
 *
 * It owns transport concerns only: connect, disconnect, backoff reconnect, JSON
 * parsing and `request_id` correlation. Interpreting any `room.*`/`game.*`/social
 * message is the caller's job.
 */

import { RECONNECT, devFlags, toWebSocketUrl } from '@shared/constants';
import {
  WS_CLOSE_NOT_ROOM_MEMBER,
  WS_CLOSE_ROOM_NOT_FOUND,
  WS_CLOSE_UNAUTHORIZED,
} from '@shared/types';

import type { ConnectionCloseReason, ConnectionStatus } from './connectionState';
import type { SocketTransport } from './transport';

export interface ManagedSocketHandlers<TInbound> {
  onStatusChange?: (status: ConnectionStatus, reason?: ConnectionCloseReason) => void;
  /** Every successfully parsed inbound frame. */
  onMessage?: (message: TInbound) => void;
  /** A frame that was not valid JSON. The socket stays open. */
  onParseError?: (raw: string, cause: unknown) => void;
}

export interface ManagedSocketOptions<TInbound> extends ManagedSocketHandlers<TInbound> {
  /** Same-origin path, e.g. `/ws/rooms/1001`. Never an absolute cross-origin URL. */
  path: string;
  /** Label used in development logging only. */
  name: string;
}

function closeReasonFor(code: number, wasClean: boolean): ConnectionCloseReason {
  switch (code) {
    case WS_CLOSE_UNAUTHORIZED:
      return 'SESSION_INVALID';
    case WS_CLOSE_ROOM_NOT_FOUND:
      return 'ROOM_NOT_FOUND';
    case WS_CLOSE_NOT_ROOM_MEMBER:
      return 'NOT_ROOM_MEMBER';
    default:
      return wasClean ? 'CLIENT_DISCONNECT' : 'TRANSPORT_DROP';
  }
}

/** Only a transport drop is worth retrying; an authorization close is not. */
function isRetryable(reason: ConnectionCloseReason): boolean {
  return reason === 'TRANSPORT_DROP' || reason === 'UNKNOWN';
}

export class ManagedSocket<TInbound, TOutbound> implements SocketTransport<TOutbound> {
  private socket: WebSocket | null = null;
  private status: ConnectionStatus = 'IDLE';
  private attempt = 0;
  private retryTimer: ReturnType<typeof setTimeout> | null = null;
  private closedByClient = false;

  private readonly options: ManagedSocketOptions<TInbound>;

  constructor(options: ManagedSocketOptions<TInbound>) {
    this.options = options;
  }

  getStatus(): ConnectionStatus {
    return this.status;
  }

  connect(): void {
    if (this.socket && (this.status === 'OPEN' || this.status === 'CONNECTING')) return;

    this.closedByClient = false;
    this.clearRetry();
    this.setStatus(this.attempt === 0 ? 'CONNECTING' : 'RECONNECTING');

    const socket = new WebSocket(toWebSocketUrl(this.options.path));
    this.socket = socket;

    // Every handler checks that its socket is still the current one: a socket replaced by
    // `reconnect()` or closed by `disconnect()` may still report a late close.
    socket.onopen = () => {
      if (this.socket !== socket) return;
      this.attempt = 0;
      this.setStatus('OPEN');
    };

    socket.onmessage = (event: MessageEvent<string>) => {
      if (this.socket !== socket) return;
      try {
        const parsed = JSON.parse(event.data) as TInbound;
        if (devFlags.logWebSocketTraffic) {
          console.debug(`[ws:${this.options.name}] in`, parsed);
        }
        this.options.onMessage?.(parsed);
      } catch (cause) {
        this.options.onParseError?.(event.data, cause);
      }
    };

    socket.onclose = (event: CloseEvent) => {
      if (this.socket !== socket) return;
      this.socket = null;
      const reason = this.closedByClient
        ? 'CLIENT_DISCONNECT'
        : closeReasonFor(event.code, event.wasClean);

      if (!this.closedByClient && isRetryable(reason)) {
        this.scheduleReconnect(reason);
        return;
      }
      this.setStatus('CLOSED', reason);
    };

    // `onerror` carries no useful detail in browsers; `onclose` always follows it.
    socket.onerror = () => {};
  }

  /** Close deliberately. No reconnect follows. */
  disconnect(): void {
    this.closedByClient = true;
    this.clearRetry();
    this.attempt = 0;
    this.release();
    this.setStatus('CLOSED', 'CLIENT_DISCONNECT');
  }

  reconnect(): void {
    this.clearRetry();
    this.release();
    this.attempt = Math.max(this.attempt, 1);
    this.connect();
  }

  /**
   * Send one command. Returns `false` when the socket is not open, so the caller can
   * decide whether to surface that — this layer never queues, because a queued command
   * replayed after a reconnect would race the fresh snapshot.
   */
  send(message: TOutbound): boolean {
    if (!this.socket || this.socket.readyState !== WebSocket.OPEN) return false;
    if (devFlags.logWebSocketTraffic) {
      console.debug(`[ws:${this.options.name}] out`, message);
    }
    this.socket.send(JSON.stringify(message));
    return true;
  }

  private scheduleReconnect(reason: ConnectionCloseReason): void {
    const { maxAttempts, initialDelayMs, maxDelayMs } = RECONNECT;
    if (maxAttempts !== null && this.attempt >= maxAttempts) {
      this.setStatus('CLOSED', reason);
      return;
    }

    const delay = Math.min(initialDelayMs * 2 ** this.attempt, maxDelayMs);
    this.attempt += 1;
    this.setStatus('RECONNECTING', reason);
    this.clearRetry();
    this.retryTimer = setTimeout(() => this.connect(), delay);
  }

  /** Close the current socket, if any, without letting it report back. */
  private release(): void {
    const socket = this.socket;
    this.socket = null;
    socket?.close();
  }

  private clearRetry(): void {
    if (this.retryTimer !== null) {
      clearTimeout(this.retryTimer);
      this.retryTimer = null;
    }
  }

  private setStatus(status: ConnectionStatus, reason?: ConnectionCloseReason): void {
    if (this.status === status && reason === undefined) return;
    this.status = status;
    this.options.onStatusChange?.(status, reason);
  }
}
