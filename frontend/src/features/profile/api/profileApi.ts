/**
 * Profile REST bindings — Bruno `rest-api/02 - Users & Profile/`.
 */

import { apiRequest } from '@shared/api';
import { CORE_API_PATH } from '@shared/constants';
import type {
  AvatarPresetListResponse,
  DeleteOwnAccountRequest,
  OwnProfile,
  PublicProfile,
  ReportUserRequest,
  ReportUserResponse,
  UpdateOwnProfileRequest,
  UploadAvatarResponse,
} from '@shared/types';

export const getOwnProfile = (): Promise<OwnProfile> => apiRequest(`${CORE_API_PATH}/users/me`);

export const getPublicProfile = (userId: number): Promise<PublicProfile> =>
  apiRequest(`${CORE_API_PATH}/users/${userId}`);

export const updateOwnProfile = (body: UpdateOwnProfileRequest): Promise<OwnProfile> =>
  apiRequest(`${CORE_API_PATH}/users/me`, { method: 'PATCH', body });

/**
 * Permanently deletes the signed-in account. The server removes the user from any room,
 * revokes every session, clears both cookies and closes every socket.
 */
export const deleteOwnAccount = (body: DeleteOwnAccountRequest): Promise<void> =>
  apiRequest(`${CORE_API_PATH}/users/me`, { method: 'DELETE', body });

/** Reports another user for moderation; the reported user is not notified. */
export const reportUser = (userId: number, body: ReportUserRequest): Promise<ReportUserResponse> =>
  apiRequest(`${CORE_API_PATH}/users/${userId}/reports`, { method: 'POST', body });

/**
 * Multipart upload, field name `avatar`. `image/jpeg`, `image/png` or `image/webp`,
 * 2 MiB maximum. Content-Type is deliberately left to the browser so the multipart
 * boundary is correct.
 */
export const uploadAvatar = (file: File): Promise<UploadAvatarResponse> => {
  const formData = new FormData();
  formData.append('avatar', file);
  return apiRequest(`${CORE_API_PATH}/users/me/avatar`, { method: 'POST', formData });
};

/** Ready-made avatars a user can pick instead of uploading a photo. */
export const listAvatarPresets = (): Promise<AvatarPresetListResponse> =>
  apiRequest(`${CORE_API_PATH}/avatars/presets`);

/** Replace the avatar with a preset; the most recent upload or pick wins. */
export const selectAvatarPreset = (presetId: string): Promise<UploadAvatarResponse> =>
  apiRequest(`${CORE_API_PATH}/users/me/avatar`, { method: 'PUT', body: { preset_id: presetId } });
