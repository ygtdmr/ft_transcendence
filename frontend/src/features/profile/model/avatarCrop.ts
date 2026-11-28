/**
 * Avatar crop geometry and export.
 *
 * Avatars are square. The crop viewport is a square the image always covers: at zoom 1
 * the image's short side fills it, and panning is clamped so no empty area shows. The
 * export draws exactly the visible square into a fixed-size canvas with a uniform
 * scale, so the result is never stretched. The server still validates and resizes.
 */

export const AVATAR_RULES = {
  types: ['image/jpeg', 'image/png', 'image/webp'] as const,
  maxBytes: 2 * 1024 * 1024,
  outputSize: 512,
  minZoom: 1,
  maxZoom: 3,
} as const;

export type AvatarFileProblem = 'INVALID_TYPE' | 'TOO_LARGE';

/** Client-side precheck; the server re-validates from the file bytes. */
export function checkAvatarFile(file: File): AvatarFileProblem | null {
  if (!(AVATAR_RULES.types as readonly string[]).includes(file.type)) return 'INVALID_TYPE';
  if (file.size > AVATAR_RULES.maxBytes) return 'TOO_LARGE';
  return null;
}

export interface CropState {
  /** Uniform zoom over the cover scale, `minZoom..maxZoom`. */
  zoom: number;
  /** Image top-left relative to the viewport's top-left, in viewport pixels. */
  x: number;
  y: number;
}

export interface ImageSize {
  width: number;
  height: number;
}

const coverScale = (image: ImageSize, viewport: number): number =>
  viewport / Math.min(image.width, image.height);

/** Centered crop at zoom 1 — also the automatic crop when the user changes nothing. */
export function initialCrop(image: ImageSize, viewport: number): CropState {
  return clampCrop(image, viewport, { zoom: 1, x: -Infinity, y: -Infinity }, true);
}

/** Clamp zoom and position so the image fully covers the viewport. */
export function clampCrop(
  image: ImageSize,
  viewport: number,
  crop: CropState,
  center = false,
): CropState {
  const zoom = Math.min(AVATAR_RULES.maxZoom, Math.max(AVATAR_RULES.minZoom, crop.zoom));
  const scale = coverScale(image, viewport) * zoom;
  const w = image.width * scale;
  const h = image.height * scale;
  const clamp = (value: number, size: number) =>
    center ? (viewport - size) / 2 : Math.min(0, Math.max(viewport - size, value));
  return { zoom, x: clamp(crop.x, w), y: clamp(crop.y, h) };
}

/** Zoom around the viewport center, keeping the centered point fixed. */
export function zoomCrop(
  image: ImageSize,
  viewport: number,
  crop: CropState,
  zoom: number,
): CropState {
  const ratio = zoom / crop.zoom;
  const c = viewport / 2;
  return clampCrop(image, viewport, {
    zoom,
    x: c - (c - crop.x) * ratio,
    y: c - (c - crop.y) * ratio,
  });
}

export function panCrop(
  image: ImageSize,
  viewport: number,
  crop: CropState,
  dx: number,
  dy: number,
): CropState {
  return clampCrop(image, viewport, { ...crop, x: crop.x + dx, y: crop.y + dy });
}

/** Where the image sits in the viewport, in viewport pixels, for drawing the crop. */
export function displayRect(image: ImageSize, viewport: number, crop: CropState) {
  const scale = coverScale(image, viewport) * crop.zoom;
  return { x: crop.x, y: crop.y, width: image.width * scale, height: image.height * scale };
}

/** The square region of the source image that the viewport shows, in source pixels. */
export function sourceRect(image: ImageSize, viewport: number, crop: CropState) {
  const scale = coverScale(image, viewport) * crop.zoom;
  return { sx: -crop.x / scale, sy: -crop.y / scale, size: viewport / scale };
}

/** Render the crop to a square file ready for `POST /api/v1/users/me/avatar`. */
export async function exportAvatar(
  image: CanvasImageSource & ImageSize,
  viewport: number,
  crop: CropState,
): Promise<File> {
  const { outputSize } = AVATAR_RULES;
  const canvas = document.createElement('canvas');
  canvas.width = outputSize;
  canvas.height = outputSize;
  const context = canvas.getContext('2d');
  if (!context) throw new Error('Canvas 2D context is unavailable.');
  const { sx, sy, size } = sourceRect(image, viewport, crop);
  context.imageSmoothingQuality = 'high';
  context.drawImage(image, sx, sy, size, size, 0, 0, outputSize, outputSize);

  const blob = await new Promise<Blob | null>((resolve) =>
    canvas.toBlob(resolve, 'image/webp', 0.9),
  );
  if (!blob) throw new Error('The cropped image could not be encoded.');
  // Browsers without WebP encoding fall back to PNG; name the file after the real type.
  const extension = blob.type === 'image/webp' ? 'webp' : 'png';
  return new File([blob], `avatar.${extension}`, { type: blob.type });
}
