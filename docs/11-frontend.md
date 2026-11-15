# Frontend Architecture

**Files:** `frontend/index.html`, `frontend/approvals.html`, `frontend/static/styles.css`

A single-page application served directly by the Go HTTP server (`internal/server`), from the directory given by the `-frontend` flag (default `frontend`). Uses **Alpine.js** for reactive UI state, chart libraries from CDNs, and a shared local stylesheet at `/static/styles.css` for theme, utility classes, responsive layout, panels, buttons, grids, tables, and charts. No build step, bundler, or Node.js toolchain is required.

The primary dashboard remains available at `/` and uses the `layout-ledger-glass` workspace promoted from the former trial layouts. The `/index2` through `/index9` trial theme shells have been removed, so the dashboard has a single maintained HTML entry point with the shared Alpine component, DOM IDs, API calls, and stylesheet.

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
- `.core-grid` is a responsive status workspace: Markets, P&L Summary, and Signals are equal 1/3-width panels until the small-mobile breakpoint, use the same fixed panel height, and scroll internally when content is taller than the row.
- `.approval-details` and `.chart-grid` are vertical stacks so each block uses the full available width.
- `.news-grid` is a responsive article-card grid inside the full-width News Feed panel.
- `.chart-pair-grid` is the only content grid that may place items side by side; it is reserved for the 5-minute and 15-minute chart panes inside each market chart card.
- `.panel`, `.panel-header`, `.panel-body`, `.responsive-table`, `.trade-table`, `.activity-list`, `.core-panel`, `.core-scroll`, `.llm-status`, and `.llm-row` standardise panel structure while preserving existing Alpine state and DOM IDs.
- `.chart-pane` and `.equity-chart-shell` use `clamp()` so widescreen users get larger charts while mobile users avoid horizontal page scroll.
- `.modal-backdrop` (fixed, full-screen flex box) and `.modal-panel` centre every dialog: trading settings, Settings and the signal detail (`.signal-panel`, 820 px wide).
- Elements toggled with `x-show` must not set `display` in an inline `style`: when Alpine shows an element it removes the inline `display` property, so an inline `display:flex` turns into `display:block`. Put the layout in a CSS class instead (for example `.modal-backdrop`, `.live-indicator`).
- `.area-charts` and `.area-news` share `--news-chart-panel-height`; Price Charts starts expanded, both panels are three times taller than the previous chart/news height, Price Charts scrolls vertically inside the panel, and the 5-minute and 15-minute panes stay side by side until the mobile breakpoint.

Breakpoint coverage used for layout verification:

| Viewport | Expected layout |
|---|---|
| `390x844` | Single-column dashboard, wrapped header, no page-level horizontal scroll |
| `768x1024` | Markets, P&L Summary, and Signals remain equal thirds; other major panels stack full-width |
| `1366x768` | Full-width vertical panels with in-panel table scrolling |
| `1920x1080` | Main content uses the monitor width; blocks remain stacked vertically |
| `2560x1440` | Dashboard still uses the available width, with readable gutters and full-width blocks |

The CSS keeps the existing theme palette and visual treatment. Functional hooks such as `equity-chart`, `markets-list`, `signals-list`, `positions-list`, `approvals-queue`, `ledger-body`, `closed-body`, `candle-grid`, and `news-grid` are intentionally retained.

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

        // Connection state (stale banner)
        connectionLost:       false,
        lastDashboardAt:      null,  // Date of the last applied dashboard response
        _dashboardRequestSeq: 0,     // numbers every dashboard request
        _dashboardAppliedSeq: 0,     // number of the response currently shown

        // Equity chart instance (not reactive — managed imperatively)
        _chart:        null,
        _chartMarkets: [],   // markets in the current market list (chart card order)
        _liveMarkets:  new Set(),    // markets toggled live

        // --- Methods ---
        init() { ... },
        ...
    }
}
```

---

## Service Worker Caching

`/sw.js` uses a versioned cache (`trading-bot-v<app version>`, bumped with every release) and claims open tabs as soon as the new worker activates. Navigation and other `text/html` requests are network-first, so the dashboard shell does not keep running stale inline scripts after frontend fixes; non-HTML static assets use stale-while-revalidate.

API responses are never cached and never broadcast. The page applies the response of its own request, so each dashboard poll is applied exactly once per tab and one tab's poll cannot overwrite another tab's state. When an API request to `/api/dashboard`, `/api/trades`, `/api/closed-trades`, `/api/rejected-trades` or `/api/news` fails because the bot is unreachable, the worker answers `503` (not the last cached data) and posts `{ type: 'API_OFFLINE', endpoint, ts }` to the requesting page only.

## Connection Lost (Stale Data)

If the bot stops answering, the page keeps the last data it received but makes clear that it is old:

- A red banner under the header reads *Connection to the bot lost — showing data from HH:MM* (or *no data has been received yet*), the main area is dimmed (`.is-stale`), and the green **Live** indicator next to Equity Graph is hidden.
- Three triggers set `connectionLost`: the service worker's `API_OFFLINE` message for `/api/dashboard`, a failed or non-OK dashboard fetch, and `_checkStale()` (every 5 s) when the last applied dashboard response is older than `STALE_AFTER_MS` (20 s). The time-based check also works without a service worker and catches requests that hang.
- While stale, every action (resume, market on/off, per-market live, close position, forget live position, approve/reject, reset, trading mode, paper/live, strategy) first asks *Send the request anyway?* via `_actionAllowedWhileStale()`. The emergency stop is not delayed by this extra question.
- The next successful dashboard response clears the banner (`_markConnected()`).

Overlapping dashboard requests (the 5-second poll plus the refresh after an action) are numbered; a response older than the one already shown is dropped, so a slow pre-change response cannot overwrite a newer one.

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
init() {
    this._initialLoad();   // awaits loadDashboard(), then settings, news, ledger, closed trades, P&L, rejected trades
    setInterval(() => this.loadDashboard(),      5_000);
    setInterval(() => this._checkStale(),        5_000);
    setInterval(() => this.loadNews(),         300_000);
    setInterval(() => this.loadLedger(),        15_000);
    setInterval(() => this.loadClosedTrades(),  30_000);
    setInterval(() => this.loadPnlSummary(),    30_000);
    setInterval(() => this.refreshCharts(),     60_000);
    setInterval(() => this.loadRejectedTrades(), 30_000);
}
```

The dashboard is loaded first so the currency symbol of the configured base currency is known before the ledger, closed trades and P&L summary render (the default `€` is only a placeholder).

---

## Dashboard Load Cycle (`loadDashboard`)

Called every 5 seconds and after each action. Fetches `/api/dashboard`; when the response is newer than the one shown, `_applyDashboard(d)` updates all reactive state properties and triggers `renderMarkets`, `renderSignals`, `renderPositions`, `renderApprovals`, `renderChart` and `_syncChartCards`. A failed request marks the connection as lost (see above). The sketch below shows the fields used; see `_applyDashboard` for the exact code.

```javascript
_applyDashboard(d) {

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

Because the strings become HTML, three rules apply to every panel (`TestFrontendSafety` in `internal/server/frontend_safety_test.go` checks them):

- **Escape server text.** Anything that came from the API (news headlines and feed names, LLM theses, market names, reasons) goes through `esc()`, which escapes `& < > " '`. News headlines and LLM text are written by third parties, so unescaped they could run script next to the Approve buttons.
- **Links go through `safeUrl()`**, which returns the URL only when it starts with `http://` or `https://`, otherwise `#`.
- **No server text in inline handlers.** Buttons and chips in generated rows carry their target in `data-*` attributes, and one click listener registered in `init()` dispatches them. The same rules apply to `approvals.html`.

Actions that change the bot (emergency stop, resume, approve, reject, reset, settings) wait for the server's answer, check it, and tell the operator when it failed or did not execute. The emergency-stop state in particular is shown only after the server confirms it.

### Market Briefing Banner

A persistent banner rendered at the very top of `<main>`, above all other panels. Shown only when `llmBriefing` is non-null (`x-show="llmBriefing"`). Contains:

- **Label:** "Market Briefing" in small caps
- **Overall sentiment pill:** green "▲ Bullish" / red "▼ Bearish" / grey "— Neutral" based on `overall_sentiment` threshold (±0.1)
- **Age + article count:** e.g. "12m ago · 4 articles", right-aligned
- **Key insight:** `llmBriefing.key_insight` as body text — no per-market outlook pills

The full per-market outlook breakdown remains available in the collapsible **LLM Intelligence** panel lower on the page.

---

### Markets Panel (`renderMarkets`)

Displays one row per active market showing the live price (from `price_value`, formatted by `fmtPrice`) and an enable/disable toggle button.

```
BTC/EUR           €85,420.00             [On]
SHIB/EUR  LIVE    €0.00001234   [Live]   [On]     ← live environment only
PEPE/EUR          €0.000008500  [Paper]  [On]
```

The On/Off buttons call `toggleMarket(market)` which POSTs to `/api/control/markets/{market}/toggle`.

In the **live** environment each row also has a **Live / Paper** button and markets toggled live carry a red **LIVE** badge (the `live` flag of each market row). The button (`data-toggle-live`, dispatched by the delegated click listener) calls `toggleMarketLive(market)`, which asks for confirmation naming the market — turning live on says that real orders with real money will be sent to Kraken for it — and then POSTs to `/api/control/markets/{market}/live-toggle`. A refusal or network error is shown as an alert. In the paper environment the button is hidden, because live-toggled markets trade on paper there anyway.

The top header shows **Active Strategy**, populated from the selected strategy in `/api/dashboard`, so the operator can see the current trading logic at a glance. The strategy is changed in the **Trading settings** dialog (below).

---

### Signals Panel (`renderSignals`)

Displays a rolling buffer of up to 12 signals, newest first. Signals persist across ticks — a signal stays visible until a new signal for the same market replaces it, or until it is pushed out when the buffer reaches 12 entries. Each signal shows:
- Market + direction (color-coded: green for LONG, red for SHORT)
- Confidence as a percentage with a horizontal progress bar
- Thesis text
- Risk status badge (green "Approved" or red "Rejected" with reason)

The entire signal card is clickable (`sig-btn` class, `data-signal-id` attribute) — clicking opens the signal detail modal via the global click delegate. No separate "Detail" button exists.

The buffer is managed in `internal/app` via `signalBufferMax = 12`. On each strategy tick that produces signals, `mergeSignals()` prepends the new entries and drops any existing entries for the same market.

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

Full-width table with one single-line row per open position. Shows:

| Column | Content |
|--------|---------|
| Date Time | Position open timestamp from `opened_at`, falling back to the matching ledger open trade timestamp |
| Opened | Elapsed time since open |
| Market | Trading pair; a live position (`environment === 'live'`, a real Kraken holding) also carries a blue **LIVE** badge |
| Direction | ▲ Long (green) / ▼ Short (red) |
| Size | Units held |
| Avg Price | Average entry price (`avg_price_value`, adaptive precision) |
| Value | `size × avg_price`, 2 decimals |
| Unrealised P&L | Current mark-to-market P&L, coloured green/red |
| Strategy | Human-readable strategy label from the position payload or matching opening ledger row |
| Source | Opening source from the position payload or matching opening ledger row |
| Status | The position's `status`: **open**, or for a live position **entry pending** / **exit pending** (gold) while its Kraken buy or sell is open |
| Pos ID | First 8 chars of position UUID |
| Signal | `📊 Signal` button when linked |
| Action | `✕ Close` button — calls `closePosition(position_id_full)` which confirms with the user then POSTs to `POST /api/positions/{position_id_full}/close`, and refreshes the dashboard, ledger, and closed trades panels on success. A paper close reports the fill price and cash; a live close (`status: "closing"`) says the sell order was sent and the P&L appears once Kraken confirms the fill. The button is disabled while a live position's entry or exit is pending. Live positions also get a grey **Forget** button (`data-forget-position`, delegated click → `forgetPosition(position_id_full)`): after a `confirm()` that says no order is sent and the Kraken holding is untouched, it POSTs `/api/positions/{position_id_full}/forget` and refreshes the dashboard; an error shows the server's `detail` in an alert. It is for a holding already sold on Kraken or too small to sell |

`decideApproval()` (and `decide()` on the approvals page) explain an approval that did not execute: `503` means nothing was sent and the approval is still pending (no price, or the emergency stop); `409` shows the risk-check refusal detail; a `rejected` order names its `environment`.

Also renders the "Reset Positions" button if `environment === 'paper'`. This button calls `resetPositions()` which POSTs to `/api/positions/reset`.

The panel uses the same market and direction filter chip controls as Closed Positions. Rows use subtle alternate contrast through the shared `.trade-table` styling.

---

### Trade Ledger (`renderLedger`)

Fetches `/api/trades` (200 most recent filled order records) and renders a table with columns. Rows use subtle alternate contrast through the shared `.trade-table` styling:

| Column | Content |
|--------|---------|
| Date Time | UTC date and time of order fill |
| Market | Trading pair |
| Action | **Buy** when the asset was purchased (long); **Sell** when it was sold (short). An inline badge shows **Open** (green) or **Close** (orange) based on `trade_type` — independent of direction. |
| Direction | **▲ Long** (green) / **▼ Short** (red) |
| Size | Units |
| Price | Fill price (adaptive precision) |
| Value | `size × price` |
| P&L | Realised close-order P&L and percentage, or `--` |
| Strategy | Human-readable strategy label derived from the row's `strategy` ID |
| Source | `auto` / `manual` / `stop-loss` coloured label |
| Status | `filled` (green) or rejection reason (red) |
| Pos ID | First 8 chars of position UUID — same value on paired open and close rows |
| Signal | `📊 Signal` button — opens the signal detail modal. `—` if no signal is linked. |

`trade_type` is determined in `get_trade_ledger()` by a two-pass algorithm: rows arrive newest-first, so iterating and overwriting a `position_id → order_id` map leaves the *oldest* order's id as the value (= the opener). Any other order for that `position_id` is a closer. This correctly handles short-opens (Sell · Open) and long-closes (Buy · Close) without relying on direction alone.

---

### Closed Positions Panel (`renderClosedTrades`)

Fetches `/api/closed-trades` (200 most recent signal outcomes) and renders a table with columns. Rows use subtle alternate contrast through the shared `.trade-table` styling:

| Column | Content |
|--------|---------|
| Date Time Closed | Exit timestamp (`exit_at`) |
| Date Time Opened | Entry timestamp (`entry_at`) |
| Duration | Time between open and close, formatted as `Xd Yh`, `Xh Ym`, `Xm Ys`, or `Xs` |
| Market | Trading pair |
| Direction | ▲ Long (green) / ▼ Short (red) |
| Size | Units held |
| Price | Entry price (avg price at open, adaptive precision) |
| Value | `size × entry_price` (position value at open) |
| P&L | Absolute P&L (£) and percentage return combined in one cell, coloured green/red |
| Strategy | Human-readable strategy label derived from the row's `strategy` ID |
| Source | Humanised exit reason: `Stop Loss` / `Auto` / `Manual` / `Reset` / `Rejected`; other reasons with underscores as spaces; `—` when the reason is missing |
| Status | Static **Closed** badge |
| Pos ID | First 8 chars of position UUID |
| Open Signal | `📊 Open` button — opens signal detail for the signal that opened the position. `—` if unlinked. |
| Close Signal | `📊 Close` button — opens signal detail for the SHORT signal that triggered the close. `—` for stop-loss, manual, or reset closes. |

Both signal buttons are `sig-btn` elements handled by the global click delegate in `initEventListeners`, so clicking them calls `showSignal(tradeIdeaId)`.

---

### Rejected Trades Register

Fetches `/api/rejected-trades` (100 most recent execution-level rejections) and renders a collapsed-by-default zebra-striped table separate from the trade ledger. Rows show:

| Column | Content |
|--------|---------|
| Date Time | Rejection timestamp |
| Market | Trading pair |
| Direction | Long/short pill |
| Size | Requested base asset quantity |
| Price | Price used when execution was attempted |
| Value | `size x price` |
| Strategy | Human-readable strategy label derived from the row's `strategy` ID |
| Source | Rejection source, defaulting to `system` when the API has no source |
| Status | Static **Rejected** badge |
| Reason | Rejection reason with underscores shown as spaces |
| Confidence | Signal confidence percentage, or `--` if unavailable |
| Signal | Detail button linked by `trade_idea_id`, if present |

This panel is for intents that never became trades, such as paper insufficient-funds blocks or Kraken submission errors. The main trade ledger remains limited to filled paper orders and accepted live orders.

---

### Price Charts (`_syncChartCards` / `refreshCharts`)

**Expanded by default.** Uses the **Lightweight Charts** library to render candlestick charts per market. The panel has the same fixed height as Crypto News, both are three times taller than before, and Price Charts scrolls internally when there are more market cards than fit on screen.

Each market gets a single card containing **two side-by-side charts** — 5-min candles on the left and 15-min candles on the right. With up to 50 markets, charts are only built and refreshed for cards that are on screen.

```
┌─ BTC/EUR ──────────────────────────────────────── £84,201.12 +0.14% ─┐
│  5 MIN                        │  15 MIN                               │
│  [candlestick chart]          │  [candlestick chart]                  │
└───────────────────────────────────────────────────────────────────────┘
```

**Lifecycle and data flow:**
- Chart state is kept outside Alpine in two `Map`s (so the chart objects are not wrapped in reactive proxies): `candleCharts` (market → `{ card, visible, chart5, series5, chart15, series15, resizers }`) and `chartRequests` (market → the request in flight).
- Every applied dashboard response calls `_syncChartCards(symbols)`: an empty card is added for each new market, and for each market that left the list the charts are destroyed (`chart.remove()`), their `ResizeObserver`s disconnected and the card removed.
- An `IntersectionObserver` marks cards visible or hidden. The first time a card becomes visible its candles are fetched and its two charts created.
- `_loadChart(market)` allows one request per market: a second call while one is running shares the same promise, so a first render can never append two cards.
- `_fetchAndRender(market)` fetches `/api/ohlc/{market}?interval=5` and `?interval=15` in parallel, checks both responses are OK, and ignores the result if the market was removed meanwhile. Candles go through `toCandleSeries`, which keeps one candle per timestamp and sorts by time (Lightweight Charts rejects unsorted or repeated times).
- Each series' `priceFormat` is set to `{ type: 'price', precision, minMove }` from the last price (`priceDecimals`), so sub-cent markets get a readable price scale instead of a flat line at 0.00. The card header shows the price with `fmtPrice` and the change in %.
- `refreshCharts()` runs every 60 seconds and refreshes only visible cards, one every `CHART_REFRESH_GAP_MS` (250 ms) instead of all at once; a refresh still running is not started twice. The backend only refreshes the OHLC cache every 2 minutes, so most frontend polls return cached data.

---

### Price Formatting

Prices and money amounts are formatted differently:

- **Prices** (market list, Avg Price, ledger/closed/rejected Price, chart header and price scale, the close-position alert) use `fmtPrice(value)`: 2 decimals at or above 1, otherwise enough decimals for 4 significant digits (`priceDecimals`, e.g. `0.0000085` → `0.000008500`). The dashboard sends the raw numbers as `markets[].price_value` and `positions[].avg_price_value`; the text fields `price` and `avg_price` use the same rule on the server.
- **Money** (equity, cash, value, P&L) keeps 2 decimals.

---

### Equity Graph

Uses **Chart.js** to render a line chart of total equity over time. Each backend market-data tick records cash plus the current mark-to-market value of held crypto/stock positions into `equity_history`; `/api/dashboard` aligns the final history point with the same `Total Equity` value shown in the header, then the frontend redraws it on the next 5-second dashboard refresh. `_equity_ticker_loop` also records supplemental points from the latest known prices. Startup restores the full 1,440-point in-memory window, so the graph can show roughly 4 hours at 10-second resolution.

The chart updates the existing Chart.js instance with `chart.update('none')`, so each dashboard poll repaints without a page reload. Hover uses index mode with `intersect: false`, a larger hit radius, a tooltip with timestamp and equity value, and a custom `equityCursorLine` plugin that draws a vertical guide at the hovered point. The y-axis uses Chart.js autoscaling with `grace: '35%'`, so live refreshes do not mutate resolved Chart.js option proxy objects.

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

Displays the last 20 risk rejections from `d.risk_rejections`. Each row shows market, direction, confidence, and the rejection reason. Sourced from the in-memory `riskRejections` list in `internal/app` (capped at 50).

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
| Toggle market live (live environment only) | `toggleMarketLive(market)` (confirm dialog naming the market first) | `POST /api/control/markets/{market}/live-toggle` |
| Select strategy | `selectStrategy(id)` (confirm dialog first) | `POST /api/control/strategies/{id}/select` |
| Change trading mode | `setTradingMode(mode)` (confirm dialog first) | `POST /api/control/trading-mode` |
| Switch paper / live | `setTradingEnvironment(env)` (typed `LIVE` to go live, confirm dialog to go back to paper) | `POST /api/control/trading-environment` |
| Load settings | `loadSettings()` (on start-up and when the dialog opens) | `GET /api/settings` |
| Save settings | `saveSettings()` (confirm dialog listing every change first) | `POST /api/settings` |
| Close position | `closePosition(positionIdFull)` | `POST /api/positions/{position_id_full}/close` |
| Forget live position | `forgetPosition(positionIdFull)` (confirm dialog first; no order is sent) | `POST /api/positions/{position_id_full}/forget` |
| Reset positions | `resetPositions()` | `POST /api/positions/reset` |

The PAPER/LIVE and mode badges in the header are one button that opens the **Trading settings** dialog (`#trading-settings`, `openTradingSettings()`). The dialog has three sections, in order: **Trading mode**, **Strategy** and **Money**. A strategy card shows the label split into a name and a description (`_strategyLabelParts()`, from `Name (description)`). Nothing changes on a single click: each mode button, each strategy and the Paper button ask `confirm()` first, and the Live button opens a red panel whose "Switch to live" button stays disabled until `LIVE` is typed. A refused change (for example missing Kraken API keys) shows the API's `detail` message in the dialog (`tradingSettingsError`). The mode panel's **Change trading mode** button opens the same dialog.

### Settings Dialog

**⚙ Settings** in the header opens `#settings-dialog` (`openSettings()`), which reloads `GET /api/settings` and fills a draft. The dialog is a fixed-height panel:

- a header with the *Live* / *Restart* legend and, while anything is pending, a *Restart the bot to apply* strip;
- a **side navigation** (`.settings-nav`) listing the groups (`settingsGroups` getter); the selected one is `settingsGroup`, and a gold dot marks groups with unsaved changes (`settingsGroupChanged()`). On narrow screens it becomes a scrolling row of tabs;
- the selected group's settings, one **row** per setting (`.setting-row`): label, *Live*/*Restart* tag, *Restart pending* tag and help text on the left (`.setting-text`), the control on the right (`.setting-control`, 300 px). List settings span the full width;
- a footer with the unsaved-change count, **Undo changes** and **Save…**.

`loadSettings()` fills the draft (`resetSettingsDraft(data.fields)`) before it sets `settingsFields`, so list rows never render without a draft array. Neutral buttons (Add, Remove all, Clear/Keep, Undo changes) use `.btn-secondary`, which follows the theme colours.

| Kind | Control |
|---|---|
| `int`, `float` | Narrow right-aligned text box (`is-number`; the server validates numbers and ranges) |
| `string` | Text box |
| `bool` | Switch (`role="switch"`, `aria-checked`) |
| `list` | Removable chips (`.chip-list`, **×** calls `removeListItem()`) plus an add box (`addListItem()`, Enter or **Add**; market symbols are upper-cased, duplicates ignored), an entry count and **Remove all**. News feeds show the name part of `Name::URL` on the chip, with the full entry as its tooltip |
| secret | `type="password"`, `autocomplete="new-password"`, never pre-filled. Placeholder *Set — type to replace* / *Not set*; **Clear** / **Keep** toggles clearing it |

State: `settingsFields` (from the API), `settingsDraft` (key → edited value: arrays for lists, `''` for an untouched secret), `settingsClear` (secrets to clear), `settingsGroup`, `settingsRestartRequired`, `settingsError`, `settingsNotice`, `settingsSaving`. `settingsChanges()` compares the draft with the saved values and returns only the changed keys; the footer shows the count, **Undo changes** (`resetSettingsDraft()`) and **Save…**.

`saveSettings()` asks `confirm()` with one line per change (`label: old → new (applies now|after restart)`; lists show only what was added `+` and removed `−`; secrets only say *new value* or *cleared*) before posting. A refusal shows the API's `detail` (`settingsError`); success shows which keys are in use now, reloads the settings and refreshes the dashboard. Closing the dialog with unsaved changes asks before discarding them.

While `settingsRestartRequired` is non-empty, a `.restart-banner` under the header lists the keys: *Settings saved. Restart the bot to apply: …*.

All actions call `loadDashboard()` on completion to immediately reflect the new state. `closePosition` additionally refreshes the trade ledger and closed trades panels.

---

## Utility: `timeAgo()`

```javascript
// Takes a Date; callers pass parseUtc(serverTimestamp).
timeAgo(date) {
    const s = Math.floor((Date.now() - date) / 1000);
    if (s < 60)    return 'just now';
    if (s < 3600)  return Math.floor(s / 60) + 'm ago';
    if (s < 86400) return Math.floor(s / 3600) + 'h ago';
    ...
}
```

Every server timestamp is parsed with `parseUtc()`, never a bare `new Date(...)`. Stored timestamps are ISO 8601 in UTC without a zone suffix, which a browser would read as local time; `parseUtc()` appends `'Z'` when the string has no zone or offset and leaves strings that already carry one (live equity points) alone.

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
| Markets, P&L Summary, Signals | Responsive equal-height `.core-grid` panels with internal scrolling |
| Open Positions | Full-width table panel |
| Closed Positions | Full-width table panel |
| Pending Approvals | Full-width block |
| Trade Ledger, Risk Rejections | Full-width blocks |
| Price Charts | Full-width block; only the 5-minute and 15-minute panes may sit side by side |
| News Feed | Full-width panel containing a responsive article-card grid |
| Local LLM, Bot Activity | Full-width blocks; Local LLM fields render as separate `.llm-row` lines |

Tables keep horizontal scrolling inside `.panel-body-scroll`; the page itself should not gain horizontal scroll. Chart panels use `.chart-grid`, `.chart-pair-grid`, and `.chart-pane`, with dimensions controlled by CSS rather than fixed inline pixel heights.
