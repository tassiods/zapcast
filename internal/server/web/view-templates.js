// view-templates.js — custom template variables for broadcast messages.

import { state, set } from './state.js';
import { api, safeApi } from './api.js';
import { $, escHTML, escAttr, toast, toastErr, confirmDialog } from './ui.js';
import { t } from './i18n.js';

let onChange = () => {};

/** Called by app.js so broadcast view re-renders its var tag list. */
export function setOnChange(fn) { onChange = fn; }

// ─── Data loading ──────────────────────────────────────────────

export async function loadTemplates() {
  set({ templateVars: await safeApi('/api/templates', {}) });
  render();
  onChange();
}

// ─── Render ────────────────────────────────────────────────────

function render() {
  const el = $('templatesList');
  const keys = Object.keys(state.templateVars);

  if (!keys.length) {
    el.innerHTML = `<div class="empty-state"><div class="icon">&#x1F3F7;</div><p>${escHTML(t('empty_templates'))}</p></div>`;
    return;
  }

  el.innerHTML = keys.map((k) =>
    `<div class="template-row">
      <span class="key">{${escHTML(k)}}</span>
      <span class="value">${escHTML(state.templateVars[k])}</span>
      <div class="actions">
        <button class="btn btn-secondary btn-sm" data-action="edit-var" data-key="${escAttr(k)}">${escHTML(t('ui_edit_var'))}</button>
        <button class="btn btn-danger btn-sm" data-action="delete-var" data-key="${escAttr(k)}">${escHTML(t('ui_delete'))}</button>
      </div>
    </div>`,
  ).join('');
}

// ─── Form ──────────────────────────────────────────────────────

function showAdd() {
  /** @type {HTMLInputElement} */ ($('templateKey')).value = '';
  /** @type {HTMLInputElement} */ ($('templateValue')).value = '';
  /** @type {HTMLInputElement} */ ($('templateKey')).disabled = false;
  $('templateForm').classList.add('open');
  $('templateKey').focus();
}

function showEdit(key) {
  /** @type {HTMLInputElement} */ ($('templateKey')).value = key;
  /** @type {HTMLInputElement} */ ($('templateKey')).disabled = true;
  /** @type {HTMLInputElement} */ ($('templateValue')).value = state.templateVars[key] || '';
  $('templateForm').classList.add('open');
  $('templateValue').focus();
}

function hideForm() { $('templateForm').classList.remove('open'); }

async function save() {
  const rawKey = /** @type {HTMLInputElement} */ ($('templateKey')).value.trim();
  const key = rawKey.replace(/^\{|\}$/g, '');
  const value = /** @type {HTMLInputElement} */ ($('templateValue')).value.trim();
  if (!key) { toast('err_type_var_name', 'error'); return; }
  if (!value) { toast('err_type_var_value', 'error'); return; }

  try {
    const res = await api('/api/templates', 'POST', { key, value });
    if (res?.error) { toast(res.error, 'error'); return; }
    set({ templateVars: { ...state.templateVars, [key]: value } });
    hideForm();
    render();
    onChange();
    toast('msg_var_saved', 'success');
  } catch (e) {
    toastErr(e);
  }
}

async function remove(key) {
  const ok = await confirmDialog({
    title: t('ui_remove'),
    message: t('confirm_remove_var').replace('%s', key),
    confirmLabel: t('ui_remove'),
    danger: true,
  });
  if (!ok) return;
  try {
    await api('/api/templates/' + encodeURIComponent(key), 'DELETE');
    const next = { ...state.templateVars };
    delete next[key];
    set({ templateVars: next });
    render();
    onChange();
    toast('msg_var_removed', 'info');
  } catch (e) {
    toastErr(e);
  }
}

// ─── Wiring ────────────────────────────────────────────────────

export function initTemplatesView() {
  $('templateAddBtn').addEventListener('click', showAdd);
  $('templateSaveBtn').addEventListener('click', save);
  $('templateCancelBtn').addEventListener('click', hideForm);

  $('templatesList').addEventListener('click', (e) => {
    const btn = /** @type {HTMLElement} */ (e.target).closest('button[data-action]');
    if (!btn) return;
    const key = btn.getAttribute('data-key');
    if (!key) return;
    if (btn.getAttribute('data-action') === 'edit-var') showEdit(key);
    if (btn.getAttribute('data-action') === 'delete-var') remove(key);
  });
}
