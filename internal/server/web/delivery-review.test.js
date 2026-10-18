import { it, expect, vi, afterEach } from 'vitest';
import { readFileSync } from 'node:fs';
import { beginSession } from './api.js';
import { loadHistory, initHistoryView } from './view-history.js';
/** Test fixture nodes intentionally include buttons, inputs and containers.
 * @param {string} selector
 * @returns {any}
 */
const dom = selector => document.querySelector(selector);
const html = readFileSync('internal/server/web/index.html','utf8');
const intent = {id:11,broadcastId:1,messageRowId:101,campaignStatus:'uncertain',status:'uncertain',phone:'123',contactName:'PRIVATE <img src=x onerror=alert(1)>',attempts:1};
const click = selector => dom(selector).dispatchEvent(new MouseEvent('click',{bubbles:true}));
function setup(review=[intent], handler=null) {
  document.body.innerHTML=html; beginSession();
  const fetchMock=vi.fn((path,opts) => {
    if (handler) { const result=handler(path,opts); if(result) return result; }
    return Promise.resolve(new Response(JSON.stringify(path==='/api/delivery/review' ? review : String(path).startsWith('/api/broadcasts?') ? {items:[{id:1,status:'uncertain',total:1,pending:0,cancelled:0,uncertain:1,unresolved:0}]} : {}),{headers:{'Content-Type':'application/json'}}));
  });
  vi.stubGlobal('fetch',fetchMock); initHistoryView(); return fetchMock;
}
const posts = mock => mock.mock.calls.filter(([,opts])=>opts.method==='POST');
afterEach(()=>{window.dispatchEvent(new Event('zapcast:session-cleared'));vi.unstubAllGlobals();});
it('loads bounded safe review DTOs on history, escapes text and blocks impossible retries',async()=>{
  const fetchMock=setup([intent,{...intent,id:12,campaignStatus:'cancelled'},{...intent,id:13,noPayload:true},{...intent,id:14,noMessageRowId:true},{...intent,id:15,messageRowId:0},{...intent,id:16,status:'inflight'},{...intent,id:17,status:'pending'}]);
  await loadHistory();
  expect(fetchMock.mock.calls.some(([p])=>p==='/api/delivery/review')).toBe(true);
  expect(dom('#deliveryReview').textContent).toContain('PRIVATE <img');
  expect(dom('#deliveryReview img')).toBeNull();
  expect(dom('[data-review-id="11"] [data-review-action="retry"]').disabled).toBe(false);
  for (const id of [12,13,14,15,16,17]) expect(dom(`[data-review-id="${id}"] [data-review-action="retry"]`).disabled).toBe(true);
});

it('review retry requires explicit duplicate-risk acknowledgment, dismiss sends nothing, and posts only the resolve contract',async()=>{
  let current = [{...intent}];
  const fetchMock=setup(current,(path,opts)=>{
    if(opts.method==='POST') { current=[{...intent,status:'pending'}]; return Promise.resolve(new Response(JSON.stringify({id:11,status:'pending',duplicateRisk:true}),{headers:{'Content-Type':'application/json'}})); }
    if(path==='/api/delivery/review') return Promise.resolve(new Response(JSON.stringify(current),{headers:{'Content-Type':'application/json'}}));
  });
  await loadHistory();
  click('[data-review-id="11"] [data-review-action="retry"]');
  expect(dom('#deliveryReviewModal')).not.toBeNull();
  expect(dom('#deliveryReviewSubmit').disabled).toBe(true);
  click('#deliveryReviewDismiss');
  await Promise.resolve(); expect(posts(fetchMock)).toHaveLength(0);
  click('[data-review-id="11"] [data-review-action="retry"]');
  dom('#deliveryDuplicateAck').checked=true;
  dom('#deliveryDuplicateAck').dispatchEvent(new Event('change',{bubbles:true}));
  click('#deliveryReviewSubmit');
  await vi.waitFor(()=>expect(posts(fetchMock)).toHaveLength(1));
  const [path,opts]=posts(fetchMock)[0];
  expect(path).toBe('/api/delivery/11/resolve');
  expect(JSON.parse(opts.body)).toEqual({action:'retry',acknowledgeDuplicateRisk:true});
  await vi.waitFor(()=>expect(dom('[data-review-id="11"] [data-review-action="retry"]').disabled).toBe(true));
  expect(fetchMock.mock.calls.filter(([p])=>p==='/api/delivery/review').length).toBeGreaterThan(1);
});
it('confirm_sent requires explicit provider confirmation and validated operator-supplied ID, never fills the stored ID',async()=>{
  const fetchMock=setup([{...intent,messageId:'STORED-ID'}]); await loadHistory();
  click('[data-review-id="11"] [data-review-action="confirm_sent"]');
  expect(dom('#deliveryProviderMessageId')).not.toBeNull();
  const id=dom('#deliveryProviderMessageId');
  expect(id.value).toBe('');
  const ack=dom('#deliveryProviderConfirmed');
  const submit=dom('#deliveryReviewSubmit');
  const input=value=>{id.value=value;id.dispatchEvent(new Event('input',{bubbles:true}));};
  input('OPERATOR-ID'); expect(submit.disabled).toBe(true);
  ack.checked=true;ack.dispatchEvent(new Event('change',{bubbles:true}));
  for(const value of ['', ' ', 'has space', 'bad\u0001ID', 'x'.repeat(257)]) {input(value);expect(submit.disabled).toBe(true);}
  input('OPERATOR-ID'); expect(submit.disabled).toBe(false);click('#deliveryReviewSubmit');
  await vi.waitFor(()=>expect(posts(fetchMock)).toHaveLength(1));
  expect(posts(fetchMock)[0][0]).toBe('/api/delivery/11/resolve');
  expect(JSON.parse(posts(fetchMock)[0][1].body)).toEqual({action:'confirm_sent',providerConfirmed:true,messageId:'OPERATOR-ID'});
});
it('recovered campaign cancellation requires confirmation and targets only that campaign',async()=>{
  const fetchMock=setup();await loadHistory();
  expect(dom('[data-action="cancel-campaign"][data-id="1"]')).not.toBeNull();
  click('[data-action="cancel-campaign"][data-id="1"]');
  expect(dom('#modalBackdrop').classList.contains('open')).toBe(true);
  click('#modalCancel');await Promise.resolve();expect(posts(fetchMock)).toHaveLength(0);
  click('[data-action="cancel-campaign"][data-id="1"]');click('#modalConfirm');
  await vi.waitFor(()=>expect(posts(fetchMock)).toHaveLength(1));
  expect(posts(fetchMock)[0][0]).toBe('/api/broadcasts/1/cancel');
  expect(JSON.parse(posts(fetchMock)[0][1].body)).toEqual({});
  expect(posts(fetchMock).some(([path])=>path==='/api/broadcast/cancel')).toBe(false);
});
it('HTTP409 remains visible through refresh, escapes the error and never claims completion',async()=>{
  const fetchMock=setup([intent],(_path,opts)=>opts.method==='POST' ? Promise.resolve(new Response(JSON.stringify({error:'Missing payload <img src=x onerror=alert(1)>'}),{status:409,headers:{'Content-Type':'application/json'}})) : null);
  await loadHistory();click('[data-review-action="retry"]');
  const ack=dom('#deliveryDuplicateAck');ack.checked=true;ack.dispatchEvent(new Event('change',{bubbles:true}));click('#deliveryReviewSubmit');
  await vi.waitFor(()=>expect(dom('.delivery-review-error').textContent).toContain('HTTP 409'));
  await loadHistory();
  expect(dom('.delivery-review-error').textContent).toContain('Missing payload <img');
  expect(dom('#deliveryReview img')).toBeNull();
  expect(dom('[data-review-id="11"] [data-review-action="retry"]').disabled).toBe(true);
  expect(dom('[data-review-id="11"]').textContent).toContain('Incerto');
  expect(dom('[data-review-id="11"]').textContent).not.toContain('Concluída');
  expect(posts(fetchMock)).toHaveLength(1);
});
it('session cleanup closes review, erases operator ID and cached conflicts, and makes old controls inert',async()=>{
  const fetchMock=setup();await loadHistory();click('[data-review-action="confirm_sent"]');
  const id=dom('#deliveryProviderMessageId');id.value='PRIVATE-OPERATOR-ID';
  const oldSubmit=dom('#deliveryReviewSubmit');
  window.dispatchEvent(new Event('zapcast:session-cleared'));
  expect(dom('#deliveryReviewModal')).toBeNull();
  expect(id.value).toBe('');expect(dom('#deliveryReview').textContent).toBe('');
  oldSubmit.dispatchEvent(new MouseEvent('click',{bubbles:true}));await Promise.resolve();expect(posts(fetchMock)).toHaveLength(0);
  await loadHistory();expect(dom('[data-review-action="retry"]').disabled).toBe(false);
  expect(dom('.delivery-review-error').textContent).toBe('');
});
it('session cleanup ignores late history and safe review body reads',async()=>{
  const pending=[];
  setup([intent],path=>String(path).includes('/api/broadcasts?') || path==='/api/delivery/review' ? new Promise(resolve=>pending.push({path,resolve})) : null);
  const loading=loadHistory();window.dispatchEvent(new Event('zapcast:session-cleared'));
  for(const request of pending) request.resolve(new Response(JSON.stringify(request.path==='/api/delivery/review' ? [intent] : {items:[{id:1,groupName:'PRIVATE LATE HISTORY'}]}),{headers:{'Content-Type':'application/json'}}));
  await loading;
  expect(dom('#deliveryReview').textContent).not.toContain('PRIVATE');
  expect(dom('#historyList').textContent).not.toContain('PRIVATE');
});
it('refresh while resolution is inflight disables both actions and retains a later conflict on the current row',async()=>{
  /** @type {(response: Response) => void} */
  let reply = () => { throw new Error('No pending POST'); };
  const fetchMock=setup([intent],(_path,opts)=>opts.method==='POST' ? new Promise(resolve=>{reply=resolve;}) : null);
  await loadHistory();click('[data-review-action="retry"]');
  const ack=dom('#deliveryDuplicateAck');ack.checked=true;ack.dispatchEvent(new Event('change',{bubbles:true}));click('#deliveryReviewSubmit');
  await vi.waitFor(()=>expect(posts(fetchMock)).toHaveLength(1));await loadHistory();
  expect(dom('[data-review-action="confirm_sent"]').disabled).toBe(true);
  reply(new Response(JSON.stringify({error:'late conflict'}),{status:409,headers:{'Content-Type':'application/json'}}));
  await vi.waitFor(()=>expect(dom('.delivery-review-error').textContent).toContain('late conflict'));
});
it('failed authoritative readback stays visible after accepted resolution without claiming completion',async()=>{
  let accepted=false;
  setup([intent],(path,opts)=>{
    if(opts.method==='POST') {accepted=true;return Promise.resolve(new Response(JSON.stringify({status:'pending'}),{headers:{'Content-Type':'application/json'}}));}
    if(accepted && path==='/api/delivery/review') return Promise.resolve(new Response(JSON.stringify({error:'readback unavailable'}),{status:500,headers:{'Content-Type':'application/json'}}));
    return null;
  });
  await loadHistory();click('[data-review-action="retry"]');
  const ack=dom('#deliveryDuplicateAck');ack.checked=true;ack.dispatchEvent(new Event('change',{bubbles:true}));click('#deliveryReviewSubmit');
  await vi.waitFor(()=>expect(dom('#deliveryReview').textContent).toContain('readback unavailable'));
  await new Promise(resolve=>setTimeout(resolve,0));
  expect(dom('#deliveryReview').textContent).toContain('readback unavailable');
  expect(dom('[data-review-action="retry"]')).toBeNull();
  expect(dom('#deliveryReview').textContent).not.toContain('Concluída');
});
it('late POST success after session cleanup causes no refresh, completion label, or private repopulation',async()=>{
  /** @type {(response: Response) => void} */
  let reply = () => { throw new Error('No pending POST'); };
  const fetchMock=setup([intent],(_path,opts)=>opts.method==='POST' ? new Promise(resolve=>{reply=resolve;}) : null);
  await loadHistory();click('[data-review-action="retry"]');
  const ack=dom('#deliveryDuplicateAck');ack.checked=true;ack.dispatchEvent(new Event('change',{bubbles:true}));click('#deliveryReviewSubmit');
  await vi.waitFor(()=>expect(posts(fetchMock)).toHaveLength(1));
  const count=fetchMock.mock.calls.length;
  window.dispatchEvent(new Event('zapcast:session-cleared'));
  reply(new Response(JSON.stringify({status:'pending',duplicateRisk:true}),{headers:{'Content-Type':'application/json'}}));
  await new Promise(resolve=>setTimeout(resolve,0));
  expect(fetchMock.mock.calls).toHaveLength(count);
  expect(dom('#deliveryReview').textContent).toBe('');
  expect(document.body.textContent).not.toContain('PRIVATE');
});
