// view-auth.js — QR pairing flow and the logout button.
//
// /api/auth/qr can reply either as JSON (already_connected, error) or stream
// SSE-formatted lines with QR refreshes. We dispatch on Content-Type to keep
// the happy path single-pass.

import { api } from './api.js';
import { $, show, toast, toastErr, confirmDialog } from './ui.js';
import { t } from './i18n.js';
import { stopSSE } from './sse.js';

/** @type {() => void} */
let onLoggedOut = () => {};
/** @type {() => void} */
let onConnected = () => {};

/** Budget for automatic QR refresh after expiry — a manual click resets it. */
let qrAutoRetries = 0;
const QR_MAX_AUTO_RETRIES = 3;

export function setAuthCallbacks({ onConnected: c, onLoggedOut: l }) {
  if (c) onConnected = c;
  if (l) onLoggedOut = l;
}

// ─── QR ────────────────────────────────────────────────────────

async function startAuth(resetBudget = true) {
  const btn = /** @type {HTMLButtonElement} */ ($('authBtn'));
  btn.disabled = true;
  btn.innerHTML = '<span class="spinner"></span> ' + t('msg_connecting');
  show($('qrContainer'), true);
  if (resetBudget) qrAutoRetries = 0;

  try {
    const response = await fetch('/api/auth/qr', { method: 'POST' });
    const ct = response.headers.get('Content-Type') || '';

    if (ct.includes('application/json')) {
      const data = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(data.error || ('HTTP ' + response.status));
      if (data.status === 'already_connected') {
        show($('qrContainer'), false);
        toast('msg_already_connected', 'success');
        onConnected();
      }
      return;
    }

    if (!response.ok) {
      const text = await response.text();
      throw new Error(text || ('HTTP ' + response.status));
    }

    await consumeQRStream(response);
  } catch (e) {
    toastErr(e);
  } finally {
    btn.disabled = false;
    btn.innerHTML = '&#x1F4F1; ' + t('ui_login');
  }
}

/**
 * Read an SSE-formatted response body until done. Each `data:` line carries
 * one JSON event ({ type: 'qr'|'success'|'timeout', ... }).
 *
 * @param {Response} response
 */
async function consumeQRStream(response) {
  if (!response.body) throw new Error('Empty body');
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    const lines = buffer.split('\n');
    buffer = lines.pop() || '';
    for (const line of lines) {
      if (!line.startsWith('data: ')) continue;
      let evt;
      try { evt = JSON.parse(line.slice(6)); } catch { continue; }
      handleQREvent(evt);
    }
  }
}

function handleQREvent(evt) {
  // QR pairing frames now share the wire shape of /api/events:
  // { type: "<kind>", data: <payload> }. The payload for qr is
  // { code, png }; success/timeout carry no payload.
  if (evt.type === 'qr') {
    const png = evt.data?.png;
    if (png) /** @type {HTMLImageElement} */ ($('qrImg')).src = png;
    $('qrStatus').textContent = t('ui_qr_instructions');
  } else if (evt.type === 'success') {
    qrAutoRetries = 0;
    $('qrStatus').textContent = t('msg_whatsapp_connected');
    toast('msg_whatsapp_connected', 'success');
    onConnected();
    show($('qrContainer'), false);
  } else if (evt.type === 'timeout') {
    // Auto-refresh the QR a few times so the operator doesn't have to
    // re-click "Gerar QR Code" after every expiry. The budget resets on
    // each manual click (startAuth) and on success.
    if (qrAutoRetries < QR_MAX_AUTO_RETRIES) {
      qrAutoRetries++;
      $('qrStatus').textContent = t('msg_qr_retrying');
      setTimeout(() => {
        if ($('qrContainer') && !$('qrContainer').hasAttribute('hidden')) startAuth(false);
      }, 1500);
    } else {
      $('qrStatus').textContent = t('msg_qr_expired');
      toast('msg_qr_expired', 'error');
    }
  }
}

// ─── Logout ────────────────────────────────────────────────────

async function doLogout() {
  const ok = await confirmDialog({
    title: t('ui_disconnect') || 'Desconectar do WhatsApp',
    message: t('confirm_disconnect'),
    confirmLabel: t('ui_disconnect'),
    danger: true,
  });
  if (!ok) return;
  try {
    await api('/api/auth/logout', 'POST');
    // Stop the SSE stream so progress events from the previous session aren't
    // visible to the next one that signs in.
    stopSSE();
    toast('msg_disconnected_info', 'info');
    onLoggedOut();
  } catch (e) {
    toastErr(e);
  }
}

// ─── Wiring ────────────────────────────────────────────────────

export function initAuthView() {
  $('authBtn').addEventListener('click', () => startAuth());
  $('logoutBtn').addEventListener('click', doLogout);
}
