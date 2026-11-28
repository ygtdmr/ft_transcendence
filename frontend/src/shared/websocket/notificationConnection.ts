/**
 * The per-user notification socket (`/ws/v2/notifications`), independent of any room.
 *
 * Same cookie-only authentication as the room socket. The socket is server-push only:
 * it carries social events (friend requests, friendship changes and profile changes) and
 * accepts no commands. Keeping it open makes the signed-in user count as online. It
 * replays nothing, so the friend list and friend requests are read over REST again after
 * every `notifications.ready` that follows a drop.
 */

import { WS_NOTIFICATIONS_PATH } from '@shared/constants';
import type {
  NotificationServerMessage,
  NotificationsReadyEvent,
  SocialEvent,
} from '@shared/types';

import type { ConnectionCloseReason, ConnectionStatus } from './connectionState';
import { createTransport } from './transport';
import type { SocketTransport } from './transport';

export interface NotificationConnectionHandlers {
  onStatusChange?: (status: ConnectionStatus, reason?: ConnectionCloseReason) => void;
  /** The gateway accepted the session; emitted once per (re)connection. */
  onReady?: (event: NotificationsReadyEvent) => void;
  /** A friend request, friendship or profile changed. */
  onSocial?: (event: SocialEvent) => void;
}

export class NotificationConnection {
  private readonly socket: SocketTransport<never>;

  private readonly handlers: NotificationConnectionHandlers;

  constructor(handlers: NotificationConnectionHandlers = {}) {
    this.handlers = handlers;
    this.socket = createTransport<NotificationServerMessage, never>({
      name: 'notifications',
      path: WS_NOTIFICATIONS_PATH,
      onStatusChange: (status, reason) => this.handlers.onStatusChange?.(status, reason),
      onMessage: (message) => this.dispatch(message),
    });
  }

  connect(): void {
    this.socket.connect();
  }

  disconnect(): void {
    this.socket.disconnect();
  }

  getStatus(): ConnectionStatus {
    return this.socket.getStatus();
  }

  private dispatch(message: NotificationServerMessage): void {
    switch (message.type) {
      case 'notifications.ready':
        this.handlers.onReady?.(message);
        return;
      case 'friend.request.received':
      case 'friend.request.resolved':
      case 'friend.removed':
      case 'friend.restored':
      case 'user.profile.updated':
        this.handlers.onSocial?.(message);
    }
  }
}
