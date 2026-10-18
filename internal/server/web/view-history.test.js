import { it, expect, vi, afterEach } from 'vitest';
import { readFileSync } from 'node:fs';
import { beginSession } from './api.js';
/** Test fixture nodes intentionally include buttons, inputs and containers.
 * @param {string} selector
 * @returns {any}
 */
const dom = selector => document.querySelector(selector);
const html = readFileSync('internal/server/web/index.html', 'utf8');
afterEach(() => { window.dispatchEvent(new Event('zapcast:session-cleared')); vi.unstubAllGlobals(); });
const campaign = {id:1,status:'interrupted',groupName:'Recovered',total:8,ok:1,fail:1,pending:2,inflight:1,uncertain:1,cancelled:1,unresolved:2};
/** @param {any[]} [items] @param {any[]} [review] */
function setup(items = [campaign], review = []) {
  document.body.innerHTML = html; beginSession();
  vi.stubGlobal('fetch', vi.fn(path => Promise.resolve(new Response(JSON.stringify(
    path === '/api/delivery/review' ? review : path === '/api/broadcasts/1' ? [{status:'uncertain',phone:'123',contactName:'Private'}, {status:'x\" onclick=\"alert(1)',phone:'456'}] :
      String(path).startsWith('/api/broadcasts?') ? {items} : {}
  ), {headers:{'Content-Type':'application/json'}}))));
  history.initHistoryView();
}

it('history displays explicit durable counts and escaped whitelisted recipient states, never retries uncertainty', async () => {
  setup(); await history.loadHistory();
  expect(dom('#historyList').textContent).toMatch(/Interrompida/);
  for (const text of ['Pendentes: 2','Em envio: 1','Incerto: 1','Cancelados: 1','Não resolvidos: 2']) expect(dom('#historyList').textContent).toContain(text);
  dom('[data-action="toggle-history"]').click();
  await vi.waitFor(() => expect(dom('#hd-1').textContent).toContain('Private'));
  expect(dom('[data-action="retry-failed"]')).toBeNull();
  expect(dom('[onclick]')).toBeNull();
  expect([...document.querySelectorAll('#hd-1 .badge-status')].map(el => el.className)).toEqual(['badge-status badge-uncertain','badge-status badge-unknown']);
});
import * as history from './view-history.js';

it('expanded history displays the complete recorded campaign message as escaped text', async () => {
  const message = 'Primeira linha\n' + 'Conteúdo longo '.repeat(12) + '\n<img src=x onerror=alert(1)> fim da mensagem';
  setup([{...campaign, message}]); await history.loadHistory();
  dom('[data-action="toggle-history"]').click();
  await vi.waitFor(() => expect(dom('#hd-1 .history-message-text')?.textContent).toBe(message));
  expect(dom('#hd-1 img')).toBeNull();
  window.dispatchEvent(new Event('zapcast:session-cleared'));
  expect(dom('#historyList').textContent).not.toContain('fim da mensagem');
});

it('retryFailed selects only definitive failed rows in a complete completed campaign', () => {
  expect(history.safeFailedPhones).toBeTypeOf('function');
  const rows = ['failed','uncertain','pending','inflight','cancelled','sent','unknown'].map((status, i) => ({status,phone:String(i+1)}));
  const campaign = {status:'completed', total:7, pending:0, uncertain:0, cancelled:0, unresolved:0};
  expect(history.safeFailedPhones(campaign, rows)).toEqual(['1']);
  for (const status of ['cancelled','interrupted','legacy','active','uncertain',undefined]) {
    expect(history.safeFailedPhones({...campaign,status}, rows)).toEqual([]);
  }
  for (const key of ['pending','uncertain','cancelled','unresolved']) {
    expect(history.safeFailedPhones({...campaign,[key]:1}, rows)).toEqual([]);
  }
  expect(history.safeFailedPhones({...campaign,total:8}, rows)).toEqual([]);
  expect(history.safeFailedPhones({...campaign,unresolved:undefined}, rows)).toEqual([]);
});
it('legacy unresolved and incomplete campaigns never appear in the clean success filter',async()=>{
  setup([{...campaign,id:1,groupName:'PRIVATE LEGACY',status:'legacy',fail:0,unresolved:2},{...campaign,id:2,groupName:'PRIVATE INTERRUPTED',fail:0},{id:3,groupName:'CLEAN',status:'completed',total:1,ok:1,fail:0,pending:0,uncertain:0,cancelled:0,unresolved:0}]);
  await history.loadHistory();
  dom('#historyStatusFilter').value='clean';history.applyHistoryFilter();
  expect(dom('#historyList').textContent).toContain('CLEAN');
  expect(dom('#historyList').textContent).not.toContain('PRIVATE');
});
it('late recipient and stats responses cannot repopulate private data after auth cleanup, even when main is removed',async()=>{
  /** @type {(response: Response) => void} */
  let detailReply = () => { throw new Error('No pending detail request'); };
  /** @type {(response: Response) => void} */
  let statsReply = () => { throw new Error('No pending stats request'); };
  setup();await history.loadHistory();
  vi.stubGlobal('fetch',vi.fn(path=>new Promise(resolve=>{if(path==='/api/stats')statsReply=resolve;else detailReply=resolve;})));
  dom('[data-action="toggle-history"]').dispatchEvent(new MouseEvent('click',{bubbles:true}));
  const stats=history.refreshBroadcastStats();window.dispatchEvent(new Event('zapcast:session-cleared'));
  detailReply(new Response(JSON.stringify([{status:'uncertain',phone:'PRIVATE',contactName:'PRIVATE DETAIL'}]),{headers:{'Content-Type':'application/json'}}));
  statsReply(new Response(JSON.stringify({broadcasts:12345}),{headers:{'Content-Type':'application/json'}}));
  await stats;await Promise.resolve();
  expect(dom('#statsCards').textContent).toBe('');
  expect(dom('#historyList').textContent).not.toContain('PRIVATE');
  dom('main').replaceChildren();
  expect(()=>history.applyHistoryFilter()).not.toThrow();
});
