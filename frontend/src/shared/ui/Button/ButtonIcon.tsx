import { cn } from '@shared/utils';
import { Icon } from '@shared/ui/Icon';

import { iconBaseStyles, iconSizeStyles, variantStyles } from './Button.styles';
import type { ButtonIconProps } from './Button.types';

/**
 * Circular icon-only button: close, send, navigate.
 *
 * It has no visible label, so `aria-label` is required and says what the button does.
 * `sizeClassName` replaces the preset diameter and glyph size, as it does on `Button`.
 */
export function ButtonIcon({
  variant = 'primary',
  size = 'md',
  sizeClassName,
  loading = false,
  className,
  disabled,
  type = 'button',
  theme = 'fill',
  icon,
  ...props
}: ButtonIconProps) {
  return (
    <button
      type={type}
      disabled={disabled}
      aria-busy={loading || undefined}
      aria-disabled={loading || undefined}
      className={cn(
        iconBaseStyles[theme],
        variantStyles[theme][variant],
        sizeClassName ?? iconSizeStyles[size],
        loading && 'pointer-events-none',
        className,
      )}
      {...props}
    >
      <Icon name={icon} spin={loading} />
    </button>
  );
}
