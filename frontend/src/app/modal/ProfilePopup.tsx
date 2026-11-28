import { useCallback } from 'react';

import { useRelationship } from '@features/friends/hooks/useRelationship';
import { ProfileModal } from '@features/profile/components/ProfileModal';
import { useRoomStore } from '@features/room/store/roomStore';
import { GameHistory } from '@features/stats/components/GameHistory';
import { useSessionStore } from '@shared/stores';

/**
 * The Profile pop-up with everything it needs from other features, joined here at app
 * level so no feature imports another: the relationship and its actions from `friends`, the room the user
 * is in from the room store, GAME HISTORY from `stats`, and the Room Owner's Remove from
 * Room, which the room screen confirms.
 */
export function ProfilePopup({ userId, onClose }: { userId: number; onClose: () => void }) {
  const me = useSessionStore((state) => state.user);
  const isSelf = me?.user_id === userId;

  return isSelf ? (
    <ProfileModal userId={userId} isSelf onClose={onClose} room={null} history={<GameHistory />} />
  ) : (
    <OtherProfile userId={userId} onClose={onClose} />
  );
}

function OtherProfile({ userId, onClose }: { userId: number; onClose: () => void }) {
  const friendship = useRelationship(userId);
  const activeRoomId = useSessionStore((state) => state.activeRoomId);
  const room = useRoomStore((state) =>
    state.room && state.room.room_id === activeRoomId ? state.room : null,
  );
  // The Room Owner removes a member: the room screen behind asks to confirm it.
  const selfId = useSessionStore((state) => state.user?.user_id);
  const canKick =
    room !== null &&
    room.status !== 'CLOSED' &&
    room.host_user_id === selfId &&
    room.players.some((p) => p.user_id === userId);
  const onKick = useCallback(() => {
    onClose();
    useRoomStore.getState().requestKick(userId);
  }, [onClose, userId]);

  return (
    <ProfileModal
      userId={userId}
      isSelf={false}
      onClose={onClose}
      friendship={friendship}
      room={room}
      onKick={canKick ? onKick : undefined}
    />
  );
}
