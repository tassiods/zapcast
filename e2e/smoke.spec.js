// e2e/smoke.spec.js — first-pass coverage. We only assert structural
// things that won't regress with cosmetic CSS tweaks: the broadcast
// page renders, nav links route to other pages, console stays clean.

import { test, expect } from '@playwright/test';
test.beforeEach(async ({page}) => {
  await page.goto('/');
  await page.fill('#authInput','synthetic-test-token');
  await page.click('#authSubmit');
  await expect(page.locator('#authBackdrop')).not.toHaveClass(/open/);
  await expect(page.locator('#versionChip')).not.toHaveText('v…');
});

test('boots without console errors', async ({ page }) => {
  const errors = [];
  page.on('console', (msg) => {
    if (msg.type() === 'error' || msg.type() === 'warning') errors.push(msg.text());
  });

  await page.goto('/');
  await expect(page.getByRole('heading', { name: /Broadcast/i }).first()).toBeVisible();
  await expect(page.locator('#statBroadcasts')).toBeVisible();
  await expect(page.locator('#statSent')).toBeVisible();
  expect(errors, errors.join('\n')).toEqual([]);
});

test('hash router activates the contacts page', async ({ page }) => {
  await page.goto('/#/contacts');
  await expect(page.locator('#page-contacts')).toHaveClass(/active/);
  await expect(page.getByRole('heading', { name: /Contatos|Contacts/i }).first()).toBeVisible();
});

test('settings drawer opens and lists accent swatches', async ({ page }) => {
  await page.goto('/');
  await page.click('#settingsToggle');
  await expect(page.locator('#settingsBackdrop')).toHaveClass(/open/);
  await expect(page.locator('.swatch[data-accent="volt"]')).toBeVisible();
  await expect(page.locator('.swatch[data-accent="indigo"]')).toBeVisible();
});

test('translations endpoint replies 304 on a second GET', async ({ request }) => {
  const first = await request.get('/api/translations');
  expect(first.status()).toBe(200);
  const etag = first.headers()['etag'];
  expect(etag).toBeTruthy();

  const second = await request.get('/api/translations', { headers: { 'If-None-Match': etag } });
  expect(second.status()).toBe(304);
});

test('structured error envelope on missing resource', async ({ page }) => {
  const res = await page.context().request.delete('/api/contacts/zzz');
  expect(res.status()).toBe(404);
  const body = await res.json();
  expect(body.error?.code).toBe('err_contact_not_found');
  expect(typeof body.error?.message).toBe('string');
});
