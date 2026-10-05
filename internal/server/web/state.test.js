// state.test.js — verifies the pub/sub store contract that every view relies on.

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { state, set, on, reindexContacts } from './state.js';

beforeEach(() => {
  // The store is a module singleton — reset between tests so cases don't leak.
  set({
    contacts: [],
    groups: [],
    templateVars: {},
    status: { connected: false, loggedIn: false },
    activeBroadcastId: null,
    broadcastMode: 'group',
    contactsPage: 50,
  });
});

describe('state.set', () => {
  it('writes patched keys onto the store', () => {
    set({ broadcastMode: 'contacts' });
    expect(state.broadcastMode).toBe('contacts');
  });

  it('notifies subscribers for each touched key', () => {
    const fn = vi.fn();
    on('contacts', fn);
    set({ contacts: [{ id: 'a', name: 'A', phone: '1' }] });
    expect(fn).toHaveBeenCalledTimes(1);
    expect(fn.mock.calls[0][0].contacts).toHaveLength(1);
  });

  it('does not notify when the value is unchanged (identity check)', () => {
    const list = [];
    set({ contacts: list });
    const fn = vi.fn();
    on('contacts', fn);
    set({ contacts: list });
    expect(fn).not.toHaveBeenCalled();
  });

  it('fires the wildcard channel once per set call', () => {
    const fn = vi.fn();
    on('*', fn);
    set({ contacts: [{ id: 'a', name: 'A', phone: '1' }], broadcastMode: 'contacts' });
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it('returns an unsubscribe that prevents further notifications', () => {
    const fn = vi.fn();
    const off = on('contacts', fn);
    off();
    set({ contacts: [{ id: 'a', name: 'A', phone: '1' }] });
    expect(fn).not.toHaveBeenCalled();
  });

  it('isolates a throwing subscriber from peers', () => {
    const peer = vi.fn();
    on('contacts', () => { throw new Error('boom'); });
    on('contacts', peer);
    set({ contacts: [{ id: 'a', name: 'A', phone: '1' }] });
    expect(peer).toHaveBeenCalled();
  });
});

describe('reindexContacts', () => {
  it('rebuilds the phone→name index from the current contacts slice', () => {
    set({ contacts: [
      { id: 'a', name: 'Ana', phone: '111' },
      { id: 'b', name: 'Bob', phone: '222' },
    ]});
    reindexContacts();
    expect(state.nameByPhone.get('111')).toBe('Ana');
    expect(state.nameByPhone.get('222')).toBe('Bob');
    expect(state.nameByPhone.size).toBe(2);
  });

  it('produces an empty index when there are no contacts', () => {
    reindexContacts();
    expect(state.nameByPhone.size).toBe(0);
  });
});
