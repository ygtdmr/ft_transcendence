/**
 * PICK AVATAR pop-up in Settings.
 *
 * Loads the preset catalog when the pop-up opens. The current avatar starts selected
 * when its URL matches a preset; tapping a tile moves the selection, SAVE AVATAR applies
 * it with `PUT /api/v1/users/me/avatar`, and Cancel discards it.
 */

import { useCallback, useEffect, useRef, useState } from 'react';

import type { ApiError } from '@shared/api';
import type { AvatarPreset } from '@shared/types';

import { listAvatarPresets, selectAvatarPreset } from '../api';

export type AvatarPresetsStatus = 'LOADING' | 'ERROR' | 'READY';

export function useAvatarPresets(currentAvatarUrl: string, onSaved: (avatarUrl: string) => void) {
  const [presets, setPresets] = useState<AvatarPreset[]>([]);
  const [status, setStatus] = useState<AvatarPresetsStatus>('LOADING');
  const [chosenId, setChosenId] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const busy = useRef(false);

  const load = useCallback(async () => {
    setStatus('LOADING');
    setError(null);
    try {
      const { presets: list } = await listAvatarPresets();
      setPresets(list);
      setStatus('READY');
    } catch (cause) {
      setError(cause as ApiError);
      setStatus('ERROR');
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const currentId = presets.find((p) => p.avatar_url === currentAvatarUrl)?.preset_id ?? null;

  const save = useCallback(async () => {
    if (!chosenId || chosenId === currentId || busy.current) return;
    busy.current = true;
    setSaving(true);
    setError(null);
    try {
      const { avatar_url: avatarUrl } = await selectAvatarPreset(chosenId);
      setChosenId(null);
      onSaved(avatarUrl);
    } catch (cause) {
      setError(cause as ApiError);
    } finally {
      busy.current = false;
      setSaving(false);
    }
  }, [chosenId, currentId, onSaved]);

  return {
    presets,
    status,
    selectedId: chosenId ?? currentId,
    /** SAVE AVATAR is enabled only when a different avatar is selected. */
    canSave: chosenId !== null && chosenId !== currentId && !saving,
    saving,
    error,
    choose: setChosenId,
    save,
    cancel: () => setChosenId(null),
    retry: () => void load(),
  };
}
