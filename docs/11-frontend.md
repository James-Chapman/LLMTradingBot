# Frontend Architecture

**Files:** `frontend/index.html`, `frontend/approvals.html`, `frontend/static/styles.css`

A single-page application served directly by FastAPI. Uses **Alpine.js** for reactive UI state, chart libraries from CDNs, and a shared local stylesheet at `/static/styles.css` for theme, utility classes, responsive layout, panels, buttons, grids, tables, and charts. No build step, bundler, or Node.js toolchain is required.

---

## Technology Stack

| Library | Version | Purpose |
|---------|---------|---------|
| Alpine.js | 3.x (CDN) | Reactive component state and DOM binding |
| Chart.js | 4.x (CDN) | Equity curve line chart |
| Lightweight Charts | 4.x (CDN) | Candlestick price charts per market |
| Shared CSS | `frontend/static/styles.css` | Theme variables, utility classes, responsive shells, panel layout, tables, charts, buttons |

---

## Responsive Layout and Styling

The dashboard and approvals page load `/static/styles.css`. This file contains the dark theme variables and the responsive layout primitives used across both pages:

- Local utility classes such as `.sticky`, `.top-0`, `.z-50`, `.flex`, `.items-center`, `.justify-between`, and `.gap-*` replace the previous Tailwind browser CDN script.
- `.app-shell` with `--page-gutter` replaces fixed-width page caps and uses the full viewport width minus gutters.
- `.dashboard-grid` is a vertical flex stack for major page sections.
- `.core-grid` is a responsive status workspace: Markets, Signals, Open Positions, and Closed Positions sit side by side on desktop, use the same fixed panel height, and scroll internally when content is taller than the row.
- `.approval-details` and `.chart-grid` are vertical stacks so each block uses the full available width.
- `.news-grid` is a responsive article-card grid inside the full-width News Feed panel.
- `.chart-pair-grid` is the only content grid that may place items side by side; it is reserved for the 5-minute and 15-minute chart panes inside each market chart card.
- `.panel`, `.panel-header`, `.panel-body`, `.responsive-table`, `.activity-list`, `.core-panel`, `.core-scroll`, `.llm-status`, and `.llm-row` standardise panel structure while preserving existing Alpine state and DOM IDs.
- `.chart-pane` and `.equity-chart-shell` use `clamp()` so widescreen users get larger charts while mobile users avoid horizontal page scroll.

Breakpoint coverage used for layout verification:

| Viewport | Expected layout |
|---|---|
| `390x844` | Single-column dashboard, wrapped header, no page-level horizontal scroll |
| `768x1024` | Vertical block stack, wrapped header, panels remain touch-friendly |
| `1366x768` | Full-width vertical panels with in-panel table scrolling |
| `1920x1080` | Main content uses the monitor width; blocks remain stacked vertically |
| `2560x1440` | Dashboard still uses the available width, with readable gutters and full-width blocks |

The CSS keeps the existing theme palette and visual treatment. Functional hooks such as `equity-chart`, `markets-list`, `signals-list`, `positions-list`, `approvals-queue`, `ledger-body`, `closed-body`, `candle-grid`, and `news-grid` are intentionally retained.

---

## Component Initialisation

The entire UI is a single Alpine.js component mounted on `<div x-data="dashboard()">`. The `dashboard()` factory function returns all state and methods. Alpine calls `init()` automatically when the component mounts.

```javascript
function dashboard() {
    return {
        // --- State ---
        mode:          'manual',
        environment:   'paper',
        equity:        '£0.00',
        cash:          '0.00',
        pendingCount:  0,
        emergencyStop: false,
        strategies:    [],
        riskRejections: [],
        activityLog:   [],
        llmAvailable:  false,
        llmModel:      '',
        llmReflection: null,
        llmBriefing:   null,
        signalModal:     null,    // full signal object for the detail modal
        signalBreakdown: null,    // computed confidence waterfall
        signalVoteRows:  [],      // per-indicator vote rows
        ledgerStatus:  '',
        closedStatus:  '',
        newsStatus:    '',

        // Set of full position UUIDs currently open
        _openPositionIds: new Set(),

        // Desktop notification deduplication
        _seenSignalIds: new Set(),   // IDs already notified — prevents re-firing on each poll
        _notifyReady:   false,       // true after first render; seeds set without firing on load

        // Service Worker live update state
        _swActive:   false,
        _lastUpdate: null,

        // Chart instances (not reactive — managed imperatively)
        _chart:        null,
        _candleCharts: {},   // market → { chart5, series5, chart15, series15 }
        _chartMarkets: [],   // markets rendered so far (used by refreshCharts)

        // --- Methods ---
        init() { ... },
        ...
    }
}
```

---

## Service Worker Caching

`/sw.js` uses a versioned cache (`trading-bot-v0.5.5`) and claims open tabs as soon as the new worker activates. Navigation and other `text/html` requests are network-first, so the dashboard shell does not keep running stale inline scripts after frontend fixes. API dashboard endpoints remain network-first with client broadcasts, while non-HTML static assets use stale-while-revalidate.

---

## Polling Architecture

The frontend polls the backend on fixed intervals set up in `init()`. There is no WebSocket or server-sent event stream — all updates are pull-based.

| Interval | Data | Endpoint |
|----------|------|----------|
| 5 seconds | Prices, signals, positions, equity, approvals, activity, LLM state | `GET /api/dashboard` |
| 15 seconds | Full trade ledger | `GET /api/trades` |
| 30 seconds | Closed trades | `GET /api/closed-trades` |
| 30 seconds | Rejected trades register | `GET /api/rejected-trades` |
| 300 seconds | News feed | `GET /api/news` |
| 60 seconds | Price candlestick charts (both intervals) | `GET /api/ohlc/{market}?interval=5` and `?interval=15` |

```javascript
async init() {
    await this.loadDashboard();
    await this.loadNews();
    setInterval(() => this.loadDashboard(),    5_000);
    setInterval(() => this.renderLedger(),    15_000);
    setInterval(() => this.loadClosedTrades(), 30_000);
    setInterval(() => this.loadRejectedTrades(), 30_000);
    setInterval(() => this.loadNews(),        300_000);
    setInterval(() => this.refreshCharts(),    60_000);
}
```

---

## Dashboard Load Cycle (`loadDashboard`)

Called every 5 seconds. Fetches `/api/dashboard` and updates all reactive state properties. Also triggers `renderMarkets`, `renderSignals`, `renderPositions`, `renderApprovals`, and `renderChart`.

```javascript
async loadDashboard() {
    const d = await fetch('/api/dashboard').then(r => r.json());

    this.mode          = d.mode;
    this.environment   = d.environment;
    this.equity        = '£' + (d.equity ?? 0).toFixed(2);
    this.cash          = (d.cash ?? 0).toFixed(2);
    this.pendingCount  = (d.approvals ?? []).length;
    this.emergencyStop = d.control?.emergency_stop ?? false;
    this.strategies    = Object.entries(d.strategies ?? {})
                               .map(([id, enabled]) => ({id, enabled}));
    this.activeStrategyLabel = (d.strategies ?? []).find(s => s.selected)?.label ?? '';
    this.riskRejections = d.risk_rejections ?? [];
    this.activityLog    = d.activity ?? [];
    this.llmAvailable   = d.llm?.available ?? false;
    this.llmModel       = d.llm?.model ?? '';
    this.llmBriefing    = d.llm?.briefing ?? null;
    this.llmReflection  = d.llm?.reflection ?? null;
    this._openPositionIds = new Set(d.open_position_ids ?? []);

    this.renderMarkets(d.markets ?? [], d.control?.disabled_markets ?? []);
    this.renderSignals(d.signals ?? []);
    this.renderPositions(d.positions ?? []);
    this.renderApprovals(d.approvals ?? []);
    this.renderChart(d.equity_history ?? []);
}
```

---

## Panel Rendering

Each panel is rendered by injecting HTML strings into a container element via `innerHTML`. This avoids complex `x-for` templating for tables and grids.

### Market Briefing Banner

A persistent banner rendered at the very top of `<main>`, above all other panels. Shown only when `llmBriefing` is non-null (`x-show="llmBriefing"`). Contains:

- **Label:** "Market Briefing" in small caps
- **Overall sentiment pill:** green "▲ Bullish" / red "▼ Bearish" / grey "— Neutral" based on `overall_sentiment` threshold (±0.1)
- **Age + article count:** e.g. "12m ago · 4 articles", right-aligned
- **Key insight:** `llmBriefing.key_insight` as body text — no per-market outlook pills

The full per-market outlook breakdown remains available in the collapsible **LLM Intelligence** panel lower on the page.

---

### Markets Panel (`renderMarkets`)

Displays one row per active market showing the live price and an enable/disable toggle button.

```
BTC/EUR    £85,420.00    [Disable]
ETH/EUR    £1,842.00     [Disable]
```

Toggle buttons call `toggleMarket(market)` which POSTs to `/api/control/markets/{market}/toggle`.

The top header shows **Active Strategy**, populated from the selected strategy in `/api/dashboard`, so the operator can see the current trading logic without opening the Markets panel.

---

### Signals Panel (`renderSignals`)

Displays a rolling buffer of up to 12 signals, newest first. Signals persist across ticks — a signal stays visible until a new signal for the same market replaces it, or until it is pushed out when the buffer reaches 12 entries. Each signal shows:
- Market + direction (color-coded: green for LONG, red for SHORT)
- Confidence as a percentage with a horizontal progress bar
- Thesis text
- Risk status badge (green "Approved" or red "Rejected" with reason)

The entire signal card is clickable (`sig-btn` class, `data-signal-id` attribute) — clicking opens the signal detail modal via the global click delegate. No separate "Detail" button exists.

The buffer is managed in `main.py` via `_SIGNAL_BUFFER_MAX = 12`. On each strategy tick that produces signals, new entries are prepended and any existing entries for the same market are replaced.

#### Desktop Notifications

`init()` calls `Notification.requestPermission()` on page load (no-op if already decided).

`renderSignals()` fires a native browser desktop notification for each signal whose `trade_idea_id` has not been seen before:
- **On first render** — all current signal IDs are seeded into `_seenSignalIds` without firing, preventing a burst of notifications on page load.
- **On subsequent renders** — any signal with an ID not in `_seenSignalIds` triggers a `new Notification(...)` and its ID is added to the set.

Notification format:
- **Title:** `BTC/EUR ▲ LONG — 72% confidence`
- **Body:** Thesis (truncated to 120 chars) + risk approval status
- **Tag:** `trade_idea_id` — ensures the OS deduplicates if the same signal appears in two consecutive polls

---

### Positions Panel (`renderPositions`)

One row per open position. Shows:
- Market + direction
- Size (units held)
- Average entry price
- Unrealised P&L (green if positive, red if negative)
- A "✕ Close" button per row — calls `closePosition(position_id_full)` which confirms with the user then POSTs to `POST /api/positions/{position_id_full}/close`, and refreshes the dashboard, ledger, and closed trades panels on success.

Also renders the "Reset Positions" button if `environment === 'paper'`. This button calls `resetPositions()` which POSTs to `/api/positions/reset`.

---

### Trade Ledger (`renderLedger`)

Fetches `/api/trades` (200 most recent filled order records) and renders a table with columns:

| Column | Content |
|--------|---------|
| Time | UTC time of order fill |
| Market | Trading pair |
| Strategy | Human-readable strategy label derived from the row's `strategy` ID |
| Action | **Buy** when the asset was purchased (long); **Sell** when it was sold (short). An inline badge shows **Open** (green) or **Close** (orange) based on `trade_type` — independent of direction. |
| Direction | **▲ Long** (green) / **▼ Short** (red) |
| Size | Units |
| Price | Fill price |
| Value | `size × price` |
| Source | `auto` / `manual` / `stop-loss` coloured label |
| Status | `filled` (green) or rejection reason (red) |
| Pos ID | First 8 chars of position UUID — same value on paired open and close rows |
| Signal | `📊 Signal` button — opens the signal detail modal. `—` if no signal is linked. |

`trade_type` is determined in `get_trade_ledger()` by a two-pass algorithm: rows arrive newest-first, so iterating and overwriting a `position_id → order_id` map leaves the *oldest* order's id as the value (= the opener). Any other order for that `position_id` is a closer. This correctly handles short-opens (Sell · Open) and long-closes (Buy · Close) without relying on direction alone.

---

### Closed Positions Panel (`renderClosedTrades`)

Fetches `/api/closed-trades` (200 most recent signal outcomes) and renders a table with columns:

| Column | Content |
|--------|---------|
| Time Closed | Exit timestamp (`exit_at`) |
| Time Opened | Entry timestamp (`entry_at`) |
| Duration | Time between open and close, formatted as `Xd Yh`, `Xh Ym`, `Xm Ys`, or `Xs` |
| Market | Trading pair |
| Direction | ▲ Long (green) / ▼ Short (red) |
| Size | Units held |
| Price | Entry price (avg price at open) |
| Value | `size × entry_price` (position value at open) |
| P&L | Absolute P&L (£) and percentage return combined in one cell, coloured green/red |
| Source | Humanised exit reason: `Stop Loss` / `Auto` / `Manual` / `Reset` / `Rejected` |
| Status | Static **Closed** badge |
| Pos ID | First 8 chars of position UUID |
| Open Signal | `📊 Open` button — opens signal detail for the signal that opened the position. `—` if unlinked. |
| Close Signal | `📊 Close` button — opens signal detail for the SHORT signal that triggered the close. `—` for stop-loss, manual, or reset closes. |

Both signal buttons are `sig-btn` elements handled by the global click delegate in `initEventListeners`, so clicking them calls `showSignal(tradeIdeaId)`.

---

### Rejected Trades Register

Fetches `/api/rejected-trades` (100 most recent execution-level rejections) and renders a collapsed-by-default table separate from the trade ledger. Rows show:

| Column | Content |
|--------|---------|
| Time | Rejection timestamp |
| Market | Trading pair |
| Strategy | Human-readable strategy label derived from the row's `strategy` ID |
| Direction | Long/short pill |
| Size | Requested base asset quantity |
| Confidence | Signal confidence percentage, or `--` if unavailable |
| Price | Price used when execution was attempted |
| Value | `size x price` |
| Reason | Rejection reason with underscores shown as spaces |
| Signal | Detail button linked by `trade_idea_id`, if present |

This panel is for intents that never became trades, such as paper insufficient-funds blocks or Kraken submission errors. The main trade ledger remains limited to filled paper orders and accepted live orders.

---

### Price Charts (`loadCharts` / `refreshCharts`)

**Collapsed by default.** Uses the **Lightweight Charts** library to render candlestick charts per market.

Each market gets a single card containing **two side-by-side charts** — 5-min candles on the left and 15-min candles on the right. Charts are created lazily the first time a market appears; subsequent calls to `refreshCharts()` only update the series data.

```
┌─ BTC/EUR ──────────────────────────────────────── £84,201.12 +0.14% ─┐
│  5 MIN                        │  15 MIN                               │
│  [candlestick chart]          │  [candlestick chart]                  │
└───────────────────────────────────────────────────────────────────────┘
```

**Data flow:**
- `_fetchAndRender(market)` fetches `/api/ohlc/{market}?interval=5` and `/api/ohlc/{market}?interval=15` in parallel.
- On first call: builds the market card DOM, creates two chart instances via a shared `makeChart(elId)` factory, and stores `{ chart5, series5, chart15, series15 }` in `_candleCharts[market]`.
- On subsequent calls: calls `series.setData()` on both existing series without rebuilding the DOM.
- A `ResizeObserver` on each chart container keeps the chart width correct when the panel is opened or the window is resized.
- `refreshCharts()` is called on a 60-second timer. The backend only refreshes the OHLC cache every 2 minutes, so most frontend polls return cached data.

---

### Equity Graph

Uses **Chart.js** to render a line chart of total equity over time. Each backend market-data tick records cash plus the current mark-to-market value of held crypto/stock positions into `equity_history`; `/api/dashboard` aligns the final history point with the same `Total Equity` value shown in the header, then the frontend redraws it on the next 5-second dashboard refresh. `_equity_ticker_loop` also records supplemental points from the latest known prices. Startup restores the full 1,440-point in-memory window, so the graph can show roughly 4 hours at 10-second resolution.

The chart updates the existing Chart.js instance with `chart.update('none')`, so polling and service-worker broadcasts repaint without a page reload. Hover uses index mode with `intersect: false`, a larger hit radius, a tooltip with timestamp and equity value, and a custom `equityCursorLine` plugin that draws a vertical guide at the hovered point. The y-axis uses Chart.js autoscaling with `grace: '35%'`, so live refreshes do not mutate resolved Chart.js option proxy objects.

```javascript
renderChart(history) {
    const labels = history.map(p => this._formatEquityTick(p.timestamp));
    const values = history.map(p => Number(p.equity));

    if (!this._chart) {
        const ctx = document.getElementById('equity-chart').getContext('2d');
        this._chart = new Chart(ctx, {
            type: 'line',
            data: { labels, datasets: [{ data: values, pointHitRadius: 12, pointHoverRadius: 4 }] },
            plugins: [this._equityCursorLine()],
            options: {
                interaction: { mode: 'index', intersect: false },
                scales: { y: { grace: '35%' } },
            }
        });
    } else {
        this._chart.data.labels   = labels;
        this._chart.data.datasets[0].data = values;
        this._chart.update('none');   // suppress animation on live update
    }
}
```

---

### LLM Status Card

Two sub-panels rendered from `llmBriefing` and `llmReflection` state properties:

**Market Briefing sub-panel** (news-triggered):
- `key_insight` text
- `overall_sentiment` displayed as a coloured arrow (▲ for positive, ▼ for negative)
- Per-market outlook pills: `BTC/EUR: Bullish +0.70` (green/red/grey depending on bias)
- Article count and generation timestamp

**Hourly Reflection sub-panel**:
- `pattern` — observed trading pattern
- `suggestion` — actionable improvement
- `insight_confidence` as percentage
- Generation timestamp

Both sub-panels show a "Not yet generated" placeholder if the respective value is `null`.

---

### Approvals Panel

Displays the top 3 pending approvals from `d.approvals`. Each card shows:
- Market, direction, confidence %
- Thesis, entry plan, exit plan
- Risk decision badge
- Expiry time countdown
- Approve / Reject buttons

Clicking Approve: POSTs to `/api/approvals/{id}/approve`, refreshes `loadDashboard()`.  
Clicking Reject: POSTs to `/api/approvals/{id}/reject`, refreshes `loadDashboard()`.

A "View All" link navigates to `/approvals` (a separate page with the full approval queue).

---

### Risk Rejections Panel

Displays the last 20 risk rejections from `d.risk_rejections`. Each row shows market, direction, confidence, and the rejection reason. Sourced from the `_risk_rejections` deque in `main.py` (maxlen 50).

---

### Activity Log Panel

Displays the most recent 60 activity log entries in reverse-chronological order (newest at top). Each entry shows:
- Relative timestamp (`timeAgo()` helper — "2m ago", "1h ago", etc.)
- Level badge (green/blue/amber/red)
- Message text
- Detail text (smaller, dimmer — optional)

---

### News Feed Panel

Displays all articles published in the **last 12 hours** in a responsive grid of article cards inside the full-width News Feed panel. No item cap — the count varies with market activity. The panel header shows the article count and last-updated time. Each card shows:
- Source badge (blue for CoinDesk, gold for CoinTelegraph)
- Publication time (`timeAgo()`)
- Title (truncated to 3 lines)
- Clicking the card opens the article URL in a new tab

---

## Control Actions

| Action | Method | API Call |
|--------|--------|---------|
| Emergency stop | `activateStop()` | `POST /api/control/emergency-stop` |
| Resume trading | `resumeBot()` | `POST /api/control/resume` |
| Toggle market | `toggleMarket(market)` | `POST /api/control/markets/{market}/toggle` |
| Select strategy | `selectStrategy(id)` | `POST /api/control/strategies/{id}/select` |
| Close position | `closePosition(positionIdFull)` | `POST /api/positions/{position_id_full}/close` |
| Reset positions | `resetPositions()` | `POST /api/positions/reset` |

All actions call `loadDashboard()` on completion to immediately reflect the new state. `closePosition` additionally refreshes the trade ledger and closed trades panels.

---

## Utility: `timeAgo()`

```javascript
timeAgo(dateStr) {
    const diff = Math.floor((Date.now() - new Date(dateStr + 'Z')) / 1000);
    if (diff < 60)    return `${diff}s ago`;
    if (diff < 3600)  return `${Math.floor(diff/60)}m ago`;
    if (diff < 86400) return `${Math.floor(diff/3600)}h ago`;
    return             `${Math.floor(diff/86400)}d ago`;
}
```

Appends `'Z'` to the date string to force UTC interpretation (the backend emits ISO 8601 timestamps without a timezone suffix).

---

## Layout Structure

Panels are still collapsible and keep the same DOM IDs and API actions, but they no longer sit inside a fixed `1280px` vertical stack. The outer dashboard is:

```html
<main class="app-shell dashboard-shell dashboard-grid">
```

All dashboard areas are stacked vertically in document order. The shell uses the available browser width instead of capping at the old fixed desktop width.

| Area | Layout |
|---|---|
| Market Briefing | Full-width block |
| Mode Banner | Full-width block |
| Equity Graph | Full-width block |
| Markets, Signals, Open Positions, Closed Positions | Responsive equal-height `.core-grid` panels with internal scrolling |
| Pending Approvals | Full-width block |
| Trade Ledger, P&L Summary, Risk Rejections | Full-width blocks |
| Price Charts | Full-width block; only the 5-minute and 15-minute panes may sit side by side |
| News Feed | Full-width panel containing a responsive article-card grid |
| Local LLM, Bot Activity | Full-width blocks; Local LLM fields render as separate `.llm-row` lines |

Tables keep horizontal scrolling inside `.panel-body-scroll`; the page itself should not gain horizontal scroll. Chart panels use `.chart-grid`, `.chart-pair-grid`, and `.chart-pane`, with dimensions controlled by CSS rather than fixed inline pixel heights.
