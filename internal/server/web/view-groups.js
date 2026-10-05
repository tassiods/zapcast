// view-groups.js — list + CRUD for groups, plus the expand/collapse drawer.

import { state, set } from './state.js';
import { api, safeApi } from './api.js';
import { $, escHTML, escAttr, toast, toastErr, confirmDialog, debounceSearch, fold } from './ui.js';
import { t } from './i18n.js';
import { renderPickerList, filterPicker, toggleAllPicker, pickerValues } from './picker.js';

const PICKER_LIST_ID = 'groupPickerList';
const PICKER_SEARCH_ID = 'groupPickerSearch';

// ─── Data loading ──────────────────────────────────────────────

export async function loadGroups() {
  set({ groups: await safeApi('/api/groups', []) });
  render();
}

// ─── Render ────────────────────────────────────────────────────

function filtered() {
  const q = fold(/** @type {HTMLInputElement} */ ($('groupSearch'))?.value);
  return q ? state.groups.filter((g) => fold(g.name).includes(q)) : state.groups;
}

function render() {
  const list = filtered();
  const el = $('groupsList');

  if (!list.length) {
    el.innerHTML = `<div class="empty-state"><div class="icon">&#x1F4CB;</div><p>${escHTML(t('empty_groups'))}</p></div>`;
    return;
  }

  el.innerHTML = list.map(rowHTML).join('');
}

function rowHTML(g) {
  const count = g.count ?? g.contacts.length;
  return `<div class="list-item" style="cursor:pointer" data-action="toggle-group" data-id="${escAttr(g.id)}">
    <div class="info">
      <div class="name">${escHTML(g.name)}</div>
      <div class="meta">${count} contato${count !== 1 ? 's' : ''} — clique para expandir</div>
    </div>
    <div class="actions" data-stop-bubble>
      <button class="btn btn-secondary btn-sm" data-action="edit-group" data-id="${escAttr(g.id)}">${escHTML(t('ui_edit'))}</button>
      <button class="btn btn-danger btn-sm" data-action="delete-group" data-id="${escAttr(g.id)}">${escHTML(t('ui_remove'))}</button>
    </div>
  </div>
  <div class="group-detail" id="gd-${escAttr(g.id)}"></div>`;
}

// ─── Form ──────────────────────────────────────────────────────

function showAdd() {
  /** @type {HTMLInputElement} */ ($('groupEditId')).value = '';
  /** @type {HTMLInputElement} */ ($('groupNameInput')).value = '';
  renderPicker([]);
  $('groupForm').classList.add('open');
  $('groupNameInput').focus();
}

function showEdit(id) {
  const g = state.groups.find((x) => x.id === id);
  if (!g) return;
  /** @type {HTMLInputElement} */ ($('groupEditId')).value = g.id;
  /** @type {HTMLInputElement} */ ($('groupNameInput')).value = g.name;
  renderPicker(g.contacts);
  $('groupForm').classList.add('open');
  $('groupNameInput').focus();
}

function hideForm() { $('groupForm').classList.remove('open'); }

function renderPicker(selectedPhones = []) {
  /** @type {HTMLInputElement} */ ($(PICKER_SEARCH_ID)).value = '';
  renderPickerList({
    listId: PICKER_LIST_ID,
    valueField: 'id',
    selectedSet: new Set(selectedPhones),
    emptyKey: 'empty_groups_picker',
    onSynced: updatePickerCount,
  });
}

function updatePickerCount() {
  const checked = pickerValues(PICKER_LIST_ID).length;
  $('groupPickerCount').textContent = checked > 0 ? `(${checked} selecionados)` : '';
}

async function save() {
  const id = /** @type {HTMLInputElement} */ ($('groupEditId')).value;
  const name = /** @type {HTMLInputElement} */ ($('groupNameInput')).value.trim();
  if (!name) { toast('err_fill_name_phone', 'error'); return; }
  const contacts = pickerValues(PICKER_LIST_ID);

  try {
    if (id) {
      const updated = await api('/api/groups/' + encodeURIComponent(id), 'PUT', { name, contacts });
      const next = state.groups.map((g) => (g.id === id ? { ...g, ...updated, name, contacts } : g));
      set({ groups: next });
    } else {
      const created = await api('/api/groups', 'POST', { name, contacts });
      set({ groups: [...state.groups, created] });
    }
    hideForm();
    render();
    toast(id ? 'msg_group_updated' : 'msg_group_created2', 'success');
  } catch (e) {
    toastErr(e);
  }
}

async function remove(id) {
  const g = state.groups.find((x) => x.id === id);
  const ok = await confirmDialog({
    title: t('ui_remove'),
    message: t('confirm_remove_group').replace('%s', g?.name || id),
    confirmLabel: t('ui_remove'),
    danger: true,
  });
  if (!ok) return;
  try {
    await api('/api/groups/' + encodeURIComponent(id), 'DELETE');
    set({ groups: state.groups.filter((x) => x.id !== id) });
    render();
    toast('msg_group_removed', 'info');
  } catch (e) {
    toastErr(e);
  }
}

function toggleDetail(gid) {
  const panelId = 'gd-' + gid;
  const el = document.getElementById(panelId);
  if (!el) return;
  if (el.classList.contains('open')) { el.classList.remove('open'); return; }

  for (const open of document.querySelectorAll('.group-detail.open')) open.classList.remove('open');

  const g = state.groups.find((x) => x.id === gid);
  if (!g) return;

  const MAX = 100;
  const members = g.contacts.map((phone) => ({ name: state.nameByPhone.get(phone) || phone, phone }));
  let html = members.slice(0, MAX).map((m) =>
    `<div class="member"><span>${escHTML(m.name)}</span><span>${escHTML(m.phone)}</span></div>`,
  ).join('');
  if (members.length > MAX) {
    html += `<div class="member" style="color:var(--accent);font-weight:600">... e mais ${members.length - MAX} contatos</div>`;
  }
  el.innerHTML = html;
  el.classList.add('open');
}

// ─── Wiring ────────────────────────────────────────────────────

export function initGroupsView() {
  $('groupAddBtn').addEventListener('click', showAdd);
  $('groupSaveBtn').addEventListener('click', save);
  $('groupCancelBtn').addEventListener('click', hideForm);
  $('groupSearch').addEventListener('input', () => debounceSearch('groupSearch', render));

  $('groupPickerSearch').addEventListener('input', () => filterPicker(PICKER_LIST_ID, PICKER_SEARCH_ID));

  // One delegated change handler for the entire picker list — set once.
  $(PICKER_LIST_ID).addEventListener('change', updatePickerCount);

  // Picker-actions (select all / clear).
  $('groupContactPicker').addEventListener('click', (e) => {
    const btn = /** @type {HTMLElement} */ (e.target).closest('button[data-action]');
    if (!btn) return;
    const a = btn.getAttribute('data-action');
    if (a === 'picker-select-all') toggleAllPicker(PICKER_LIST_ID, true, updatePickerCount);
    else if (a === 'picker-clear') toggleAllPicker(PICKER_LIST_ID, false, updatePickerCount);
  });

  $('groupsList').addEventListener('click', (e) => {
    const stop = /** @type {HTMLElement} */ (e.target).closest('[data-stop-bubble]');
    const btn = /** @type {HTMLElement} */ (e.target).closest('button[data-action]');
    if (btn) {
      const id = btn.getAttribute('data-id');
      if (btn.getAttribute('data-action') === 'edit-group' && id) { e.stopPropagation(); showEdit(id); return; }
      if (btn.getAttribute('data-action') === 'delete-group' && id) { e.stopPropagation(); remove(id); return; }
    }
    if (stop) return;
    const row = /** @type {HTMLElement} */ (e.target).closest('[data-action="toggle-group"]');
    if (row) toggleDetail(row.getAttribute('data-id'));
  });
}
