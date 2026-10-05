// ui.js — DOM helpers, toasts and the shared confirm/auth/settings modals.
//
// Kept dependency-free so views can pull in the bits they need without
// dragging the rest of the app along. The escape helpers are exported as
// `escHTML` / `escAttr` rather than `esc` so reads at the call site disclose
// what is being protected against.

import { t } from './i18n.js';

/** @param {string} id */
export const $ = (id) => /** @type {HTMLElement} */ (document.getElementById(id));

/**
 * @template T
 * @param {string} sel
 * @param {ParentNode} [root]
 * @returns {T | null}
 */
export const $$ = (sel, root = document) =>
  /** @type {T | null} */ (root.querySelector(sel));

/**
 * @param {string} sel
 * @param {ParentNode} [root]
 * @returns {Element[]}
 */
export const $$all = (sel, root = document) => Array.from(root.querySelectorAll(sel));

/** Show/hide via the standard `hidden` attribute. Keeps DOM diffs trivial. */
export function show(el, on = true) {
  if (!el) return;
  if (on) el.removeAttribute('hidden');
  else el.setAttribute('hidden', '');
}

/** HTML-escape for inner text inserted into innerHTML templates. */
export function escHTML(s) {
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

/** Escape a value going into a double-quoted HTML attribute (e.g. data-*). */
export function escAttr(s) { return escHTML(s); }

// ─── Toasts ────────────────────────────────────────────────────

/**
 * @param {string} message  Either a translation key or a literal.
 * @param {'success'|'error'|'info'} [type]
 */
export function toast(message, type = 'info') {
  const text = translate(message);
  const el = document.createElement('div');
  el.className = 'toast ' + type;
  el.textContent = text;
  // Errors hit the assertive live region so screen readers interrupt; success
  // and info stay polite to avoid drowning out the user's current focus.
  const containerId = type === 'error' ? 'toastErrorContainer' : 'toastContainer';
  const container = document.getElementById(containerId) || document.getElementById('toastContainer');
  if (!container) return;
  container.appendChild(el);
  setTimeout(() => {
    el.style.opacity = '0';
    setTimeout(() => el.remove(), 300);
  }, 4000);
}

/**
 * Surfaces a thrown value from `api()` as a localised toast. The backend
 * normalises errors to `{ error }`; generic exceptions expose `.message`.
 *
 * @param {unknown} err
 */
export function toastErr(err) {
  if (!err) return;
  const prefix = t('msg_err_prefix') || 'Error: ';
  let msg;
  if (err instanceof Error) {
    msg = err.message;
  } else if (err && typeof err === 'object' && 'payload' in err) {
    // Structured backend errors carry { error: { code, message } }; legacy
    // endpoints return { error: "string" }. Reach for the human-readable
    // bit either way.
    const payload = /** @type {{ payload?: { error?: any } }} */ (err).payload;
    const e = payload?.error;
    if (e && typeof e === 'object') msg = e.message || e.code || String(err);
    else msg = e || String(err);
  } else {
    msg = String(err);
  }
  toast(prefix + msg, 'error');
}

function translate(key) {
  const v = t(key);
  return v === key ? key : v;
}

// ─── Generic confirm modal ─────────────────────────────────────

let activeCleanup = null;

/**
 * @typedef {Object} ConfirmOpts
 * @property {string} [title]
 * @property {string} [message]
 * @property {string} [confirmLabel]
 * @property {string} [cancelLabel]
 * @property {boolean} [danger]
 */

/**
 * Open an accessible confirm modal. Resolves true/false. Replaces
 * window.confirm() so destructive flows get consistent styling, focus
 * trapping and screen reader hooks.
 *
 * @param {ConfirmOpts} [opts]
 * @returns {Promise<boolean>}
 */
export function confirmDialog(opts = {}) {
  return new Promise((resolve) => {
    if (activeCleanup) activeCleanup(false);

    const backdrop = $('modalBackdrop');
    const titleEl = $('modalTitle');
    const msgEl = $('modalMsg');
    const confirmBtn = $('modalConfirm');
    const cancelBtn = $('modalCancel');

    titleEl.textContent = opts.title || t('ui_confirm') || 'Confirmar';
    msgEl.textContent = opts.message || '';
    confirmBtn.textContent = opts.confirmLabel || t('ui_confirm') || 'Confirmar';
    cancelBtn.textContent = opts.cancelLabel || t('ui_cancel') || 'Cancelar';
    confirmBtn.className = 'btn btn-sm ' + (opts.danger ? 'btn-danger' : 'btn-primary');

    const previousFocus = document.activeElement;
    backdrop.classList.add('open');

    function cleanup(result) {
      backdrop.classList.remove('open');
      document.removeEventListener('keydown', onKey, true);
      backdrop.removeEventListener('mousedown', onBackdrop);
      confirmBtn.removeEventListener('click', onConfirm);
      cancelBtn.removeEventListener('click', onCancel);
      activeCleanup = null;
      try { /** @type {HTMLElement|null} */ (previousFocus)?.focus?.(); } catch { /* lost focus target */ }
      resolve(result);
    }

    const onConfirm = () => cleanup(true);
    const onCancel = () => cleanup(false);
    const onBackdrop = (e) => { if (e.target === backdrop) onCancel(); };
    const onKey = (e) => {
      if (e.key === 'Escape') { e.preventDefault(); onCancel(); return; }
      if (e.key === 'Enter') {
        const tag = (document.activeElement && document.activeElement.tagName) || '';
        if (tag !== 'TEXTAREA') { e.preventDefault(); onConfirm(); return; }
      }
      if (e.key === 'Tab') {
        // Trap focus between the two action buttons.
        const focusables = [cancelBtn, confirmBtn];
        const idx = focusables.indexOf(/** @type {HTMLElement} */ (document.activeElement));
        const next = idx === -1
          ? 0
          : (idx + (e.shiftKey ? -1 : 1) + focusables.length) % focusables.length;
        focusables[next].focus();
        e.preventDefault();
      }
    };

    confirmBtn.addEventListener('click', onConfirm);
    cancelBtn.addEventListener('click', onCancel);
    backdrop.addEventListener('mousedown', onBackdrop);
    document.addEventListener('keydown', onKey, true);
    activeCleanup = cleanup;

    (opts.danger ? cancelBtn : confirmBtn).focus();
  });
}

// ─── Auth token prompt ─────────────────────────────────────────

/**
 * Show the auth token modal. Resolves to the entered (and stored) token.
 * Used both at boot (`/api/auth/required`) and any later 401 response.
 *
 * @returns {Promise<string>}
 */
export function showAuthPrompt() {
  return new Promise((resolve) => {
    const backdrop = $('authBackdrop');
    const input = /** @type {HTMLInputElement} */ ($('authInput'));
    const submit = $('authSubmit');
    const previousFocus = document.activeElement;

    input.value = '';
    backdrop.classList.add('open');
    setTimeout(() => input.focus(), 50);

    // Guard against a second invocation stacking listeners before this one
    // resolves — the 401 handler and the boot probe can both reach here.
    let done = false;
    function cleanup(result) {
      if (done) return;
      done = true;
      backdrop.classList.remove('open');
      submit.removeEventListener('click', onSubmit);
      document.removeEventListener('keydown', onKey, true);
      try { /** @type {HTMLElement|null} */ (previousFocus)?.focus?.(); } catch { /* lost target */ }
      resolve(result);
    }
    function onSubmit() {
      const v = input.value.trim();
      if (!v) { input.focus(); return; }
      cleanup(v);
    }
    // Escape cancels (empty result = "no token"); Tab is trapped between the
    // input and the submit button so focus can't leave the modal dialog.
    function onKey(e) {
      if (e.key === 'Escape') { e.preventDefault(); cleanup(''); return; }
      if (e.key === 'Enter') { e.preventDefault(); onSubmit(); return; }
      if (e.key === 'Tab') {
        const focusables = [input, submit];
        const idx = focusables.indexOf(/** @type {HTMLElement} */ (document.activeElement));
        const next = idx === -1
          ? 0
          : (idx + (e.shiftKey ? -1 : 1) + focusables.length) % focusables.length;
        focusables[next].focus();
        e.preventDefault();
      }
    }

    submit.addEventListener('click', onSubmit);
    document.addEventListener('keydown', onKey, true);
  });
}

// ─── Small string helpers used in templates ────────────────────

/**
 * Renders a single .summary-cards tile.
 *
 * @param {number|string} num
 * @param {string} label
 * @param {string} [color]  CSS-var suffix applied to the number text.
 * @param {boolean} [tinted]  When true (default), tints the card background.
 */
export function summaryCard(num, label, color, tinted = true) {
  const bg = (color && tinted) ? ` style="background:var(--${color}-dim)"` : '';
  const numStyle = color ? ` style="color:var(--${color})"` : '';
  return `<div class="summary-card"${bg}>` +
    `<div class="num"${numStyle}>${escHTML(num)}</div>` +
    `<div class="label">${escHTML(label)}</div>` +
    `</div>`;
}

/** Standard empty-state block used by every list page. */
export function emptyState(icon, key) {
  return `<div class="empty-state"><div class="icon">${icon}</div><p>${escHTML(t(key))}</p></div>`;
}

// ─── Debounce keyed by call site ──────────────────────────────

/** @type {Record<string, ReturnType<typeof setTimeout>>} */
const debounceTimers = {};

/**
 * @param {string} key
 * @param {() => void} fn
 * @param {number} [ms]
 */
export function debounceSearch(key, fn, ms = 150) {
  clearTimeout(debounceTimers[key]);
  debounceTimers[key] = setTimeout(fn, ms);
}

// ─── Phone validation (mirrors backend validatePhone) ──────────

/**
 * @typedef {Object} PhoneCheck
 * @property {boolean} ok
 * @property {string}  [msg]
 * @property {string}  [digits]
 */

/**
 * @param {string} raw
 * @returns {PhoneCheck}
 */
export function validatePhoneInline(raw) {
  const digits = String(raw || '').replace(/\D+/g, '');
  if (!digits) return { ok: false, msg: t('err_phone_required') || 'Telefone é obrigatório.' };
  if (digits.length < 7) return { ok: false, msg: t('err_phone_short') || 'Telefone muito curto (mínimo 7 dígitos).' };
  if (digits.length > 15) return { ok: false, msg: t('err_phone_long') || 'Telefone muito longo (máximo 15 dígitos).' };
  if (digits[0] === '0') return { ok: false, msg: t('err_phone_leading_zero') || 'Inclua o código do país (não pode começar com 0).' };
  return { ok: true, digits };
}

/**
 * Human date-time in the operator's own locale and timezone.
 * "2026-10-05T13:38:01Z" becomes "05/10/2026, 10:38" on a pt-BR machine.
 * Falls back to the raw value when it isn't parseable.
 *
 * @param {*} iso
 * @returns {string}
 */
export function formatDateTime(iso) {
  const d = new Date(String(iso ?? ''));
  if (Number.isNaN(d.getTime())) return String(iso ?? '');
  return d.toLocaleString(undefined, {
    day: '2-digit',
    month: '2-digit',
    year: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  });
}

/** Human-friendly file size. KB/MB only — no need to chase TB. */
export function humanSize(bytes) {
  if (bytes < 1024) return bytes + ' B';
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
  return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
}

/**
 * Accent- and case-insensitive fold for search. "Jose" matches "José",
 * "AVENIDA" matches "avenida". Null-safe — null/undefined fold to ''.
 *
 * @param {*} s
 * @returns {string}
 */
export function fold(s) {
  return String(s ?? '')
    .toLowerCase()
    .normalize('NFD')
    .replace(/[\u0300-\u036f]/g, '');
}
