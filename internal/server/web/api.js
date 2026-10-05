// api.js — HTTP helpers + auth-token plumbing.
//
// Everything goes through `request()` so the 401 handler, auth header and
// error normalisation are consistent. `api()` is the JSON convenience wrapper
// most callers use; `apiFormData()` keeps the multipart path (file uploads)
// without forcing a Content-Type on FormData.

const API_BASE = '';
const TOKEN_KEY = 'zapcast_auth_token';

/** @returns {string} Empty when no token has been stored. */
export function getAuthToken() {
  try { return sessionStorage.getItem(TOKEN_KEY) || ''; } catch { return ''; }
}

export function setAuthToken(tok) {
  try { sessionStorage.setItem(TOKEN_KEY, tok); } catch { /* private mode */ }
}

export function clearAuthToken() {
  try { sessionStorage.removeItem(TOKEN_KEY); } catch { /* private mode */ }
}

/**
 * Append `?token=` for the URLs that can't carry an Authorization header
 * (EventSource, plain `<img src>` etc).
 *
 * @param {string} url
 * @returns {string}
 */
export function withAuthQuery(url) {
  const tok = getAuthToken();
  if (!tok) return url;
  return url + (url.includes('?') ? '&' : '?') + 'token=' + encodeURIComponent(tok);
}

let onUnauthorized = () => {};
/** Wire the 401 hook from app.js so api.js stays free of UI imports. */
export function setUnauthorizedHandler(fn) { onUnauthorized = fn; }

/**
 * @typedef {Object} ApiError
 * @property {number} status
 * @property {string} message
 * @property {*} [payload]
 */

class HttpError extends Error {
  constructor(status, message, payload, code = '') {
    super(message);
    this.name = 'HttpError';
    this.status = status;
    this.payload = payload;
    /** Machine-readable error code (e.g. `err_phone_too_short`). */
    this.code = code;
  }
}

/**
 * Pull the structured error fields out of a backend error envelope. Returns
 * an object `{ code, message }` — `code` may be empty for unstructured
 * payloads (HTTP-level failures, network errors).
 *
 * @param {any} payload
 * @returns {{ code: string, message: string }}
 */
function extractError(payload) {
  if (!payload) return { code: '', message: '' };
  const err = payload.error;
  if (err && typeof err === 'object' && typeof err.code === 'string') {
    return { code: err.code, message: err.message || err.code };
  }
  // Legacy string-shaped error — used by old endpoints that haven't
  // migrated yet. Treat the string as the display message and leave the
  // machine code empty so callers fall through to status-based branches.
  if (typeof err === 'string') {
    return { code: '', message: err };
  }
  return { code: '', message: '' };
}

/**
 * Low-level request used by `api()` and `apiFormData()`. Parses JSON on the
 * way out and throws an HttpError with the server-provided `error` string
 * when the backend rejects the call.
 *
 * @param {string} path
 * @param {RequestInit} opts
 */
async function request(path, opts) {
  const tok = getAuthToken();
  const headers = { ...(opts.headers || {}) };
  if (tok) headers['Authorization'] = 'Bearer ' + tok;

  const res = await fetch(API_BASE + path, { ...opts, headers });

  if (res.status === 401) {
    onUnauthorized();
    throw new HttpError(401, 'Unauthorized');
  }

  let payload = null;
  try { payload = await res.json(); } catch { /* empty / non-json — fine */ }

  if (!res.ok) {
    const { code, message } = extractError(payload);
    const msg = message || res.statusText || ('HTTP ' + res.status);
    throw new HttpError(res.status, msg, payload, code);
  }
  return payload;
}

/**
 * JSON request. `body` is serialised when provided.
 *
 * @param {string} path
 * @param {string} [method]
 * @param {*} [body]
 */
export function api(path, method = 'GET', body = null) {
  const opts = { method, headers: { 'Content-Type': 'application/json' } };
  if (body !== null) opts.body = JSON.stringify(body);
  return request(path, opts);
}

/** Same as `api()` but for multipart uploads — leaves Content-Type to fetch. */
export function apiFormData(path, formData) {
  return request(path, { method: 'POST', body: formData, headers: {} });
}

/**
 * GET that swallows the error and returns `fallback` instead. Used by load*
 * helpers so the global cache never sits as undefined while the user is
 * looking at the UI.
 *
 * @template T
 * @param {string} path
 * @param {T} fallback
 * @returns {Promise<T>}
 */
export async function safeApi(path, fallback) {
  try {
    const v = await api(path);
    return v == null ? fallback : v;
  } catch {
    return fallback;
  }
}
