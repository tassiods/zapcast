// view-history.js — stats cards and broadcast history list.

import { api } from './api.js';
import { $, escHTML, escAttr, toast, summaryCard, debounceSearch, fold, formatDateTime } from './ui.js';
import { t } from './i18n.js';
import { prefillContacts } from './view-broadcast.js';
import { navigate } from './router.js';

/** Full last page — filters run client-side over this. */
let allItems = [];

// ─── Loading ───────────────────────────────────────────────────

export async function loadHistory() {
  try {
    const [stats, page] = await Promise.all([
      api('/api/stats'),
      api('/api/broadcasts?limit=500'),
    ]);
    renderStats(stats || {});
    // Backwards compat: backend now returns { items, nextBefore } but tests
    // and older builds may still return a bare array.
    const items = Array.isArray(page) ? page : (page?.items || []);
    allItems = items;
    applyHistoryFilter();
  } catch (e) {
    toast(t('msg_err_load_history') + (e instanceof Error ? e.message : String(e)), 'error');
  }
}

/** Lightweight stat refresh used when navigating to the broadcast page. */
export async function refreshBroadcastStats() {
  try { renderStats((await api('/api/stats')) || {}); } catch { /* non-fatal */ }
}

// ─── Render ────────────────────────────────────────────────────

function renderStats(s) {
  const cards = $('statsCards');
  if (cards) {
    cards.innerHTML =
      summaryCard(s.broadcasts || 0, t('ui_broadcast'), 'accent', false) +
      summaryCard(s.ok || 0, t('msg_ok'), 'ok', false) +
      summaryCard(s.fail || 0, t('msg_fail'), 'err', false) +
      summaryCard(s.delivered || 0, t('msg_delivered'), 'warn', false) +
      summaryCard(s.read || 0, t('msg_read'), 'ok', false);
  }

  const sb = $('statBroadcasts'); if (sb) sb.textContent = (s.broadcasts || 0).toLocaleString();
  const ss = $('statSent');       if (ss) ss.textContent = (s.ok || 0).toLocaleString();
  const sd = $('statDelivered');  if (sd) sd.textContent = (s.delivered || 0).toLocaleString();
  const sr = $('statRead');       if (sr) sr.textContent = (s.read || 0).toLocaleString();
}

function renderList(items) {
  const el = $('historyList');
  if (!items || !items.length) {
    const searchVal = /** @type {HTMLInputElement|null} */ ($('historySearch'))?.value || '';
    const filterVal = /** @type {HTMLSelectElement|null} */ ($('historyStatusFilter'))?.value || 'all';
    const searching = searchVal !== '' || filterVal !== 'all';
    const msg = escHTML(searching ? t('msg_no_records') : t('empty_history'));
    el.innerHTML = `<div class="empty-state"><div class="icon">&#x1F4CA;</div><p>${msg}</p></div>`;
    return;
  }
  el.innerHTML = items.map(rowHTML).join('');
}

// ─── Search + status filter (client-side over the loaded page) ─

export function applyHistoryFilter() {
  const q = fold(/** @type {HTMLInputElement|null} */ ($('historySearch'))?.value);
  const f = /** @type {HTMLSelectElement|null} */ ($('historyStatusFilter'))?.value || 'all';
  renderList(allItems.filter((b) => {
    if (f === 'failed' && !(Number(b.fail) > 0)) return false;
    if (f === 'clean' && Number(b.fail) > 0) return false;
    if (!q) return true;
    const hay = fold(`${b.groupName || ''} ${b.message || ''} ${b.sentAt || ''}`);
    return hay.includes(q);
  }));
}

function rowHTML(b) {
  const id = Number(b.id) || 0;
  // Media-only broadcasts can carry a null message — guard so one bad row
  // doesn't throw and blank the whole history list.
  const message = b.message || '';
  const preview = message.length > 80 ? message.slice(0, 80) + '…' : message;
  const mediaChip = b.mediaType
    ? ` <span class="tag" style="background:var(--surface3);color:var(--ink-2)">&#x1F4CE; ${escHTML(b.mediaName || b.mediaType)}</span>`
    : '';
  return `<div class="list-item" style="cursor:pointer" data-action="toggle-history" data-id="${escAttr(String(id))}">
    <div class="info">
      <div class="name">${escHTML(b.groupName)} <span class="tag">${Number(b.ok) || 0}/${Number(b.total) || 0}</span>${mediaChip}</div>
      <div class="meta">${escHTML(formatDateTime(b.sentAt))} — ${escHTML(preview)}</div>
    </div>
  </div>
  <div class="group-detail" id="hd-${escAttr(String(id))}"></div>`;
}

async function toggleHistoryDetail(id) {
  const el = document.getElementById('hd-' + id);
  if (!el) return;
  if (el.classList.contains('open')) { el.classList.remove('open'); return; }
  for (const open of document.querySelectorAll('.group-detail.open')) open.classList.remove('open');

  try {
    const msgs = await api('/api/broadcasts/' + id);
    const failedPhones = (msgs || [])
      .filter((m) => !['sent', 'delivered', 'read'].includes(m.status))
      .map((m) => m.phone)
      .filter(Boolean);
    el.innerHTML = (msgs || []).map((m) => {
      const cls = 'badge-' + m.status;
      return `<div class="member">
        <span>${escHTML(m.contactName)}</span>
        <span class="badge-status ${cls}">${escHTML(m.status)}</span>
        <span>${escHTML(m.phone)}${m.error ? ' — ' + escHTML(m.error) : ''}</span>
      </div>`;
    }).join('') || `<div class="member">${escHTML(t('msg_no_records'))}</div>`;
    if (failedPhones.length) {
      el.innerHTML += `<div style="margin-top:10px;text-align:right">
        <button class="btn btn-secondary btn-sm" data-action="retry-failed" data-phones="${escAttr(failedPhones.join(','))}">${escHTML(t('ui_retry_failed'))} (${failedPhones.length})</button>
      </div>`;
    }
    el.classList.add('open');
  } catch (e) {
    toast(t('msg_err_load_history') + (e instanceof Error ? e.message : String(e)), 'error');
  }
}

// ─── Wiring ────────────────────────────────────────────────────

export function initHistoryView() {
  $('historyRefreshBtn').addEventListener('click', loadHistory);
  $('historySearch').addEventListener('input', () => debounceSearch('historySearch', applyHistoryFilter));
  $('historyStatusFilter').addEventListener('change', applyHistoryFilter);
  $('historyList').addEventListener('click', (e) => {
    const retry = /** @type {HTMLElement} */ (e.target).closest('[data-action="retry-failed"]');
    if (retry) {
      const phones = (retry.getAttribute('data-phones') || '').split(',').filter(Boolean);
      if (phones.length) {
        prefillContacts(phones);
        navigate('broadcast');
      }
      return;
    }
    const row = /** @type {HTMLElement} */ (e.target).closest('[data-action="toggle-history"]');
    if (!row) return;
    const id = row.getAttribute('data-id');
    if (id) toggleHistoryDetail(id);
  });
}
