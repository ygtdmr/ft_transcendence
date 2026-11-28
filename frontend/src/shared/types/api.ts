/**
 * REST envelopes and the error taxonomy, from Bruno `rest-api/opencollection.yml`
 * (Response envelopes) and `04 - Error & State Examples/folder.yml`.
 *
 * Wire field names stay snake_case exactly as the contract defines them.
 */

/** Every non-empty REST success is `{ "data": value }`. `204` has no body. */
export interface ApiSuccess<T> {
  data: T;
}

/** Every REST error is `{ "error": { code, message, details } }`; `details` is always an object. */
export interface ApiErrorPayload {
  code: string;
  message: string;
  details: Record<string, unknown>;
}

export interface ApiErrorBody {
  error: ApiErrorPayload;
}

/** Codes listed in Bruno's cross-endpoint REST taxonomy. */
export type RestErrorCode =
  | 'INVALID_EMAIL'
  | 'INVALID_USERNAME'
  | 'INVALID_PASSWORD'
  | 'EMAIL_TAKEN'
  | 'USERNAME_TAKEN'
  | 'INVALID_CREDENTIALS'
  | 'SESSION_EXPIRED'
  | 'UNAUTHORIZED'
  | 'USER_NOT_FOUND'
  | 'ALREADY_FRIENDS'
  | 'NOT_FRIENDS'
  | 'REQUEST_ALREADY_SENT'
  | 'REQUEST_ALREADY_RECEIVED'
  | 'REQUEST_NOT_FOUND'
  | 'RELATIONSHIP_BLOCKED'
  | 'ROOM_NOT_FOUND'
  | 'ROOM_NOT_JOINABLE'
  | 'ROOM_FULL'
  | 'USER_OFFLINE'
  | 'USER_IN_ROOM'
  | 'ALREADY_IN_ROOM'
  | 'NOT_ROOM_MEMBER'
  | 'INVALID_IMAGE'
  | 'INVALID_AVATAR_PRESET'
  | 'CONFIRMATION_MISMATCH'
  | 'CANNOT_REPORT_SELF'
  | 'ALREADY_REPORTED'
  | 'VALIDATION_ERROR'
  | 'RATE_LIMITED'
  | 'SERVICE_UNAVAILABLE';

/** `GET /api/health` */
export interface HealthResponse {
  status: 'OK';
  contract_version: string;
  server_time: string;
}
