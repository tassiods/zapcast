// view-history.js — stats cards and broadcast history list.

import { api } from './api.js';
import { $, escHTML, escAttr, toast, summaryCard, debounceSearch, fold, formatDateTime } from './ui.js';
import { t } from './i18n.js';
import { campaignSummary, statusHTML } from './delivery-labels.js';
import { loadDeliveryReview, initDeliveryReview, cancelRecoveredCampaign } from './delivery-review.js';

/** Definitive failure is not the aggregate non-success count. Fail closed for legacy/incomplete campaigns. */
export function safeFailedPhones(campaign, rows) {
  if (campaign?.status !== 'completed' || !Array.isArray(rows) ||
      Number(campaign.total) !== rows.length ||
      !['pending', 'uncertain', 'cancelled', 'unresolved'].every(key => campaign[key] === 0)) return [];
  return rows.filter(row => row.status === 'failed').map(row => row.phone).filter(Boolean);
}

/** Full last page — filters run client-side over this. */
let allItems = [];
let historyEpoch = 0;
let historySequence = 0;
window.addEventListener('zapcast:session-cleared', () => {
  historyEpoch++; historySequence++; allItems = [];
  $('historyList')?.replaceChildren(); $('statsCards')?.replaceChildren();
});

// ─── Loading ───────────────────────────────────────────────────

export async function loadHistory() {
  const current = historyEpoch, sequence = ++historySequence;
  const review = loadDeliveryReview();
  try {
    const [stats, page] = await Promise.all([
      api('/api/stats'),
      api('/api/broadcasts?limit=500'),
    ]);
    if (current !== historyEpoch || sequence !== historySequence) return;
    renderStats(stats || {});
    // Backwards compat: backend now returns { items, nextBefore } but tests
    // and older builds may still return a bare array.
    const items = Array.isArray(page) ? page : (page?.items || []);
    allItems = items;
    applyHistoryFilter();
    await review;
  } catch (e) {
    if (current !== historyEpoch) return;
    toast(t('msg_err_load_history') + (e instanceof Error ? e.message : String(e)), 'error');
  }
}

/** Lightweight stat refresh used when navigating to the broadcast page. */
export async function refreshBroadcastStats() {
  const current = historyEpoch;
  try {
    const stats = await api('/api/stats');
    if (current === historyEpoch) renderStats(stats || {});
  } catch { /* non-fatal */ }
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
  if (!el) return;
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
    if (f === 'clean' && !(b.status === 'completed' && b.fail === 0 &&
      ['pending','uncertain','cancelled','unresolved'].every(key => b[key] === 0))) return false;
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
      <div class="meta">${campaignSummary(b)}</div>
      <div class="meta">${escHTML(formatDateTime(b.sentAt))}</div>
      ${message ? `<div class="history-message-preview">${escHTML(preview)}</div>` : ''}
    </div>
  </div>
  ${['active','interrupted','uncertain'].includes(b.status) ? `<button type="button" class="btn btn-danger btn-sm" data-action="cancel-campaign" data-id="${id}">Cancelar campanha</button>` : ''}
  <div id="campaignError-${id}" role="alert"></div>
  <div class="group-detail" id="hd-${escAttr(String(id))}"></div>`;
}

async function toggleHistoryDetail(id) {
  const el = document.getElementById('hd-' + id);
  if (!el) return;
  if (el.classList.contains('open')) { el.classList.remove('open'); return; }
  for (const open of document.querySelectorAll('.group-detail.open')) open.classList.remove('open');

  const current = historyEpoch;
  try {
    const msgs = await api('/api/broadcasts/' + id);
    if (current !== historyEpoch || !el.isConnected) return;
    const campaign = allItems.find(b => Number(b.id) === Number(id));
    const failedPhones = safeFailedPhones(campaign, msgs);
    const message = campaign?.message || '';
    const messageHTML = message ? `<section class="history-message"><strong>${escHTML(t('ui_message'))}</strong><div class="history-message-text">${escHTML(message)}</div></section>` : '';
    const recipientsHTML = (msgs || []).map((m) => {
      return `<div class="member">
        <span>${escHTML(m.contactName)}</span>
        ${statusHTML(m.status)}
        <span>${escHTML(m.phone)}${m.error ? ' — ' + escHTML(m.error) : ''}</span>
      </div>`;
    }).join('') || `<div class="member">${escHTML(t('msg_no_records'))}</div>`;
    el.innerHTML = messageHTML + recipientsHTML;
    // Definitive failures still require linked durable intent review; never prefill a new broadcast.
    if (failedPhones.length) el.innerHTML += `<p>${escHTML(t('ui_retry_failed'))}: ${failedPhones.length} — revisão individual abaixo.</p>`;
    el.classList.add('open');
  } catch (e) {
    if (current !== historyEpoch) return;
    toast(t('msg_err_load_history') + (e instanceof Error ? e.message : String(e)), 'error');
  }
}

// ─── Wiring ────────────────────────────────────────────────────

export function initHistoryView() {
  initDeliveryReview(loadHistory);
  $('historyRefreshBtn').addEventListener('click', loadHistory);
  $('historySearch').addEventListener('input', () => debounceSearch('historySearch', applyHistoryFilter));
  $('historyStatusFilter').addEventListener('change', applyHistoryFilter);
  $('historyList').addEventListener('click', (e) => {
    const cancel = /** @type {HTMLElement} */ (e.target).closest('[data-action="cancel-campaign"]');
    if (cancel) {
      const id = Number(cancel.getAttribute('data-id'));
      if (allItems.some(row => Number(row.id) === id && ['active','interrupted','uncertain'].includes(row.status))) cancelRecoveredCampaign(id, loadHistory);
      return;
    }
    const row = /** @type {HTMLElement} */ (e.target).closest('[data-action="toggle-history"]');
    if (!row) return;
    const id = row.getAttribute('data-id');
    if (id) toggleHistoryDetail(id);
  });
}
