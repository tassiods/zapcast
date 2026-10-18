// view-broadcast.js — compose, validate and dispatch broadcasts.
//
// Two recipient modes: pick a saved group, or hand-pick contacts via the
// picker. Attachments are kept locally until send-time then either stream
// through multipart (`apiFormData`) or fold into the JSON payload.

import { state, set, on } from './state.js';
import { api, apiFormData } from './api.js';
import { t } from './i18n.js';
import { navigate } from './router.js';
import { $, escHTML, escAttr, show, toast, toastErr, confirmDialog, humanSize, summaryCard } from './ui.js';
import { renderPickerList, filterPicker, toggleAllPicker, pickerValues } from './picker.js';

const PICKER_LIST_ID = 'broadcastPickerList';
const PICKER_SEARCH_ID = 'broadcastPickerSearch';

/** @type {?{ file: File, previewURL: string|null, isImage: boolean, optimized: boolean }} */
let attachment = null;

// ─── Mode tabs ─────────────────────────────────────────────────

function setMode(mode) {
  set({ broadcastMode: mode });
  for (const tab of document.querySelectorAll('.broadcast-tab')) {
    tab.classList.toggle('active', tab.getAttribute('data-mode') === mode);
  }
  show($('modeGroupPanel'), mode === 'group');
  show($('modeContactsPanel'), mode === 'contacts');
  if (mode === 'contacts') renderContactPicker();
  queueSaveDraft();
}

// ─── Group select ──────────────────────────────────────────────

export function renderGroupOptions() {
  const sel = /** @type {HTMLSelectElement} */ ($('broadcastGroup'));
  const current = sel.value;
  const placeholder = `<option value="">${escHTML(t('placeholder_select_group'))}</option>`;
  sel.innerHTML = placeholder + state.groups.map((g) => {
    const count = g.count ?? g.contacts.length;
    return `<option value="${escAttr(g.id)}" ${g.id === current ? 'selected' : ''}>${escHTML(g.name)} (${count})</option>`;
  }).join('');
  updateGroupInfo();
}

function updateGroupInfo() {
  const gid = /** @type {HTMLSelectElement} */ ($('broadcastGroup')).value;
  const info = $('broadcastGroupInfo');
  if (!gid) { show(info, false); return; }
  const g = state.groups.find((x) => x.id === gid);
  if (!g || !g.contacts.length) { show(info, false); return; }

  const count = g.contacts.length;
  $('broadcastCount').textContent = `${count} destinatário${count !== 1 ? 's' : ''}`;
  const names = g.contacts.map((phone) => state.nameByPhone.get(phone) || phone);
  renderRecipientBadges('broadcastRecipients', names);
  show(info, true);
}

// ─── Contact picker ────────────────────────────────────────────

export function renderContactPicker(selected) {
  /** @type {HTMLInputElement} */ ($(PICKER_SEARCH_ID)).value = '';
  renderPickerList({
    listId: PICKER_LIST_ID,
    valueField: 'phone',
    selectedSet: selected instanceof Set ? selected : new Set(),
    emptyKey: 'empty_broadcast_no_contacts',
    onSynced: updateContactsCount,
  });
}

function updateContactsCount() {
  const phones = pickerValues(PICKER_LIST_ID);
  const n = phones.length;
  $('broadcastContactsCount').textContent = n > 0 ? `(${n} selecionado${n !== 1 ? 's' : ''})` : '';

  const info = $('broadcastContactsInfo');
  if (!n) { show(info, false); return; }
  $('broadcastContactsCountText').textContent = `${n} destinatário${n !== 1 ? 's' : ''}`;
  const names = phones.map((p) => state.nameByPhone.get(p) || p);
  renderRecipientBadges('broadcastContactsChips', names);
  show(info, true);
}

function renderRecipientBadges(containerId, names) {
  const el = $(containerId);
  const MAX = 50;
  if (names.length <= MAX) {
    el.innerHTML = names.map((n) => `<span class="recipient-badge">${escHTML(n)}</span>`).join('');
    return;
  }
  const visible = names.slice(0, MAX).map((n) => `<span class="recipient-badge">${escHTML(n)}</span>`).join('');
  const hidden = names.slice(MAX).map((n) => `<span class="recipient-badge" hidden>${escHTML(n)}</span>`).join('');
  el.innerHTML = visible + hidden +
    `<button type="button" class="recipient-badge recipient-more" data-action="expand-recipients">+${names.length - MAX} mais</button>`;
}

function expandRecipientBadges(btn) {
  for (const el of btn.parentElement.querySelectorAll('.recipient-badge[hidden]')) {
    el.removeAttribute('hidden');
  }
  btn.remove();
}

// Drafts deliberately remain only in the live page. No automatic persistence:
// proxy/SSO mode supplies no native operator identity to scope saved text.
function queueSaveDraft() {} // Switching views keeps the existing textarea.
function clearDraft() {
  try { localStorage.removeItem('zapcast.draft.v1'); } catch { /* denied storage */ }
}
export function restoreBroadcastDraft() { clearDraft(); }
export function clearBroadcastPrivate() {
  clearDraft();
  if (attachment?.previewURL) URL.revokeObjectURL(attachment.previewURL);
  attachment = null;
}

/**
 * Prefill the contacts picker (used by "retry failed" from history).
 * Switches to contacts mode, checks exactly `phones`, navigates over.
 */
export function prefillContacts(phones) {
  const sel = new Set(Array.isArray(phones) ? phones : []);
  setMode('contacts');
  renderContactPicker(sel);
  updateContactsCount();
}

// ─── Attachment ────────────────────────────────────────────────

async function onFilePicked(e) {
  const raw = e.target.files && e.target.files[0];
  if (!raw) return;
  // Downscale photos before upload: WhatsApp recompresses images anyway, so
  // sending a 12 MP original only wastes bandwidth and risks the 16 MB cap.
  let file = raw;
  let optimized = false;
  if (raw.type.startsWith('image/')) {
    try {
      const small = await downscaleImage(raw);
      if (small !== raw) { file = small; optimized = true; }
    } catch { /* keep the original */ }
  }
  const MAX = 16 * 1024 * 1024;
  if (file.size > MAX) {
    toast('err_file_too_large', 'error');
    e.target.value = '';
    return;
  }
  clearAttachment();
  const isImage = file.type.startsWith('image/');
  attachment = { file, previewURL: isImage ? URL.createObjectURL(file) : null, isImage, optimized };
  renderAttachment();
}

/**
 * Shrink oversized photos to a WhatsApp-friendly size (max 1600px, JPEG
 * q0.82 on a white canvas so PNG transparency doesn't turn black).
 * Returns the original file when it's already small enough or when the
 * browser can't decode it — never rejects the pick.
 *
 * @param {File} file
 * @returns {Promise<File>}
 */
function downscaleImage(file) {
  const MAX_DIM = 1600;
  const SMALL_ENOUGH = 2 * 1024 * 1024;
  return new Promise((resolve) => {
    const url = URL.createObjectURL(file);
    const img = new Image();
    const done = (f) => { URL.revokeObjectURL(url); resolve(f); };
    img.onload = () => {
      const scale = Math.min(1, MAX_DIM / Math.max(img.width, img.height));
      if (scale >= 1 && file.size <= SMALL_ENOUGH) { done(file); return; }
      const w = Math.max(1, Math.round(img.width * scale));
      const h = Math.max(1, Math.round(img.height * scale));
      const canvas = document.createElement('canvas');
      canvas.width = w;
      canvas.height = h;
      const ctx = canvas.getContext('2d');
      if (!ctx) { done(file); return; }
      ctx.fillStyle = '#ffffff';
      ctx.fillRect(0, 0, w, h);
      ctx.drawImage(img, 0, 0, w, h);
      canvas.toBlob((blob) => {
        if (!blob) { done(file); return; }
        const name = file.name.replace(/\.[a-z0-9]+$/i, '') || 'imagem';
        done(new File([blob], name + '.jpg', { type: 'image/jpeg' }));
      }, 'image/jpeg', 0.82);
    };
    img.onerror = () => done(file);
    img.src = url;
  });
}

function clearAttachment() {
  if (attachment?.previewURL) URL.revokeObjectURL(attachment.previewURL);
  attachment = null;
  /** @type {HTMLInputElement} */ ($('broadcastFile')).value = '';
  renderAttachment();
}

function renderAttachment() {
  const el = $('broadcastFilePreview');
  if (!attachment) { show(el, false); el.innerHTML = ''; return; }
  const a = attachment;
  const thumb = a.isImage
    ? `<img src="${escAttr(a.previewURL || '')}" style="height:48px;width:48px;object-fit:cover;border-radius:6px;border:1px solid var(--line)" alt="">`
    : `<span style="font-size:22px">&#x1F4CE;</span>`;
  el.innerHTML = `
    <div style="display:flex;align-items:center;gap:10px;padding:6px 10px;background:var(--surface2);border:1px solid var(--line);border-radius:10px">
      ${thumb}
      <div style="flex:1;min-width:0">
        <div style="font-size:13px;font-weight:500;color:var(--ink);white-space:nowrap;overflow:hidden;text-overflow:ellipsis">${escHTML(a.file.name)}</div>
        <div style="font-family:var(--font-mono);font-size:11px;color:var(--ink-3)">${escHTML(a.file.type || 'application/octet-stream')} · ${humanSize(a.file.size)}${a.optimized ? ' · ' + escHTML(t('ui_optimized')) : ''}</div>
      </div>
      <button type="button" class="icon-btn" data-action="clear-attachment" aria-label="${escAttr(t('ui_remove_attachment'))}" style="width:30px;height:30px;font-size:14px">&times;</button>
    </div>`;
  show(el, true);
}

// ─── Send ──────────────────────────────────────────────────────

async function send() {
  // Refuse early while WhatsApp is down: the backend would 400 after the
  // operator already picked recipients and confirmed. The button is also
  // disabled via updateSendGate — this covers the status-change race.
  if (!state.status.connected || !state.status.loggedIn) {
    toast('err_whatsapp_not_conn', 'error');
    navigate('auth');
    return;
  }

  const message = /** @type {HTMLTextAreaElement} */ ($('broadcastMsg')).value.trim();
  if (!message) { toast('err_type_message', 'error'); return; }

  let payload = { message };
  let confirmMsg = '';

  if (state.broadcastMode === 'group') {
    const groupId = /** @type {HTMLSelectElement} */ ($('broadcastGroup')).value;
    if (!groupId) { toast('err_select_group', 'error'); return; }
    const g = state.groups.find((x) => x.id === groupId);
    if (!g || !g.contacts.length) { toast('err_group_no_contacts', 'error'); return; }
    payload.groupId = groupId;
    confirmMsg = t('confirm_send_group').replace('%d', String(g.contacts.length)).replace('%s', g.name);
  } else {
    const phones = pickerValues(PICKER_LIST_ID);
    if (!phones.length) { toast('err_select_min_one', 'error'); return; }
    payload.phones = phones;
    confirmMsg = t('confirm_send_contacts').replace('%d', String(phones.length));
  }

  // Extra confirm text is opt-in: an empty/untranslated key appends nothing
  // (t() falls back to the key itself, so compare before using it).
  const extra = t('confirm_send_extra');
  const ok = await confirmDialog({
    title: t('ui_send_broadcast'),
    message: confirmMsg + (extra && extra !== 'confirm_send_extra' ? '\n\n' + extra : ''),
    confirmLabel: t('ui_send'),
  });
  if (!ok) return;

  const btn = /** @type {HTMLButtonElement} */ ($('broadcastBtn'));
  btn.disabled = true;
  sending = true;
  btn.innerHTML = '<span class="spinner"></span> ' + t('msg_broadcast_queued');

  // Hide stale results from the previous broadcast.
  show($('broadcastResults'), false);
  show($('broadcastProgress'), true);
  resetCancelBtn();
  $('progressFill').style.width = '0%';
  $('progressInfo').textContent = t('msg_broadcast_starting') || 'Iniciando...';

  try {
    let result;
    if (attachment) {
      const fd = new FormData();
      fd.append('message', message);
      if (payload.groupId) fd.append('groupId', payload.groupId);
      if (payload.phones) fd.append('phones', payload.phones.join(','));
      fd.append('file', attachment.file);
      result = await apiFormData('/api/broadcast', fd);
    } else {
      result = await api('/api/broadcast', 'POST', payload);
    }
    set({ activeBroadcastId: result.broadcastId });
    btn.innerHTML = '<span class="spinner"></span> ' + t('msg_broadcast_started2').replace('%d', result.total);
    toast('msg_broadcast_started', 'info');
    clearAttachment();
    clearDraft();
  } catch (e) {
    toastErr(e);
    btn.disabled = false;
    sending = false;
    btn.innerHTML = '&#x1F4E8; ' + t('ui_send_broadcast');
    updateSendGate();
  }
}

// ─── Cancel (operator abort of the in-flight broadcast) ────────

function resetCancelBtn() {
  const btn = /** @type {HTMLButtonElement|null} */ ($('broadcastCancelBtn'));
  if (!btn) return;
  btn.disabled = false;
  btn.textContent = t('ui_cancel_broadcast');
}

async function cancelBroadcast() {
  const btn = /** @type {HTMLButtonElement|null} */ ($('broadcastCancelBtn'));
  if (btn) btn.disabled = true;
  try {
    const r = await api('/api/broadcast/cancel', 'POST');
    if (r && r.cancelled) {
      toast('msg_cancel_sent', 'info');
    } else {
      // Nothing was running (stale panel) — just put it away.
      show($('broadcastProgress'), false);
    }
  } catch (e) {
    toastErr(e);
    if (btn) btn.disabled = false;
  }
}

// ─── Send gate: keep the big button unusable while offline ──────

// Explicit in-flight flag: updateSendGate must not infer "sending" from
// btn.disabled, because the gate itself sets disabled while offline —
// inferring would latch the button disabled forever after any offline
// period (status → online would early-return and never re-enable).
let sending = false;

function updateSendGate() {
  const btn = /** @type {HTMLButtonElement|null} */ ($('broadcastBtn'));
  if (!btn || sending) return;
  const online = !!state.status.connected && !!state.status.loggedIn;
  btn.toggleAttribute('disabled', !online);
  if (!online) btn.title = t('err_whatsapp_not_conn');
  else btn.removeAttribute('title');
}

// ─── Progress (called from SSE) ────────────────────────────────

export function onProgress(d) {
  // The SSE channel is shared with every other open tab on the same
  // backend — only paint progress for the broadcast THIS tab queued.
  if (!d || d.broadcastId !== state.activeBroadcastId) return;
  show($('broadcastProgress'), true);
  const pct = d.total > 0 ? Math.round((d.current / d.total) * 100) : 0;
  $('progressFill').style.width = pct + '%';
  // Coerce the numeric SSE fields with Number() before interpolating into
  // innerHTML — defense in depth so only d.contact (already escaped) carries
  // untrusted text.
  $('progressInfo').innerHTML = `
    <span>${Number(d.current) || 0}/${Number(d.total) || 0} — ${escHTML(d.contact)}</span>
    <span><strong style="color:var(--green)">${Number(d.ok) || 0}</strong> OK / <strong style="color:var(--red)">${Number(d.fail) || 0}</strong> ${escHTML(t('msg_fail'))}</span>`;
}

export function hideProgress() {
  setTimeout(() => show($('broadcastProgress'), false), 3000);
}

export async function onDone(d) {
  hideProgress();
  // Only render the summary if THIS tab queued the broadcast — the SSE
  // channel is shared with every other open tab on the same backend.
  if (!d || d.broadcastId !== state.activeBroadcastId) return;

  show($('broadcastResults'), true);
  $('broadcastSummary').innerHTML =
    summaryCard(d.ok, t('msg_ok'), 'green') +
    summaryCard(d.fail, t('msg_fail'), 'red') +
    summaryCard(d.total, t('ui_total'), 'accent');

  try {
    const msgs = await api('/api/broadcasts/' + d.broadcastId);
    $('broadcastResultsList').innerHTML = (msgs || []).map((m) => {
      const ok = m.status === 'sent' || m.status === 'delivered' || m.status === 'read';
      const icon = ok ? '&#x2713;' : '&#x2715;';
      const detail = escHTML(m.phone) + (m.error ? ' — ' + escHTML(m.error) : '');
      return `<div class="result-item">
        <div class="status-icon ${ok ? 'ok' : 'fail'}">${icon}</div>
        <div class="info"><div class="name">${escHTML(m.contactName)}</div><div class="detail">${detail}</div></div>
      </div>`;
    }).join('');
  } catch { /* non-fatal */ }

  const baseMsg = t('msg_broadcast_done').replace('%d', d.ok).replace('%d', d.total);
  const cancelledSuffix = t('msg_broadcast_cancelled');
  const finalMsg = d.aborted ? baseMsg + (cancelledSuffix !== 'msg_broadcast_cancelled' ? cancelledSuffix : ' (cancelado)') : baseMsg;
  toast(finalMsg, d.aborted || d.fail > 0 ? 'error' : 'success');

  const btn = /** @type {HTMLButtonElement} */ ($('broadcastBtn'));
  btn.disabled = false;
  sending = false;
  btn.innerHTML = '&#x1F4E8; ' + t('ui_send_broadcast');
  updateSendGate();
  set({ activeBroadcastId: null });
}

// ─── Preview / vars ────────────────────────────────────────────

export function renderVarTags() {
  const el = $('broadcastVarTags');
  const builtins = [
    { key: 'nome', builtin: true },
    { key: 'telefone', builtin: true },
  ];
  const customs = Object.keys(state.templateVars).map((k) => ({ key: k, builtin: false }));
  const all = [...builtins, ...customs];
  el.innerHTML = all.map((v) =>
    `<span class="var-tag${v.builtin ? ' builtin' : ''}" data-action="insert-var" data-key="${escAttr(v.key)}">{${escHTML(v.key)}}</span>`,
  ).join('');
}

function insertVar(key) {
  const ta = /** @type {HTMLTextAreaElement} */ ($('broadcastMsg'));
  const start = ta.selectionStart;
  const end = ta.selectionEnd;
  const insert = '{' + key + '}';
  ta.value = ta.value.slice(0, start) + insert + ta.value.slice(end);
  ta.selectionStart = ta.selectionEnd = start + insert.length;
  ta.focus();
  updatePreview();
}

export function updatePreview() {
  const msg = /** @type {HTMLTextAreaElement} */ ($('broadcastMsg')).value;
  const group = $('previewGroup');
  if (!msg) { show(group, false); return; }

  const sampleName = state.contacts[0]?.name || 'João Silva';
  const samplePhone = state.contacts[0]?.phone || '5511999998888';

  let preview = msg
    .replace(/\{nome\}/gi, sampleName)
    .replace(/\{name\}/gi, sampleName)
    .replace(/\{telefone\}/gi, samplePhone)
    .replace(/\{phone\}/gi, samplePhone);
  for (const [k, v] of Object.entries(state.templateVars)) {
    preview = preview.split('{' + k + '}').join(v);
  }

  $('msgPreview').textContent = preview;
  show(group, true);
}

// ─── First-run checklist ─────────────────────────────────────

const ONBOARD_KEY = 'zapcast.onboard.dismissed';

function isOnboardDismissed() {
  try { return localStorage.getItem(ONBOARD_KEY) === '1'; } catch { return true; }
}

/**
 * Show the 1-2-3 checklist until WhatsApp is connected AND at least one
 * contact exists (or the operator dismisses it). Refreshes on every
 * status/contacts change so steps tick off live.
 */
export function refreshOnboarding() {
  const panel = $('onboardPanel');
  if (!panel || isOnboardDismissed()) { if (panel) show(panel, false); return; }
  const online = !!state.status.connected && !!state.status.loggedIn;
  const hasContacts = state.contacts.length > 0;
  show(panel, !online || !hasContacts);
  show($('onboardCheck1'), online);
  show($('onboardCheck2'), hasContacts);
}

function dismissOnboarding() {
  try { localStorage.setItem(ONBOARD_KEY, '1'); } catch { /* ignore */ }
  show($('onboardPanel'), false);
}

// ─── Char counter + test send ────────────────────────────────

export function updateCharCount() {
  const el = $('broadcastCharCount');
  if (!el) return;
  const n = /** @type {HTMLTextAreaElement} */ ($('broadcastMsg')).value.length;
  el.textContent = n > 0 ? `${n.toLocaleString()} ${t('ui_chars')}` : '';
}

/**
 * Send the current message to the operator's own connected number.
 * Reuses /api/broadcast with a single recipient so progress, history and
 * receipts behave exactly like a real send.
 */
async function sendTest() {
  if (!state.status.connected || !state.status.loggedIn || !state.status.phone) {
    toast('err_whatsapp_not_conn', 'error');
    navigate('auth');
    return;
  }
  const message = /** @type {HTMLTextAreaElement} */ ($('broadcastMsg')).value.trim();
  if (!message) { toast('err_type_message', 'error'); return; }
  const own = state.status.phone;
  const ok = await confirmDialog({
    title: t('ui_send_test'),
    message: t('confirm_send_test').replace('%s', own),
    confirmLabel: t('ui_send'),
  });
  if (!ok) return;
  const btn = /** @type {HTMLButtonElement} */ ($('broadcastBtn'));
  btn.disabled = true;
  sending = true;
  show($('broadcastResults'), false);
  show($('broadcastProgress'), true);
  resetCancelBtn();
  $('progressFill').style.width = '0%';
  $('progressInfo').textContent = t('msg_broadcast_starting') || 'Iniciando...';
  try {
    const result = await api('/api/broadcast', 'POST', { message, phones: [own] });
    set({ activeBroadcastId: result.broadcastId });
    toast('msg_broadcast_started', 'info');
  } catch (e) {
    toastErr(e);
    btn.disabled = false;
    sending = false;
    updateSendGate();
  }
}

// ─── Wiring ────────────────────────────────────────────────────

export function initBroadcastView() {
  for (const tab of document.querySelectorAll('.broadcast-tab')) {
    tab.addEventListener('click', () => setMode(/** @type {HTMLElement} */ (tab).getAttribute('data-mode')));
  }

  $('broadcastGroup').addEventListener('change', updateGroupInfo);
  $('broadcastGroup').addEventListener('change', queueSaveDraft);
  $('broadcastBtn').addEventListener('click', send);
  $('broadcastTestBtn').addEventListener('click', sendTest);
  $('broadcastCancelBtn').addEventListener('click', cancelBroadcast);
  $('onboardDismiss').addEventListener('click', dismissOnboarding);
  on('status', updateSendGate);
  on('status', refreshOnboarding);
  on('contacts', refreshOnboarding);
  updateSendGate();
  refreshOnboarding();
  updateCharCount();

  $('broadcastAttachBtn').addEventListener('click', () => $('broadcastFile').click());
  $('broadcastFile').addEventListener('change', onFilePicked);

  $('broadcastMsg').addEventListener('input', updatePreview);
  $('broadcastMsg').addEventListener('input', updateCharCount);
  $('broadcastMsg').addEventListener('input', queueSaveDraft);

  // Picker wiring (one delegated change handler).
  $(PICKER_LIST_ID).addEventListener('change', updateContactsCount);
  $(PICKER_LIST_ID).addEventListener('change', queueSaveDraft);
  $('broadcastPickerSearch').addEventListener('input', () => filterPicker(PICKER_LIST_ID, PICKER_SEARCH_ID));
  $('broadcastContactPicker').addEventListener('click', (e) => {
    const btn = /** @type {HTMLElement} */ (e.target).closest('button[data-action]');
    if (!btn) return;
    const a = btn.getAttribute('data-action');
    if (a === 'picker-select-all') toggleAllPicker(PICKER_LIST_ID, true, updateContactsCount);
    else if (a === 'picker-clear') toggleAllPicker(PICKER_LIST_ID, false, updateContactsCount);
  });

  // Var insert / recipient expand / attachment remove via delegation.
  document.getElementById('page-broadcast').addEventListener('click', (e) => {
    const el = /** @type {HTMLElement} */ (e.target).closest('[data-action]');
    if (!el) return;
    const a = el.getAttribute('data-action');
    if (a === 'insert-var') {
      const k = el.getAttribute('data-key');
      if (k) insertVar(k);
    } else if (a === 'expand-recipients') {
      expandRecipientBadges(el);
    } else if (a === 'clear-attachment') {
      clearAttachment();
    }
  });
}
