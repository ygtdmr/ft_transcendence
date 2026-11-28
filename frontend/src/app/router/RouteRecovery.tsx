import { useEffect, useState } from 'react';
import { Navigate, Outlet, useParams } from 'react-router-dom';

import { ROUTES, roomPath } from '@shared/constants';
import { showToast, useSessionStore } from '@shared/stores';
import type { ToastNotice } from '@shared/stores';

/**
 * Route recovery: the screen comes from the server's view of the user's room membership,
 * never from the URL alone.
 *
 * `activeRoomId` mirrors `active_room_id` from `GET /api/v1/auth/session` at bootstrap and is
 * kept current by entering and leaving rooms, so these guards hold every authenticated
 * route to it: a member is in their room, and a non-member is in the Lobby. Once inside the
 * room, the screen itself follows the authoritative `room.status` from the room snapshot.
 *
 * A guard explains itself only when the user arrived on the wrong route (a reload, a link,
 * a typed address). When the membership changes while the user is already on the route —
 * leaving, or a room that closed — whoever ended it says why, and the guard just follows.
 */

type Notice = Omit<ToastNotice, 'id'>;

const RETURNED: Notice = { message: 'room.recovery.returned', tone: 'neutral', icon: 'login' };
const NOT_MEMBER: Notice = { message: 'room.recovery.notMember', tone: 'error', icon: 'warning' };

/** The Lobby: a member of a room is sent back into it. */
export function LobbyRouteGuard() {
  const activeRoomId = useSessionStore((state) => state.activeRoomId);
  // Read once: whether the user arrived here while belonging to a room.
  const [arrivedInRoom] = useState(activeRoomId !== null);

  useEffect(() => {
    if (arrivedInRoom) showToast(RETURNED);
  }, [arrivedInRoom]);

  if (activeRoomId !== null) return <Navigate to={roomPath(activeRoomId)} replace />;
  return <Outlet />;
}

/**
 * `/room/:roomId`: only the room the user belongs to opens. Without a room the user goes to
 * the Lobby; with a different one, to that room.
 */
export function RoomRouteGuard() {
  const { roomId } = useParams<{ roomId: string }>();
  const activeRoomId = useSessionStore((state) => state.activeRoomId);
  const misrouted = activeRoomId === null || String(activeRoomId) !== roomId;
  // Read once: the notice that fits the route the user arrived on, if it was the wrong one.
  const [arrivalNotice] = useState(() =>
    misrouted ? (activeRoomId === null ? NOT_MEMBER : RETURNED) : null,
  );

  useEffect(() => {
    if (arrivalNotice) showToast(arrivalNotice);
  }, [arrivalNotice]);

  if (activeRoomId === null) return <Navigate to={ROUTES.lobby} replace />;
  if (misrouted) return <Navigate to={roomPath(activeRoomId)} replace />;
  return <Outlet />;
}
