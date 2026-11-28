import { useId } from 'react';
import { useTranslation } from 'react-i18next';

import { useSecondsUntil } from '@shared/hooks';
import type { PlayingRole, Staffing, StaffingDeparture, Team } from '@shared/types';
import { Button, Dialog, Icon } from '@shared/ui';
import type { ButtonVariant } from '@shared/ui';
import { cn } from '@shared/utils';

import * as styles from './StaffingDialog.styles';

const TEAMS: Team[] = ['RED', 'BLUE'];

/** A claim is drawn in its team's color: coral for Red, blue for Blue. */
const TEAM_BUTTON: Record<Team, ButtonVariant> = { RED: 'secondary', BLUE: 'primary' };

/** `2:05` from whole seconds. */
const clock = (seconds: number): string =>
  `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;

/** A free seat a member without one may claim, with what happens while the claim is on its way. */
export interface SeatClaim {
  onClaim: (team: Team, role: PlayingRole) => void;
  pending: boolean;
  /** Translation key of why the last claim was refused. */
  failure: string | null;
}

function TeamDeadline({
  team,
  missing,
  deadline,
  clockOffsetMs,
}: {
  team: Team;
  missing: PlayingRole[];
  deadline: string | null;
  clockOffsetMs: number;
}) {
  const { t } = useTranslation();
  const seconds = useSecondsUntil(deadline, clockOffsetMs);
  const need = missing.length > 1 ? 'BOTH' : (missing[0] ?? 'OPERATIVE');

  return (
    <li className={cn(styles.team, styles.teamTone[team])}>
      <span className={styles.teamText}>
        {t(`game.staffing.needs.${need}`, { team: t(`game.teamName.${team}`) })}
      </span>
      {seconds !== null && (
        <span className={styles.timer}>
          <Icon name="timer" />
          <span className="sr-only">{t('game.staffing.closesIn')} </span>
          {clock(seconds)}
        </span>
      )}
    </li>
  );
}

/**
 * GAME PAUSED: the room's shutdown countdown, over the board.
 *
 * A team lost its only Spymaster or its last Operative, so the server paused the match and
 * gave that team a deadline. The dialog says who left and why, which seat each short team
 * needs, and how long remains, counted from the server's absolute deadline. A player who
 * joined the paused match takes a free seat here, which restores the team. The dialog closes when the server says
 * play resumes; if a deadline passes, the server closes the room and everyone is taken back
 * to the Lobby. Nothing here ends the game on its own.
 */
export function StaffingDialog({
  staffing,
  departure,
  clockOffsetMs,
  claim,
  onLeave,
}: {
  staffing: Staffing;
  departure: StaffingDeparture | null;
  clockOffsetMs: number;
  /** Present for a member without a seat, who joined to take a free one. */
  claim: SeatClaim | null;
  onLeave: () => void;
}) {
  const { t } = useTranslation();
  const titleId = useId();
  const bodyId = useId();
  const short = TEAMS.filter(
    (team) => staffing[team === 'RED' ? 'red' : 'blue'].missing_roles.length,
  );

  return (
    <Dialog
      role="alertdialog"
      labelledBy={titleId}
      describedBy={bodyId}
      closeLabel={t('common.close')}
      onClose={() => {}}
      closable={false}
      showCloseButton={false}
      className={styles.card}
    >
      <span aria-hidden="true" className={styles.badge}>
        <Icon name="timer" />
      </span>
      <h2 id={titleId} className={styles.title}>
        {t('game.staffing.title')}
      </h2>
      <p id={bodyId} className={styles.body}>
        {departure
          ? t(`game.staffing.departure.${departure.reason}`, {
              username: departure.username,
              team: t(`game.teamName.${departure.team}`),
              role: t(`game.staffing.role.${departure.role}`),
            })
          : t('game.staffing.departure.unknown')}{' '}
        {t('game.staffing.body')}
      </p>

      <ul className={styles.teams}>
        {short.map((team) => {
          const entry = staffing[team === 'RED' ? 'red' : 'blue'];
          return (
            <TeamDeadline
              key={team}
              team={team}
              missing={entry.missing_roles}
              deadline={entry.deadline_at}
              clockOffsetMs={clockOffsetMs}
            />
          );
        })}
      </ul>

      {claim && (
        <div className={styles.claim}>
          <p className={styles.claimTitle}>{t('game.staffing.claimTitle')}</p>
          <div className={styles.claimActions}>
            {short.flatMap((team) =>
              staffing[team === 'RED' ? 'red' : 'blue'].missing_roles.map((role) => (
                <Button
                  key={`${team}-${role}`}
                  variant={TEAM_BUTTON[team]}
                  sizeClassName={styles.claimButton}
                  onClick={() => !claim.pending && claim.onClaim(team, role)}
                  aria-busy={claim.pending || undefined}
                  aria-disabled={claim.pending || undefined}
                  className={styles.claimPlacement}
                >
                  {t('game.staffing.claim', {
                    team: t(`game.team.${team}`),
                    role: t(`game.staffing.role.${role}`),
                  })}
                </Button>
              )),
            )}
          </div>
          {claim.failure && (
            <p role="alert" className={styles.error}>
              {t(claim.failure)}
            </p>
          )}
        </div>
      )}

      <Button
        theme="link"
        variant="secondary"
        icon="logout"
        sizeClassName={styles.leave}
        onClick={onLeave}
        aria-haspopup="dialog"
        className="mt-[18px]"
      >
        {t('game.leave')}
      </Button>
    </Dialog>
  );
}
