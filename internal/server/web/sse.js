// sse.js — single EventSource subscription for all real-time updates.
//
// EventSource already reconnects on its own with a sane backoff, so the only
// extra plumbing we add is:
//   1) tearing down the current source on auth changes,
//   2) re-syncing app state on each open (events sent during a gap are *not*
//      replayed by the backend).
//
// Consumers pass per-event handlers; the dispatcher walks `event.type` to
// avoid the giant `if/else` that lived in the old monolith.


/** @type {EventSource|null} */
let current = null;

// Manual-restart backoff: doubles per consecutive failure, resets on a
// successful open. EventSource's own retries stay in charge for transient
// blips; this only paces our close-and-recreate cycle.
const RECONNECT_BASE_MS = 1000;
const RECONNECT_MAX_MS = 30000;
let reconnectDelay = RECONNECT_BASE_MS;

/**
 * @typedef {Object} SSEHandlers
 * @property {() => void}                 [onOpen]
 * @property {(data: any) => void}        [onProgress]
 * @property {(data: any) => void}        [onDone]
 * @property {() => void}                 [onReceipt]
 * @property {() => void}                 [onDisconnected]
 */

/** @param {SSEHandlers} handlers */
export function startSSE(handlers) {
  stopSSE();

  // Same-origin EventSource sends the HttpOnly session cookie automatically.
  const es = new EventSource('/api/events');
  current = es;

  es.onopen = () => {
    reconnectDelay = RECONNECT_BASE_MS;
    try { handlers.onOpen?.(); } catch (e) { console.error('[sse] onOpen', e); }
  };

  es.onmessage = (ev) => {
    let evt;
    try { evt = JSON.parse(ev.data); } catch { return; }
    switch (evt.type) {
      case 'broadcast_progress':
        handlers.onProgress?.(evt.data); break;
      case 'broadcast_done':
        handlers.onDone?.(evt.data); break;
      case 'receipt':
        handlers.onReceipt?.(); break;
      case 'disconnected':
      case 'logged_out':
        handlers.onDisconnected?.(); break;
    }
  };

  es.onerror = () => {
    try { es.close(); } catch { /* ignore */ }
    if (current === es) {
      const delay = reconnectDelay;
      reconnectDelay = Math.min(reconnectDelay * 2, RECONNECT_MAX_MS);
      setTimeout(() => { if (current === es) { current = null; startSSE(handlers); } }, delay);
    }
  };
}

export function stopSSE() {
  if (!current) return;
  try { current.close(); } catch { /* ignore */ }
  current = null;
}
