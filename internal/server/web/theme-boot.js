// theme-boot.js — runs before any other stylesheet resolves so we avoid
// the light→dark flash on reload. Loaded synchronously (no `defer`,
// no `type="module"`) from index.html. Mirrors the logic in settings.js
// but stays vanilla so the file can be cached aggressively and reused
// across SPA navigations.

(function () {
  try {
    var t = localStorage.getItem('zapcast_theme');
    var a = localStorage.getItem('zapcast_accent');
    var d = localStorage.getItem('zapcast_density');
    var root = document.documentElement;
    if (!t) {
      var prefersDark = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches;
      t = prefersDark ? 'dark' : 'light';
    }
    root.dataset.theme = t;
    if (a) root.dataset.accent = a;
    if (d) root.dataset.density = d;
    var meta = document.getElementById('metaThemeColor');
    if (meta) meta.setAttribute('content', t === 'dark' ? '#1a1d28' : '#fafaf7');
  } catch (_) { /* localStorage may be blocked — defaults stay */ }
})();
