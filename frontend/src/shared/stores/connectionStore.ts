/**
 * Live connection status for both sockets.
 *
 * Purely frontend/ephemeral state: it drives the Reconnecting overlay, the Disconnected
 * notice and any "you are offline" affordance. It never mirrors domain data — room and
 * game state come from the snapshots and events the connection delivers.
 */

import { create } from 'zustand';

import type { RoomStatus } from '@shared/types';
import type { ConnectionCloseReason, ConnectionStatus } from '@shared/websocket';

interface SocketState {
  status: ConnectionStatus;
  closeReason: ConnectionCloseReason | null;
}

/**
 * A room connection that was live and dropped, while the client tries to get it back.
 * `deadline` is when the seat the server holds is expected to run out, counted from the
 * drop; `roomStatus` is the room's status when it dropped.
 */
export interface RoomRecovery {
  deadline: number;
  roomStatus: RoomStatus;
}

interface ConnectionState {
  room: SocketState & { recovery: RoomRecovery | null };
  notifications: SocketState;
  /**
   * The room connection could not be restored, so the player was taken back to Room
   * Discovery. Shown there once, until the player acknowledges it.
   */
  roomLost: boolean;

  setRoomStatus: (status: ConnectionStatus, reason?: ConnectionCloseReason) => void;
  setRoomRecovery: (recovery: RoomRecovery | null) => void;
  setNotificationStatus: (status: ConnectionStatus, reason?: ConnectionCloseReason) => void;
  setRoomLost: (lost: boolean) => void;
}

const initial: SocketState = { status: 'IDLE', closeReason: null };

export const useConnectionStore = create<ConnectionState>((set) => ({
  room: { ...initial, recovery: null },
  notifications: initial,
  roomLost: false,

  setRoomStatus: (status, reason) =>
    set((state) => ({ room: { ...state.room, status, closeReason: reason ?? null } })),
  setRoomRecovery: (recovery) => set((state) => ({ room: { ...state.room, recovery } })),
  setNotificationStatus: (status, reason) =>
    set({ notifications: { status, closeReason: reason ?? null } }),
  setRoomLost: (roomLost) => set({ roomLost }),
}));
