import { test, expect } from '@playwright/test';

test('offline shell contains no operator data and protected/SSO/debug URLs are never cached',async ({page,context}) => {
  await page.goto('/');
  await page.fill('#authInput','synthetic-test-token');await page.click('#authSubmit');
  await expect.poll(() => page.evaluate(async()=>!!(await navigator.serviceWorker.getRegistration())?.active)).toBe(true);
  await page.reload();
  await page.fill('#broadcastMsg','PRIVATE OFFLINE DRAFT');
  await page.evaluate(async()=> {
    for(const url of ['/debug/vars','/sso/login','/api/status','/api/contacts']) await fetch(url);
  });
  const cacheBodies = await page.evaluate(async()=>{
    const bodies=[];
    for(const name of await caches.keys()) {
      const cache=await caches.open(name);
      for(const req of await cache.keys()) {
        bodies.push(req.url);
        if(/(?:\.js|\.css|\.webmanifest)$/.test(req.url) || new URL(req.url).pathname === '/') bodies.push(await (await cache.match(req)).text());
      }
    }
    return bodies.join('\n');
  });
  expect(cacheBodies).not.toContain('PRIVATE OFFLINE DRAFT');
  expect(cacheBodies).not.toContain('PRIVATE DEBUG');
  expect(cacheBodies).not.toContain('PRIVATE SSO');
  expect(cacheBodies).not.toMatch(/http:\/\/[^\s]+\/(?:api|debug|sso)\//);
  await context.setOffline(true);
  await page.reload();
  await expect(page.locator('h1')).toHaveText(/ZapCast/);
  await expect(page.locator('#sessionLocked')).toBeVisible();
  expect(await page.evaluate(()=>document.body.textContent.includes('PRIVATE OFFLINE DRAFT'))).toBe(false);
  expect(await page.evaluate(async()=>{try{await fetch('/debug/vars');return true;}catch{return false;}})).toBe(false);
});

for(const policy of ['redirect','no-store','mime','tamper']) test(`worker rejects ${policy} shell without leaving partial caches`,async ({page,request}) => {
  await request.get(`/__fixture/policy?value=${policy}`);
  try {
    await page.goto('/sso/login');
    const result = await page.evaluate(async () => {
      const reg = await navigator.serviceWorker.register('/sw.js');
      const worker = reg.installing || reg.waiting || reg.active;
      if(!worker) return 'missing';
      return new Promise(resolve => {
        const check = () => {if(['activated','redundant'].includes(worker.state))resolve(worker.state);};
        worker.addEventListener('statechange',check);check();
      });
    });
    expect(result).toBe('redundant');
    expect(await page.evaluate(async () => (await caches.keys()).filter(k => k.startsWith('zapcast-')))).toEqual([]);
  } finally { await request.get('/__fixture/policy?value='); }
});

test('production worker installs with emitted assets and every manifest icon exists', async ({page,context}) => {
  await page.goto('/');
  await page.fill('#authInput','synthetic-test-token');
  await page.click('#authSubmit');
  await expect.poll(() => page.evaluate(async () => !!(await navigator.serviceWorker.getRegistration())?.active)).toBe(true);
  const manifest = await page.evaluate(async () => {
    const href = document.querySelector('link[rel=manifest]').href;
    return (await fetch(href)).json();
  });
  for(const icon of manifest.icons) {
    const response = await page.request.get(icon.src);
    expect(response.status(),icon.src).toBe(200);
    expect(response.headers()['content-type']).toMatch(/image\//);
  }
  await page.reload();
  expect(await page.evaluate(() => !!navigator.serviceWorker.controller)).toBe(true);
  const keys = await page.evaluate(async () => {
    const rows = [];
    for(const name of await caches.keys())for(const req of await (await caches.open(name)).keys())rows.push(new URL(req.url).pathname);
    return rows;
  });
  expect(keys).toContain('/');
  expect(keys.some(p => /^\/assets\/.*\.js$/.test(p))).toBe(true);
  expect(keys.some(p => /api|debug|sso|\.map$|\.test\./.test(p))).toBe(false);
  const cdp = await context.newCDPSession(page);
  const result = await cdp.send('Page.getInstallabilityErrors');
  expect(result.installabilityErrors).toEqual([]);
});
