import { useCallback, useState } from 'react';
import { useTranslation } from 'react-i18next';

import createRoomArtwork from '@assets/lobby/lobby-create-room.svg';
import joinRoomArtwork from '@assets/lobby/lobby-join-room.svg';
import mascotArtwork from '@assets/lobby/lobby-mascot.svg';
import { OnlinePlayers } from '@features/friends/components/OnlinePlayers';
import { ProfileMenu } from '@features/profile/components/ProfileMenu';
import { CreateRoomDialog } from '@features/room/components/CreateRoomDialog';
import { JoinRoomDialog } from '@features/room/components/JoinRoomDialog';
import { Button, Icon } from '@shared/ui';
import type { IconName } from '@shared/ui';
import { cn } from '@shared/utils';

import * as styles from './LobbyPage.styles';

type RoomDialog = 'create' | 'join';

const ACTIONS: {
  dialog: RoomDialog;
  artwork: string;
  badge: IconName;
  tile: IconName;
  buttonIcon: IconName;
}[] = [
  { dialog: 'create', artwork: createRoomArtwork, badge: 'host', tile: 'add', buttonIcon: 'add' },
  {
    dialog: 'join',
    artwork: joinRoomArtwork,
    badge: 'team',
    tile: 'login',
    buttonIcon: 'forward',
  },
];

/**
 * Room Discovery, the authenticated home: the player creates a room or joins one by its
 * code, and sees which friends are online.
 *
 * Rooms are found by code only; there is no public room list. Create Room and Join Room
 * open as dialogs over this page, which stays mounted and visible behind them, and only a
 * room the server has created or joined moves the player on, to that room. The profile
 * summary and the player cards open profiles over the page as well.
 */
export function LobbyPage() {
  const { t } = useTranslation();
  const [dialog, setDialog] = useState<RoomDialog | null>(null);
  const closeDialog = useCallback(() => setDialog(null), []);

  return (
    <main className={styles.page}>
      <div className={styles.column}>
        <header className={styles.header}>
          <ProfileMenu className={styles.profile} />
        </header>

        <img src={mascotArtwork} alt="" className={styles.mascot} />

        <h1 className={styles.headline}>{t('lobby.title')}</h1>
        <p className={styles.subtitle}>{t('lobby.subtitle')}</p>

        <div className={styles.actions}>
          {ACTIONS.map((action) => (
            <section
              key={action.dialog}
              aria-labelledby={`lobby-${action.dialog}-title`}
              className={styles.card}
            >
              <img
                src={action.artwork}
                alt=""
                className={cn(styles.cardArtwork, styles.cardArtworkPlacement[action.dialog])}
              />
              <span
                aria-hidden="true"
                className={cn(styles.cardBadge, styles.cardBadgeTone[action.dialog])}
              >
                <Icon name={action.badge} />
              </span>

              <div className={styles.cardTitleRow}>
                <span
                  aria-hidden="true"
                  className={cn(styles.cardTile, styles.cardTileTone[action.dialog])}
                >
                  <Icon name={action.tile} />
                </span>
                <h2 id={`lobby-${action.dialog}-title`} className={styles.cardTitle}>
                  {t(`lobby.${action.dialog}.title`)}
                </h2>
              </div>
              <p className={styles.cardDescription}>
                <span className={styles.cardDescriptionShort}>
                  {t(`lobby.${action.dialog}.descriptionShort`)}
                </span>
                <span className={styles.cardDescriptionLong}>
                  {t(`lobby.${action.dialog}.description`)}
                </span>
              </p>

              <Button
                variant={action.dialog === 'create' ? 'primary' : 'secondary'}
                sizeClassName={styles.cardButton}
                onClick={() => setDialog(action.dialog)}
                aria-haspopup="dialog"
                className={styles.cardButtonPlacement}
              >
                {t(`lobby.${action.dialog}.action`)}
                <span
                  aria-hidden="true"
                  className={cn(styles.cardButtonIcon, styles.cardButtonIconTone[action.dialog])}
                >
                  <Icon name={action.buttonIcon} />
                </span>
              </Button>
            </section>
          ))}
        </div>

        <OnlinePlayers onInvite={() => setDialog('create')} className={styles.players} />
      </div>

      {dialog === 'create' && <CreateRoomDialog onClose={closeDialog} />}
      {dialog === 'join' && <JoinRoomDialog onClose={closeDialog} />}
    </main>
  );
}
