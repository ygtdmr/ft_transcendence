/**
 * REST client foundation.
 *
 * Authentication is cookie-only:
 *   - requests go to the same-origin `/api` prefix, so the browser attaches the
 *     HttpOnly `ft_session` cookie by itself;
 *   - `credentials: 'include'` keeps that true for every call;
 *   - no authentication header, bearer token or JS-readable credential is attached
 *     anywhere — the frontend cannot read either cookie.
 *
 * Feature-level REST calls live in each feature's `api/` folder and use `apiRequest`;
 * this module intentionally implements no endpoint of its own.
 */

import { API_BASE_PATH, devFlags } from '@shared/constants';
import type { ApiSuccess } from '@shared/types';

import { ApiError, NETWORK_ERROR_CODE, normalizeErrorResponse } from './errors';

/** Query values are serialized with `String()`; `undefined` entries are dropped. */
export type QueryParams = Record<string, string | number | boolean | undefined>;

export interface ApiRequestOptions {
  method?: 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE';
  /** Serialized as JSON. Use `formData` for multipart uploads instead. */
  body?: unknown;
  /** Multipart payload (avatar upload). Never set Content-Type by hand for this. */
  formData?: FormData;
  query?: QueryParams;
  signal?: AbortSignal;
  /**
   * Skip the silent-refresh retry. Used by the auth calls themselves so a failed
   * refresh cannot recurse.
   */
  skipRefresh?: boolean;
}

function buildUrl(path: string, query?: ApiRequestOptions['query']): string {
  const url = `${API_BASE_PATH}${path}`;
  if (!query) return url;

  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined) search.set(key, String(value));
  }
  const serialized = search.toString();
  return serialized ? `${url}?${serialized}` : url;
}

/**
 * Hook for the silent-refresh step.
 *
 * `features/auth` installs the real implementation at bootstrap (it owns
 * `POST /api/v1/auth/refresh` and the session store). Keeping the slot here — rather
 * than importing the auth feature — is what stops `shared/` depending on `features/`.
 *
 * The contract: return `true` when a refresh succeeded and the original request should
 * be retried once; return `false` to surface the original `401`.
 */
export type RefreshHandler = () => Promise<boolean>;

let refreshHandler: RefreshHandler | null = null;
let inFlightRefresh: Promise<boolean> | null = null;

export function setRefreshHandler(handler: RefreshHandler | null): void {
  refreshHandler = handler;
}

/** Collapses concurrent 401s onto a single refresh attempt. */
function runRefresh(): Promise<boolean> {
  if (!refreshHandler) return Promise.resolve(false);
  inFlightRefresh ??= refreshHandler().finally(() => {
    inFlightRefresh = null;
  });
  return inFlightRefresh;
}

/**
 * Transport seam for REST calls, mirroring the WebSocket transport factory.
 *
 * The default sends a real `fetch` to the same-origin `/api` prefix. A development
 * build can install the mock REST layer (`shared/api/mock`), which answers with real
 * `Response` objects in the documented envelopes, so everything above this seam —
 * envelope unwrapping, error normalization, silent refresh — runs unchanged.
 */
export type RequestTransport = (path: string, options: ApiRequestOptions) => Promise<Response>;

let activeTransport: RequestTransport = fetchTransport;

/** Replace the REST transport. `null` restores the browser `fetch` one. */
export function setRequestTransport(transport: RequestTransport | null): void {
  activeTransport = transport ?? fetchTransport;
}

function send(path: string, options: ApiRequestOptions): Promise<Response> {
  return activeTransport(path, options);
}

async function fetchTransport(path: string, options: ApiRequestOptions): Promise<Response> {
  const { method = 'GET', body, formData, query, signal } = options;

  const headers: Record<string, string> = { Accept: 'application/json' };
  if (formData === undefined && body !== undefined) {
    headers['Content-Type'] = 'application/json';
  }

  const init: RequestInit = {
    method,
    headers,
    // Cookie-only auth: this is the entire credential mechanism.
    credentials: 'include',
  };
  if (signal) init.signal = signal;
  if (formData !== undefined) init.body = formData;
  else if (body !== undefined) init.body = JSON.stringify(body);

  return fetch(buildUrl(path, query), init);
}

/**
 * Issue one REST call and unwrap the `{ "data": ... }` envelope.
 *
 * A `401 UNAUTHORIZED` triggers one silent refresh and one retry; if that refresh
 * fails, the original error is thrown and the caller applies `AUTH_REDIRECT`.
 */
export async function apiRequest<T>(path: string, options: ApiRequestOptions = {}): Promise<T> {
  let response: Response;
  try {
    response = await send(path, options);
  } catch (cause) {
    throw new ApiError(0, NETWORK_ERROR_CODE, 'The server could not be reached.', {
      cause: String(cause),
    });
  }

  if (response.status === 401 && !options.skipRefresh && (await runRefresh())) {
    return apiRequest<T>(path, { ...options, skipRefresh: true });
  }

  if (!response.ok) {
    const error = await normalizeErrorResponse(response);
    if (devFlags.logApiErrors) {
      console.warn(`[api] ${options.method ?? 'GET'} ${path} -> ${error.status} ${error.code}`);
    }
    throw error;
  }

  // `204 No Content` (logout, leave room, remove friend) carries no envelope.
  if (response.status === 204) return undefined as T;

  const payload = (await response.json()) as ApiSuccess<T>;
  return payload.data;
}
