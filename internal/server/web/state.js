// state.js — central app store.
//
// One mutable object plus a pub/sub fan-out. Views read from `state` and
// subscribe to slices via `on(key, fn)`. We never expose the raw object so
// callers go through `set(patch)`, which lets us batch DOM updates per turn.
//
// JSDoc typedefs below give editors enough shape info without dragging in a
// TS build step.

/**
 * @typedef {Object} Contact
 * @property {string} id
 * @property {string} name
 * @property {string} phone
 * @property {string[]} [groups]
 */

/**
 * @typedef {Object} Group
 * @property {string} id
 * @property {string} name
 * @property {string[]} contacts   Phones for legacy callers; ids for picker.
 * @property {number} [count]
 */

/**
 * @typedef {Object} Status
 * @property {boolean} connected
 * @property {boolean} loggedIn
 * @property {string}  [phone]
 * @property {string}  [version]
 */

/** @typedef {Record<string, string>} TemplateVars */

/**
 * @typedef {Object} BroadcastSummary
 * @property {number} broadcastId
 * @property {number} ok
 * @property {number} fail
 * @property {number} total
 * @property {boolean} [aborted]
 */

/**
 * @typedef {Object} AppState
 * @property {Contact[]}      contacts
 * @property {Group[]}        groups
 * @property {TemplateVars}   templateVars
 * @property {Map<string,string>} nameByPhone
 * @property {Status}         status
 * @property {string}         lang
 * @property {?number}        activeBroadcastId
 * @property {'group'|'contacts'} broadcastMode
 * @property {number}         contactsPage
 */

/** @type {AppState} */
export const state = {
  contacts: [],
  groups: [],
  templateVars: {},
  nameByPhone: new Map(),
  status: { connected: false, loggedIn: false },
  lang: 'pt-BR',
  activeBroadcastId: null,
  broadcastMode: 'group',
  contactsPage: 50,
};

/** @type {Map<string, Set<(s: AppState) => void>>} */
const subscribers = new Map();

/**
 * Subscribe to a single key. Returns an unsubscribe handle.
 *
 * @param {keyof AppState | '*'} key
 * @param {(s: AppState) => void} fn
 * @returns {() => void}
 */
export function on(key, fn) {
  let set = subscribers.get(key);
  if (!set) {
    set = new Set();
    subscribers.set(key, set);
  }
  set.add(fn);
  return () => set.delete(fn);
}

/**
 * Merge `patch` into state and notify subscribers for each touched key
 * plus the wildcard '*' channel. Synchronous — no batching microtask, which
 * keeps the call order predictable when a save triggers a re-render.
 *
 * @param {Partial<AppState>} patch
 */
export function set(patch) {
  const touched = [];
  for (const k of Object.keys(patch)) {
    if (state[k] !== patch[k]) {
      state[k] = patch[k];
      touched.push(k);
    }
  }
  if (!touched.length) return;
  for (const k of touched) notify(k);
  notify('*');
}

function notify(key) {
  const set = subscribers.get(key);
  if (!set) return;
  for (const fn of set) {
    try { fn(state); } catch (e) { console.error('[state] subscriber threw', e); }
  }
}

/** Rebuild the phone→name index. Call after replacing `contacts`. */
export function reindexContacts() {
  state.nameByPhone = new Map(state.contacts.map((c) => [c.phone, c.name]));
}

/**
 * Replace `contacts` and its phone→name index in a SINGLE set(), so that
 * `contacts` subscribers already observe a fresh `nameByPhone` when they run.
 * The old pattern (set({contacts}) then reindexContacts()) notified
 * subscribers before the index was rebuilt, leaving recipient badges reading a
 * stale map until the next unrelated render.
 *
 * @param {Contact[]} contacts
 * @param {Partial<AppState>} [extra]
 */
export function setContacts(contacts, extra = {}) {
  set({
    contacts,
    nameByPhone: new Map(contacts.map((c) => [c.phone, c.name])),
    ...extra,
  });
}
