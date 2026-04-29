'use strict';
/**
 * Kraken Bot — Service Worker
 *
 * Strategy
 * ────────
 * • Static assets  → stale-while-revalidate  (instant load, background refresh)
 * • API endpoints  → network-first           (always fresh; fall back to cache)
 *
 * After every successful API fetch the response data is broadcast to all open
 * window clients via postMessage so the dashboard updates immediately without
 * the page having to wait for its next polling interval.
 *
 * Message shape sent to clients
 * ─────────────────────────────
 * { type: 'API_UPDATE',  endpoint: '/api/dashboard', data: {...}, ts: 1234567890 }
 * { type: 'API_OFFLINE', endpoint: '/api/dashboard',              ts: 1234567890 }
 */

const CACHE  = 'trading-bot-v0.5.25';  // update this to invalidate old caches

/**
 * GET endpoints whose responses should be cached AND broadcast to clients.
 * POST/PATCH/DELETE requests are never intercepted.
 */
const LIVE_ENDPOINTS = new Set([
    '/api/dashboard',
    '/api/trades',
    '/api/closed-trades',
    '/api/rejected-trades',
    '/api/news',
]);

// ── Lifecycle ──────────────────────────────────────────────────────────────

self.addEventListener('install', event => {
    // Activate immediately — don't wait for old tabs to close
    event.waitUntil(self.skipWaiting());
});

self.addEventListener('activate', event => {
    // Remove caches from older SW versions
    event.waitUntil(
        caches.keys()
            .then(keys => Promise.all(
                keys.filter(k => k !== CACHE).map(k => caches.delete(k))
            ))
            .then(() => self.clients.claim())   // take control of all open tabs now
    );
});

// ── Fetch interception ─────────────────────────────────────────────────────

self.addEventListener('fetch', event => {
    const req = event.request;

    // Only intercept same-origin GET requests
    if (req.method !== 'GET') return;
    if (!req.url.startsWith(self.location.origin)) return;

    const path = new URL(req.url).pathname;
    const acceptsHtml = req.headers.get('accept')?.includes('text/html') ?? false;

    if (req.mode === 'navigate' || acceptsHtml) {
        // Dashboard documents must be network-first so old HTML does not keep
        // running stale inline scripts after frontend fixes are deployed.
        event.respondWith(networkFirst(req));
        return;
    }

    if (LIVE_ENDPOINTS.has(path)) {
        // Network first → cache → broadcast result to all tabs
        event.respondWith(networkFirstBroadcast(req, path));
        return;
    }

    if (!path.startsWith('/api/')) {
        // Static assets (HTML, JS, CSS, images) → stale-while-revalidate
        event.respondWith(staleWhileRevalidate(req));
    }
    // All other /api/* paths (OHLC, signal detail, etc.) pass through untouched
});

// ── Fetch strategies ───────────────────────────────────────────────────────

async function networkFirstBroadcast(request, path) {
    const cache = await caches.open(CACHE);
    try {
        const response = await fetch(request);

        if (response.ok) {
            // Persist fresh response
            cache.put(request, response.clone());

            // Parse JSON and push to all window clients
            const data = await response.clone().json().catch(() => null);
            if (data !== null) _broadcast({ type: 'API_UPDATE', endpoint: path, data, ts: Date.now() });
        }

        return response;

    } catch (_networkError) {
        // Backend unreachable — serve stale cache and let clients know
        const cached = await cache.match(request);
        _broadcast({ type: 'API_OFFLINE', endpoint: path, ts: Date.now() });

        return cached ?? new Response(JSON.stringify({ error: 'offline' }), {
            status: 503,
            headers: { 'Content-Type': 'application/json' },
        });
    }
}

async function networkFirst(request) {
    const cache = await caches.open(CACHE);
    try {
        const response = await fetch(request);
        if (response.ok) cache.put(request, response.clone());
        return response;
    } catch (_networkError) {
        return await cache.match(request) ?? Response.error();
    }
}

async function staleWhileRevalidate(request) {
    const cache  = await caches.open(CACHE);
    const cached = await cache.match(request);

    // Kick off network fetch in background regardless of cache hit
    const fresh = fetch(request).then(res => {
        if (res.ok) cache.put(request, res.clone());
        return res;
    }).catch(() => null);

    // Return cached instantly; background refresh updates cache for next visit
    return cached ?? (await fresh);
}

// ── Helpers ────────────────────────────────────────────────────────────────

async function _broadcast(payload) {
    const all = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    all.forEach(c => c.postMessage(payload));
}
