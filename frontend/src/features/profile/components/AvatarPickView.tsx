import { useRef } from 'react';
import type { KeyboardEvent, RefObject } from 'react';
import { useTranslation } from 'react-i18next';

import { Button, FieldMessage, Icon, Skeleton } from '@shared/ui';
import { cn } from '@shared/utils';

import { useAvatarPresets } from '../hooks/useAvatarPresets';
import * as styles from './SettingsModal.styles';

/** Placeholders while the catalog loads: the design's grid holds 11 avatars. */
const SKELETON_TILES = 11;

/** `aviator-fox` reads as "aviator fox". */
const presetName = (presetId: string): string => presetId.replace(/[-_]+/g, ' ');

/**
 * Pick Avatar, inside the Settings dialog: the ready-made avatars from
 * `GET /api/v1/avatars/presets` as one radio group. The current avatar starts selected;
 * choosing another moves the selection, SAVE AVATAR applies it and Cancel keeps the
 * current one.
 */
export function AvatarPickView({
  currentAvatarUrl,
  titleId,
  titleRef,
  onSaved,
  onCancel,
}: {
  currentAvatarUrl: string;
  titleId: string;
  titleRef: RefObject<HTMLHeadingElement | null>;
  onSaved: (avatarUrl: string) => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const picker = useAvatarPresets(currentAvatarUrl, onSaved);
  const tiles = useRef<(HTMLButtonElement | null)[]>([]);
  const { presets, selectedId, choose } = picker;

  // One stop in the Tab order; the arrow keys move the selection, as in any radio group.
  const focusIndex = Math.max(
    0,
    presets.findIndex((p) => p.preset_id === selectedId),
  );
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const step = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[event.key];
    if (!step || presets.length === 0) return;
    event.preventDefault();
    const next = (focusIndex + step + presets.length) % presets.length;
    choose(presets[next]!.preset_id);
    tiles.current[next]?.focus();
  };

  return (
    <>
      <div className={styles.header}>
        <h2 id={titleId} ref={titleRef} tabIndex={-1} className={styles.title}>
          {t('settings.pick.title')}
        </h2>
        <p className={styles.subtitle}>{t('settings.pick.subtitle')}</p>
      </div>

      {picker.status === 'READY' ? (
        <div
          role="radiogroup"
          aria-labelledby={titleId}
          onKeyDown={onKeyDown}
          className={styles.grid}
        >
          {presets.map((preset, index) => {
            const selected = preset.preset_id === selectedId;
            return (
              <button
                key={preset.preset_id}
                ref={(node) => {
                  tiles.current[index] = node;
                }}
                type="button"
                role="radio"
                aria-checked={selected}
                aria-label={t('settings.pick.option', { name: presetName(preset.preset_id) })}
                tabIndex={index === focusIndex ? 0 : -1}
                onClick={() => choose(preset.preset_id)}
                disabled={picker.saving}
                className={styles.tile}
              >
                <img
                  src={preset.avatar_url}
                  alt=""
                  className={cn(styles.tileImage, selected && styles.tileImageSelected)}
                />
                {selected && (
                  <span aria-hidden="true" className={styles.tileCheck}>
                    <Icon name="check" />
                  </span>
                )}
              </button>
            );
          })}
        </div>
      ) : picker.status === 'LOADING' ? (
        <div role="status" aria-label={t('settings.pick.loading')} className={styles.grid}>
          {Array.from({ length: SKELETON_TILES }, (_, i) => (
            <Skeleton key={i} shape="circle" className={styles.tileSkeleton} />
          ))}
        </div>
      ) : (
        <div role="alert" className={styles.pickNotice}>
          <p className={styles.hint}>{t('settings.pick.loadFailed')}</p>
          <Button
            theme="outline"
            sizeClassName={styles.outlineButton}
            onClick={picker.retry}
            className={styles.outlineButtonWidth}
          >
            {t('settings.pick.retry')}
          </Button>
        </div>
      )}

      {picker.status === 'READY' && picker.error ? (
        <FieldMessage id={`${titleId}-error`} status="error" className={styles.fieldError}>
          {t('settings.pick.failed')}
        </FieldMessage>
      ) : (
        <p className={styles.hint}>{t('settings.pick.hint')}</p>
      )}

      <div className={styles.actions}>
        <Button
          theme="outline"
          sizeClassName={styles.actionOutline}
          onClick={onCancel}
          disabled={picker.saving}
          className="bg-surface"
        >
          {t('settings.pick.cancel')}
        </Button>
        <Button
          sizeClassName={styles.actionPrimary}
          onClick={() => void picker.save()}
          disabled={!picker.canSave}
          aria-busy={picker.saving}
        >
          {t(picker.saving ? 'settings.pick.saving' : 'settings.pick.save')}
        </Button>
      </div>
    </>
  );
}
