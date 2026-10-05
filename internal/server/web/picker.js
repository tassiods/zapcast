// picker.js — reusable contact picker used by groups and broadcast.
//
// The picker is just a list of `.contact-check` labels with a checkbox each.
// Search filtering toggles `hidden` instead of inline display, so we don't
// repaint styles per element on every keystroke.

import { state } from './state.js';
import { t } from './i18n.js';
import { escHTML, escAttr, fold } from './ui.js';

/**
 * @typedef {Object} RenderOpts
 * @property {string}          listId        Container that holds the rows.
 * @property {'id'|'phone'}    valueField    Property used as checkbox value.
 * @property {Set<string>}     selectedSet
 * @property {string}          emptyKey      i18n key shown when no contacts.
 * @property {() => void}      onSynced      Fired once after the list paints.
 */

/**
 * Repaint a picker list. Callers register their own delegated `change`
 * listener on the parent container during init() so we don't stack them
 * on every re-render.
 *
 * @param {RenderOpts} opts
 */
export function renderPickerList(opts) {
  const el = document.getElementById(opts.listId);
  if (!el) return;

  if (!state.contacts.length) {
    el.innerHTML =
      '<div style="padding:12px;color:var(--text-muted);font-size:0.82rem">' +
      escHTML(t(opts.emptyKey)) +
      '</div>';
  } else {
    el.innerHTML = state.contacts.map((c) => {
      const value = c[opts.valueField];
      const checked = opts.selectedSet.has(value) ? ' checked' : '';
      return `<label class="contact-check">
        <input type="checkbox" value="${escAttr(value)}"${checked}>
        <span class="name">${escHTML(c.name)}</span>
        <span class="phone">${escHTML(c.phone)}</span>
      </label>`;
    }).join('');
  }

  try { opts.onSynced(); } catch (e) { console.error('[picker] onSynced threw', e); }
}

/**
 * Filter rows by case-insensitive name OR phone substring.
 *
 * @param {string} listId
 * @param {string} searchId
 */
export function filterPicker(listId, searchId) {
  const list = document.getElementById(listId);
  const input = /** @type {HTMLInputElement|null} */ (document.getElementById(searchId));
  if (!list || !input) return;
  const q = fold(input.value);
  for (const row of list.querySelectorAll('.contact-check')) {
    const name = fold(row.querySelector('.name')?.textContent);
    const phone = row.querySelector('.phone')?.textContent || '';
    const match = !q || name.includes(q) || phone.includes(q);
    if (match) row.removeAttribute('hidden');
    else row.setAttribute('hidden', '');
  }
}

/**
 * Flip every visible checkbox to `checked`.
 *
 * @param {string} listId
 * @param {boolean} checked
 * @param {() => void} after
 */
export function toggleAllPicker(listId, checked, after) {
  const list = document.getElementById(listId);
  if (!list) return;
  for (const input of list.querySelectorAll('.contact-check:not([hidden]) input')) {
    /** @type {HTMLInputElement} */ (input).checked = checked;
  }
  after();
}

/**
 * Collect the currently-checked values.
 *
 * @param {string} listId
 * @returns {string[]}
 */
export function pickerValues(listId) {
  const list = document.getElementById(listId);
  if (!list) return [];
  return Array.from(list.querySelectorAll('input:checked')).map((cb) => /** @type {HTMLInputElement} */ (cb).value);
}
