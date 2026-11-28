import { useId } from 'react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { openProfileModal, useModalStore } from '@shared/stores';
import { USER_SEARCH_QUERY } from '@shared/types';
import type { Friend, FriendRequest, UserSearchResult } from '@shared/types';
import { AvatarImage, Button, Dialog, Icon, LoadingDots } from '@shared/ui';
import type { IconName } from '@shared/ui';

import { useFriendList } from '../hooks/useFriendList';
import { useFriendRequests } from '../hooks/useFriendRequests';
import { useSocialActions } from '../hooks/useSocialActions';
import type { SocialAction, SocialActions } from '../hooks/useSocialActions';
import { useUserSearch } from '../hooks/useUserSearch';
import { pendingRequestWith, relationshipOf, useFriendsStore } from '../store/friendsStore';
import * as styles from './FriendsDialog.styles';

export interface FriendsDialogProps {
  onClose: () => void;
}

/**
 * Friends, over Room Discovery: friend requests waiting for an answer, the user's whole
 * friend list with live presence, and a username search to find someone new.
 *
 * Friendship takes a request and its acceptance. Add Friend sends a request and the row
 * then reads Request Sent; the other player becomes a friend only once they accept. A
 * request sent to the user is answered here with Accept or Decline. Every row follows the
 * server's answers and the social events of the notification socket, so a request accepted on the
 * other side appears here without a reload. A name in any list opens that player's profile
 * over this dialog, where the friendship can also be ended and the player blocked; this
 * dialog waits underneath until it closes.
 *
 * Presence (`is_online`) is what the server reports on the latest read of the list; the
 * list is read again when the dialog opens and after a reconnect.
 */
export function FriendsDialog({ onClose }: FriendsDialogProps) {
  const { t } = useTranslation();
  const id = useId();
  const friendCount = useFriendsStore((state) => state.friendCount);
  // A profile opened from here stands over this dialog; Escape then belongs to it.
  const profileOpen = useModalStore((state) => state.active !== null);
  const list = useFriendList();
  const requests = useFriendRequests();
  const search = useUserSearch();
  const actions = useSocialActions();

  return (
    <Dialog
      labelledBy={`${id}-title`}
      describedBy={`${id}-subtitle`}
      closeLabel={t('common.close')}
      onClose={onClose}
      closable={!profileOpen}
      className={styles.card}
    >
      <header className={styles.header}>
        <h2 id={`${id}-title`} className={styles.title}>
          {t('friends.title')}
        </h2>
        <p id={`${id}-subtitle`} className={styles.subtitle}>
          {list.status === 'READY'
            ? t('friends.count', { count: friendCount })
            : t('friends.subtitle')}
        </p>
      </header>

      <div className="flex flex-col gap-[6px]">
        <label className={styles.search}>
          <span className="sr-only">{t('friends.search.label')}</span>
          <Icon name="search" className={styles.searchIcon} />
          <input
            type="search"
            value={search.input}
            onChange={(event) => search.setInput(event.target.value)}
            maxLength={USER_SEARCH_QUERY.maxLength}
            placeholder={t('friends.search.placeholder')}
            autoComplete="off"
            spellCheck={false}
            aria-describedby={`${id}-search-hint`}
            className={styles.searchInput}
          />
        </label>
        <p id={`${id}-search-hint`} className={styles.hint}>
          {t('friends.search.hint', { max: USER_SEARCH_QUERY.maxLength })}
        </p>
      </div>

      {search.term === null ? (
        <>
          <RequestList
            requests={requests.incoming}
            actions={actions}
            titleId={`${id}-incoming`}
            kind="incoming"
          />
          <RequestList
            requests={requests.outgoing}
            actions={actions}
            titleId={`${id}-outgoing`}
            kind="outgoing"
          />
          <FriendList list={list} titleId={`${id}-friends`} />
        </>
      ) : (
        <SearchResults search={search} actions={actions} titleId={`${id}-results`} />
      )}
    </Dialog>
  );
}

function FriendList({
  list,
  titleId,
}: {
  list: ReturnType<typeof useFriendList>;
  titleId: string;
}) {
  const { t } = useTranslation();
  // Online friends first; each group keeps the server's username order.
  const friends = [...list.friends].sort((a, b) => Number(b.is_online) - Number(a.is_online));

  return (
    <section aria-labelledby={titleId} aria-busy={list.status === 'LOADING'}>
      <h3 id={titleId} className="sr-only">
        {t('friends.listTitle')}
      </h3>
      {list.status === 'LOADING' && <Loading label={t('friends.loading')} />}
      {list.status === 'ERROR' && (
        <State
          icon="disconnected"
          title={t('friends.errorTitle')}
          body={t('friends.errorBody')}
          action={{ label: t('common.retry'), icon: 'history', onClick: list.retry }}
          alert
        />
      )}
      {list.status === 'READY' && friends.length === 0 && (
        <State icon="playersOff" title={t('friends.emptyTitle')} body={t('friends.emptyBody')} />
      )}
      {list.status === 'READY' && friends.length > 0 && (
        <ul className={styles.list}>
          {friends.map((friend) => (
            <li key={friend.user_id} className={styles.row}>
              <PlayerIdentity player={friend}>
                <span className={friend.is_online ? styles.badgeOnline : styles.badgeOffline}>
                  <span aria-hidden="true" className={styles.badgeDot} />
                  {t(friend.is_online ? 'friends.online' : 'friends.offline')}
                </span>
              </PlayerIdentity>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

/**
 * Friend requests waiting for an answer: sent to the user, with Accept and Decline, or
 * sent by the user, marked Request Sent with Cancel. Nothing shows while there are none.
 */
function RequestList({
  requests,
  actions,
  titleId,
  kind,
}: {
  requests: FriendRequest[];
  actions: SocialActions;
  titleId: string;
  kind: 'incoming' | 'outgoing';
}) {
  const { t } = useTranslation();
  if (requests.length === 0) return null;
  return (
    <section aria-labelledby={titleId} className="flex flex-col gap-[6px]">
      <h3 id={titleId} className={styles.sectionTitle}>
        {t(`friends.requests.${kind}Title`, { count: requests.length })}
      </h3>
      <ul className={styles.requestList}>
        {requests.map((request) => {
          const other = kind === 'incoming' ? request.from_user : request.to_user;
          const busy = actions.busy.get(other.user_id);
          const failure = actions.failed.get(other.user_id);
          return (
            <li key={request.request_id} className={styles.row}>
              <PlayerIdentity player={other}>
                {kind === 'outgoing' && (
                  <span className={styles.badgePending}>
                    <Icon name="timer" />
                    {t('friends.requests.sent')}
                  </span>
                )}
              </PlayerIdentity>
              {kind === 'incoming' && (
                <span className={styles.rowActions}>
                  <Button
                    icon="check"
                    sizeClassName={styles.addButton}
                    onClick={() => !busy && void actions.accept(other.user_id, request.request_id)}
                    aria-disabled={busy !== undefined || undefined}
                    aria-busy={busy === 'ACCEPT' || undefined}
                    aria-label={t('friends.requests.acceptLabel', { username: other.username })}
                    className={styles.addPlacement}
                  >
                    {t(
                      busy === 'ACCEPT' ? 'friends.requests.accepting' : 'friends.requests.accept',
                    )}
                  </Button>
                  <Button
                    variant="neutral"
                    sizeClassName={styles.addButton}
                    onClick={() => !busy && void actions.decline(other.user_id, request.request_id)}
                    aria-disabled={busy !== undefined || undefined}
                    aria-busy={busy === 'DECLINE' || undefined}
                    aria-label={t('friends.requests.declineLabel', { username: other.username })}
                    className={styles.addPlacement}
                  >
                    {t(
                      busy === 'DECLINE'
                        ? 'friends.requests.declining'
                        : 'friends.requests.decline',
                    )}
                  </Button>
                </span>
              )}
              {kind === 'outgoing' && (
                <CancelRequestButton
                  username={other.username}
                  busy={busy}
                  onCancel={() => void actions.cancel(other.user_id, request.request_id)}
                />
              )}
              {failure && (
                <p role="alert" className={styles.rowError}>
                  {t(failure)}
                </p>
              )}
            </li>
          );
        })}
      </ul>
    </section>
  );
}

function SearchResults({
  search,
  actions,
  titleId,
}: {
  search: ReturnType<typeof useUserSearch>;
  actions: SocialActions;
  titleId: string;
}) {
  const { t } = useTranslation();
  // The friends store is current after every request, answer and social event; a result's
  // own relationship is as old as the search that returned it.
  const store = useFriendsStore();
  const relationship = (result: UserSearchResult) =>
    relationshipOf(store, result.user_id, result.relationship) ?? result.relationship;
  const requestIdFor = (result: UserSearchResult) =>
    pendingRequestWith(store, result.user_id)?.request.request_id ?? result.friend_request_id;
  const settled = search.status === 'READY' && search.searched === search.term;

  return (
    <section aria-labelledby={titleId} aria-busy={search.status === 'SEARCHING'}>
      <h3 id={titleId} className="sr-only">
        {t('friends.search.resultsTitle')}
      </h3>
      <p role="status" className="sr-only">
        {settled && t('friends.search.resultCount', { count: search.results.length })}
      </p>
      {(search.status === 'SEARCHING' || (search.status !== 'ERROR' && !settled)) && (
        <Loading label={t('friends.search.searching')} />
      )}
      {search.status === 'ERROR' && (
        <State
          icon="disconnected"
          title={t('friends.search.errorTitle')}
          body={t('friends.search.errorBody')}
          action={{ label: t('common.retry'), icon: 'history', onClick: search.retry }}
          alert
        />
      )}
      {settled && search.results.length === 0 && (
        <State
          icon="search"
          title={t('friends.search.emptyTitle')}
          body={t('friends.search.emptyBody', { term: search.term })}
        />
      )}
      {settled && search.results.length > 0 && (
        <ul className={styles.list}>
          {search.results.map((result) => {
            const state = relationship(result);
            const requestId = requestIdFor(result);
            const busy = actions.busy.get(result.user_id);
            const failure = actions.failed.get(result.user_id);
            return (
              <li key={result.user_id} className={styles.row}>
                <PlayerIdentity player={result}>
                  {state === 'FRIENDS' && (
                    <span className={styles.badgeFriend}>
                      <Icon name="check" />
                      {t('friends.search.alreadyFriend')}
                    </span>
                  )}
                  {state === 'REQUEST_SENT' && (
                    <span className={styles.badgePending}>
                      <Icon name="timer" />
                      {t('friends.requests.sent')}
                    </span>
                  )}
                  {state === 'BLOCKED' && (
                    <span className={styles.badgeOffline}>
                      <Icon name="block" />
                      {t('friends.search.blocked')}
                    </span>
                  )}
                </PlayerIdentity>
                {state === 'NONE' && (
                  <Button
                    icon="userAdd"
                    sizeClassName={styles.addButton}
                    onClick={() => !busy && void actions.sendRequest(result.user_id)}
                    aria-disabled={busy !== undefined || undefined}
                    aria-busy={busy === 'REQUEST' || undefined}
                    aria-label={t('friends.search.addLabel', { username: result.username })}
                    className={styles.addPlacement}
                  >
                    {t(busy === 'REQUEST' ? 'friends.search.adding' : 'friends.search.add')}
                  </Button>
                )}
                {state === 'REQUEST_SENT' && requestId !== null && (
                  <CancelRequestButton
                    username={result.username}
                    busy={busy}
                    onCancel={() => void actions.cancel(result.user_id, requestId)}
                  />
                )}
                {state === 'REQUEST_RECEIVED' && requestId !== null && (
                  <Button
                    icon="check"
                    sizeClassName={styles.addButton}
                    onClick={() => !busy && void actions.accept(result.user_id, requestId)}
                    aria-disabled={busy !== undefined || undefined}
                    aria-busy={busy === 'ACCEPT' || undefined}
                    aria-label={t('friends.requests.acceptLabel', { username: result.username })}
                    className={styles.addPlacement}
                  >
                    {t(
                      busy === 'ACCEPT' ? 'friends.requests.accepting' : 'friends.requests.accept',
                    )}
                  </Button>
                )}
                {failure && (
                  <p role="alert" className={styles.rowError}>
                    {t(failure)}
                  </p>
                )}
              </li>
            );
          })}
          {search.hasMore && (
            <li className="flex">
              <button
                type="button"
                onClick={search.loadMore}
                aria-busy={search.loadingMore || undefined}
                className={styles.showMore}
              >
                {t(search.loadingMore ? 'friends.search.loadingMore' : 'friends.search.more')}
              </button>
            </li>
          )}
        </ul>
      )}
    </section>
  );
}

/** Cancel on a request the user sent: withdraws it while it waits for an answer. */
function CancelRequestButton({
  username,
  busy,
  onCancel,
}: {
  username: string;
  busy: SocialAction | undefined;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  return (
    <Button
      variant="neutral"
      sizeClassName={styles.addButton}
      onClick={() => !busy && onCancel()}
      aria-disabled={busy !== undefined || undefined}
      aria-busy={busy === 'CANCEL' || undefined}
      aria-label={t('friends.requests.cancelLabel', { username })}
      className={styles.addPlacement}
    >
      {t(busy === 'CANCEL' ? 'friends.requests.cancelling' : 'friends.requests.cancel')}
    </Button>
  );
}

/** Avatar and name, which open the player's profile, with a line of status under the name. */
function PlayerIdentity({
  player,
  children,
}: {
  player: Pick<Friend, 'user_id' | 'username' | 'avatar_url'>;
  children?: ReactNode;
}) {
  const { t } = useTranslation();
  return (
    <button
      type="button"
      onClick={() => openProfileModal(player.user_id)}
      aria-haspopup="dialog"
      aria-label={t('lobby.players.openProfile', { username: player.username })}
      className={styles.profileButton}
    >
      <span className={styles.avatar}>
        <AvatarImage
          src={player.avatar_url}
          className={styles.avatarImage}
          placeholderClassName={styles.avatarPlaceholder}
        />
      </span>
      <span className={styles.identity}>
        <span className={styles.name}>{player.username}</span>
        {children}
      </span>
    </button>
  );
}

function Loading({ label }: { label: string }) {
  return (
    <div className={styles.state}>
      <LoadingDots size="sm" label={label} />
    </div>
  );
}

function State({
  icon,
  title,
  body,
  action,
  alert = false,
}: {
  icon: IconName;
  title: string;
  body: string;
  action?: { label: string; icon: IconName; onClick: () => void };
  alert?: boolean;
}) {
  return (
    <div role={alert ? 'alert' : undefined} className={styles.state}>
      <Icon name={icon} className={styles.stateIcon} />
      <p className={styles.stateTitle}>{title}</p>
      <p className={styles.stateBody}>{body}</p>
      {action && (
        <button type="button" onClick={action.onClick} className={styles.stateAction}>
          <Icon name={action.icon} />
          {action.label}
        </button>
      )}
    </div>
  );
}
