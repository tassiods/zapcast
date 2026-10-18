import {test,expect} from '@playwright/test';
import { builds } from './builds.mjs';

for(const [from,to] of [[0,1],[1,2]]) test(`real production update ${from+1} → ${to+1} keeps immutable modules until explicit update`,async({page,request,context}) => {
  await request.get('/__fixture/build?dir='+encodeURIComponent(builds[from]));
  await page.goto('/');
  await page.fill('#authInput','synthetic-test-token');
  await page.click('#authSubmit');
  await expect.poll(() => page.evaluate(async()=>!!(await navigator.serviceWorker.getRegistration())?.active)).toBe(true);
  await page.reload();
  const before = await page.evaluate(async()=> {
    const src=document.querySelector('script[type=module]').src;
    return {src,body:await (await fetch(src)).text(),release:document.querySelector('meta[name=e2e-release]').content, keys:await caches.keys()};
  });
  expect(before.release).toBe(String(from+1));
  const other = await context.newPage();
  await other.goto('/');
  await request.get('/__fixture/build?dir='+encodeURIComponent(builds[to]));
  await page.evaluate(async()=>{await (await navigator.serviceWorker.getRegistration()).update();});
  await expect(page.locator('#swUpdateBtn')).toBeVisible();
  expect(await page.evaluate(()=>document.querySelector('meta[name=e2e-release]').content)).toBe(String(from+1));
  const oldBefore=await page.evaluate(async src=>(await fetch(src)).text(),before.src);
  expect(oldBefore).toBe(before.body);
  await Promise.all([page.waitForEvent('domcontentloaded'), page.click('#swUpdateBtn')]);
  await expect(page.locator('meta[name=e2e-release]')).toHaveAttribute('content',String(to+1));
  const after = await page.evaluate(async()=>({src:document.querySelector('script[type=module]').src,keys:await caches.keys()}));
  expect(after.src).not.toBe(before.src);
  expect(after.keys.length).toBe(2);
  // The still-open old tab must never receive the new release at an old URL.
  expect(await other.evaluate(async src=>(await fetch(src)).text(),before.src)).toBe(before.body);
  await other.close();
});
