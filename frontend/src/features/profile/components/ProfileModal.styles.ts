/**
 * Class recipes for the Profile pop-up.
 *
 * Phones are the base: the design's 358px mobile card at full size, except the avatar and
 * the name, which are drawn at 80% so a username of up to 20 characters fits beside the
 * avatar. From `md:` the card is the desktop design at 80%, 608px wide, the scale the other
 * pop-ups use, so it stays a compact card over the page on tablets as well. The shared
 * `Dialog` shell draws the backdrop and the close button in the top-right corner.
 */

export const card: string =
  'max-w-[358px] gap-[18px] rounded-[35px] bg-surface px-[20px] py-[24px] shadow-auth-dialog ' +
  'md:max-w-[608px] md:gap-[19px] md:rounded-[28px] md:px-[32px] md:py-[29px]';

// ---- Header ----

export const header: string = 'flex items-center gap-[20px] md:gap-[19px]';

export const avatarFrame: string = 'relative shrink-0';

export const avatar: string =
  'flex size-[88px] items-center justify-center overflow-hidden rounded-pill border-3 ' +
  'border-primary-sky bg-accent-yellow-deep md:size-[90px]';

export const avatarImage: string = 'h-full w-full object-cover';

export const avatarPlaceholder: string = 'text-[44px] text-text';

export const onlineDot: string =
  'absolute right-0 bottom-0 size-[22px] rounded-pill border-3 border-surface bg-success-soft';

/** Clear of the close button, which sits over the header's top-right corner. */
export const identity: string =
  'flex min-w-0 flex-1 flex-col gap-[8px] pr-[36px] md:gap-[6px] md:pr-[44px]';

/** A long username wraps rather than being cut off: the name is what the pop-up is about. */
export const name: string =
  'text-4xl leading-none font-bold [overflow-wrap:anywhere] text-text-ink normal-case';

export const handle: string =
  'truncate text-2xl leading-none font-regular text-text-gray md:text-lg';

export const statusBase: string =
  'flex items-center gap-[8px] text-xl leading-none font-bold md:gap-[6px] md:text-[16px]';

export const status = {
  online: 'text-success-deep',
  offline: 'text-text-muted',
};

export const statusDot: string = 'size-[13px] shrink-0 rounded-pill bg-current md:size-[10px]';

// ---- Actions ----

export const actions: string = 'flex flex-col gap-[10px] md:gap-[8px]';

export const actionRow: string = 'flex gap-[14px]';

/** Geometry of the action row's buttons; the shared `Button` draws Add, Remove and Message. */
export const action: string =
  'h-[58px] gap-[8px] rounded-[22px] px-[12px] text-xl leading-none font-bold tracking-tight ' +
  'whitespace-nowrap md:h-[46px] md:rounded-[18px] md:text-[16px]';

/** Add Friend is primary and Remove Friend neutral, the safe secondary choice. */
export const friend: string = 'min-w-0 flex-1 aria-disabled:pointer-events-none';

export const friendSkeleton: string =
  'h-[58px] flex-1 rounded-[22px] bg-surface-sunken motion-safe:animate-pulse md:h-[46px] ' +
  'md:rounded-[18px]';

/** Takes no room while there is nothing to say, so no blank line sits under the buttons. */
export const hintBase: string =
  'text-center text-md leading-[22px] font-regular empty:hidden md:text-sm md:leading-[18px]';

export const hint = {
  neutral: 'text-text-muted',
  success: 'text-success-deep',
  error: 'text-accent-red',
};

export const hintAction: string =
  'ml-1 rounded-sm font-bold text-primary-sky underline-offset-2 hover:text-primary ' +
  'hover:underline';

/** Report Player: a quiet link at the foot of another player's profile. */
export const report: string =
  'gap-[8px] text-md leading-[22px] font-bold md:text-sm md:leading-[18px]';

/** Muted at rest, it turns red on hover, as the serious step it is. */
export const reportPlacement: string = 'mx-auto not-aria-disabled:hover:text-accent-red';

// ---- Stats ----

export const statsBlock: string = 'flex flex-col gap-[8px] md:gap-[6px]';

export const stats: string = 'grid grid-cols-3 gap-[12px] md:gap-[13px]';

/** Matches played, under the three tiles: every settled match, forfeits included. */
export const statsCaption: string =
  'text-center text-md leading-[20px] font-bold text-text-muted md:text-sm md:leading-[18px]';

export const stat: string =
  'flex min-w-0 flex-col items-center gap-[4px] rounded-[20px] bg-surface-muted px-[6px] ' +
  'py-[16px] md:gap-[3px] md:rounded-[16px] md:px-[8px] md:py-[13px]';

export const statLabel: string =
  'text-lg leading-none font-black tracking-[2px] text-text-muted uppercase ' +
  'md:text-md md:tracking-[1.6px]';

export const statValue: string =
  'text-4xl leading-none font-bold text-text-ink tabular-nums md:text-[26px]';

// ---- Loading, not found and error ----

export const skeletonHeader: string =
  'flex items-center gap-[20px] pr-[36px] md:gap-[19px] md:pr-[44px]';

export const skeletonAvatar: string = 'size-[88px] shrink-0 md:size-[90px]';

export const skeletonStat: string = 'h-[86px] rounded-[20px] md:h-[69px] md:rounded-[16px]';

export const notice: string =
  'flex flex-col items-center gap-[12px] px-[8px] pt-[36px] pb-[8px] text-center md:pt-[24px]';

export const noticeIcon: string = 'text-[48px] text-text-disabled md:text-[40px]';

export const noticeTitle: string =
  'text-3xl leading-[1.2] font-black text-text-ink normal-case md:text-2xl';

export const noticeBody: string =
  'max-w-[400px] text-lg leading-[1.4] font-regular text-text-muted md:text-md';

export const retry: string =
  'h-[54px] rounded-[22px] px-[20px] text-xl leading-none font-bold md:h-[43px] ' +
  'md:rounded-[18px] md:text-[18px]';

export const retryPlacement: string = 'mt-[8px] w-full md:w-[240px]';
