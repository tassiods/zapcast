import { it, expect, vi, afterEach } from 'vitest';
import { readFileSync } from 'node:fs';
import {loadHistory, applyHistoryFilter} from './view-history.js';
import {initImportView} from './view-import.js';
import {beginSession} from './api.js';
import { restoreBroadcastDraft, initBroadcastView } from './view-broadcast.js';
const html = readFileSync('internal/server/web/index.html', 'utf8');
afterEach(() => { vi.useRealTimers(); localStorage.clear(); vi.unstubAllGlobals(); });
it('session cleanup discards pending CSV and ignores a late FileReader', () => {
  document.body.innerHTML = html; beginSession();
  let reader = { onload: (_event) => {} };
  vi.stubGlobal('FileReader', class {
    constructor() { reader = this; }
    onload = (_event) => {};
    readAsText(_file) {}
  });
  const fetchMock = vi.fn(() => Promise.resolve(new Response('{}',{headers:{'Content-Type':'application/json'}})));
  vi.stubGlobal('fetch', fetchMock);
  initImportView();
  const input = document.querySelector('#importFile');
  Object.defineProperty(input, 'files', {value:[new File(['name,phone\nPRIVATE CSV,123'],'test.csv')]});
  input.dispatchEvent(new Event('change'));
  reader.onload({target:{result:'name,phone\nPRIVATE CSV,123'}});
  expect(document.querySelector('#importPreviewTable').textContent).toContain('PRIVATE CSV');
  window.dispatchEvent(new Event('zapcast:session-cleared'));
  document.querySelector('#importBtn').dispatchEvent(new Event('click'));
  expect(fetchMock).not.toHaveBeenCalled();
  input.dispatchEvent(new Event('change'));
  window.dispatchEvent(new Event('zapcast:session-cleared'));
  document.querySelector('#importPreviewTable').textContent = '';
  reader.onload({target:{result:'name,phone\nPRIVATE LATE CSV,123'}});
  expect(document.querySelector('#importPreviewTable').textContent).not.toContain('PRIVATE');
});

it('session cleanup erases the history module cache, not just its rendered DOM', async () => {
  document.body.innerHTML = html; beginSession();
  vi.stubGlobal('fetch', vi.fn(path => Promise.resolve(new Response(JSON.stringify(path.includes('broadcasts') ? [{id:1,message:'PRIVATE HISTORY',groupName:'PRIVATE GROUP',sentAt:'2026-01-01'}] : {}),{headers:{'Content-Type':'application/json'}}))));
  await loadHistory();
  expect(document.querySelector('#historyList').textContent).toContain('PRIVATE HISTORY');
  window.dispatchEvent(new Event('zapcast:session-cleared'));
  applyHistoryFilter();
  expect(document.querySelector('#historyList').textContent).not.toContain('PRIVATE');
});
it('discards legacy cross-operator drafts and never autosaves to disk', () => {
  document.body.innerHTML = html;
  localStorage.setItem('zapcast.draft.v1', JSON.stringify({message: 'PRIVATE LEGACY'}));
  restoreBroadcastDraft();
  expect(/** @type {HTMLTextAreaElement} */ (document.querySelector('#broadcastMsg')).value).toBe('');
  expect(localStorage.getItem('zapcast.draft.v1')).toBeNull();
  vi.useFakeTimers();
  initBroadcastView();
  /** @type {HTMLTextAreaElement} */ (document.querySelector('#broadcastMsg')).value = 'PRIVATE CURRENT';
  document.querySelector('#broadcastMsg').dispatchEvent(new Event('input'));
  vi.advanceTimersByTime(1000);
  expect(JSON.stringify({...localStorage})).not.toContain('PRIVATE');
});
