'use strict';
/*
 * Dashboard core: shared helpers, the Alpine root component and the view
 * module contract. Loaded first; every view file is loaded after it.
 *
 * Module contract
 * ───────────────
 * A view file calls  Dashboard.register('<name>', { state…, methods… }).
 * The root component (x-data="dashboard()" on <body>) merges every registered
 * mixin into one object at start-up. State and method names share one
 * namespace: a name defined twice is reported as a console error. Getters are
 * kept as getters. A view may call another view's methods (for example
 * showSignal from intelligence.js or closePosition from trading.js).
 *
 * Optional hooks, where <Name> is the view name with a capital first letter:
 *   init<Name>()        once, when the page starts
 *   on<Name>Shown(arg)  each time the view becomes active; arg is the rest of
 *                       the hash (#/markets/BTC-EUR → 'BTC-EUR')
 *   on<Name>Hidden()    when another view becomes active
 *   on<Name>Data(d)     after each applied /api/dashboard response, on every view
 *
 * Data that is not in /api/dashboard is loaded with
 *   this.pollWhileShown('<name>', intervalMs, () => this.loadSomething())
 * (call it from init<Name>): it runs when the view is shown and then every
 * intervalMs while the view stays shown and the browser tab is visible.
 *
 * Markup for a view lives in index.html inside
 *   <section data-view="<name>" x-show="view === '<name>'">.
 *
 * Safety rules for every view: server, LLM and news text goes through Alpine
 * x-text (or esc() when HTML is built as a string), links through safeUrl(),
 * timestamps through parseUtc(), prices through fmtPrice() and money through
 * fmtMoney(). Every action except the emergency stop starts with
 * this._actionAllowedWhileStale().
 */

// Sends the browser to the login form when the bot answers 401. That only happens
// when the bot listens beyond this computer and the session is missing or the
// access token was changed; every API call on the page goes through this wrapper.
const plainFetch = window.fetch.bind(window);
window.fetch = async (...args) => {
    const response = await plainFetch(...args);
    if (response.status === 401) window.location.assign('/login');
    return response;
};

// ── Formatting and safety helpers (global, usable in Alpine expressions) ──────

// Escapes a server, LLM or news string for safe use inside innerHTML (text and quoted attributes).
function esc(value) {
    return String(value ?? '')
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&#39;');
}

// Returns the URL when it is a plain http(s) link, otherwise '#' (blocks javascript: and data: links).
function safeUrl(url) {
    return /^https?:\/\//i.test(url || '') ? url : '#';
}

// Parses an API timestamp. Storage timestamps are naive UTC ("2026-11-14T10:00:00"),
// so a 'Z' is appended when the string carries no zone or offset.
function parseUtc(value) {
    const text = String(value ?? '');
    const hasZone = /(Z|[+-]\d{2}:?\d{2})$/i.test(text);
    return new Date(hasZone ? text : text + 'Z');
}

// Decimals to show for a price: 2 at or above 1, otherwise enough for 4 significant
// digits, so coins priced far below a cent never show as 0.00 (e.g. 0.0000085 → 9).
function priceDecimals(value) {
    const v = Math.abs(Number(value));
    if (!Number.isFinite(v) || v === 0 || v >= 1) return 2;
    return Math.min(12, 3 - Math.floor(Math.log10(v)));
}

// Formats a market price with adaptive precision. Money amounts (equity, P&L, value) use fmtMoney instead.
function fmtPrice(value) {
    const v = Number(value);
    if (value === null || value === undefined || value === '' || !Number.isFinite(v)) return '—';
    const decimals = priceDecimals(v);
    return v.toLocaleString('en-GB', { minimumFractionDigits: decimals, maximumFractionDigits: decimals });
}

// Formats a money amount with exactly 2 decimals and thousands separators.
function fmtMoney(value) {
    const v = Number(value);
    if (value === null || value === undefined || value === '' || !Number.isFinite(v)) return '—';
    return v.toLocaleString('en-GB', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

// Formats a P&L with its sign before the currency: +€1.20 or −€3.40.
function fmtSignedMoney(value, symbol) {
    const v = Number(value);
    if (value === null || value === undefined || value === '' || !Number.isFinite(v)) return '—';
    return `${v < 0 ? '−' : '+'}${symbol}${fmtMoney(Math.abs(v))}`;
}

// Formats a percentage (already ×100) with a sign: +1.25%.
function fmtSignedPct(value, decimals = 2) {
    const v = Number(value);
    if (!Number.isFinite(v)) return '—';
    return `${v < 0 ? '−' : '+'}${Math.abs(v).toFixed(decimals)}%`;
}

// Formats a position or order size (6 decimals, as the API sends it).
function fmtSize(value) {
    const v = parseFloat(value);
    return Number.isFinite(v) ? v.toFixed(6) : '—';
}

// The CSS class for a signed amount: 'pos', 'neg' or '' for zero / unknown.
function pnlClass(value) {
    const v = Number(value);
    if (!Number.isFinite(v) || v === 0) return '';
    return v > 0 ? 'pos' : 'neg';
}

// Formats a Date as "14 Nov 10:42" (with seconds when asked); '—' for a missing or invalid date.
function fmtDateTime(date, withSeconds = false) {
    if (!date || Number.isNaN(date.getTime())) return '—';
    const time = withSeconds
        ? { hour: '2-digit', minute: '2-digit', second: '2-digit' }
        : { hour: '2-digit', minute: '2-digit' };
    return date.toLocaleDateString('en-GB', { day: '2-digit', month: 'short' }) + ' ' +
        date.toLocaleTimeString([], time);
}

// Formats a Date as "10:42" (or "10:42:05" with seconds).
function fmtTime(date, withSeconds = false) {
    if (!date || Number.isNaN(date.getTime())) return '—';
    return withSeconds
        ? date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
        : date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

// Formats a millisecond duration as "2d 3h", "4h 10m", "12m" (or "12m 5s" / "40s" with seconds).
function fmtDuration(ms, withSeconds = false) {
    if (!Number.isFinite(ms) || ms < 0) return '—';
    const totalSec = Math.floor(ms / 1000);
    const d = Math.floor(totalSec / 86400);
    const h = Math.floor((totalSec % 86400) / 3600);
    const m = Math.floor((totalSec % 3600) / 60);
    const s = totalSec % 60;
    if (d > 0) return `${d}d ${h}h`;
    if (h > 0) return `${h}h ${m}m`;
    if (!withSeconds) return `${m}m`;
    return m > 0 ? `${m}m ${s}s` : `${s}s`;
}

// Formats how long ago a Date was: "just now", "5m ago", "3h ago", "2d ago".
function timeAgo(date) {
    const s = Math.floor((Date.now() - date) / 1000);
    if (!Number.isFinite(s)) return '—';
    if (s < 60) return 'just now';
    if (s < 3600) return Math.floor(s / 60) + 'm ago';
    if (s < 86400) return Math.floor(s / 3600) + 'h ago';
    return Math.floor(s / 86400) + 'd ago';
}

// True when an element's text is cut off by a line clamp (.is-clamped), so a "more" button is worth showing.
function isClamped(el) {
    return el.scrollHeight > el.clientHeight + 1;
}

// Reads a design token (e.g. '--accent') from the stylesheet, for chart colours.
function cssVar(name) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

// Returns a fresh holder for data loaded by fetchResource: state is
// 'idle' | 'loading' | 'ready' | 'error'; error keeps the last failure.
function resource(initial) {
    return { state: 'idle', error: '', at: null, seq: 0, data: initial };
}

// ── Constants ─────────────────────────────────────────────────────────────────

// Views in navigation order; an unknown hash opens the first one.
const VIEWS = ['overview', 'markets', 'trading', 'intelligence', 'backtests', 'activity'];

// How often the dashboard is polled, on every view.
const DASHBOARD_POLL_MS = 5000;

// The dashboard counts as stale when no dashboard response has arrived for this long.
const STALE_AFTER_MS = 20000;

// Elements that can take keyboard focus inside a dialog.
const FOCUSABLE = 'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

// Polls registered by views: { viewName, intervalMs, load, lastRun }. Kept
// outside Alpine so the functions are not wrapped in reactive proxies.
const viewPolls = [];

// ── Module registry ───────────────────────────────────────────────────────────

window.Dashboard = {
    views: {},
    // Registers a view's state and methods; dashboard() merges them at start-up.
    register(name, mixin) {
        if (this.views[name]) console.error(`Dashboard: view "${name}" is registered twice`);
        this.views[name] = mixin;
    },
};

// Upper-cases the first letter: 'overview' → 'Overview' (used for hook names).
function capitalise(text) {
    return text.charAt(0).toUpperCase() + text.slice(1);
}

// The Alpine root component: core state and methods plus every registered view mixin.
function dashboard() {
    const root = dashboardCore();
    for (const [name, mixin] of Object.entries(Dashboard.views)) {
        const descriptors = Object.getOwnPropertyDescriptors(mixin);
        for (const key of Object.keys(descriptors)) {
            if (key in root) console.error(`Dashboard: "${key}" from view "${name}" is already defined`);
        }
        // Descriptors keep getters as getters (Object.assign would read them once).
        Object.defineProperties(root, descriptors);
    }
    return root;
}

// Core state shared by every view, the polling, navigation, staleness and header actions.
function dashboardCore() {
    return {
        // ── Navigation ──
        view: 'overview',
        viewArg: '',
        _ready: false,               // true once the first dashboard response is applied

        // ── Dashboard data (from /api/dashboard) ──
        mode: 'manual',
        environment: 'paper',
        currencySymbol: '€',
        equity: '0.00',
        cash: '0.00',
        markets: [],
        disabledMarkets: [],
        signals: [],
        positions: [],
        approvals: [],
        pendingCount: 0,
        equityHistory: [],
        riskRejections: [],
        activityLog: [],
        strategies: [],
        learning: [],
        emergencyStop: false,
        stopSince: null,
        warmupTicksRemaining: 0,
        activeStrategyId: '',
        activeStrategyLabel: '—',
        appVersion: '',
        llmAvailable: false,
        llmStatus: 'not_configured',
        llmModel: '',
        llmBackend: 'none',
        llmReflection: null,
        llmBriefing: null,

        // ── Shared view data ──
        pnlSummary: resource(null),  // /api/pnl-summary: used by Overview (today) and Trading

        // ── Connection state: the page shows a banner when the bot stops answering ──
        connectionLost: false,
        lastDashboardAt: null,       // Date of the last dashboard response that was applied
        _dashboardRequestSeq: 0,     // incremented for every dashboard request
        _dashboardAppliedSeq: 0,     // sequence number of the response currently shown

        // ── Feedback ──
        toasts: [],
        _toastSeq: 0,
        _dialogReturnFocus: null,

        tradingModes: [
            {
                id: 'manual', label: 'Manual', short: 'MANUAL',
                description: 'Signals are shown only. Nothing is traded.',
                summary: 'The bot evaluates markets and surfaces signals. No trades are placed until you switch mode.',
            },
            {
                id: 'semi_automated', label: 'Semi-automated', short: 'SEMI-AUTO',
                description: 'Each trade waits for your approval.',
                summary: 'The bot proposes trades. Each one waits in Pending Approvals for you to accept or reject.',
            },
            {
                id: 'fully_automated', label: 'Fully automated', short: 'AUTO',
                description: 'Confident signals are traded without approval.',
                summary: 'The bot executes trades immediately without approval. Stop-losses are automatic.',
            },
        ],

        // Starts the page: view hooks, routing, polling and the service worker.
        init() {
            this._callViewHooks('init', '');
            this._applyRoute(false);
            window.addEventListener('hashchange', () => this._applyRoute(true));
            this._initialLoad();
            setInterval(() => this.loadDashboard(), DASHBOARD_POLL_MS);
            setInterval(() => this._checkStale(), 5000);
            setInterval(() => this._runDuePolls(), 1000);
            // Service worker: caches the page shell and tells this page when the bot is unreachable
            if ('serviceWorker' in navigator) {
                navigator.serviceWorker.register('/sw.js')
                    .catch(err => console.warn('[SW] Registration failed:', err));
                navigator.serviceWorker.addEventListener('message', event => {
                    this._handleSwMessage(event.data);
                });
            }
        },

        // Loads the dashboard first so the currency and market list are known
        // before any view loads its own data, then starts the shown view's polls.
        async _initialLoad() {
            await this.loadDashboard();
            this.loadSettings().catch(e => console.error('Settings:', e));
            this._ready = true;
            this._runViewPolls(this.view);
        },

        // ── Navigation ───────────────────────────────────────────────────────

        // Calls hook <prefix><Name><suffix> on every view that defines it; one failing view does not stop the rest.
        _callViewHooks(prefix, suffix, ...args) {
            for (const name of Object.keys(Dashboard.views)) {
                const hook = this[prefix + capitalise(name) + suffix];
                if (typeof hook !== 'function') continue;
                try {
                    hook.apply(this, args);
                } catch (e) {
                    console.error(`Dashboard: ${prefix}${capitalise(name)}${suffix} failed`, e);
                }
            }
        },

        // Calls one view's hook if it defines it.
        _callViewHook(name, prefix, suffix, ...args) {
            const hook = this[prefix + capitalise(name) + suffix];
            if (typeof hook !== 'function') return;
            try {
                hook.apply(this, args);
            } catch (e) {
                console.error(`Dashboard: ${prefix}${capitalise(name)}${suffix} failed`, e);
            }
        },

        // Reads "#/<view>/<arg>" and shows that view; an unknown hash becomes #/overview.
        _applyRoute(byUser) {
            const [name, ...rest] = location.hash.replace(/^#\/?/, '').split('/');
            if (!VIEWS.includes(name)) {
                history.replaceState(null, '', '#/overview');
                this._showView('overview', '', byUser);
                return;
            }
            this._showView(name, rest.map(decodeURIComponent).join('/'), byUser);
        },

        // Switches the active view, runs its hooks and (once loaded) its data polls.
        _showView(name, arg, byUser) {
            const previous = this.view;
            const changed = previous !== name;
            if (changed) this._callViewHook(previous, 'on', 'Hidden');
            this.view = name;
            this.viewArg = arg;
            this._callViewHook(name, 'on', 'Shown', arg);
            if (!changed) return;
            if (this._ready) this._runViewPolls(name);
            if (byUser) {
                // Move focus to the new view's heading so keyboard and screen-reader users follow the change.
                window.scrollTo(0, 0);
                this.$nextTick(() => document.querySelector(`[data-view="${name}"] h1`)?.focus({ preventScroll: true }));
            }
        },

        // Opens a view (and optional argument) by changing the hash, so the back button works.
        navigate(name, arg = '') {
            location.hash = '#/' + name + (arg ? '/' + encodeURIComponent(arg) : '');
        },

        // Registers a loader that runs while a view is shown (see the module contract).
        pollWhileShown(viewName, intervalMs, load) {
            viewPolls.push({ viewName, intervalMs, load, lastRun: 0 });
        },

        // Runs every poll of a view now (the view was just shown).
        _runViewPolls(viewName) {
            for (const poll of viewPolls) {
                if (poll.viewName !== viewName) continue;
                poll.lastRun = Date.now();
                poll.load();
            }
        },

        // Runs the shown view's polls whose interval has passed (skipped while the tab is hidden).
        _runDuePolls() {
            if (!this._ready || document.hidden) return;
            const now = Date.now();
            for (const poll of viewPolls) {
                if (poll.viewName !== this.view || now - poll.lastRun < poll.intervalMs) continue;
                poll.lastRun = now;
                poll.load();
            }
        },

        // ── Connection state ─────────────────────────────────────────────────

        // The service worker only sends API_OFFLINE, and only to the page whose request failed.
        _handleSwMessage(msg) {
            if (msg && msg.type === 'API_OFFLINE' && msg.endpoint === '/api/dashboard') {
                this._markConnectionLost();
            }
        },

        // Shows the stale banner when no dashboard response has arrived recently
        // (also covers a request that hangs, and pages without a service worker).
        _checkStale() {
            if (!this.lastDashboardAt || Date.now() - this.lastDashboardAt.getTime() > STALE_AFTER_MS) {
                this._markConnectionLost();
            }
        },

        // Marks the shown data as stale (banner, dimmed views, hidden Live indicator).
        _markConnectionLost() {
            this.connectionLost = true;
        },

        // Marks the data as current after a dashboard response was applied.
        _markConnected() {
            this.connectionLost = false;
            this.lastDashboardAt = new Date();
        },

        // Returns true when an action may go ahead; while the data is stale the operator confirms first.
        _actionAllowedWhileStale() {
            if (!this.connectionLost) return true;
            return confirm('The connection to the bot is lost and this page shows old data.\n\nSend the request anyway?');
        },

        // ── Dashboard data ───────────────────────────────────────────────────

        // Stores one /api/dashboard response in the shared state, then lets each view react to it.
        _applyDashboard(d) {
            this.mode = d.mode;
            this.environment = d.environment;
            this.currencySymbol = d.currency_symbol || '€';
            this.equity = d.equity;
            this.cash = d.cash || '0.00';
            this.markets = d.markets || [];
            this.disabledMarkets = d.control?.disabled_markets || [];
            this.signals = d.signals || [];
            this.positions = d.positions || [];
            this.approvals = d.approvals || [];
            this.pendingCount = this.approvals.length;
            this.equityHistory = d.equity_history || [];
            this.riskRejections = d.risk_rejections || [];
            this.activityLog = d.activity || [];
            this.learning = d.learning || [];
            this.emergencyStop = d.control?.emergency_stop ?? false;
            this.stopSince = d.control?.stop_since ?? null;
            this.warmupTicksRemaining = d.warmup_ticks_remaining ?? 0;
            this.strategies = d.strategies || [];
            const selectedStrategy = this.strategies.find(s => s.selected);
            this.activeStrategyId = d.control?.selected_strategy || selectedStrategy?.id || '';
            this.activeStrategyLabel = selectedStrategy?.label || this._strategyLabel(this.activeStrategyId);
            if (d.version) this.appVersion = d.version;
            this.llmAvailable = d.llm?.available ?? false;
            this.llmStatus = d.llm?.status ?? (this.llmAvailable ? 'available' : 'not_configured');
            this.llmModel = d.llm?.model ?? '';
            this.llmBackend = d.llm?.backend ?? 'none';
            this.llmReflection = d.llm?.reflection ?? null;
            this.llmBriefing = d.llm?.briefing ?? null;
            this._callViewHooks('on', 'Data', d);
        },

        // Fetches and applies the dashboard. Polls and post-action refreshes can overlap,
        // so each request is numbered and a response older than the one shown is dropped.
        async loadDashboard() {
            const seq = ++this._dashboardRequestSeq;
            try {
                const r = await fetch('/api/dashboard');
                if (!r.ok) throw new Error(`HTTP ${r.status}`);
                const data = await r.json();
                if (seq < this._dashboardAppliedSeq) return;
                this._dashboardAppliedSeq = seq;
                this._applyDashboard(data);
                this._markConnected();
            } catch (e) {
                console.error('Dashboard:', e);
                // A failure of an old request does not hide a newer success
                if (seq > this._dashboardAppliedSeq) this._markConnectionLost();
            }
        },

        // Loads JSON from url into a resource (see resource()); a response older than
        // the one shown is dropped, and a failed refresh keeps the data it already has.
        async fetchResource(res, url) {
            const seq = ++res.seq;
            if (res.state !== 'ready') res.state = 'loading';
            try {
                const r = await fetch(url);
                if (!r.ok) throw new Error(await this._refusalDetail(r, `HTTP ${r.status}`));
                const data = await r.json();
                if (seq !== res.seq) return;
                res.data = data;
                res.state = 'ready';
                res.error = '';
                res.at = new Date();
            } catch (e) {
                if (seq !== res.seq) return;
                console.error(url, e);
                res.error = e.message;
                if (!res.at) res.state = 'error';
            }
        },

        // A resource's status line: "12 orders · updated 10:42", "Loading…" or the last error.
        resourceStatus(res, noun) {
            if (res.state === 'idle' || res.state === 'loading') return 'Loading…';
            if (res.state === 'error') return `Could not load: ${res.error}`;
            const count = Array.isArray(res.data) ? `${res.data.length} ${noun} · ` : '';
            const updated = `${count}updated ${fmtTime(res.at)}`;
            return res.error ? `${updated} · refresh failed: ${res.error}` : updated;
        },

        // True while a resource has not loaded yet (first load still running).
        isLoading(res) {
            return res.state === 'idle' || res.state === 'loading';
        },

        // Loads the realised P&L summary (by day and by market).
        loadPnlSummary() {
            return this.fetchResource(this.pnlSummary, '/api/pnl-summary');
        },

        // Reads the API's {"detail": ...} message from a refused request.
        async _refusalDetail(response, fallback = `Change refused (HTTP ${response.status})`) {
            const body = await response.json().catch(() => ({}));
            return typeof body.detail === 'string' && body.detail ? body.detail : fallback;
        },

        // ── Shared labels ────────────────────────────────────────────────────

        // The trading mode entry for an id (falls back to Manual).
        modeOption(id) {
            return this.tradingModes.find(m => m.id === id) || this.tradingModes[0];
        },

        // A readable name for a strategy id.
        _strategyLabel(strategyId) {
            const labels = {
                basic_strategy_1h: 'Basic 1h',
                basic_strategy_long_1h: 'Basic 1h · long only',
                basic_strategy_4h: 'Basic 4h',
                basic_strategy_long_4h: 'Basic 4h · long only',
                basic_and_llm_strategy_1h: 'Basic + LLM 1h',
                basic_and_llm_strategy_long_1h: 'Basic + LLM 1h · long only',
                basic_and_llm_strategy_4h: 'Basic + LLM 4h',
                basic_and_llm_strategy_long_4h: 'Basic + LLM 4h · long only',
                basic_strategy: 'Basic (ticks, retired)',                // trades before 0.18.0
                basic_and_llm_strategy: 'Basic + LLM (ticks, retired)',  // trades before 0.18.0
                llm_only_strategy: 'LLM Only',
                trend_strategy: 'Trend (4h)',
                trend_and_llm_strategy: 'Trend + LLM',
                donchian_strategy: 'Donchian (4h)',
                donchian_and_llm_strategy: 'Donchian + LLM',
                rsi_dip_strategy: 'RSI Dip (4h)',
                rsi_dip_and_llm_strategy: 'RSI Dip + LLM',
                macd_strategy: 'MACD (4h)',
                macd_and_llm_strategy: 'MACD + LLM',
                squeeze_strategy: 'Squeeze (4h)',
                squeeze_and_llm_strategy: 'Squeeze + LLM',
                rotation_strategy: 'Rotation',
                rotation_and_llm_strategy: 'Rotation + LLM',
                combined: 'Combined',   // legacy alias
                llm: 'LLM Only',        // legacy alias
            };
            return labels[strategyId] || strategyId || '—';
        },

        // The LLM backend as shown to the operator.
        llmBackendLabel() {
            return this.llmBackend === 'lmstudio' ? 'LM Studio' : this.llmBackend === 'remote' ? 'Remote' : 'None';
        },

        // The LLM status as shown to the operator.
        llmStatusLabel() {
            return this.llmStatus === 'available' ? 'Available'
                : this.llmStatus === 'unavailable' ? 'Unavailable' : 'Not configured';
        },

        // A direction as an arrow and word: '▲ Long' / '▼ Short'.
        directionLabel(direction) {
            return direction === 'long' ? '▲ Long' : '▼ Short';
        },

        // ── Header actions ───────────────────────────────────────────────────

        // Activates the emergency stop; the stopped state is shown only once the server confirms it.
        async activateStop() {
            if (!confirm('Activate emergency stop? This will halt all trading and clear the approval queue.')) return;
            try {
                const response = await fetch('/api/control/emergency-stop', { method: 'POST' });
                if (!response.ok) {
                    alert(`EMERGENCY STOP FAILED — the bot may still be trading.\n\n${await this._refusalDetail(response)}`);
                    return;
                }
                this.emergencyStop = true;
            } catch (e) {
                alert(`EMERGENCY STOP FAILED — the server could not be reached, the bot may still be trading.\n\n${e.message}`);
                return;
            } finally {
                this.loadDashboard();
            }
        },

        // Lifts the emergency stop after confirmation; the running state is shown only once the server confirms it.
        async resumeBot() {
            if (!this._actionAllowedWhileStale()) return;
            if (!confirm('Resume trading? The bot will start opening and closing positions again.')) return;
            try {
                const response = await fetch('/api/control/resume', { method: 'POST' });
                if (!response.ok) {
                    alert(`RESUME FAILED — the emergency stop is still active.\n\n${await this._refusalDetail(response)}`);
                    return;
                }
                this.emergencyStop = false;
            } catch (e) {
                alert(`RESUME FAILED — the server could not be reached.\n\n${e.message}`);
                return;
            } finally {
                this.loadDashboard();
            }
        },

        // ── Feedback and dialogs ─────────────────────────────────────────────

        // Shows a toast. level is 'success' | 'info' | 'warning' | 'error'; errors and
        // warnings stay until dismissed, the others disappear after a few seconds.
        notify(level, message, sticky = level === 'error' || level === 'warning') {
            const id = ++this._toastSeq;
            this.toasts.push({ id, level, message });
            if (!sticky) setTimeout(() => this.dismissToast(id), 6000);
        },

        // Removes one toast.
        dismissToast(id) {
            this.toasts = this.toasts.filter(t => t.id !== id);
        },

        // Runs fn once elements just revealed by x-show have their size (x-show
        // shows them on the next animation frame, after Alpine's $nextTick).
        afterReveal(fn) {
            this.$nextTick(() => requestAnimationFrame(fn));
        },

        // Remembers the focused element and moves focus into a dialog that just opened.
        focusDialog(panel) {
            this._dialogReturnFocus = document.activeElement;
            // x-show reveals the dialog on the next animation frame and a hidden element cannot
            // take focus: $nextTick waits until x-show has queued that frame, then ours follows it.
            this.$nextTick(() => requestAnimationFrame(() => panel.querySelector(FOCUSABLE)?.focus()));
        },

        // Gives focus back to the element that opened the dialog.
        restoreFocus() {
            const target = this._dialogReturnFocus;
            this._dialogReturnFocus = null;
            if (target && document.contains(target)) this.$nextTick(() => target.focus());
        },

        // Keeps Tab and Shift+Tab focus inside a dialog panel.
        trapTab(event) {
            const items = [...event.currentTarget.querySelectorAll(FOCUSABLE)].filter(el => el.offsetParent !== null);
            if (!items.length) return;
            const first = items[0];
            const last = items[items.length - 1];
            if (event.shiftKey && document.activeElement === first) {
                event.preventDefault();
                last.focus();
            } else if (!event.shiftKey && document.activeElement === last) {
                event.preventDefault();
                first.focus();
            }
        },
    };
}
