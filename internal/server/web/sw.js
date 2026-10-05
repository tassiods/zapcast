// ZapCast service worker.
//
// The cache key is derived from the backend ETag (which itself is a hash of
// every embedded asset). When a new binary ships the hash flips, the next
// install opens a fresh cache and the activate handler purges everything
// older. No manual VERSION bump to forget on deploy.

const FALLBACK_VERSION = 'zapcast-bootstrap';
const APP_SHELL = [
  '/',
  '/styles.css',
  '/theme-boot.js',
  '/app.js',
  '/state.js',
  '/api.js',
  '/ui.js',
  '/i18n.js',
  '/router.js',
  '/settings.js',
  '/sse.js',
  '/picker.js',
  '/view-broadcast.js',
  '/view-contacts.js',
  '/view-groups.js',
  '/view-import.js',
  '/view-templates.js',
  '/view-history.js',
  '/view-auth.js',
  '/manifest.webmanifest',
  '/icon.svg',
  '/icon-maskable.svg',
  '/favicon.svg',
  '/favicon.ico',
  '/apple-touch-icon.png',
  '/favicon-96x96.png',
  '/web-app-manifest-192x192.png',
  '/web-app-manifest-512x512.png',
];

self.addEventListener('install', (event) => {
  event.waitUntil((async () => {
    const version = await readVersion();
    const cache = await caches.open(version);
    await cache.addAll(APP_SHELL);
    self.skipWaiting();
  })());
});

self.addEventListener('activate', (event) => {
  event.waitUntil((async () => {
    const version = await readVersion();
    const keys = await caches.keys();
    const stale = keys.filter((k) => k !== version);
    await Promise.all(stale.map((k) => caches.delete(k)));
    await self.clients.claim();
    // A purge means a new binary shipped while tabs were open — tell them
    // so the UI can offer a reload instead of running the old shell.
    if (stale.length > 0) {
      const clients = await self.clients.matchAll({ type: 'window' });
      for (const c of clients) c.postMessage({ type: 'sw-updated', version });
    }
  })());
});

self.addEventListener('fetch', (event) => {
  const req = event.request;
  if (req.method !== 'GET') return;

  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;

  // Never intercept API, SSE or probes — these are live data and must hit
  // the backend so receipts, broadcast progress and auth checks stay accurate.
  if (
    url.pathname.startsWith('/api/') ||
    url.pathname === '/healthz' ||
    url.pathname === '/readyz'
  ) {
    return;
  }

  const wantsHTML =
    req.headers.get('accept')?.includes('text/html') ||
    req.mode === 'navigate';

  if (wantsHTML) {
    event.respondWith(networkFirst(req));
    return;
  }

  event.respondWith(cacheFirst(req));
});

// ─── Strategies ────────────────────────────────────────────────

async function networkFirst(req) {
  try {
    const resp = await fetch(req);
    // Only cache a genuine 200 same-origin shell. Caching a 500 deploy blip,
    // a proxy error page or a redirect here would poison '/' and serve that
    // as the offline shell forever (matches cacheFirst's guard).
    if (resp.ok && resp.type === 'basic') {
      const cache = await caches.open(await readVersion());
      cache.put('/', resp.clone());
    }
    return resp;
  } catch {
    const cached = await caches.match('/');
    return cached || Response.error();
  }
}

async function cacheFirst(req) {
  const cached = await caches.match(req);
  if (cached) return cached;
  const resp = await fetch(req);
  if (resp.ok && resp.type === 'basic') {
    const cache = await caches.open(await readVersion());
    cache.put(req, resp.clone());
  }
  return resp;
}

// ─── Version ───────────────────────────────────────────────────

/** Resolve the asset ETag exposed by the static middleware. */
async function readVersion() {
  try {
    const head = await fetch('/', { method: 'HEAD', cache: 'no-store' });
    const tag = (head.headers.get('ETag') || '').replace(/"/g, '');
    return tag ? 'zapcast-' + tag : FALLBACK_VERSION;
  } catch {
    return FALLBACK_VERSION;
  }
}
