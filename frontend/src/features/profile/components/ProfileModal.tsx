import { useEffect, useId, useRef, useState } from 'react';
import type { ReactNode, RefObject } from 'react';
import { useTranslation } from 'react-i18next';

import type { Relationship, Room } from '@shared/types';
import { AvatarImage, Button, Dialog, Icon, Skeleton } from '@shared/ui';
import { cn } from '@shared/utils';

import { useProfile } from '../hooks/useProfile';
import type { ProfileView } from '../hooks/useProfile';
import { useReportUser } from '../hooks/useReportUser';
import * as styles from './ProfileModal.styles';
import { ReportView } from './ReportView';

/** A social action on the profile's player, while it is on its way. */
export type FriendshipChange =
  'REQUEST' | 'CANCEL' | 'ACCEPT' | 'DECLINE' | 'REMOVE' | 'BLOCK' | 'UNBLOCK';

/**
 * The relationship between the signed-in user and the profile's player, as the app layer
 * reads it from the friends feature. The profile feature only presents it.
 */
export interface FriendshipControl {
  status: 'LOADING' | 'READY' | 'ERROR';
  /** Meaningful once `status` is `READY`. */
  relationship: Relationship | null;
  change: FriendshipChange | null;
  /** Translation key of the last refusal. */
  failure: string | null;
  sendRequest: () => void;
  accept: () => void;
  decline: () => void;
  cancel: () => void;
  remove: () => void;
  block: () => void;
  unblock: () => void;
  retry: () => void;
}

export interface ProfileModalProps {
  userId: number;
  isSelf: boolean;
  onClose: () => void;
  /** Another player's profile only. */
  friendship?: FriendshipControl | undefined;
  /** The room the signed-in user is in now, from its live snapshot; `null` outside one. */
  room: Room | null;
  /** The Room Owner's Kick for a member of their room; absent for everyone else. */
  onKick?: (() => void) | undefined;
  /** The own profile's GAME HISTORY. */
  history?: ReactNode;
}

/**
 * The Profile pop-up, over the screen that opened it: Room Discovery, the Ready Room or the
 * Game Board, which stays mounted and live behind it. The app-level `ModalHost` renders
 * it; features open it with `openProfileModal(userId)`.
 *
 * One pop-up serves every profile and adapts to whose it is. The own profile shows the
 * stats and GAME HISTORY. Another player's shows the stats and the friend action for the
 * relationship — Add Friend, Request Sent with Cancel, Accept and Decline, or Remove Friend.
 * What the actions offer is derived from the live friend list, and the server decides every
 * action again when it is sent.
 *
 * Another player's profile also offers BLOCK USER (or UNBLOCK USER) and REPORT at its foot.
 * A block ends private contact both ways: the friendship and requests go away until it is
 * lifted. The report form takes the
 * profile's place inside the same pop-up, and Close steps back from it to the profile. It
 * is never offered on the user's own profile. For the Room Owner, a member of their room's
 * profile also offers REMOVE FROM ROOM there, which asks the room to confirm the kick.
 *
 * It is mounted once per user (`key`), so while one player's profile loads nothing of the
 * previous one can show.
 */
export function ProfileModal({
  userId,
  isSelf,
  onClose,
  friendship,
  room,
  onKick,
  history,
}: ProfileModalProps) {
  const { t } = useTranslation();
  const titleId = useId();
  const { profile, status, retry } = useProfile(userId, isSelf);
  const [view, setView] = useState<'PROFILE' | 'REPORT'>('PROFILE');
  // Only a room this player is in too gives the report its room context.
  const reportRoomId = room && room.players.some((p) => p.user_id === userId) ? room.room_id : null;
  const report = useReportUser(userId, reportRoomId);
  const reportSending = report.state.status === 'SENDING';
  const titleRef = useRef<HTMLHeadingElement>(null);
  const reportRef = useRef<HTMLButtonElement>(null);

  // A view swap replaces the focused control: the report's heading takes focus, and
  // coming back to the profile returns it to REPORT.
  const previousView = useRef(view);
  useEffect(() => {
    if (previousView.current === view) return;
    previousView.current = view;
    (view === 'REPORT' ? titleRef : reportRef).current?.focus();
  }, [view]);

  return (
    <Dialog
      labelledBy={titleId}
      closeLabel={t(view === 'REPORT' ? 'profile.report.back' : 'profile.close')}
      onClose={view === 'REPORT' ? () => setView('PROFILE') : onClose}
      closable={!reportSending}
      className={styles.card}
    >
      {view === 'REPORT' && profile ? (
        <ReportView
          username={profile.username}
          state={report.state}
          onSubmit={(reason, details) => void report.submit(reason, details)}
          onBack={() => setView('PROFILE')}
          titleId={titleId}
          titleRef={titleRef}
        />
      ) : status === 'READY' && profile ? (
        <>
          <ProfileHeader profile={profile} titleId={titleId} />
          {!isSelf && friendship && <ProfileActions profile={profile} friendship={friendship} />}
          <ProfileStats profile={profile} />
          {isSelf && history}
          {!isSelf && onKick && (
            <Button
              theme="link"
              variant="muted"
              icon="kick"
              sizeClassName={styles.report}
              onClick={onKick}
              aria-haspopup="dialog"
              className={styles.reportPlacement}
            >
              {t('room.kick.fromProfile')}
            </Button>
          )}
          {!isSelf && friendship && <BlockButton friendship={friendship} />}
          {!isSelf && (
            <ReportButton
              buttonRef={reportRef}
              reported={
                report.state.status === 'SENT' || report.state.status === 'ALREADY_REPORTED'
              }
              onReport={() => setView('REPORT')}
            />
          )}
        </>
      ) : status === 'LOADING' ? (
        <ProfileSkeleton titleId={titleId} />
      ) : (
        <ProfileNotice
          titleId={titleId}
          kind={status === 'NOT_FOUND' ? 'notFound' : 'error'}
          onRetry={retry}
        />
      )}
    </Dialog>
  );
}

function ProfileHeader({ profile, titleId }: { profile: ProfileView; titleId: string }) {
  const { t } = useTranslation();
  const presence = profile.is_online ? 'online' : 'offline';

  return (
    <div className={styles.header}>
      <span className={styles.avatarFrame}>
        <span className={styles.avatar}>
          <AvatarImage
            src={profile.avatar_url}
            className={styles.avatarImage}
            placeholderClassName={styles.avatarPlaceholder}
          />
        </span>
        {profile.is_online && <span aria-hidden="true" className={styles.onlineDot} />}
      </span>
      <div className={styles.identity}>
        <h2 id={titleId} className={styles.name}>
          {profile.username}
        </h2>
        <p aria-hidden="true" className={styles.handle}>
          @{profile.username}
        </p>
        <p className={cn(styles.statusBase, styles.status[presence])}>
          <span aria-hidden="true" className={styles.statusDot} />
          {t(`profile.presence.${presence}`)}
        </p>
      </div>
    </div>
  );
}

/**
 * BLOCK USER, or UNBLOCK USER for a player the user blocks, at the foot of the profile
 * beside Report. Shown once the relationship is known, and held while a change is on its
 * way.
 */
function BlockButton({ friendship }: { friendship: FriendshipControl }) {
  const { t } = useTranslation();
  if (friendship.status !== 'READY' || !friendship.relationship) return null;
  const blocked = friendship.relationship === 'BLOCKED';
  const busy = friendship.change !== null;
  const working = friendship.change === 'BLOCK' || friendship.change === 'UNBLOCK';
  return (
    <Button
      theme="link"
      variant="muted"
      icon="block"
      sizeClassName={styles.report}
      onClick={() => {
        if (busy) return;
        if (blocked) friendship.unblock();
        else friendship.block();
      }}
      aria-disabled={busy || undefined}
      aria-busy={working || undefined}
      className={styles.reportPlacement}
    >
      {t(
        working
          ? blocked
            ? 'profile.block.unblocking'
            : 'profile.block.blocking'
          : blocked
            ? 'profile.block.unblock'
            : 'profile.block.block',
      )}
    </Button>
  );
}

/** REPORT at the foot of another player's profile; Reported once the server has it. */
function ReportButton({
  buttonRef,
  reported,
  onReport,
}: {
  buttonRef: RefObject<HTMLButtonElement | null>;
  reported: boolean;
  onReport: () => void;
}) {
  const { t } = useTranslation();
  return (
    <Button
      ref={buttonRef}
      theme="link"
      variant="muted"
      icon={reported ? 'check' : 'flag'}
      sizeClassName={styles.report}
      onClick={() => !reported && onReport()}
      aria-disabled={reported || undefined}
      className={styles.reportPlacement}
    >
      {t(reported ? 'profile.report.reported' : 'profile.report.action')}
    </Button>
  );
}

function ProfileStats({ profile }: { profile: ProfileView }) {
  const { t } = useTranslation();
  const stats = [
    ['wins', profile.wins],
    ['losses', profile.losses],
    ['level', profile.level],
  ] as const;

  // Level is the server's own value (`1 + floor(wins / 5)`), shown as it arrives.
  return (
    <div className={styles.statsBlock}>
      <dl className={styles.stats}>
        {stats.map(([key, value]) => (
          <div key={key} className={styles.stat}>
            <dt className={styles.statLabel}>{t(`profile.stats.${key}`)}</dt>
            <dd className={styles.statValue}>{value}</dd>
          </div>
        ))}
      </dl>
      <p className={styles.statsCaption}>
        {t('profile.stats.played', { count: profile.matches_played })}
      </p>
    </div>
  );
}

function ProfileActions({
  profile,
  friendship,
}: {
  profile: ProfileView;
  friendship: FriendshipControl;
}) {
  const { t } = useTranslation();
  const hintId = useId();
  const relationship = friendship.status === 'READY' ? friendship.relationship : null;

  const hint: Hint | null =
    friendship.status === 'ERROR'
      ? { tone: 'error', text: t('profile.friend.loadFailed'), onRetry: friendship.retry }
      : friendship.failure
        ? { tone: 'error', text: t(friendship.failure) }
        : relationship === 'BLOCKED'
          ? { tone: 'neutral', text: t('profile.block.hint', { username: profile.username }) }
          : relationship === 'REQUEST_RECEIVED'
            ? {
                tone: 'neutral',
                text: t('profile.friend.received', { username: profile.username }),
              }
            : null;

  return (
    <div className={styles.actions}>
      <div className={styles.actionRow}>
        {friendship.status === 'LOADING' ? (
          <span aria-hidden="true" className={styles.friendSkeleton} />
        ) : (
          <FriendButtons friendship={friendship} relationship={relationship} hintId={hintId} />
        )}
      </div>
      <HintLine id={hintId} hint={hint} />
    </div>
  );
}

/**
 * The friend action for the relationship: Add Friend; Request Sent, held, with Cancel
 * Request while a request waits for the other player; Accept and Decline for a request they sent; Remove Friend for
 * a friend. A blocked player gets none — Unblock sits at the foot.
 */
function FriendButtons({
  friendship,
  relationship,
  hintId,
}: {
  friendship: FriendshipControl;
  relationship: Relationship | null;
  hintId: string;
}) {
  const { t } = useTranslation();
  const busy = friendship.change !== null;
  const press = (action: () => void) => () => {
    if (!busy) action();
  };
  const common = {
    sizeClassName: styles.action,
    'aria-disabled': busy || undefined,
    'aria-describedby': hintId,
    className: styles.friend,
  };

  switch (relationship) {
    case 'NONE':
      return (
        <Button
          variant="primary"
          {...common}
          onClick={press(friendship.sendRequest)}
          aria-busy={friendship.change === 'REQUEST' || undefined}
        >
          {t(friendship.change === 'REQUEST' ? 'profile.friend.sending' : 'profile.friend.add')}
        </Button>
      );
    case 'REQUEST_SENT':
      return (
        <>
          <Button variant="neutral" {...common} disabled icon="timer">
            {t('profile.friend.requestSent')}
          </Button>
          <Button
            variant="neutral"
            {...common}
            onClick={press(friendship.cancel)}
            aria-busy={friendship.change === 'CANCEL' || undefined}
          >
            {t(
              friendship.change === 'CANCEL'
                ? 'profile.friend.cancelling'
                : 'profile.friend.cancel',
            )}
          </Button>
        </>
      );
    case 'REQUEST_RECEIVED':
      return (
        <>
          <Button
            variant="primary"
            {...common}
            onClick={press(friendship.accept)}
            aria-busy={friendship.change === 'ACCEPT' || undefined}
          >
            {t(
              friendship.change === 'ACCEPT' ? 'profile.friend.accepting' : 'profile.friend.accept',
            )}
          </Button>
          <Button
            variant="neutral"
            {...common}
            onClick={press(friendship.decline)}
            aria-busy={friendship.change === 'DECLINE' || undefined}
          >
            {t(
              friendship.change === 'DECLINE'
                ? 'profile.friend.declining'
                : 'profile.friend.decline',
            )}
          </Button>
        </>
      );
    case 'FRIENDS':
      return (
        <Button
          variant="neutral"
          {...common}
          onClick={press(friendship.remove)}
          aria-busy={friendship.change === 'REMOVE' || undefined}
        >
          {t(friendship.change === 'REMOVE' ? 'profile.friend.removing' : 'profile.friend.remove')}
        </Button>
      );
    default:
      return null;
  }
}

type Hint = { tone: 'neutral' | 'success' | 'error'; text: string; onRetry?: () => void };

/** The one line under the actions that says why a button is as it is. Announced politely. */
function HintLine({ id, hint }: { id: string; hint: Hint | null }) {
  const { t } = useTranslation();
  return (
    <p id={id} aria-live="polite" className={cn(styles.hintBase, hint && styles.hint[hint.tone])}>
      {hint?.text}
      {hint?.onRetry && (
        <button type="button" onClick={hint.onRetry} className={styles.hintAction}>
          {t('profile.retry')}
        </button>
      )}
    </p>
  );
}

function ProfileSkeleton({ titleId }: { titleId: string }) {
  const { t } = useTranslation();
  return (
    <div role="status" className="flex flex-col gap-[inherit]">
      <h2 id={titleId} className="sr-only">
        {t('profile.loading')}
      </h2>
      <div aria-hidden="true" className={styles.skeletonHeader}>
        <Skeleton shape="circle" className={styles.skeletonAvatar} />
        <span className="flex min-w-0 flex-1 flex-col gap-3">
          <Skeleton className="h-8 w-3/5" />
          <Skeleton className="w-2/5" />
          <Skeleton className="w-1/4" />
        </span>
      </div>
      <div aria-hidden="true" className={styles.stats}>
        {[0, 1, 2].map((i) => (
          <Skeleton key={i} shape="block" className={styles.skeletonStat} />
        ))}
      </div>
    </div>
  );
}

function ProfileNotice({
  titleId,
  kind,
  onRetry,
}: {
  titleId: string;
  kind: 'notFound' | 'error';
  onRetry: () => void;
}) {
  const { t } = useTranslation();
  return (
    <div role={kind === 'error' ? 'alert' : undefined} className={styles.notice}>
      <Icon name={kind === 'error' ? 'disconnected' : 'playersOff'} className={styles.noticeIcon} />
      <h2 id={titleId} className={styles.noticeTitle}>
        {t(`profile.${kind}.title`)}
      </h2>
      <p className={styles.noticeBody}>{t(`profile.${kind}.body`)}</p>
      {kind === 'error' && (
        <Button sizeClassName={styles.retry} onClick={onRetry} className={styles.retryPlacement}>
          {t('profile.retry')}
        </Button>
      )}
    </div>
  );
}
