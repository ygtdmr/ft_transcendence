import { useEffect, useRef } from 'react';
import type { ReactNode } from 'react';
import { createPortal } from 'react-dom';

import { useFocusTrap, useScrollLock } from '@shared/hooks';
import { cn } from '@shared/utils';
import { Icon } from '@shared/ui/Icon';

export interface DialogProps {
  /** Id of the element that names the dialog, usually its heading. */
  labelledBy: string;
  /** Id of the element that describes the dialog, usually its subtitle. */
  describedBy?: string | undefined;
  /** Accessible name of the close button. */
  closeLabel: string;
  onClose: () => void;
  /**
   * `false` while the dialog must not be dismissed, such as during a request whose
   * result it still has to show: the close button is disabled and Escape is ignored.
   */
  closable?: boolean;
  /**
   * `false` for a dialog whose design has no close button, such as a confirmation that
   * offers its own safe action. Escape still closes it while it is `closable`.
   */
  showCloseButton?: boolean;
  /** `alertdialog` for a dialog that interrupts to ask for a decision or to announce one. */
  role?: 'dialog' | 'alertdialog';
  /** The dialog surface: its width, background, padding and shadow. */
  className?: string;
  children: ReactNode;
}

const backdrop: string =
  'fixed inset-0 flex overflow-y-auto overscroll-contain bg-overlay p-3 ' +
  'backdrop-blur-[2px] motion-safe:animate-fade-in';

const surface: string = 'relative m-auto flex w-full shrink-0 flex-col motion-safe:animate-pop-in';

const closeButton: string =
  'absolute top-[12px] right-[12px] z-10 inline-flex h-10 w-10 cursor-pointer items-center ' +
  'justify-center rounded-pill bg-surface-muted text-xl text-text-slate ' +
  'transition-[scale,background-color,color,opacity] duration-100 ease-in ' +
  'not-disabled:hover:bg-surface-sunken not-disabled:hover:text-text-ink ' +
  'motion-safe:not-disabled:hover:scale-105 motion-safe:not-disabled:active:scale-95 ' +
  'disabled:cursor-default disabled:opacity-50 md:top-[16px] md:right-[14px]';

/**
 * Popup dialog over the page that opened it. The page stays mounted and visible behind a
 * dimmed backdrop, and opening or closing the dialog never changes the route.
 *
 * The shell owns the dialog keyboard contract: focus is trapped inside, Escape and the close
 * button close it, and focus returns to the control that opened it. The page behind is held
 * still; where the scrollbar takes up room, its width is kept as padding so the page does
 * not shift sideways. A dialog taller than the viewport scrolls inside the backdrop.
 *
 * A press on the backdrop does not close it, so a stray click never discards what the user
 * entered. The feature supplies the surface styling and the content.
 */
export function Dialog({
  labelledBy,
  describedBy,
  closeLabel,
  onClose,
  closable = true,
  showCloseButton = true,
  role = 'dialog',
  className,
  children,
}: DialogProps) {
  const dialogRef = useRef<HTMLDivElement>(null);

  useFocusTrap(dialogRef, true, 'first');
  useScrollLock();

  useEffect(() => {
    if (!closable) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [closable, onClose]);

  return createPortal(
    <div
      className={cn(backdrop, 'z-(--z-modal)')}
      // A press on the backdrop would move focus to the page body, outside the trap.
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) event.preventDefault();
      }}
    >
      <div
        ref={dialogRef}
        role={role}
        aria-modal
        aria-labelledby={labelledBy}
        aria-describedby={describedBy}
        tabIndex={-1}
        className={cn(surface, className)}
      >
        {showCloseButton && (
          <button
            type="button"
            onClick={onClose}
            disabled={!closable}
            aria-label={closeLabel}
            className={closeButton}
          >
            <Icon name="close" />
          </button>
        )}
        {children}
      </div>
    </div>,
    document.body,
  );
}
