import { useEffect, useId, useRef } from 'react';
import { useTranslation } from 'react-i18next';

import type { MatchHistoryEntry } from '@shared/types';
import { Button, Icon, Skeleton } from '@shared/ui';
import { cn } from '@shared/utils';

import { useMatchHistory } from '../hooks/useMatchHistory';
import * as styles from './GameHistory.styles';

/**
 * GAME HISTORY on the own profile: the signed-in user's finished matches, newest first,
 * from `GET /api/v1/users/me/matches`. The contract offers no history for other players.
 *
 * Each row shows WIN or LOSS, the opponents, the user's team and role, how the game ended,
 * the final score with the user's team first, and the date. Older matches page in with the
 * `before` cursor as the list is scrolled to its end.
 */
export function GameHistory() {
  const { t } = useTranslation();
  const headingId = useId();
  const history = useMatchHistory();
  const listRef = useRef<HTMLUListElement>(null);
  const endRef = useRef<HTMLLIElement>(null);
  const { loadMore } = history;

  useEffect(() => {
    const root = listRef.current;
    const end = endRef.current;
    if (!root || !end) return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) loadMore();
      },
      { root, rootMargin: '0px 0px 80px 0px' },
    );
    observer.observe(end);
    return () => observer.disconnect();
  }, [loadMore, history.status]);

  return (
    <section aria-labelledby={headingId} className={styles.section}>
      <div className={styles.header}>
        <h3 id={headingId} className={styles.heading}>
          {t('profile.history.title')}
        </h3>
        {history.status === 'READY' && (
          <span className={styles.count}>
            {t('profile.history.count', { count: history.matches.length })}
          </span>
        )}
      </div>

      {history.status === 'LOADING' && (
        <div role="status" aria-label={t('profile.history.loading')} className={styles.list}>
          {[0, 1, 2].map((i) => (
            <SkeletonRow key={i} />
          ))}
        </div>
      )}

      {history.status === 'EMPTY' && (
        <div className={styles.empty}>
          <Icon name="history" className={styles.emptyIcon} />
          <p className={styles.emptyTitle}>{t('profile.history.emptyTitle')}</p>
          <p className={styles.emptyBody}>{t('profile.history.emptyBody')}</p>
        </div>
      )}

      {history.status === 'ERROR' && (
        <>
          <div role="alert" className={styles.alert}>
            <p className={styles.alertTitle}>{t('profile.history.errorTitle')}</p>
            <p className={styles.alertBody}>{t('profile.history.errorBody')}</p>
          </div>
          <Button
            theme="link"
            sizeClassName={styles.retry}
            onClick={history.retry}
            className={styles.retryPlacement}
          >
            {t('profile.history.retry')}
          </Button>
        </>
      )}

      {history.status === 'READY' && (
        <ul ref={listRef} tabIndex={0} aria-labelledby={headingId} className={styles.list}>
          {history.matches.map((match) => (
            <li key={match.game_id}>
              <MatchRow match={match} />
            </li>
          ))}
          <li ref={endRef} aria-hidden={!history.loadingMore}>
            {history.loadingMore && <SkeletonRow />}
            {!history.loadingMore && history.hasMore && history.error && (
              <Button
                theme="link"
                sizeClassName={styles.retry}
                onClick={loadMore}
                className={styles.retryPlacement}
              >
                {t('profile.history.retry')}
              </Button>
            )}
          </li>
        </ul>
      )}
    </section>
  );
}

function MatchRow({ match }: { match: MatchHistoryEntry }) {
  const { t, i18n } = useTranslation();
  const own = match.team === 'RED' ? match.score.red : match.score.blue;
  const other = match.team === 'RED' ? match.score.blue : match.score.red;
  const date = new Intl.DateTimeFormat(i18n.language, { month: 'short', day: 'numeric' }).format(
    new Date(match.finished_at),
  );
  const opponents = match.opponents.map((o) => o.username).join(', ');

  return (
    <div className={styles.row}>
      <span className={cn(styles.resultBase, styles.result[match.result])}>
        {t(`profile.history.result.${match.result}`)}
      </span>
      <span className={styles.info}>
        <span className={styles.opponents}>
          {t('profile.history.opponents', { names: opponents })}
        </span>
        <span className={styles.meta}>
          {[
            t(`game.team.${match.team}`),
            t(`room.ready.role.${match.role}`),
            t(`profile.history.endReason.${match.end_reason}`),
          ].join(' · ')}
        </span>
      </span>
      <span className={styles.scoreColumn}>
        <span className={styles.score}>
          <span aria-hidden="true">
            {own} – {other}
          </span>
          <span className="sr-only">{t('profile.history.scoreLabel', { own, other })}</span>
        </span>
        <time dateTime={match.finished_at} className={styles.date}>
          {date}
        </time>
      </span>
    </div>
  );
}

function SkeletonRow() {
  return (
    <div aria-hidden="true" className={styles.skeletonRow}>
      <Skeleton className={styles.skeletonBadge} />
      <span className="flex min-w-0 flex-1 flex-col gap-2">
        <Skeleton className="w-3/5" />
        <Skeleton className="w-4/5" />
      </span>
      <Skeleton className="w-10" />
    </div>
  );
}
