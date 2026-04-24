# Frontend Architecture

**File:** `frontend/index.html`

A single-page application served directly by FastAPI via `StaticFiles`. Uses **Alpine.js** for reactive UI state and **Tailwind CSS** (CDN) for styling. No build step, bundler, or Node.js toolchain is required.

---

## Technology Stack

| Library | Version | Purpose |
|---------|---------|---------|
| Alpine.js | 3.x (CDN) | Reactive component state and DOM binding |
| Tailwind CSS | 3.x (CDN) | Utility-class styling |
| Chart.js | 4.x (CDN) | Equity curve line chart |
| Lightweight Charts | 4.x (CDN) | Candlestick price charts per market |

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

## Polling Architecture

The frontend polls the backend on fixed intervals set up in `init()`. There is no WebSocket or server-sent event stream — all updates are pull-based.

| Interval | Data | Endpoint |
|----------|------|----------|
| 5 seconds | Prices, signals, positions, equity, approvals, activity, LLM state | `GET /api/dashboard` |
| 15 seconds | Full trade ledger | `GET /api/trades` |
| 30 seconds | Closed trades | `GET /api/closed-trades` |
| 300 seconds | News feed | `GET /api/news` |
| 60 seconds | Price candlestick charts (both intervals) | `GET /api/ohlc/{market}?interval=5` and `?interval=15` |

```javascript
async init() {
    await this.loadDashboard();
    await this.loadNews();
    setInterval(() => this.loadDashboard(),    5_000);
    setInterval(() => this.renderLedger(),    15_000);
    setInterval(() => this.loadClosedTrades(), 30_000);
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

### Portfolio Value

Uses **Chart.js** to render a line chart of total portfolio value over time. The data source is `equity_history` from `/api/dashboard` — up to 1 440 data points (4 hours at 10-second resolution, supplied by `_equity_ticker_loop`). The chart calls `chart.update('none')` on each live refresh to skip animation overhead and repaint immediately.

```javascript
renderChart(history) {
    const labels = history.map(p => new Date(p.timestamp).toLocaleTimeString());
    const values = history.map(p => p.equity);

    if (!this._chart) {
        const ctx = document.getElementById('equityChart').getContext('2d');
        this._chart = new Chart(ctx, {
            type: 'line',
            data: {
                labels,
                datasets: [{ label: 'Equity', data: values, borderColor: '#10B981', ... }]
            },
            ...
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

Displays all articles published in the **last 12 hours** in a 3-column grid. No item cap — the count varies with market activity. The panel header shows the article count and last-updated time. Each card shows:
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

Panels are ordered top to bottom as follows. All panels are collapsible — clicking the panel header row toggles the body open or closed. **Risk Rejections** and **Price Charts** are collapsed by default; all others open.

```
┌─────────────────────────────────────────────────────────┐
│  Header: Total Equity | Available Cash | Mode |         │
│          Environment | Emergency Stop                   │
├─────────────────────────────────────────────────────────┤
│  Mode Banner                                            │
├─────────────────────────────────────────────────────────┤
│  Portfolio Value (Chart.js)                             │
├──────────────┬──────────────────────────────────────────┤
│  Markets     │                                          │
│  Signals     │  (same row)                              │
│  Positions   │                                          │
├──────────────┴──────────────────────────────────────────┤
│  Pending Approvals                                      │
├─────────────────────────────────────────────────────────┤
│  Trade Ledger (OPEN/CLOSED badges with P&L)             │
│  Closed Trades (entry/exit/P&L%)                        │
│  Risk Rejections  [collapsed by default]                │
├─────────────────────────────────────────────────────────┤
│  Price Charts (5min+15min side by side) [collapsed]     │
│  News Feed (last 12 hours, 3-column grid)               │
│  Local LLM Status (Briefing + Reflection)               │
│  Bot Activity Log                                       │
└─────────────────────────────────────────────────────────┘
```
