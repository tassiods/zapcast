import { test, expect } from '@playwright/test';
// Native UI contract against a local, synthetic cookie server; never production.
// Real Go cookie integration is separately runnable with E2E_NATIVE_URL.
test('app logout clears private page, cookies, drafts, own caches only, without WA logout', async ({ page, context }) => {
  const requests = [];
  page.on('request', r => requests.push(r.url()));
  await page.goto('/');
  await page.fill('#authInput', 'synthetic-test-token');
  await page.click('#authSubmit');
  await expect(page.locator('#authBackdrop')).not.toHaveClass(/open/);
  await expect(page.locator('#authInput')).toHaveValue('');
  await expect.poll(() => requests.some(x => x.endsWith('/api/events'))).toBe(true);
  expect(requests.every(x => !x.includes('token='))).toBe(true);
  expect((await context.cookies()).some(c => c.name === 'zapcast_session' && c.httpOnly)).toBe(true);
  await expect.poll(() => page.evaluate(async()=>!!(await navigator.serviceWorker.getRegistration())?.active)).toBe(true);
  const other = await context.newPage();
  await other.goto('/');
  await other.fill('#broadcastMsg', 'PRIVATE OTHER TAB');
  await page.fill('#broadcastMsg', 'PRIVATE DRAFT');
  await page.evaluate(async () => {
    localStorage.setItem('zapcast.draft.v1', 'PRIVATE OLD');
    await caches.open('other-app');
    await caches.open('zapcast-obsolete-private');
  });
  await page.click('#appLogoutBtn');
  await expect(page.locator('#sessionLocked')).toBeVisible();
  await expect.poll(async () => (await context.cookies()).some(c => c.name === 'zapcast_session')).toBe(false);
  expect(requests.some(x => new URL(x).pathname === '/api/logout')).toBe(false);
  expect(await page.evaluate(() => document.body.textContent.includes('PRIVATE DRAFT'))).toBe(false);
  expect(await page.evaluate(() => JSON.stringify({...localStorage, ...sessionStorage}))).not.toContain('PRIVATE');
  await expect(other.locator('#sessionLocked')).toBeVisible();
  expect(await page.evaluate(() => caches.keys())).toEqual(['other-app']);
  expect(await page.evaluate(async()=>await navigator.serviceWorker.getRegistration())).toBeUndefined();
  await other.close();
});

test('logout aborts a pending private fetch without reopening the login prompt', async({page,request,context}) => {
  await page.goto('/');await page.fill('#authInput','synthetic-test-token');await page.click('#authSubmit');
  await expect(page.locator('#versionChip')).not.toHaveText('v…');
  await request.get('/__fixture/hold-history?value=1');
  try {
    await Promise.all([page.waitForRequest(r=>new URL(r.url()).pathname === '/api/broadcasts'),page.click('a[data-page=history]')]);
    await page.click('#appLogoutBtn');
    await expect.poll(async()=> (await context.cookies()).some(c=>c.name === 'zapcast_session')).toBe(false);
    await expect(page.locator('#authBackdrop')).not.toHaveClass(/open/);
    await expect(page.locator('#sessionLocked')).toBeVisible();
  } finally {await request.get('/__fixture/hold-history?value=0');}
});

test('proxy SSO redirect clears private page and caches, never prompts for a native token', async({page,request}) => {
  await request.get('/__fixture/mode?value=proxy');
  try {
    await page.goto('/');
    await expect(page.locator('#versionChip')).not.toHaveText('v…');
    await page.fill('#broadcastMsg','PRIVATE PROXY');
    await request.get('/__fixture/expire');
    await page.evaluate(()=>window.dispatchEvent(new Event('focus')));
    await expect(page.locator('#sessionLocked')).toBeVisible();
    await expect(page.locator('#sessionLocked')).toHaveText(/SSO/);
    await expect(page.locator('#authBackdrop')).not.toHaveClass(/open/);
    await expect.poll(()=>page.evaluate(async()=>(await caches.keys()).filter(k=>k.startsWith('zapcast-')))).toEqual([]);
    expect(await page.evaluate(()=>JSON.stringify({...localStorage,...sessionStorage}))).not.toContain('PRIVATE PROXY');
  } finally {await request.get('/__fixture/mode?value=native');}
});

test('native login never forwards the token POST body to an SSO redirect',async({page,request}) => {
  const forwarded=[];
  page.on('request',r=>{if(new URL(r.url()).pathname === '/sso/login')forwarded.push(r.postData());});
  await request.get('/__fixture/login-redirect?value=1');
  try {
    await page.goto('/');await page.fill('#authInput','synthetic-test-token');await page.click('#authSubmit');
    await expect(page.locator('#sessionLocked')).toBeVisible();
    expect(forwarded.join('')).not.toContain('synthetic-test-token');
  } finally {await request.get('/__fixture/login-redirect?value=0');}
});

test('legacy token and draft copies are removed before the initial auth prompt',async({page}) => {
  await page.addInitScript(()=>{
    sessionStorage.setItem('zapcast_auth_token','PRIVATE LEGACY TOKEN');
    localStorage.setItem('zapcast.draft.v1','PRIVATE LEGACY DRAFT');
  });
  await page.goto('/');
  await expect(page.locator('#authBackdrop')).toHaveClass(/open/);
  expect(await page.evaluate(()=>JSON.stringify({...localStorage,...sessionStorage}))).not.toContain('PRIVATE LEGACY');
});

test('telemetry never logs or posts raw errors, query strings, fragments or login secrets', async ({page}) => {
  const output = [];
  page.on('console', msg => output.push(msg.text()));
  page.on('request', req => { if(req.url().includes('/api/client-error')) output.push(req.postData()); });
  await page.goto('/');
  await page.fill('#authInput', 'synthetic-test-token');
  await page.click('#authSubmit');
  await expect(page.locator('#authBackdrop')).not.toHaveClass(/open/);
  await page.evaluate(() => window.dispatchEvent(new ErrorEvent('error', {message:'PRIVATE-SECRET',filename:'http://localhost/app.js?token=PRIVATE-SECRET#PRIVATE-SECRET'})));
  await expect.poll(() => output.length).toBeGreaterThan(0);
  expect(output.join('\n')).not.toContain('PRIVATE-SECRET');
});

test('synthetic QR fetch expiration clears the app session without pairing', async ({page,request}) => {
  await page.goto('/');await page.fill('#authInput','synthetic-test-token');await page.click('#authSubmit');
  await expect(page.locator('#versionChip')).not.toHaveText('v…');
  await page.goto('/#/auth');await request.get('/__fixture/expire');
  await page.click('#authBtn');
  await expect(page.locator('#sessionLocked')).toBeVisible();
});

test('CSV export expiration takes the same fail-closed app session path', async ({page,request}) => {
  await page.goto('/');await page.fill('#authInput','synthetic-test-token');await page.click('#authSubmit');
  await expect(page.locator('#versionChip')).not.toHaveText('v…');
  await page.goto('/#/import');
  await request.get('/__fixture/expire');
  await page.click('#exportContactsBtn');
  await expect(page.locator('#sessionLocked')).toBeVisible();
});

test('auth expiration clears the private page before prompting again', async ({page,request}) => {
  await page.goto('/');
  await page.fill('#authInput', 'synthetic-test-token');
  await page.click('#authSubmit');
  await expect(page.locator('#authBackdrop')).not.toHaveClass(/open/);
  await page.fill('#broadcastMsg','PRIVATE EXPIRED');
  await request.get('/__fixture/expire');
  await page.evaluate(() => window.dispatchEvent(new Event('focus')));
  await expect(page.locator('#sessionLocked')).toBeVisible();
  expect(await page.evaluate(() => document.body.textContent.includes('PRIVATE EXPIRED'))).toBe(false);
});
