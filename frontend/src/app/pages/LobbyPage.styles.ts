/**
 * Class recipes for Room Discovery.
 *
 * The page has three designed layouts: mobile (390px) is the base, `md:` is the tablet
 * layout (834px) and `desktop:` is the 1920×1080 desktop composition drawn at 80% of the
 * design's size, so the page stays comfortable on common desktop screens. On desktop the
 * content is a 958px column, centred on the viewport, with every block at the design's
 * offset inside it; the profile card sits 32px from the top right corner of the viewport.
 * Colors, shadows and radii come from the theme.
 */

export const page: string =
  'relative flex flex-1 flex-col px-3 pt-[20px] pb-4 md:px-12 md:pt-[32px] md:pb-6 ' +
  'desktop:bg-surface-raised desktop:px-3 desktop:pt-0 desktop:pb-[69px]';

export const column: string =
  'relative mx-auto flex w-full flex-col md:max-w-[738px] desktop:block ' +
  'desktop:max-w-[958px] desktop:pt-[183px]';

// ---- Header ----

/** On desktop the card sits 32px from the viewport's right edge, outside the column. */
export const header: string =
  'flex min-h-12 items-center desktop:absolute desktop:top-[21px] ' +
  'desktop:right-[calc(511px_-_50vw)]';

export const profile: string = 'min-w-0 max-w-full';

// ---- Hero ----

export const mascot: string =
  'pointer-events-none absolute top-[117px] left-[602px] hidden h-[221px] w-[389px] ' +
  'max-w-none desktop:block';

export const headline: string =
  'mt-[14px] text-4xl leading-[34px] font-black text-text-ink normal-case md:mt-[20px] ' +
  'desktop:mt-0 desktop:text-[48px] desktop:leading-[48px] desktop:tracking-[-0.8px]';

export const subtitle: string =
  'mt-[14px] text-lg leading-[29px] font-regular text-text-muted md:mt-[20px] ' +
  'desktop:mt-[11px] desktop:text-[26px] desktop:leading-[26px] desktop:font-medium ' +
  'desktop:text-text-ink';

// ---- Create Room and Join Room ----

export const actions: string =
  'mt-[14px] flex flex-col gap-[14px] md:mt-[20px] md:gap-[20px] desktop:mt-[70px] ' +
  'desktop:flex-row desktop:gap-[30px]';

export const card: string =
  'flex flex-col gap-[10px] rounded-lg bg-surface p-3 shadow-card ' +
  'desktop:relative desktop:block desktop:h-[270px] desktop:w-[464px] desktop:shrink-0 ' +
  'desktop:rounded-[18px] desktop:p-0 ' +
  'desktop:shadow-[0_7px_10px_var(--color-shadow-warm-medium),0_1px_3px_var(--color-shadow-subtle)]';

export const cardArtwork: string = 'pointer-events-none absolute hidden max-w-none desktop:block';

/** The two illustrations are drawn on differently sized frames in the design. */
export const cardArtworkPlacement = {
  create: 'top-[13px] left-[23px] h-[162px] w-[147px]',
  join: 'top-0 left-0 h-[182px] w-[178px]',
};

export const cardBadge: string =
  'absolute top-[17px] left-[399px] hidden size-[47px] items-center justify-center ' +
  'rounded-pill text-[24px] text-surface desktop:flex';

export const cardBadgeTone = {
  create: 'bg-accent-orange',
  join: 'bg-primary-sky',
};

export const cardTitleRow: string =
  'flex items-center gap-[10px] desktop:absolute desktop:top-[48px] desktop:left-[178px]';

export const cardTile: string =
  'inline-flex size-10 shrink-0 items-center justify-center rounded-md text-2xl ' +
  'text-surface desktop:hidden';

export const cardTileTone = {
  create: 'bg-primary',
  join: 'bg-secondary-deep',
};

export const cardTitle: string =
  'text-3xl leading-[43px] font-black text-text-ink normal-case desktop:text-[32px] ' +
  'desktop:leading-[32px]';

export const cardDescription: string =
  'text-md leading-[22px] font-regular text-text-muted desktop:absolute desktop:top-[90px] ' +
  'desktop:left-[178px] desktop:w-[272px] desktop:text-[22px] desktop:leading-[25px] ' +
  'desktop:font-medium desktop:text-text-ink';

/** Mobile and tablet use the one-line copy; desktop breaks it where the design does. */
export const cardDescriptionShort: string = 'desktop:hidden';

export const cardDescriptionLong: string = 'hidden whitespace-pre-line desktop:inline';

/** Create is the shared primary `Button` and Join the coral secondary one. */
export const cardButton: string =
  'h-12 gap-[10px] rounded-lg text-xl leading-none font-bold whitespace-nowrap uppercase ' +
  'desktop:h-[64px] desktop:rounded-md desktop:text-[22px]';

export const cardButtonPlacement: string =
  'w-full desktop:absolute desktop:top-[182px] desktop:left-[23px] desktop:w-[418px]';

/** The label is centred on the whole button; the icon sits after it, as the design places it. */
export const cardButtonIcon: string =
  'absolute top-[20px] left-[286px] hidden size-[24px] items-center justify-center ' +
  'rounded-pill bg-surface text-[14px] desktop:inline-flex';

export const cardButtonIconTone = {
  create: 'text-primary-sky',
  join: 'text-secondary-deep',
};

// ---- Players Online ----

export const players: string = 'mt-[14px] md:mt-[20px] desktop:mt-[43px]';
