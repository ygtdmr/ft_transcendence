import { buttonInteraction } from '@shared/ui';

/**
 * Class recipes for the Friends dialog.
 *
 * It is built from the same parts as the Settings card and the Players Online cards: the
 * Settings card surface and header, a pill search field, and player rows with the
 * Players Online presence badge. Phones are the base; from `md:` the card is the 80% scale
 * the other pop-ups over Room Discovery use.
 */

export const card: string =
  'max-w-[358px] gap-[14px] rounded-[35px] bg-surface px-[20px] py-[24px] shadow-auth-dialog ' +
  'md:max-w-[480px] md:gap-[16px] md:rounded-[28px] md:px-[28px] md:py-[26px]';

/** Clear of the close button, which sits over the header's top-right corner. */
export const header: string = 'flex w-full flex-col gap-[4px] pr-[48px]';

export const title: string =
  'text-3xl leading-[1.3] font-black text-text-ink normal-case md:text-[26px]';

export const subtitle: string = 'text-md leading-[1.5] font-regular text-text-muted';

// ---- Search ----

export const search: string =
  'relative flex h-[46px] shrink-0 items-center rounded-pill border-(length:--stroke-default) ' +
  'border-border bg-surface shadow-alert focus-within:border-primary-sky md:h-[42px]';

export const searchIcon: string =
  'pointer-events-none absolute left-[15px] text-[18px] text-text-slate md:text-[16px]';

export const searchInput: string =
  'h-full w-full rounded-pill bg-transparent pr-[16px] pl-[44px] text-lg font-regular ' +
  'text-text-ink outline-none placeholder:text-text-placeholder md:text-md';

export const hint: string = 'text-sm leading-[18px] font-regular text-text-muted';

// ---- List ----

export const sectionTitle: string =
  'text-md leading-[22px] font-black tracking-[1px] text-text-slate uppercase ' +
  'md:text-xs md:leading-[18px] md:tracking-[0.8px]';

/** The list scrolls inside the card, so the dialog keeps its place on short screens. */
export const list: string =
  '-mx-[6px] flex max-h-[min(52vh,420px)] flex-col gap-[8px] overflow-y-auto px-[6px] py-[4px]';

export const row: string =
  'flex min-h-[60px] flex-wrap items-center gap-x-[10px] gap-y-[4px] rounded-md bg-surface ' +
  'px-[10px] py-[8px] shadow-panel';

export const profileButton: string =
  'flex min-w-0 flex-1 cursor-pointer items-center gap-[10px] rounded-sm text-left ' +
  'transition-[color] duration-200 hover:text-primary-sky';

export const avatar: string =
  'flex size-[44px] shrink-0 items-center justify-center overflow-hidden rounded-pill ' +
  'bg-secondary-deep';

export const avatarImage: string = 'h-full w-full object-cover';

export const avatarPlaceholder: string = 'text-2xl text-surface';

export const identity: string = 'flex min-w-0 flex-col gap-[3px]';

export const name: string = 'truncate text-xl leading-[22px] font-black text-text-ink';

const badge: string =
  'inline-flex h-[20px] items-center gap-[4px] self-start rounded-[13px] pr-[8px] pl-[6px] ' +
  'text-[13px] leading-none font-bold';

export const badgeOnline: string = `${badge} bg-accent-mint-deep text-success-deep`;

export const badgeOffline: string = `${badge} bg-surface-muted text-text-slate`;

export const badgeFriend: string = `${badge} bg-primary-sky/15 text-primary-ink`;

export const badgePending: string = `${badge} bg-accent-yellow/25 text-accent-brown`;

export const badgeDot: string = 'size-[7px] rounded-pill bg-current';

/** The design system's compact primary button. */
export const addButton: string =
  'h-[36px] gap-[6px] rounded-[14px] px-[12px] text-md leading-none font-bold';

export const addPlacement: string = 'shrink-0 aria-disabled:opacity-70';

/** Accept and Decline side by side at the end of a request row. */
export const rowActions: string = 'ml-auto flex shrink-0 gap-[8px]';

/** Requests sit above the friend list and scroll on their own when there are many. */
export const requestList: string =
  '-mx-[6px] flex max-h-[200px] flex-col gap-[8px] overflow-y-auto px-[6px] py-[4px]';

export const rowError: string = 'basis-full text-sm leading-[18px] font-bold text-accent-red';

export const showMore: string =
  `${buttonInteraction} mx-auto inline-flex h-[36px] items-center gap-[6px] rounded-pill ` +
  'border border-(length:--stroke-thin) border-border bg-surface px-[14px] text-md ' +
  'leading-none font-bold text-text-ink not-disabled:hover:bg-surface-raised';

// ---- States ----

export const state: string =
  'flex flex-col items-center gap-[8px] rounded-md border border-(length:--stroke-thin) ' +
  'border-border bg-surface-raised px-[16px] py-[20px] text-center';

export const stateIcon: string = 'text-[32px] text-text-muted';

export const stateTitle: string = 'text-xl leading-[26px] font-black text-text-ink';

export const stateBody: string = 'text-md leading-[20px] font-regular text-text-muted';

export const stateAction: string =
  `${buttonInteraction} mt-[4px] inline-flex h-[40px] items-center gap-[8px] rounded-[20px] ` +
  'bg-surface px-[14px] text-md leading-none font-bold text-text-ink shadow-panel ' +
  'not-disabled:hover:shadow-[0_8px_12px_var(--color-shadow-warm-soft)]';

// ---- Entry point ----

/** The Friends button beside the Players Online heading. */
export const openButton: string =
  `${buttonInteraction} inline-flex h-[36px] items-center gap-[8px] rounded-pill bg-surface ` +
  'px-[14px] text-md leading-none font-bold text-text-ink shadow-panel ' +
  'not-disabled:hover:shadow-[0_8px_12px_var(--color-shadow-warm-soft)] desktop:h-[34px]';

/** Friend requests waiting for the user's answer, in the warm accent. */
export const requestCount: string =
  'inline-flex min-w-[22px] items-center justify-center rounded-pill bg-secondary px-[6px] ' +
  'py-[2px] text-sm leading-none font-bold text-surface';

export const openCount: string =
  'inline-flex min-w-[22px] items-center justify-center rounded-pill bg-primary-sky px-[6px] ' +
  'py-[2px] text-sm leading-none font-bold text-surface';
