// picker.test.js — covers the contact-picker rendering, filter and bulk
// toggle helpers shared by groups and broadcast.

import { describe, it, expect, beforeEach } from 'vitest';
import { renderPickerList, filterPicker, toggleAllPicker, pickerValues } from './picker.js';
import { state, set, reindexContacts } from './state.js';

beforeEach(() => {
  document.body.innerHTML = `
    <div id="list"></div>
    <input id="search" />
  `;
  set({
    contacts: [
      { id: 'a', name: 'Ana',  phone: '111' },
      { id: 'b', name: 'Bob',  phone: '222' },
      { id: 'c', name: 'Cleo', phone: '333' },
    ],
  });
  reindexContacts();
});

describe('renderPickerList', () => {
  it('paints one .contact-check per contact', () => {
    renderPickerList({
      listId: 'list',
      valueField: 'phone',
      selectedSet: new Set(),
      emptyKey: 'empty_broadcast_no_contacts',
      onSynced: () => {},
    });
    expect(document.querySelectorAll('#list .contact-check').length).toBe(3);
  });

  it('marks rows in selectedSet as checked', () => {
    renderPickerList({
      listId: 'list',
      valueField: 'phone',
      selectedSet: new Set(['222']),
      emptyKey: 'empty_broadcast_no_contacts',
      onSynced: () => {},
    });
    const inputs = /** @type {NodeListOf<HTMLInputElement>} */ (
      document.querySelectorAll('#list input[type=checkbox]')
    );
    expect(inputs[0].checked).toBe(false);
    expect(inputs[1].checked).toBe(true);
    expect(inputs[2].checked).toBe(false);
  });

  it('renders the empty-state when no contacts exist', () => {
    set({ contacts: [] });
    renderPickerList({
      listId: 'list',
      valueField: 'phone',
      selectedSet: new Set(),
      emptyKey: 'empty_broadcast_no_contacts',
      onSynced: () => {},
    });
    expect(document.querySelector('#list .contact-check')).toBeNull();
    expect(document.querySelector('#list').textContent).toContain('empty_broadcast_no_contacts');
  });
});

describe('filterPicker', () => {
  beforeEach(() => {
    renderPickerList({
      listId: 'list',
      valueField: 'phone',
      selectedSet: new Set(),
      emptyKey: 'empty_broadcast_no_contacts',
      onSynced: () => {},
    });
  });

  it('hides rows whose name and phone do not match the query', () => {
    /** @type {HTMLInputElement} */ (document.getElementById('search')).value = 'bo';
    filterPicker('list', 'search');
    const rows = Array.from(document.querySelectorAll('#list .contact-check'));
    const visible = rows.filter((r) => !r.hasAttribute('hidden'));
    expect(visible.length).toBe(1);
    expect(visible[0].textContent).toContain('Bob');
  });

  it('matches by phone substring too', () => {
    /** @type {HTMLInputElement} */ (document.getElementById('search')).value = '333';
    filterPicker('list', 'search');
    const visible = Array.from(document.querySelectorAll('#list .contact-check'))
      .filter((r) => !r.hasAttribute('hidden'));
    expect(visible.length).toBe(1);
    expect(visible[0].textContent).toContain('Cleo');
  });

  it('shows every row when the query is empty', () => {
    /** @type {HTMLInputElement} */ (document.getElementById('search')).value = 'xy';
    filterPicker('list', 'search');
    /** @type {HTMLInputElement} */ (document.getElementById('search')).value = '';
    filterPicker('list', 'search');
    const visible = Array.from(document.querySelectorAll('#list .contact-check'))
      .filter((r) => !r.hasAttribute('hidden'));
    expect(visible.length).toBe(3);
  });
});

describe('toggleAllPicker + pickerValues', () => {
  beforeEach(() => {
    renderPickerList({
      listId: 'list',
      valueField: 'phone',
      selectedSet: new Set(),
      emptyKey: 'empty_broadcast_no_contacts',
      onSynced: () => {},
    });
  });

  it('checks every visible row and collects its value', () => {
    toggleAllPicker('list', true, () => {});
    expect(pickerValues('list').sort()).toEqual(['111', '222', '333']);
  });

  it('clears every visible row when checked=false', () => {
    toggleAllPicker('list', true, () => {});
    toggleAllPicker('list', false, () => {});
    expect(pickerValues('list')).toEqual([]);
  });

  it('skips rows hidden by the current filter', () => {
    /** @type {HTMLInputElement} */ (document.getElementById('search')).value = 'bo';
    filterPicker('list', 'search');
    toggleAllPicker('list', true, () => {});
    expect(pickerValues('list')).toEqual(['222']);
  });
});
