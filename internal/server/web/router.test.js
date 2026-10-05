// router.test.js — exercises the hash-router contract: nav clicks fire,
// active class moves, hashchange listener picks up the new slug.

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { initRouter, navigate } from './router.js';

beforeEach(() => {
  // Reset location.hash + DOM between tests so cases don't leak.
  history.replaceState(null, '', '/');
  document.body.innerHTML = `
    <a class="nav-item" data-page="broadcast" href="#/broadcast"></a>
    <a class="nav-item" data-page="contacts"  href="#/contacts"></a>
    <a class="nav-item" data-page="groups"    href="#/groups"></a>
    <section class="page" id="page-broadcast"></section>
    <section class="page" id="page-contacts"></section>
    <section class="page" id="page-groups"></section>
    <aside class="sidebar"></aside>
    <div id="sidebarBackdrop"></div>
  `;
});

describe('initRouter', () => {
  it('activates the default page when the hash is empty', () => {
    initRouter(() => {});
    expect(document.querySelector('.nav-item.active')?.getAttribute('data-page')).toBe('broadcast');
    expect(document.querySelector('.page.active')?.id).toBe('page-broadcast');
  });

  it('honours the hash present at boot', () => {
    history.replaceState(null, '', '/#/contacts');
    initRouter(() => {});
    expect(document.querySelector('.nav-item.active')?.getAttribute('data-page')).toBe('contacts');
    expect(document.querySelector('.page.active')?.id).toBe('page-contacts');
  });

  it('fires onPageChange with the freshly activated slug', () => {
    const onPage = vi.fn();
    initRouter(onPage);
    expect(onPage).toHaveBeenCalledWith('broadcast');
  });

  it('falls back to the default page for unknown slugs', () => {
    history.replaceState(null, '', '/#/nope');
    initRouter(() => {});
    expect(document.querySelector('.page.active')?.id).toBe('page-broadcast');
  });
});

describe('navigate', () => {
  it('updates location.hash and re-activates the matching page', async () => {
    initRouter(() => {});
    navigate('groups');
    // hashchange fires async; flush microtasks before asserting.
    await new Promise((r) => setTimeout(r, 0));
    expect(location.hash).toBe('#/groups');
    expect(document.querySelector('.page.active')?.id).toBe('page-groups');
  });

  it('coerces unknown slugs to the default', async () => {
    initRouter(() => {});
    navigate('nope');
    await new Promise((r) => setTimeout(r, 0));
    expect(location.hash).toBe('#/broadcast');
  });
});
