// settings.test.js — covers the theme/accent/density persistence layer and
// the swatch/seg sync helpers. We never touch settings.initSettings()
// directly because it wires DOM listeners that other tests don't need.
//
// jsdom 29 under Node 26 doesn't expose localStorage by default, so we
// install a tiny in-memory shim on globalThis. settings.js only uses
// getItem/setItem/removeItem, so the shim is enough.

import { describe, it, expect, beforeEach, beforeAll } from 'vitest';
import { applyTheme, applyAccent, applyDensity, toggleTheme } from './settings.js';

beforeAll(() => {
  if (typeof globalThis.localStorage === 'undefined') {
    const store = new Map();
    const stub = {
      getItem: (k) => (store.has(k) ? store.get(k) : null),
      setItem: (k, v) => { store.set(k, String(v)); },
      removeItem: (k) => { store.delete(k); },
      clear: () => { store.clear(); },
      key: (i) => Array.from(store.keys())[i] || null,
      get length() { return store.size; },
    };
    Object.defineProperty(globalThis, 'localStorage', { value: stub, writable: false, configurable: true });
  }
});

beforeEach(() => {
  localStorage.clear();
  document.documentElement.removeAttribute('data-theme');
  document.documentElement.removeAttribute('data-accent');
  document.documentElement.removeAttribute('data-density');
  document.body.innerHTML = `
    <meta id="metaThemeColor" content="">
    <span id="themeIcon"></span>
    <button class="swatch" data-accent="volt"></button>
    <button class="swatch" data-accent="indigo"></button>
    <button class="swatch" data-accent="coral"></button>
    <button class="swatch" data-accent="cyan"></button>
    <div class="seg">
      <button data-density="compact"></button>
      <button data-density="regular"></button>
    </div>
  `;
});

describe('applyTheme', () => {
  it('mirrors mode onto data-theme + localStorage', () => {
    applyTheme('dark');
    expect(document.documentElement.dataset.theme).toBe('dark');
    expect(localStorage.getItem('zapcast_theme')).toBe('dark');
  });

  it('updates the meta theme-color to match the mode', () => {
    applyTheme('dark');
    expect(document.getElementById('metaThemeColor')?.getAttribute('content')).toBe('#1a1d28');
    applyTheme('light');
    expect(document.getElementById('metaThemeColor')?.getAttribute('content')).toBe('#fafaf7');
  });
});

describe('toggleTheme', () => {
  it('flips between light and dark', () => {
    applyTheme('light');
    toggleTheme();
    expect(document.documentElement.dataset.theme).toBe('dark');
    toggleTheme();
    expect(document.documentElement.dataset.theme).toBe('light');
  });
});

describe('applyAccent', () => {
  it('writes a known accent onto the root and persists it', () => {
    applyAccent('coral');
    expect(document.documentElement.dataset.accent).toBe('coral');
    expect(localStorage.getItem('zapcast_accent')).toBe('coral');
  });

  it('marks the matching swatch with aria-pressed=true', () => {
    applyAccent('cyan');
    const cyan = document.querySelector('.swatch[data-accent="cyan"]');
    const indigo = document.querySelector('.swatch[data-accent="indigo"]');
    expect(cyan?.getAttribute('aria-pressed')).toBe('true');
    expect(indigo?.getAttribute('aria-pressed')).toBe('false');
  });

  it('silently ignores unknown accents to avoid trashing the root', () => {
    applyAccent('volt');
    applyAccent('mystery');
    expect(document.documentElement.dataset.accent).toBe('volt');
  });
});

describe('applyDensity', () => {
  it('writes a known density onto the root and persists it', () => {
    applyDensity('compact');
    expect(document.documentElement.dataset.density).toBe('compact');
    expect(localStorage.getItem('zapcast_density')).toBe('compact');
  });

  it('marks the matching seg button with aria-pressed=true', () => {
    applyDensity('regular');
    const regular = document.querySelector('.seg [data-density="regular"]');
    const compact = document.querySelector('.seg [data-density="compact"]');
    expect(regular?.getAttribute('aria-pressed')).toBe('true');
    expect(compact?.getAttribute('aria-pressed')).toBe('false');
  });
});
