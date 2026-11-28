/**
 * In-memory Notification Gateway speaking the Bruno Notifications WebSocket v2 contract.
 *
 * One instance (`mockNotifications`) serves the mock notification socket
 * (`/ws/v2/notifications`). The mock REST server tells it about friend request,
 * friendship and profile changes, and it pushes them to every open notification socket,
 * as User Service and Notification Gateway do. The socket accepts no client commands:
 * any client frame is answered with `INVALID_EVENT` and the socket stays open.
 *
 *   mockSockets.notifications.dropConnection(3000)   // RECONNECTING, then notifications.ready
 *   mockSockets.notifications.duplicateNext()        // the next event arrives twice
 */

import type { NotificationErrorMessage, SocialEvent } from '@shared/types';

import type { MockEndpoint, MockServerBinding } from './mockTransport';
import { mockDirectory } from './registry';

const now = (): string => new Date().toISOString();

export class MockNotificationServer implements MockServerBinding {
  private readonly endpoints = new Set<MockEndpoint>();
  private eventSeq = 0;
  private repeatNextEvent = false;

  /* ------------------------------ transport side ---------------------------- */

  onOpen(endpoint: MockEndpoint): void {
    this.endpoints.add(endpoint);
    endpoint.deliver({
      type: 'notifications.ready',
      sent_at: now(),
      payload: { user_id: mockDirectory.selfId(), connected_at: now() },
    });
  }

  onClose(endpoint: MockEndpoint): void {
    this.endpoints.delete(endpoint);
  }

  onCommand(endpoint: MockEndpoint): void {
    const error: NotificationErrorMessage = { type: 'error', payload: { code: 'INVALID_EVENT' } };
    endpoint.deliver(error);
  }

  /** Take every notification socket down; they reconnect after `reconnectAfterMs`. */
  dropConnection(reconnectAfterMs?: number): void {
    [...this.endpoints].forEach((endpoint) => endpoint.simulateDrop(reconnectAfterMs));
  }

  /* ------------------------------- test controls ---------------------------- */

  /** The next event is delivered twice, as a broker redelivery would. */
  duplicateNext(): void {
    this.repeatNextEvent = true;
  }

  /* ------------------------------- REST side -------------------------------- */

  /** A social change from User Service, delivered to the user's notification sockets. */
  notify<T extends SocialEvent>(type: T['type'], payload: T['payload']): void {
    const event = { type, event_id: this.nextEventId(), sent_at: now(), payload };
    const times = this.repeatNextEvent ? 2 : 1;
    this.repeatNextEvent = false;
    for (let i = 0; i < times; i += 1)
      this.endpoints.forEach((endpoint) => endpoint.deliver(event));
  }

  private nextEventId(): string {
    this.eventSeq += 1;
    return `evt_n${String(this.eventSeq).padStart(6, '0')}`;
  }
}

/** The one mock Notification Gateway, shared by the mock socket and mock REST. */
export const mockNotifications = new MockNotificationServer();
