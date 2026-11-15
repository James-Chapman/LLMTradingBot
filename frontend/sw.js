'use strict';
/**
 * LLM Trading Bot — Service Worker
 *
 * Strategy
 * ────────
 * • Dashboard HTML → network-first           (cached copy only when the bot is down)
 * • App code       → network-first           (/static/app/*.js and /static/styles.css:
 *                                             a deploy is picked up on the next load)
 * • Other static   → stale-while-revalidate  (instant load, background refresh)
 * • API endpoints  → network only            (never cached: old data must not look live)
 *
 * The page applies the responses of its own requests. When an API request
 * fails because the bot is unreachable, the service worker answers 503 and
 * tells only the page that made the request, so it can show its stale banner:
 *
 * { type: 'API_OFFLINE', endpoint: '/api/dashboard', ts: 1234567890 }
 */

const CACHE  = 'trading-bot-v0.17.0';  // update this to invalidate old caches

/**
 * Polled GET endpoints whose failures are reported to the requesting page.
 * POST/PATCH/DELETE requests are never intercepted.
 */
const LIVE_ENDPOINTS = new Set([
    '/api/dashboard',
    '/api/trades',
    '/api/closed-trades',
    '/api/rejected-trades',
    '/api/news',
    '/api/pnl-summary',
]);

/**
 * The dashboard's own code. It must match the HTML that loads it, so it is
 * fetched network-first like the HTML; the cached copy is only for when the
 * bot is down.
 */
function isAppAsset(path) {
    return path.startsWith('/static/app/') || path === '/static/styles.css';
}

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

    if (isAppAsset(path)) {
        // Old scripts must not run against new HTML (or the other way round)
        event.respondWith(networkFirst(req));
        return;
    }

    if (LIVE_ENDPOINTS.has(path)) {
        // Network only; a failure is reported to the requesting page
        event.respondWith(networkOnlyApi(req, path, event.clientId));
        return;
    }

    if (!path.startsWith('/api/')) {
        // Static assets (HTML, JS, CSS, images) → stale-while-revalidate
        event.respondWith(staleWhileRevalidate(req));
    }
    // All other /api/* paths (OHLC, signal detail, etc.) pass through untouched
});

// ── Fetch strategies ───────────────────────────────────────────────────────

// Passes an API request to the bot. When the bot is unreachable it answers 503
// (never a cached copy, which the page would show as current) and tells the
// requesting page that the connection is lost.
async function networkOnlyApi(request, path, clientId) {
    try {
        return await fetch(request);
    } catch (_networkError) {
        _notifyClient(clientId, { type: 'API_OFFLINE', endpoint: path, ts: Date.now() });
        return new Response(JSON.stringify({ detail: 'The bot could not be reached.' }), {
            status: 503,
            headers: { 'Content-Type': 'application/json' },
        });
    }
}

// A response may be cached only when it is the resource itself. A request that
// was redirected (to the login form, when a session is needed) must not be:
// the login page would later be served in place of the dashboard.
function cacheable(response) {
    return response.ok && !response.redirected;
}

// Fetches from the bot and keeps a copy; the copy is served only when the bot cannot be reached.
async function networkFirst(request) {
    const cache = await caches.open(CACHE);
    try {
        const response = await fetch(request);
        if (cacheable(response)) cache.put(request, response.clone());
        return response;
    } catch (_networkError) {
        return await cache.match(request) ?? Response.error();
    }
}

// Serves the cached copy at once and refreshes it in the background for the next visit.
async function staleWhileRevalidate(request) {
    const cache  = await caches.open(CACHE);
    const cached = await cache.match(request);

    // Kick off network fetch in background regardless of cache hit
    const fresh = fetch(request).then(res => {
        if (cacheable(res)) cache.put(request, res.clone());
        return res;
    }).catch(() => null);

    // Return cached instantly; background refresh updates cache for next visit
    return cached ?? (await fresh);
}

// ── Helpers ────────────────────────────────────────────────────────────────

// Sends a message to the one page that made a request (other tabs are not told).
async function _notifyClient(clientId, payload) {
    if (!clientId) return;
    const client = await self.clients.get(clientId);
    if (client) client.postMessage(payload);
}
