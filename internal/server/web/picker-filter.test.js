// picker-filter.test.js — proves the broadcast/groups contact-picker search
// path (renderPickerList + filterPicker + pickerValues) actually filters.
// Added after a report of picker search failing on those screens while the
// contacts-screen search kept working.

import { describe, it, expect, beforeEach } from 'vitest';
import { setContacts } from './state.js';
import { renderPickerList, filterPicker, pickerValues } from './picker.js';

const CONTACTS = [
  { id: '1', name: 'João Silva', phone: '5511999998888', groups: [] },
  { id: '2', name: 'Maria Souza', phone: '5521988887777', groups: [] },
  { id: '3', name: 'Silva, Ana', phone: '5531977776666', groups: [] },
];

function visibleNames(listId) {
  return Array.from(document.querySelectorAll(`#${listId} .contact-check:not([hidden]) .name`))
    .map((el) => el.textContent);
}

/** @returns {HTMLInputElement} */
function searchBox() {
  return /** @type {HTMLInputElement} */ (document.getElementById('psearch'));
}

beforeEach(() => {
  document.body.innerHTML =
    '<input type="search" id="psearch">' +
    '<div id="plist"></div>';
  setContacts(CONTACTS);
  renderPickerList({
    listId: 'plist',
    valueField: 'phone',
    selectedSet: new Set(),
    emptyKey: 'empty_broadcast_no_contacts',
    onSynced: () => {},
  });
});

describe('picker search (broadcast/groups)', () => {
  it('renders one checkbox row per contact', () => {
    expect(document.querySelectorAll('#plist .contact-check').length).toBe(3);
  });

  it('narrows rows by name substring', () => {
    searchBox().value = 'silva';
    filterPicker('plist', 'psearch');
    expect(visibleNames('plist').sort()).toEqual(['João Silva', 'Silva, Ana']);
  });

  it('narrows rows by phone substring', () => {
    searchBox().value = '5521988887777';
    filterPicker('plist', 'psearch');
    expect(visibleNames('plist')).toEqual(['Maria Souza']);
  });

  it('restores all rows when the query is cleared', () => {
    searchBox().value = 'maria';
    filterPicker('plist', 'psearch');
    expect(visibleNames('plist')).toEqual(['Maria Souza']);
    searchBox().value = '';
    filterPicker('plist', 'psearch');
    expect(visibleNames('plist').length).toBe(3);
  });

  it('collects only checked values', () => {
    const boxes = document.querySelectorAll('#plist input[type="checkbox"]');
    /** @type {HTMLInputElement} */ (boxes[0]).checked = true;
    /** @type {HTMLInputElement} */ (boxes[2]).checked = true;
    expect(pickerValues('plist')).toEqual(['5511999998888', '5531977776666']);
  });

  it('matches unaccented query against accented names', () => {
    setContacts([
      { id: '1', name: 'José Ávila', phone: '5521980000000', groups: [] },
      { id: '2', name: 'Maria Souza', phone: '5521988887777', groups: [] },
    ]);
    renderPickerList({
      listId: 'plist',
      valueField: 'phone',
      selectedSet: new Set(),
      emptyKey: 'empty_broadcast_no_contacts',
      onSynced: () => {},
    });
    searchBox().value = 'jose';
    filterPicker('plist', 'psearch');
    expect(visibleNames('plist')).toEqual(['José Ávila']);
  });
});
