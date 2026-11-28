/**
 * Transport seam shared by the room socket and the notification socket.
 *
 * `RoomConnection` and `NotificationConnection` talk to a `SocketTransport`, never to a
 * concrete socket. The default factory builds a `ManagedSocket` over the browser
 * `WebSocket`. A development build can swap the factory for the mock transport
 * (`shared/websocket/mock`), which speaks the same envelopes and event types, so every
 * layer above the transport behaves identically whichever one is active.
 */

import type { ConnectionStatus } from './connectionState';
import { ManagedSocket } from './managedSocket';
import type { ManagedSocketOptions } from './managedSocket';

export interface SocketTransport<TOutbound> {
  connect(): void;
  disconnect(): void;
  /**
   * Drop the current socket and open a new one at once, without passing through `CLOSED`:
   * the status goes to `RECONNECTING` and then `OPEN`. Used to take a fresh snapshot.
   */
  reconnect(): void;
  /** `false` when the transport is not open. Nothing is queued. */
  send(message: TOutbound): boolean;
  getStatus(): ConnectionStatus;
}

export type TransportOptions<TInbound> = ManagedSocketOptions<TInbound>;

export type TransportFactory = <TInbound, TOutbound>(
  options: TransportOptions<TInbound>,
) => SocketTransport<TOutbound>;

const browserTransport: TransportFactory = <TInbound, TOutbound>(
  options: TransportOptions<TInbound>,
) => new ManagedSocket<TInbound, TOutbound>(options);

let activeFactory: TransportFactory = browserTransport;

export function createTransport<TInbound, TOutbound>(
  options: TransportOptions<TInbound>,
): SocketTransport<TOutbound> {
  return activeFactory<TInbound, TOutbound>(options);
}

/** Replace the transport for sockets created from now on. `null` restores the browser one. */
export function setTransportFactory(factory: TransportFactory | null): void {
  activeFactory = factory ?? browserTransport;
}
