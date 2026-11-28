/**
 * Shared in-memory room registry for the mock layers.
 *
 * The mock REST API (`shared/api/mock`) and the mock sockets resolve the same room id
 * to the same `MockRoomServer`, so a room created or joined over mock REST is the room
 * the mock room socket then connects to, exactly as REST and WebSocket share one Game
 * Session on the server.
 */

import { useSessionStore } from '@shared/stores';

import type { MockRoomServer } from './mockRoomServer';
import type { MockPlayer } from './mockRoomServer';

/** Used only when no session is loaded (sockets mocked against a real, empty session). */
const FALLBACK_SELF: MockPlayer = { user_id: 42, username: 'player_one' };

export const mockRooms = new Map<number, MockRoomServer>();

export function mockSelfPlayer(): MockPlayer {
  const user = useSessionStore.getState().user;
  return user ? { user_id: user.user_id, username: user.username } : FALLBACK_SELF;
}

/**
 * The mock network. While it is down every mock socket fails to reconnect and every mock
 * REST call fails as a network error, as they would with the device offline.
 */
export const mockNetwork = { online: true };

/**
 * Whether the handshake's session cookie would be accepted. The mock REST server installs
 * its own check here, so an expired access cookie refuses a socket until a refresh, as the
 * Gateway's `401` on the upgrade does.
 */
export const mockSession = { authorized: (): boolean => true };

/**
 * Told after a room's membership or status changed, so the mock Channel Service can follow
 * the room lifecycle as the real one follows Game Session's outbox.
 */
export const mockRoomLifecycle = { changed: (): void => {} };

/** A user as the mock sockets show them. */
export interface MockDirectoryUser {
  user_id: number;
  username: string;
  avatar_url: string;
  is_online: boolean;
}

/**
 * What the mock sockets ask the owner of users, as the real gateways ask User and Auth.
 * The mock REST server installs the real answers.
 */
export interface MockDirectory {
  selfId(): number;
  user(userId: number): MockDirectoryUser | null;
}

export const mockDirectory: MockDirectory = {
  selfId: () => mockSelfPlayer().user_id,
  user: () => null,
};
