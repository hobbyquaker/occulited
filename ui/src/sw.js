/*
 * The App's service worker (task 193, the PWA). It caches the shell - the page, the hashed
 * bundles under /assets, the icons and the manifest - under the version of the build that
 * emitted it (vite.config.ts writes __VERSION__), and never an API answer: a cached value or a
 * cached session would be worse than no offline mode. A new version takes over at once
 * (skipWaiting, clients.claim) and drops the old caches, so an updated system is not served
 * yesterday's UI from a phone's cache; the page reloads itself when the controller changes
 * (main.ts). Offline, a navigation gets the cached shell and the App says it has no connection.
 */
const VERSION = '__VERSION__';
const CACHE = `ol-shell-${VERSION}`;
const SHELL = ['/', '/app.webmanifest', '/icons/icon-192.png', '/icons/icon-512.png', '/icons/maskable-512.png', '/icons/apple-touch-icon.png'];

// keep puts a copy of an answer into the cache. The copy is taken now, before the answer goes to the
// page: once the page has read the body, clone() throws "Response body is already used" (B-116) - the
// put inside caches.open(...).then() came too late for that, and the shell was never refreshed.
function keep(key, r) {
    const copy = r.clone();
    caches.open(CACHE).then((c) => c.put(key, copy)).catch(() => undefined);
}

self.addEventListener('install', (ev) => {
    ev.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL).catch(() => undefined)).then(() => self.skipWaiting()));
});
self.addEventListener('activate', (ev) => {
    ev.waitUntil(caches.keys().then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k)))).then(() => self.clients.claim()));
});
self.addEventListener('fetch', (ev) => {
    const req = ev.request;
    if (req.method !== 'GET') return;
    const url = new URL(req.url);
    if (url.origin !== location.origin) return;
    // never the API, the addons or a stream: the network is the only truth for those
    if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/addons/') || url.pathname.startsWith('/config/')) return;
    if (url.pathname.startsWith('/assets/')) {
        // hashed and immutable: the cache first, the network once
        ev.respondWith(caches.open(CACHE).then(async (c) => (await c.match(req)) ?? fetch(req).then((r) => { if (r.ok) c.put(req, r.clone()); return r; })));
        return;
    }
    if (req.mode === 'navigate') {
        // the shell: the network first, so a new build is seen at once; the cached shell without a connection
        ev.respondWith(fetch(req).then((r) => { if (r.ok) keep('/', r); return r; }).catch(() => caches.match('/')));
        return;
    }
    if (SHELL.includes(url.pathname)) {
        ev.respondWith(fetch(req).then((r) => { if (r.ok) keep(req, r); return r; }).catch(() => caches.match(req)));
    }
});
