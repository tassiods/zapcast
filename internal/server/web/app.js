// app.js — entry point. Bootstrap order:
//   1. settings (theme/accent/density toggles, sidebar drawer)
//   2. i18n (need translations before any rendered string)
//   3. auth probe (prompt for token if AUTH_TOKEN is set on the backend)
//   4. initial status check (single GET — SSE owns subsequent updates)
//   5. data loads (contacts, groups, templates, stats)
//   6. SSE stream
//   7. router (last so renders are wired before the first hashchange fires)

import { state, set, on } from './state.js';
import { api, getAuthToken, setAuthToken, clearAuthToken, setUnauthorizedHandler } from './api.js';
import { $, show, toast, toastErr, showAuthPrompt } from './ui.js';
// `toast` is also used by the global error handlers below; we keep it in
// the same import block to make the dependency explicit.
import { detectBootLang, loadTranslations, switchLanguage, t } from './i18n.js';
import { initRouter, navigate } from './router.js';
import { initSettings } from './settings.js';
import { startSSE } from './sse.js';

import { initContactsView, loadContacts, rerenderContacts } from './view-contacts.js';
import { initGroupsView, loadGroups } from './view-groups.js';
import {
  initBroadcastView,
  renderGroupOptions,
  renderVarTags,
  renderContactPicker,
  updatePreview,
  restoreBroadcastDraft,
  onProgress,
  onDone,
} from './view-broadcast.js';
import { initImportView, setOnAfterImport } from './view-import.js';
import { initTemplatesView, loadTemplates, setOnChange as setTemplatesOnChange } from './view-templates.js';
import { initHistoryView, loadHistory, refreshBroadcastStats } from './view-history.js';
import { initAuthView, setAuthCallbacks } from './view-auth.js';

// ─── Status ────────────────────────────────────────────────────

async function checkStatus() {
  try {
    const s = await api('/api/status');
    statusLoaded = true;
    set({ status: s });
  } catch (e) {
    if (e instanceof Error && /** @type {any} */ (e).status === 401) return; // prompt handled elsewhere
    console.warn('[status] check failed', e);
  }
}

/** True once /api/status has answered at least once (avoids flashing the
 *  offline banner on boot, when the default state is disconnected). */
let statusLoaded = false;

/** Last known online value — used to toast only on connected→offline. */
let lastOnline = null;

/** Shared by views that must refuse work while WhatsApp is down. */
export function isOnline() {
  return statusLoaded && !!state.status.connected && !!state.status.loggedIn;
}

/** @param {import('./state.js').Status} s */
function applyStatus(s) {
  const pill = $('statusPill');
  const text = $('statusText');
  const versionChip = $('versionChip');
  const authConnected = $('authConnected');
  const authDisconnected = $('authDisconnected');
  const authPhone = $('authPhone');

  if (s.version && versionChip) {
    // Backend already ships the leading "v" (versionLdflag); don't stack
    // another one or the chip reads "vv1.3.x".
    versionChip.textContent = s.version.startsWith('v') ? s.version : 'v' + s.version;
  }

  const online = !!(s.connected && s.loggedIn);
  // Toast only on the transition — SSE re-syncs and tab refocus re-fetch,
  // and neither should spam an operator who already knows they're offline.
  if (lastOnline === true && !online) toast(t('msg_whatsapp_dropped'), 'error');
  lastOnline = online;
  show($('connBanner'), statusLoaded && !online);

  if (online) {
    pill?.classList.add('online');
    text.textContent = s.phone ? t('msg_connected').replace('%s', s.phone) : t('ui_status_connected');
    show(authConnected, true);
    show(authDisconnected, false);
    if (authPhone && s.phone) authPhone.textContent = s.phone;
  } else {
    pill?.classList.remove('online');
    text.textContent = t('msg_disconnected');
    show(authConnected, false);
    show(authDisconnected, true);
  }
}

// ─── Unauthorized ──────────────────────────────────────────────

let promptShown = false;
// Single auth path: route every 401 through the cookie-based login flow
// (runLoginFlow), which re-prompts on a wrong token and gives the operator
// feedback instead of the old clear→prompt→blind-reload loop that gave none.
async function handleUnauthorized() {
  if (promptShown) return;
  promptShown = true;
  const ok = await runLoginFlow();
  promptShown = false;
  if (ok) {
    window.location.reload();
  } else {
    clearAuthToken();
    toast(t('err_session_expired'), 'error');
  }
}

// ─── Reactive glue ─────────────────────────────────────────────

/**
 * Subscribe view re-renders to store mutations. We keep this explicit —
 * each subscription documents the cross-view dependency it covers.
 */
function wireReactivity() {
  // Contacts updated → broadcast group select labels + picker re-render.
  on('contacts', () => {
    renderGroupOptions();
    if (state.broadcastMode === 'contacts') renderContactPicker();
    updatePreview();
  });

  // Groups updated → broadcast group select.
  on('groups', () => renderGroupOptions());

  // Templates updated → broadcast var tags + preview.
  on('templateVars', () => { renderVarTags(); updatePreview(); });

  // Status change → header text already applied by applyStatus().
  on('status', () => applyStatus(state.status));
}

// ─── SSE ───────────────────────────────────────────────────────

// Shared handler set so the stream can be (re)started from both boot and the
// post-repair onConnected callback. startSSE() calls stopSSE() first, so
// calling connectSSE() twice is safe — it never stacks EventSources.
const sseHandlers = {
  onOpen: () => {
    // Events sent during a connect gap are not replayed — re-sync the
    // active page so the user doesn't miss late updates.
    checkStatus();
    if (location.hash.startsWith('#/history')) loadHistory();
  },
  onProgress,
  onDone,
  onReceipt: () => {
    if (location.hash.startsWith('#/history')) loadHistory();
  },
  onDisconnected: () => checkStatus(),
};

function connectSSE() {
  startSSE(sseHandlers);
}

// ─── Boot ──────────────────────────────────────────────────────

async function boot() {
  // Wire helpers that depend on UI before any fetch runs.
  setUnauthorizedHandler(handleUnauthorized);

  initSettings();
  initContactsView();
  initGroupsView();
  initBroadcastView();
  initImportView();
  initTemplatesView();
  initHistoryView();
  initAuthView();

  // Cross-module callbacks.
  setOnAfterImport(async () => {
    await Promise.all([loadContacts(), loadGroups()]);
    rerenderContacts();
  });
  setTemplatesOnChange(() => { renderVarTags(); updatePreview(); });
  setAuthCallbacks({
    // Re-pairing after a logout must also restart the SSE stream: doLogout()
    // calls stopSSE(), and without this the new session would have dead
    // progress bars / receipts / disconnect events until a full page reload.
    onConnected: () => { checkStatus(); connectSSE(); },
    onLoggedOut: () => checkStatus(),
  });

  // Topbar interactions.
  $('langSelector').addEventListener('change', (e) => switchLanguage(/** @type {HTMLSelectElement} */ (e.target).value));
  $('footerYear').textContent = String(new Date().getFullYear());

  // 1. Translations.
  const lang = detectBootLang();
  set({ lang });
  await loadTranslations(lang);

  // 2. Auth probe — prompt if token gating is on and we don't have a
  //    valid session cookie. We try a cheap authenticated GET first; a
  //    401 means we need to log in.
  try {
    const req = await fetch('/api/auth/required').then((r) => r.json()).catch(() => null);
    if (req && req.required) {
      // The cookie set by /api/auth/login is httpOnly so JS can't read
      // it directly — we probe the auth state by hitting a gated route.
      const probe = await fetch('/api/status', { credentials: 'same-origin' });
      if (probe.status === 401) {
        // Hold the guard so a concurrent 401 doesn't open a second prompt.
        promptShown = true;
        await runLoginFlow();
        promptShown = false;
      }
    }
  } catch { /* probe is optional */ }

  // 3. Single initial status check — SSE owns subsequent changes.
  await checkStatus();

  // 4. Data — fire in parallel, render once each settles.
  wireReactivity();
  await Promise.all([loadContacts(), loadGroups(), loadTemplates()]);
  refreshBroadcastStats();
  restoreBroadcastDraft();

  // 5. SSE for live progress / receipts / disconnects.
  connectSSE();

  // 6. Router (will trigger first page render).
  initRouter((page) => {
    if (page === 'history') loadHistory();
    if (page === 'broadcast') refreshBroadcastStats();
  });
}

// ─── Auth: cookie-backed login ─────────────────────────────────
//
// The legacy AUTH_TOKEN→sessionStorage flow leaks the bearer into URLs
// (EventSource) and intermediate proxy access logs. We now POST the
// token once to /api/auth/login; the backend sets an httpOnly Secure
// SameSite=Strict cookie that the browser attaches to every request.
async function runLoginFlow() {
  // Up to 3 attempts; the prompt loops on a wrong token so the operator
  // doesn't get bounced back to the page hanging.
  for (let attempt = 0; attempt < 3; attempt++) {
    const token = await showAuthPrompt();
    if (!token) return false;
    try {
      const res = await fetch('/api/auth/login', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token }),
      });
      if (res.ok) {
        // Also keep the token in sessionStorage as the SSE fallback
        // for EventSource installs that can't send the cookie
        // cross-tab. Eventually drop this when the wire fully moves to
        // cookies.
        setAuthToken(token);
        return true;
      }
      if (res.status === 401) continue;
      console.warn('[auth] login failed', res.status);
      return false;
    } catch (e) {
      console.warn('[auth] login error', e);
      return false;
    }
  }
  return false;
}

// ─── Global error handlers ─────────────────────────────────────
//
// Two surfaces matter here:
//   1) `error` for synchronous throws that escape the call stack.
//   2) `unhandledrejection` for promises that nobody .catch()ed —
//      easy to land in async views during refactors.
//
// We surface a toast so the user can see something went wrong (rather
// than silently corrupt state), log to console with a tag for grepping,
// and post a thin telemetry blob to /api/client-error when the backend
// route is wired. The fetch is best-effort: if the endpoint is missing
// (404) or rate-limited, we never retry — we don't want the error
// handler itself to become an error source.

const recentErrorKeys = new Set();
const ERROR_DEDUP_WINDOW_MS = 5_000;

function reportClientError(kind, payload) {
  const key = kind + '|' + (payload?.message || '');
  if (recentErrorKeys.has(key)) return;
  recentErrorKeys.add(key);
  setTimeout(() => recentErrorKeys.delete(key), ERROR_DEDUP_WINDOW_MS);

  console.error('[' + kind + ']', payload);
  try { toast(payload?.message || kind, 'error'); } catch { /* toast may not be ready */ }

  // Best-effort telemetry. Keep payload tiny so a broken endpoint can't
  // tarball user data. `keepalive` lets the request survive a tab close.
  try {
    fetch('/api/client-error', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      keepalive: true,
      body: JSON.stringify({
        kind,
        message: String(payload?.message || '').slice(0, 500),
        source: String(payload?.source || '').slice(0, 200),
        line: payload?.line || 0,
        col: payload?.col || 0,
        userAgent: navigator.userAgent.slice(0, 200),
        href: location.href,
      }),
    }).catch(() => {});
  } catch { /* fetch unavailable */ }
}

window.addEventListener('error', (event) => {
  reportClientError('window.error', {
    message: event.message,
    source: event.filename,
    line: event.lineno,
    col: event.colno,
  });
});

window.addEventListener('unhandledrejection', (event) => {
  const reason = event.reason;
  reportClientError('unhandledrejection', {
    message: reason instanceof Error ? reason.message : String(reason),
  });
});

// ─── Service worker ────────────────────────────────────────────

/** Ask the active service worker to check for a newer shell. Best-effort:
 *  unsupported browsers and race-y unregisters resolve to a no-op. */
function pokeSWUpdate() {
  try {
    const reg = navigator.serviceWorker?.getRegistration?.();
    if (reg && typeof reg.then === 'function') reg.then((r) => r?.update?.().catch(() => {}));
  } catch { /* ignore */ }
}

if ('serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').catch((err) => {
      console.warn('[sw] register failed', err);
    });
    pokeSWUpdate();
  });
  // Long-lived SPA tabs never navigate, so without a nudge a stale shell
  // can keep running for days after a deploy (filters/settings breakage
  // against a newer backend). Poll for updates while the tab is visible.
  setInterval(pokeSWUpdate, 15 * 60 * 1000);
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden) pokeSWUpdate();
  });
  // The SW posts sw-updated after purging stale caches on a new deploy —
  // open tabs are still running the old shell, so offer a reload.
  navigator.serviceWorker.addEventListener('message', (evt) => {
    if (evt.data?.type === 'sw-updated') {
      toast(t('ui_new_version'), 'info');
    }
  });
}

boot().catch((err) => {
  console.error('[boot] failed', err);
  toastErr(err);
});
