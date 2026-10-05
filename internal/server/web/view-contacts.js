// view-contacts.js — list + CRUD for contacts.
//
// Optimistic updates: save/delete patch the local store first, then post.
// Failures roll back by reloading from the server.

import { state, set, setContacts } from './state.js';
import { api, safeApi } from './api.js';
import { $, $$all, escHTML, escAttr, show, toast, toastErr, confirmDialog, debounceSearch, validatePhoneInline, fold } from './ui.js';
import { t } from './i18n.js';

const PAGE_SIZE = 50;

// ─── Data loading ──────────────────────────────────────────────

export async function loadContacts() {
  const list = await safeApi('/api/contacts', []);
  setContacts(list, { contactsPage: PAGE_SIZE });
  render();
}

// ─── Render ────────────────────────────────────────────────────

function filtered() {
  const q = fold(/** @type {HTMLInputElement} */ ($('contactSearch'))?.value);
  if (!q) return state.contacts;
  return state.contacts.filter((c) => fold(c.name).includes(q) || c.phone.includes(q));
}

function render() {
  const list = filtered();
  const el = $('contactsList');
  const counter = $('contactCounter');
  const more = $('contactsMore');

  const totalLabel = `${list.length} contato${list.length !== 1 ? 's' : ''}`;
  const ofLabel = list.length !== state.contacts.length ? ` (de ${state.contacts.length})` : '';
  counter.textContent = totalLabel + ofLabel;

  if (!list.length) {
    el.innerHTML = `<div class="empty-state"><div class="icon">&#x1F464;</div><p>${escHTML(t('empty_contacts'))}</p></div>`;
    show(more, false);
    return;
  }

  const visible = list.slice(0, state.contactsPage);
  el.innerHTML = visible.map(rowHTML).join('');
  show(more, list.length > state.contactsPage);
}

function rowHTML(c) {
  const tags = (c.groups || []).map((g) => `<span class="tag">${escHTML(g)}</span>`).join('');
  return `<div class="list-item">
    <div class="info">
      <div class="name">${escHTML(c.name)} ${tags}</div>
      <div class="meta">${escHTML(c.phone)}</div>
    </div>
    <div class="actions">
      <button class="btn btn-secondary btn-sm" data-action="edit-contact" data-id="${escAttr(c.id)}">${escHTML(t('ui_edit'))}</button>
      <button class="btn btn-danger btn-sm" data-action="delete-contact" data-id="${escAttr(c.id)}">${escHTML(t('ui_remove'))}</button>
    </div>
  </div>`;
}

// ─── Form ──────────────────────────────────────────────────────

function showAdd() {
  /** @type {HTMLInputElement} */ ($('contactEditId')).value = '';
  /** @type {HTMLInputElement} */ ($('contactName')).value = '';
  /** @type {HTMLInputElement} */ ($('contactPhone')).value = '';
  $('contactPhoneError').textContent = '';
  show($('contactPhoneError'), false);
  $('contactPhone').removeAttribute('aria-invalid');
  renderGroupPicker([]);
  $('contactForm').classList.add('open');
  $('contactName').focus();
}

function showEdit(id) {
  const c = state.contacts.find((x) => x.id === id);
  if (!c) return;
  /** @type {HTMLInputElement} */ ($('contactEditId')).value = c.id;
  /** @type {HTMLInputElement} */ ($('contactName')).value = c.name;
  /** @type {HTMLInputElement} */ ($('contactPhone')).value = c.phone;
  $('contactPhoneError').textContent = '';
  show($('contactPhoneError'), false);
  $('contactPhone').removeAttribute('aria-invalid');
  renderGroupPicker(c.groups || []);
  $('contactForm').classList.add('open');
  $('contactName').focus();
}

function hideForm() { $('contactForm').classList.remove('open'); }

// ─── Inline phone validation (blur + typing after first blur) ──

function validatePhoneField(showEmpty) {
  const input = /** @type {HTMLInputElement} */ ($('contactPhone'));
  const errEl = $('contactPhoneError');
  const raw = input.value.trim();
  // Empty while typing is not an error yet — save() still requires it.
  if (!raw && !showEmpty) {
    errEl.textContent = '';
    show(errEl, false);
    input.removeAttribute('aria-invalid');
    return true;
  }
  const v = validatePhoneInline(raw);
  if (!v.ok) {
    errEl.textContent = t(v.msg);
    show(errEl, true);
    input.setAttribute('aria-invalid', 'true');
    return false;
  }
  errEl.textContent = '';
  show(errEl, false);
  input.removeAttribute('aria-invalid');
  return true;
}

function renderGroupPicker(selected) {
  const el = $('contactGroupPicker');
  if (!state.groups.length) {
    el.innerHTML = `<span style="color:var(--text-muted);font-size:0.82rem">${escHTML(t('empty_groups_picker'))}</span>`;
    return;
  }
  el.innerHTML = state.groups.map((g) =>
    `<label style="display:flex;align-items:center;gap:6px;padding:4px 10px;background:var(--surface3);border-radius:16px;font-size:0.82rem;cursor:pointer">
      <input type="checkbox" class="contact-group-cb" value="${escAttr(g.name)}" ${selected.includes(g.name) ? 'checked' : ''} style="accent-color:var(--accent)">
      ${escHTML(g.name)}
    </label>`,
  ).join('');
}

async function save() {
  const id = /** @type {HTMLInputElement} */ ($('contactEditId')).value;
  const name = /** @type {HTMLInputElement} */ ($('contactName')).value.trim();
  const phoneRaw = /** @type {HTMLInputElement} */ ($('contactPhone')).value.trim();

  if (!name) { toast('err_fill_name_phone', 'error'); return; }
  const v = validatePhoneInline(phoneRaw);
  if (!v.ok) { toast(v.msg, 'error'); $('contactPhone').focus(); return; }

  const phone = v.digits;
  const groups = $$all('.contact-group-cb:checked').map((cb) => /** @type {HTMLInputElement} */ (cb).value);

  try {
    if (id) {
      const updated = await api('/api/contacts/' + encodeURIComponent(id), 'PUT', { name, phone, groups });
      // Patch local cache: prefer the server response so we keep server-set fields in sync.
      const next = state.contacts.map((c) => (c.id === id ? { ...c, ...updated, name, phone, groups } : c));
      setContacts(next);
    } else {
      const created = await api('/api/contacts', 'POST', { name, phone, groups });
      setContacts([...state.contacts, created || { id: phone, name, phone, groups }]);
    }
    hideForm();
    render();
    toast(id ? 'msg_contact_updated' : 'msg_contact_added', 'success');
  } catch (e) {
    toastErr(e);
  }
}

async function remove(id) {
  const c = state.contacts.find((x) => x.id === id);
  const ok = await confirmDialog({
    title: t('ui_remove'),
    message: t('confirm_remove_contact').replace('%s', c?.name || id),
    confirmLabel: t('ui_remove'),
    danger: true,
  });
  if (!ok) return;
  try {
    await api('/api/contacts/' + encodeURIComponent(id), 'DELETE');
    setContacts(state.contacts.filter((x) => x.id !== id));
    render();
    toast('msg_contact_removed', 'info');
  } catch (e) {
    toastErr(e);
  }
}

function loadMore() {
  set({ contactsPage: state.contactsPage + PAGE_SIZE });
  render();
}

// ─── Wiring ────────────────────────────────────────────────────

export function initContactsView() {
  $('contactAddBtn').addEventListener('click', showAdd);
  $('contactSaveBtn').addEventListener('click', save);
  $('contactCancelBtn').addEventListener('click', hideForm);
  $('contactsMoreBtn').addEventListener('click', loadMore);
  $('contactSearch').addEventListener('input', () => debounceSearch('contactSearch', render));
  $('contactPhone').addEventListener('blur', () => validatePhoneField(true));
  $('contactPhone').addEventListener('input', () => {
    if ($('contactPhone').getAttribute('aria-invalid') === 'true') validatePhoneField(false);
  });

  $('newGroupInlineBtn').addEventListener('click', createGroupInline);
  $('newGroupInline').addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); createGroupInline(); }
  });

  $('contactsList').addEventListener('click', (e) => {
    const btn = /** @type {HTMLElement} */ (e.target).closest('button[data-action]');
    if (!btn) return;
    const id = btn.getAttribute('data-id');
    if (btn.getAttribute('data-action') === 'edit-contact' && id) showEdit(id);
    if (btn.getAttribute('data-action') === 'delete-contact' && id) remove(id);
  });
}

/**
 * Inline group create from inside the contact form. Pushes the new group into
 * the cache + ticks it in the picker so the user keeps their flow.
 */
async function createGroupInline() {
  const input = /** @type {HTMLInputElement} */ ($('newGroupInline'));
  const name = input.value.trim();
  if (!name) return;
  if (state.groups.some((g) => fold(g.name) === fold(name))) {
    toast(t('err_group_exists_js').replace('%s', name), 'error');
    return;
  }
  try {
    const created = await api('/api/groups', 'POST', { name, contacts: [] });
    if (created && created.name) set({ groups: [...state.groups, created] });
    const selected = $$all('.contact-group-cb:checked').map((cb) => /** @type {HTMLInputElement} */ (cb).value);
    selected.push(name);
    renderGroupPicker(selected);
    input.value = '';
    toast(t('msg_group_created').replace('%s', name), 'success');
  } catch (e) {
    toastErr(e);
  }
}

/** Re-render after external mutations (other views call this). */
export function rerenderContacts() { render(); }
