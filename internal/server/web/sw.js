// Dev/source embeds intentionally do not cache unversioned modules.
// Vite injects the immutable list of emitted production assets.
const BUILD = self.__ZAPCAST_BUILD__;
const PREFIX = 'zapcast-';
const CACHE = BUILD ? `${PREFIX}shell-${BUILD.id}` : '';
const paths = new Set(BUILD?.assets || []);
let purged = false;
self.addEventListener('message', event => {
  if(event.data?.type === 'ACTIVATE') event.waitUntil(self.skipWaiting());
  if(event.data?.type === 'PURGE') {
    purged = true;
    event.waitUntil((async () => {
      for(const key of await caches.keys()) if(key.startsWith(PREFIX)) await caches.delete(key);
      event.ports?.[0]?.postMessage('purged');
    })());
  }
});

self.addEventListener('install', event => {
  if (!BUILD) return;
  event.waitUntil((async () => {
    const cache = await caches.open(CACHE);
    try {
      // Validate the entire generation before writing any response.
      const responses = [];
      for(const path of BUILD.assets) {
        const response = await fetch(path, {cache:'no-store', credentials:'omit', redirect:'error', integrity:BUILD.integrity[path]});
        const control = response.headers.get('Cache-Control') || '';
        const type = response.headers.get('Content-Type') || '';
        const expected = path === '/' ? /^text\/html\b/i : path.endsWith('.js') ? /^(text|application)\/(javascript|ecmascript)\b/i : path.endsWith('.css') ? /^text\/css\b/i : path.endsWith('.webmanifest') ? /^application\/(manifest\+json|json)\b/i : /^image\//i;
        if(response.status !== 200 || response.type !== 'basic' || response.redirected ||
          new URL(response.url).pathname !== path || /(?:no-store|private)/i.test(control) || !expected.test(type)) {
          throw new Error('Unsafe precache response');
        }
        responses.push([path,response]);
      }
      if(purged) throw new Error('Session cleared');
      for(const [path,response] of responses) {
        await cache.put(path,response);
        if(purged) throw new Error('Session cleared');
      }
    }
    catch (error) { await caches.delete(CACHE); throw error; }
  })());
});
self.addEventListener('activate', event => {
  event.waitUntil((async () => {
    // Retain live generations: another tab may still run their modules.
    for(const key of await caches.keys()) {
      if(key.startsWith(PREFIX) && !key.startsWith(`${PREFIX}shell-`)) await caches.delete(key);
    }
    await self.clients.claim();
  })());
});
self.addEventListener('fetch', event => {
  if (!BUILD || purged || event.request.method !== 'GET') return;
  const url = new URL(event.request.url);
  if(url.origin !== self.location.origin || url.search) return;
  const immutable = /^\/assets\/[\w-]+-[\w-]{8,}\.(?:js|css)$/.test(url.pathname);
  if(!paths.has(url.pathname) && !immutable) return;
  // Never fetch a new deployment's module into this immutable generation.
  event.respondWith((async () => {
    const cache = await caches.open(CACHE);
    const own = await cache.match(url.pathname);
    if(own) return own;
    // A new active worker can serve an old tab's content-addressed modules
    // only from an already validated ZapCast generation, never the network.
    if(immutable) for(const key of await caches.keys()) {
      if(key.startsWith(`${PREFIX}shell-`)) {
        const previous = await (await caches.open(key)).match(url.pathname);
        if(previous) return previous;
      }
    }
    return Response.error();
  })());
});
