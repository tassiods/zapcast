import {test,expect} from '@playwright/test';
// Only run against an explicitly started local prod Go binary with an empty DB,
// AUTH_TOKEN=synthetic-test-token. No pairing or WA RPCs are performed.
async function login(page) {
  await page.goto('/');
  await expect(page.locator('#authBackdrop')).toHaveClass(/open/);
  await page.fill('#authInput','synthetic-test-token');
  await page.click('#authSubmit');
  await expect(page.locator('#authBackdrop')).not.toHaveClass(/open/);
  await expect(page.locator('#versionChip')).not.toHaveText('v…');
}
test('real Go native cookie login + app logout, no bearer/query/storage or WA logout',async({page,context}) => {
  const urls=[];page.on('request',r=>urls.push(r.url()));
  const events = page.waitForResponse(r => new URL(r.url()).pathname === '/api/events', {timeout:60_000});
  await login(page);
  expect((await events).status()).toBe(200);
  const cookies=await context.cookies();
  const cookie=cookies.find(c=>c.name==='zapcast_session');
  expect(cookie?.httpOnly).toBe(true);expect(cookie?.sameSite).toBe('Strict');
  expect((await context.request.get('/api/status')).status()).toBe(200);
  await expect.poll(()=>page.evaluate(async()=>!!(await navigator.serviceWorker.getRegistration())?.active)).toBe(true);
  const manifest = await page.evaluate(async()=> (await fetch(document.querySelector('link[rel=manifest]').href)).json());
  for(const icon of manifest.icons) expect((await context.request.get(icon.src)).status(),icon.src).toBe(200);
  const cdp = await context.newCDPSession(page);
  expect((await cdp.send('Page.getInstallabilityErrors')).installabilityErrors).toEqual([]);
  expect(urls.some(url=>url.endsWith('/api/events'))).toBe(true);
  expect(urls.every(url=>!url.includes('token='))).toBe(true);
  expect(await page.evaluate(()=>JSON.stringify({...localStorage,...sessionStorage}))).not.toContain('synthetic-test-token');
  await page.fill('#broadcastMsg','PRIVATE NATIVE');
  await page.click('#appLogoutBtn');
  await expect(page.locator('#sessionLocked')).toBeVisible();
  await expect.poll(async()=> (await context.cookies()).some(c=>c.name==='zapcast_session')).toBe(false);
  expect((await context.request.get('/api/status')).status()).toBe(401);
  expect(urls.some(url=>new URL(url).pathname==='/api/auth/logout')).toBe(false);
});
test('real Go rejects expired browser cookie; private page cleared on focus',async({page,context}) => {
  await login(page);
  await page.fill('#broadcastMsg','PRIVATE EXPIRED NATIVE');
  const cookie=(await context.cookies()).find(c=>c.name==='zapcast_session');
  await context.addCookies([{...cookie,expires:1}]);
  expect((await context.request.get('/api/status')).status()).toBe(401);
  await page.evaluate(()=>window.dispatchEvent(new Event('focus')));
  await expect(page.locator('#sessionLocked')).toBeVisible();
  await expect(page.locator('#authBackdrop')).toHaveClass(/open/);
  expect(await page.evaluate(()=>document.body.textContent.includes('PRIVATE EXPIRED NATIVE'))).toBe(false);
});

test('real Go review blocks missing payload replay and confirms only the explicit operator assertion', async ({page,context}) => {
  const mutations = [];
  page.on('request', request => {
    if (request.method() === 'POST') mutations.push({path:new URL(request.url()).pathname, body:request.postData()});
  });
  await login(page);
  await page.goto('/#/history'); // Also verifies an existing cookie survives a page reload without another prompt.
  await expect(page.locator('#authBackdrop')).not.toHaveClass(/open/);
  const row = page.locator('[data-review-id="94173"]');
  await expect(row).toBeVisible();
  const review = await context.request.get('/api/delivery/review');
  expect(review.status()).toBe(200);
  const dto = (await review.json()).find(item => item.id === 94173);
  expect(dto.noPayload).toBe(true);
  expect(dto.messageRowId).toBe(94172);
  expect(dto).not.toHaveProperty('rendered');
  expect(dto).not.toHaveProperty('payload');
  await expect(row.locator('[data-review-action="retry"]')).toBeDisabled();
  await row.locator('[data-review-action="confirm_sent"]').click();
  await expect(page.locator('#deliveryReviewSubmit')).toBeDisabled();
  await page.fill('#deliveryProviderMessageId','synthetic-provider-confirmed');
  await page.locator('#deliveryProviderConfirmed').check();
  await expect(page.locator('#deliveryReviewSubmit')).toBeEnabled();
  const response = page.waitForResponse(value => new URL(value.url()).pathname === '/api/delivery/94173/resolve' && value.request().method() === 'POST');
  await page.locator('#deliveryReviewSubmit').click();
  expect((await response).status()).toBe(200);
  await expect(row).toHaveCount(0);
  const sent = mutations.find(value => value.path === '/api/delivery/94173/resolve');
  expect(JSON.parse(sent.body)).toEqual({action:'confirm_sent',providerConfirmed:true,messageId:'synthetic-provider-confirmed'});
  const history = await context.request.get('/api/broadcasts/94171');
  expect((await history.json())[0]).toMatchObject({status:'sent',messageId:'synthetic-provider-confirmed'});
  const campaigns = await context.request.get('/api/broadcasts?limit=500');
  expect((await campaigns.json()).items.find(item => item.id === 94171)).toMatchObject({status:'completed',uncertain:0,ok:1});
  expect(mutations.some(value => ['/api/broadcast','/api/auth/qr','/api/auth/logout'].includes(value.path))).toBe(false);
});
