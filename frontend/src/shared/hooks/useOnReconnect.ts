import { useEffect, useRef } from 'react';

import { useConnectionStore } from '@shared/stores';

/**
 * Calls `callback` each time the notification socket or the room socket opens again, so a view
 * can read fresh what may have changed while it was down: presence, friendships, a
 * profile. It never fires on its own mount; the view does its own first load.
 */
export function useOnReconnect(callback: () => void): void {
  const latest = useRef(callback);
  useEffect(() => {
    latest.current = callback;
  });

  useEffect(
    () =>
      useConnectionStore.subscribe((state, previous) => {
        const opened = (now: string, before: string) => now === 'OPEN' && before !== 'OPEN';
        if (
          opened(state.notifications.status, previous.notifications.status) ||
          opened(state.room.status, previous.room.status)
        ) {
          latest.current();
        }
      }),
    [],
  );
}
