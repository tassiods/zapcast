// settings.js — theme / accent / density preferences and the sidebar drawer.
//
// Pre-paint application of theme/accent/density is done by the inline script
// in index.html (see the IIFE near <head>). This module owns the runtime
// toggles and the persistence layer.

const KEY_THEME = 'zapcast_theme';
const KEY_ACCENT = 'zapcast_accent';
const KEY_DENSITY = 'zapcast_density';

const ACCENTS = ['volt', 'indigo', 'coral', 'cyan'];
const DENSITIES = ['compact', 'regular'];

/** @param {'light'|'dark'} mode */
export function applyTheme(mode) {
  document.documentElement.dataset.theme = mode;
  const meta = document.getElementById('metaThemeColor');
  if (meta) meta.setAttribute('content', mode === 'dark' ? '#1a1d28' : '#fafaf7');
  const icon = document.getElementById('themeIcon');
  if (icon) icon.innerHTML = mode === 'dark' ? '\u{1F319}' : '\u{2600}️';
  try { localStorage.setItem(KEY_THEME, mode); } catch { /* private mode */ }
}

export function toggleTheme() {
  const next = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
  applyTheme(next);
}

/** @param {string} accent */
export function applyAccent(accent) {
  if (!ACCENTS.includes(accent)) return;
  document.documentElement.dataset.accent = accent;
  try { localStorage.setItem(KEY_ACCENT, accent); } catch { /* private mode */ }
  syncAccentSwatches();
}

/** @param {string} density */
export function applyDensity(density) {
  if (!DENSITIES.includes(density)) return;
  document.documentElement.dataset.density = density;
  try { localStorage.setItem(KEY_DENSITY, density); } catch { /* private mode */ }
  syncDensityButtons();
}

export function initSettings() {
  // Mirror the icon to whatever the pre-paint script restored.
  applyTheme(/** @type {'light'|'dark'} */ (document.documentElement.dataset.theme || 'light'));
  syncAccentSwatches();
  syncDensityButtons();

  document.getElementById('themeToggle')?.addEventListener('click', toggleTheme);

  // Drawer open/close
  const backdrop = document.getElementById('settingsBackdrop');
  document.getElementById('settingsToggle')?.addEventListener('click', () => {
    backdrop?.classList.add('open');
  });
  document.getElementById('settingsClose')?.addEventListener('click', () => {
    backdrop?.classList.remove('open');
  });
  backdrop?.addEventListener('mousedown', (e) => {
    if (e.target === backdrop) backdrop.classList.remove('open');
  });

  // Accent + density delegated handlers — both groups live inside the drawer.
  // NOTE: match the buttons themselves (.swatch / .seg), never a bare
  // [data-accent]/[data-density] ancestor lookup: <html> carries
  // data-accent/data-density/data-theme, so an unscoped closest() always
  // resolves to the root value and swallows the click (density applies the
  // current accent and returns — the drawer looks dead).
  document.getElementById('settingsDialog')?.addEventListener('click', (e) => {
    const target = /** @type {HTMLElement} */ (e.target);
    const accentBtn = target.closest('.swatch[data-accent]');
    if (accentBtn) { applyAccent(/** @type {string} */ (accentBtn.getAttribute('data-accent'))); return; }
    const densityBtn = target.closest('.seg [data-density]');
    if (densityBtn) { applyDensity(/** @type {string} */ (densityBtn.getAttribute('data-density'))); }
  });

  // Sidebar (mobile)
  document.getElementById('sidebarToggle')?.addEventListener('click', () => toggleSidebar());
  document.getElementById('sidebarBackdrop')?.addEventListener('click', () => toggleSidebar(false));
}

export function toggleSidebar(force) {
  const sidebar = document.querySelector('.sidebar');
  const backdrop = document.getElementById('sidebarBackdrop');
  if (!sidebar || !backdrop) return;
  const open = typeof force === 'boolean' ? force : !sidebar.classList.contains('open');
  sidebar.classList.toggle('open', open);
  backdrop.classList.toggle('open', open);
}

function syncAccentSwatches() {
  const current = document.documentElement.dataset.accent || 'volt';
  for (const btn of document.querySelectorAll('.swatch[data-accent]')) {
    btn.setAttribute('aria-pressed', String(btn.getAttribute('data-accent') === current));
  }
}

function syncDensityButtons() {
  const current = document.documentElement.dataset.density || 'regular';
  for (const btn of document.querySelectorAll('.seg [data-density]')) {
    btn.setAttribute('aria-pressed', String(btn.getAttribute('data-density') === current));
  }
}
