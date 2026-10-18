import { test, expect } from '@playwright/test';
// Browser contract only: route-intercepted safe DTOs and local fake writes, no provider.
const intent = {id:11,broadcastId:1,messageRowId:101,campaignStatus:'uncertain',status:'uncertain',phone:'5511999999999',contactName:'PRIVATE <img src=x onerror=alert(1)>',attempts:1,messageId:'STORED-ID'};
const campaign = {id:1,groupName:'Recovered',total:8,ok:1,fail:1,status:'interrupted',pending:2,uncertain:1,cancelled:1,unresolved:2};
async function boot(page, options={}) {
  let rows = options.rows || [intent];
  let campaigns = [campaign];
  const posts = [];
  const reads = [];
  await page.route('**/api/delivery/**', async route => {
    const request=route.request(), path=new URL(request.url()).pathname;
    if(request.method()==='GET') {
      reads.push(path);
      await route.fulfill({json:rows});return;
    }
    posts.push({path,body:request.postDataJSON()});
    if(options.conflict) {await route.fulfill({status:409,json:{error:'Missing payload <img src=x onerror=alert(1)>'}});return;}
    const body=request.postDataJSON();
    rows=body.action==='retry' ? rows.map(row=>row.id===11 ? {...row,status:'pending'} : row) : rows.filter(row=>row.id!==11);
    campaigns=campaigns.map(row=>({...row,status:body.action==='retry' ? 'active' : 'completed'}));
    await route.fulfill({json:{id:11,status:body.action==='retry' ? 'pending' : 'sent',messageId:body.messageId,duplicateRisk:body.action==='retry'}});
  });
  await page.route(/\/api\/broadcasts(?:\/|\?|$)/,async route=>{
    const path=new URL(route.request().url()).pathname;
    if(path==='/api/broadcasts') {await route.fulfill({json:{items:campaigns}});return;}
    if(path==='/api/broadcasts/1/cancel') {
      posts.push({path,body:route.request().postDataJSON()});campaigns=[{...campaign,status:'cancelled',pending:0,cancelled:3}];
      rows=rows.map(row=>({...row,campaignStatus:'cancelled'}));
      await route.fulfill({json:{broadcastId:1,cancelled:true}});return;
    }
    await route.fulfill({json:[{status:'uncertain',phone:'111',contactName:'Ambiguous'},{status:'pending',phone:'222'},{status:'inflight',phone:'333'},{status:'cancelled',phone:'444'}]});
  });
  page.on('request',request=>{if(request.method()==='POST' && ['/api/broadcast','/api/broadcast/cancel'].includes(new URL(request.url()).pathname)) posts.push({path:new URL(request.url()).pathname,body:request.postData()});});
  await page.goto('/');
  await page.fill('#authInput','synthetic-test-token');await page.click('#authSubmit');
  await expect(page.locator('#authBackdrop')).not.toHaveClass(/open/);
  await page.click('a[data-page="history"]');
  await expect(page.locator('[data-review-id="11"]')).toBeVisible();
  return {posts,reads};
}
const retry = page=>page.locator('[data-review-id="11"] [data-review-action="retry"]');
test('history shows the full recorded message on a narrow screen without executing HTML',async({page})=>{
  await boot(page);
  const message='Primeira linha\n'+'Mensagem longa '.repeat(20)+'\n<img src=x onerror=alert(1)> Final visível';
  await page.route(/\/api\/broadcasts\?/,route=>route.fulfill({json:{items:[{...campaign,message}]}}));
  await page.setViewportSize({width:390,height:844});
  await page.click('#historyRefreshBtn');
  await expect(page.locator('.history-message-preview')).toBeVisible();
  await page.click('[data-action="toggle-history"][data-id="1"]');
  const body=page.locator('#hd-1 .history-message-text');
  await expect(body).toBeVisible();
  expect(await body.textContent()).toBe(message);
  await expect(page.locator('#hd-1 img')).toHaveCount(0);
  expect(await body.evaluate(el=>getComputedStyle(el).whiteSpace)).toBe('pre-wrap');
  expect(await body.evaluate(el=>el.scrollWidth<=el.clientWidth)).toBe(true);
});
test('uncertain recipients cannot bypass durable review; dismissal and unchecked confirmation send nothing; acknowledgment posts exact resolve contract',async({page})=>{
  const {posts,reads}=await boot(page);
  await page.click('[data-action="toggle-history"][data-id="1"]');
  await expect(page.locator('#hd-1')).toContainText('Ambiguous');
  await expect(page.locator('[data-action="retry-failed"]')).toHaveCount(0);
  await expect(page.locator('#historyList')).toContainText(/Interrompida|Interrupted/);
  await expect(page.locator('#historyList')).toContainText('2');
  await expect(page.locator('#deliveryReview img')).toHaveCount(0);
  await retry(page).click();await expect(page.locator('#deliveryReviewSubmit')).toBeDisabled();
  await page.click('#deliveryReviewDismiss');expect(posts).toEqual([]);
  await retry(page).click();await page.keyboard.press('Escape');expect(posts).toEqual([]);
  await retry(page).click();await page.check('#deliveryDuplicateAck');await page.click('#deliveryReviewSubmit');
  await expect(retry(page)).toBeDisabled();
  await expect.poll(()=>posts).toEqual([{path:'/api/delivery/11/resolve',body:{action:'retry',acknowledgeDuplicateRisk:true}}]);
  await expect(page.locator('[data-review-id="11"]')).toContainText(/Pendente|Pending/);
  expect(reads.length).toBeGreaterThan(1);
});
test('cancelled and unresolved intents have no retry; recovered campaign cancellation is confirmed and targeted',async({page})=>{
  const {posts}=await boot(page,{rows:[intent,{...intent,id:12,campaignStatus:'cancelled'},{...intent,id:13,noPayload:true},{...intent,id:14,noMessageRowId:true},{...intent,id:15,messageRowId:0},{...intent,id:16,status:'pending'},{...intent,id:17,status:'inflight'}]});
  for(const id of [12,13,14,15,16,17]) await expect(page.locator(`[data-review-id="${id}"] [data-review-action="retry"]`)).toBeDisabled();
  await page.click('[data-action="cancel-campaign"][data-id="1"]');await page.click('#modalCancel');expect(posts).toEqual([]);
  await page.click('[data-action="cancel-campaign"][data-id="1"]');await page.click('#modalConfirm');
  await expect(retry(page)).toBeDisabled();
  expect(posts).toEqual([{path:'/api/broadcasts/1/cancel',body:{}}]);
  await expect(page.locator('[data-action="cancel-campaign"]')).toHaveCount(0);
});
test('confirm_sent uses a validated operator ID and explicit providerConfirmed, never the stored correlation ID',async({page})=>{
  const {posts}=await boot(page);
  await page.click('[data-review-id="11"] [data-review-action="confirm_sent"]');
  await expect(page.locator('#deliveryProviderMessageId')).toHaveValue('');
  await page.fill('#deliveryProviderMessageId','OPERATOR-ID');await expect(page.locator('#deliveryReviewSubmit')).toBeDisabled();
  await page.check('#deliveryProviderConfirmed');await page.fill('#deliveryProviderMessageId','has space');await expect(page.locator('#deliveryReviewSubmit')).toBeDisabled();
  await page.fill('#deliveryProviderMessageId','OPERATOR-ID');await page.click('#deliveryReviewSubmit');
  await expect(page.locator('[data-review-id="11"]')).toHaveCount(0);
  expect(posts).toEqual([{path:'/api/delivery/11/resolve',body:{action:'confirm_sent',providerConfirmed:true,messageId:'OPERATOR-ID'}}]);
});
test('unknown captured payload HTTP409 remains visible on refresh without false completion',async({page})=>{
  const {posts}=await boot(page,{conflict:true});
  await retry(page).click();await page.check('#deliveryDuplicateAck');await page.click('#deliveryReviewSubmit');
  await expect(page.locator('[data-review-id="11"] .delivery-review-error')).toContainText('HTTP 409');
  await page.click('#historyRefreshBtn');
  await expect(page.locator('[data-review-id="11"] .delivery-review-error')).toContainText('Missing payload <img');
  await expect(retry(page)).toBeDisabled();
  await expect(page.locator('[data-review-id="11"]')).toContainText(/Incerto|Uncertain/);
  await expect(page.locator('#deliveryReview img')).toHaveCount(0);
  expect(posts).toHaveLength(1);
});
test('auth cleanup closes the sensitive review dialog',async({page})=>{
  const {posts}=await boot(page);
  await page.click('[data-review-id="11"] [data-review-action="confirm_sent"]');await page.fill('#deliveryProviderMessageId','PRIVATE-OPERATOR-ID');
  await page.evaluate(()=>window.dispatchEvent(new Event('zapcast:session-cleared')));
  await expect(page.locator('#deliveryReviewModal')).toHaveCount(0);await expect(page.locator('#deliveryReview')).toBeEmpty();expect(posts).toEqual([]);
});
test('auth cleanup ignores a GET started in a valid session before cleanup',async({page})=>{
  await boot(page);
  let release;
  const hold=new Promise(resolve=>{release=resolve;});
  await page.route('**/api/delivery/review',async route=>{await hold;await route.fulfill({json:[intent]});});
  const loading=page.waitForRequest('**/api/delivery/review');await page.click('#historyRefreshBtn');await loading;
  await page.evaluate(()=>window.dispatchEvent(new Event('zapcast:session-cleared')));
  const response=page.waitForResponse('**/api/delivery/review');release();await (await response).finished();
  // Drain the response/body-read continuation before inspecting the still-mounted DOM.
  await page.evaluate(()=>new Promise(resolve=>setTimeout(resolve,0)));
  await expect(page.locator('#deliveryReview')).toBeEmpty();
  await expect(page.locator('body')).not.toContainText('PRIVATE');
  await page.click('#appLogoutBtn');await expect(page.locator('#sessionLocked')).toBeVisible();
  await expect(page.locator('#deliveryReviewModal')).toHaveCount(0);
});
