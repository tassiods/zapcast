// api.js — cookie-only HTTP helpers and private-session lifecycle.
//
// Everything goes through `apiFetch()` so the 401 handler, cookies and
// error normalisation are consistent. `api()` is the JSON convenience wrapper
// most callers use; `apiFormData()` keeps the multipart path (file uploads)
// without forcing a Content-Type on FormData.

const API_BASE = '';
const TOKEN_KEY = 'zapcast_auth_token';

// Migration shims: never retain a master credential. Remove legacy copies.
export function getAuthToken() { clearAuthToken(); return ''; }
export function setAuthToken(_tok) { clearAuthToken(); }

export function clearAuthToken() {
  try { sessionStorage.removeItem(TOKEN_KEY); } catch { /* private mode */ }
}

/**
 * Legacy compatibility shim: never append credentials to a URL.
 *
 * @param {string} url
 * @returns {string}
 */
export function withAuthQuery(url) {
  return url;
}

let sessionEpoch = 0;
let sessionInvalid = false;
let sessionController = new AbortController();
export function beginSession() { sessionEpoch++; sessionInvalid = false; sessionController = new AbortController(); }
export function invalidateSession() {
  sessionEpoch++; sessionInvalid = true; sessionController.abort(); clearAuthToken();
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
 * Cookie-only response/stream helper. All private requests share an abort
 * signal and fail closed when authorization cannot be established.
 *
 * @param {string} path
 * @param {RequestInit} opts
 */
export async function apiFetch(path, opts = {}) {
  if (sessionInvalid) throw new HttpError(401, 'Session changed');
  const epoch = sessionEpoch;
  clearAuthToken();
  const headers = { ...(opts.headers || {}) };
  let res;
  try {
    res = await fetch(API_BASE + path, {
      ...opts, headers, signal: sessionController.signal, credentials: 'same-origin', cache: 'no-store', redirect: 'error',
    });
  } catch {
    // A blocked SSO redirect is indistinguishable from network loss. Privacy
    // wins: hide stale private data instead of treating it as still authorized.
    if(epoch === sessionEpoch) onUnauthorized();
    throw new HttpError(401, 'Session unavailable');
  }

  if (res.status === 401 || res.status === 403 || res.redirected ||
      (res.ok && (res.headers.get('Content-Type') || '').includes('text/html'))) {
    onUnauthorized();
    throw new HttpError(401, 'Unauthorized');
  }

  if(epoch !== sessionEpoch) throw new HttpError(401, 'Session changed');
  return res;
}

async function request(path, opts) {
  const epoch = sessionEpoch;
  const res = await apiFetch(path, opts);
  let payload = null;
  try { payload = await res.json(); } catch { /* empty / non-json — fine */ }

  if (!res.ok) {
    const { code, message } = extractError(payload);
    const msg = message || res.statusText || ('HTTP ' + res.status);
    throw new HttpError(res.status, msg, payload, code);
  }
  if (epoch !== sessionEpoch) throw new HttpError(401, 'Session changed');
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

/** Private binary responses also obey session changes during body reads. */
export async function apiBlob(path) {
  const epoch = sessionEpoch;
  const res = await apiFetch(path);
  if (!res.ok) throw new HttpError(res.status, 'Download failed');
  const blob = await res.blob();
  if(epoch !== sessionEpoch) throw new HttpError(401, 'Session changed');
  return blob;
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
