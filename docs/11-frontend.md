# Frontend Architecture

**Files**

| File | Purpose |
|---|---|
| `frontend/index.html` | The dashboard: header, navigation, the markup of every view, the dialogs and the toast stack. No inline script. |
| `frontend/static/app/core.js` | Shared helpers, the Alpine root component (`dashboard()`), navigation and hash routing, polling, connection state, header actions, toasts and dialog focus. |
| `frontend/static/app/overview.js` | Overview view: key figures, equity graph, approvals. |
| `frontend/static/app/markets.js` | Markets view: market table, filters, toggles and the selected market's candle charts. |
| `frontend/static/app/trading.js` | Trading view: positions, closed positions, ledger, rejected trades, risk rejections, P&L; position actions. |
| `frontend/static/app/intelligence.js` | Intelligence view: briefing, reflection, signals and the signal detail dialog, news, learning summary, LLM shadow reviews. |
| `frontend/static/app/activity.js` | Activity view: activity log and system health. |
| `frontend/static/app/settings.js` | The Trading settings and Settings dialogs (opened from the header). |
| `frontend/static/app/backtests.js` | Backtests view: read-only list of backtest results and a detail page per run (see [Backtests view](#backtests-view)). |
| `frontend/static/styles.css` | The design system: tokens, components, view layouts, breakpoints. |
| `frontend/approvals.html` | Stand-alone approvals page (plain script, same styles). |
| `frontend/login.html` | Access-token form (only `styles.css`, which is served without a session). |
| `frontend/sw.js` | Service worker. |

The pages are served by the Go HTTP server (`internal/server`) from the `-frontend` directory (default `frontend`); `/static/` serves `frontend/static/` including `static/app/`. There is no build step, bundler or Node.js toolchain.

| Library | Version | Purpose |
|---|---|---|
| Alpine.js | 3.14.1 (CDN) | Reactive state and DOM binding |
| Chart.js | 4.4.1 (CDN) | Equity graph |
| Lightweight Charts | 4.1.3 (CDN) | Candlestick charts of the selected market |

No web fonts and no CSS framework: plain CSS with a system font stack, and icons as an inline SVG sprite at the top of `index.html` (`<use href="#i-…">`).

---

## Layout

```
┌ Header: brand · version │ PAPER/LIVE · mode ⚙ │ Live / Connection lost │ LLM · model │ Approvals n │ Settings │ Emergency stop ┐
├ Nav ──────┬ Banners (connection lost · emergency stop · warm-up · restart needed) ────────────────────────────────────────────┤
│ Overview  │                                                                                                                  │
│ Markets   │   The active view (#/overview, #/markets/BTC-EUR, #/trading/ledger, …)                                         │
│ Trading   │                                                                                                                  │
│ Intellig. │                                                                                                                  │
│ Backtests │                                                                                                                  │
│ Activity  │                                                                                                                  │
└───────────┴──────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘
```

**Header (always visible).** The PAPER / LIVE and mode badges are one button that opens Trading settings. The connection state shows *Live* while dashboard responses arrive and *Connection lost* when they stop (the *Live* indicator is hidden while stale). The LLM dot is green (available), amber (unavailable) or grey (not configured) with the model name. *Approvals n* links to `#/overview/approvals`. Settings opens the Settings dialog. Emergency stop / Resume is always the last control. In the live environment the header gets an orange top line. The LLM model name may be shortened; its tooltip and Activity › LLM show it in full.

**Phone header (under 768 px): two rows.** Row 1: brand and the Emergency stop (full label, never hidden or shrunk). Row 2: one strip with PAPER / LIVE and the mode, the connection state, the LLM status dot, the approvals bell with its count, and Settings on the right. The version and the LLM model name are hidden there (both are in Activity); *Approvals* and *· real money* stay readable by screen readers (`.sr-only-sm`). The orange LIVE pill and the orange top line keep live trading unmistakable. Implementation: the header becomes a grid (`"brand stop stop" / "status status settings"`) and `.hdr-actions` uses `display: contents`, so Settings and the stop button are placed separately.

**Views** (`#/<view>/<argument>`; an unknown hash becomes `#/overview`):

| View | Contents |
|---|---|
| Overview | Bot status strip (running / stopped / warming up, environment, mode explained, *Change trading mode*). Key figures: total equity with change over the graph's window, available cash, realised P&L today (UTC), open positions with unrealised total, pending approvals, active strategy (the name without its description, wrapped onto a second line at a smaller size instead of cut off; the description is on Intelligence). Equity graph. Open positions (compact, with Close / Forget). Pending approvals with Approve / Reject. Latest 5 signals. Last 5 activity events (details clamped like the Activity view). `#/overview/approvals` scrolls to the approvals panel. |
| Markets | Searchable market table with filter chips (all, trading on/off, with position, with signal, live) and, per row, price, current signal, position P&L, the trading switch and (live environment) the live switch. Beside it (below it under 1440 px) the selected market's detail: price and change, both switches, the 5-minute and 15-minute candle charts, the position (with Close), the latest signal (with detail) and the LLM outlook. The market is in the hash: `#/markets/BTC-EUR`. From 768 px the first market is selected automatically; between 768 and 1439 px choosing a market scrolls the detail into view when it is off screen. **Phones (under 768 px)** use a list → detail flow: `#/markets` shows the list alone (Signal and Position fold into one line under the symbol so the trading switch fits) and nothing is selected; tapping a market opens `#/markets/<slug>` with only its detail and a *← Markets* control. That control (and the browser's Back button) returns to the list where it was scrolled; the charts are removed when the detail is left. |
| Trading | Tabs, each in the hash (`#/trading/<tab>`): Open positions (`positions`), Closed positions (`closed`), Trade ledger (`ledger`), Rejected trades (`rejected`), Risk rejections (`risk`), P&L summary (`pnl`). The view header has *Export closed trades (CSV)* (`/api/export/trades.csv`). Open positions, closed positions and the ledger have market and direction filter chips. Paper mode shows *Reset paper positions*. Every loaded table (closed positions, ledger, rejected trades, both P&L summary tables) shows *Loading…*, its load error and its empty message in the same empty-row style. |
| Intelligence | In this order: the market briefing beside the signals (stacked under 1200 px), so signals are on the first screen at 1440 × 900; strategy selection (same confirmation as Trading settings) beside the hourly reflection; the learning summary per strategy, market and direction; the LLM shadow reviews (`GET /api/shadow-reviews`, refreshed every minute while the view is shown): per strategy the entries reviewed, would-vetoes, scored trades, veto accuracy against the random veto and the break-even, what the vetoes would have saved (green) or cost (red) and a verdict pill, then the newest 20 reviews with the LLM's answer, the trades and P&L under it, and its reasoning; news of the last 12 hours with source filter chips. The briefing shows its text, a count line (*9 bearish · 18 bullish · 28 neutral*) and the market outlooks bearish first, then bullish, then neutral (alphabetical within each; shades such as `mildly_bullish` count with their direction). Nine are shown (three columns, one on phones); *Show all N outlooks* / *Show fewer* toggles the rest and resets when the view is left. Each signal opens the signal detail dialog. |
| Backtests | List of the backtester's result folders (`#/backtests`) and one run's detail (`#/backtests/<id>`). Read-only; see [Backtests view](#backtests-view). |
| Activity | Activity log (last 60 events) with level filter chips and counts; each entry's detail is clamped to two lines with a *more* / *less* button when it is cut off (the full text stays in the page; the open state is kept per entry across polls). System health: LLM backend, model and status; dashboard feed; market data (markets priced); bot state; environment; mode; warm-up; version. |

**Where every old panel went:** Market briefing banner → Intelligence (and the per-market outlook also in the Markets detail). Mode banner → Overview status strip. Equity graph → Overview. Markets panel → Markets table. P&L summary → Trading › P&L summary (today's figure also on Overview). Signals → Overview (latest 5) and Intelligence (all). Open positions → Overview (compact) and Trading (all columns). Closed positions, trade ledger, rejected trades, risk rejections → Trading tabs. Pending approvals → Overview. Price charts (one card per market) → Markets detail (selected market only). News feed → Intelligence. Local LLM panel → header (status) and Activity › System health (backend, model, status) and Intelligence (briefing, reflection). Bot activity → Activity (and the last 5 on Overview). Learning summary → Intelligence.

### Today's realised P&L

The Overview figure is `pnl-summary.by_day[<today's UTC date>]`. `/api/pnl-summary` groups closed trades by the UTC date of their exit, so this is the realised P&L of trades closed since 00:00 UTC; it is `0` when none closed today and shows `…` / *Unavailable* until the summary has loaded. Nothing is estimated from other data.

---

## Module contract

`core.js` defines `window.Dashboard = { views: {}, register(name, mixin) }`. Each view file calls

```javascript
Dashboard.register('markets', {
    marketSearch: '',                       // state
    initMarkets() { … },                    // optional hooks
    onMarketsShown(arg) { … },
    onMarketsHidden() { … },
    onMarketsData(d) { … },
    selectMarket(symbol) { … },             // methods
});
```

At start-up `dashboard()` (the `x-data` on `<body>`) merges `dashboardCore()` and every registered mixin into one Alpine component. Property descriptors are copied, so getters such as `settingsGroups` stay getters. All views share one namespace: a name defined twice is a console error (`Dashboard: "<key>" from view "<view>" is already defined`), and registering a view twice is too. A view may call another view's methods (for example `showSignal` or `closePosition` from Overview and Markets).

| Hook (`<Name>` = view name, capitalised) | When |
|---|---|
| `init<Name>()` | Once, when the component starts (before the first dashboard load). |
| `on<Name>Shown(arg)` | Every time the view becomes active; `arg` is the rest of the hash (`#/markets/BTC-EUR` → `BTC-EUR`). |
| `on<Name>Hidden()` | When another view becomes active. |
| `on<Name>Data(d)` | After every applied `/api/dashboard` response, on every view. |

Hooks are called inside `try/catch`, so one failing view does not stop the others.

**View data.** `/api/dashboard` is polled every 5 s on every view and fills the shared state in `core.js`. Anything else is loaded with

```javascript
initTrading() {
    this.pollWhileShown('trading', 15000, () => this.loadLedger());
}
```

A registered poll runs immediately when its view is shown (once the first dashboard response has arrived), then every `intervalMs` while that view stays shown and the browser tab is visible. Only `core.js` starts timers. Loaders use `fetchResource(res, url)` with a holder from `resource(initial)`: `{state: 'idle'|'loading'|'ready'|'error', error, at, seq, data}`. Each load is numbered so an older response is dropped, a failed refresh keeps the rows already shown (with *refresh failed* in the status line), and `resourceStatus(res, noun)` gives the panel's status line.

| Data | Endpoint | When |
|---|---|---|
| Dashboard state | `GET /api/dashboard` | Every 5 s, every view, and after every action |
| Settings | `GET /api/settings` | Start-up and when the dialog opens |
| P&L summary | `GET /api/pnl-summary` | Every 30 s on Overview and Trading |
| Trade ledger | `GET /api/trades` | Every 15 s on Trading |
| Closed positions | `GET /api/closed-trades` | Every 30 s on Trading |
| Rejected trades | `GET /api/rejected-trades` | Every 30 s on Trading |
| News | `GET /api/news` | Every 5 min on Intelligence |
| Candles | `GET /api/ohlc/{market}?interval=5` and `?interval=15` | Selected market, on selection and every 60 s on Markets |
| Signal detail | `GET /api/signals/{id}` | When a signal is opened |

**Adding a view.** Add its name to `VIEWS` in `core.js`, a nav link in `index.html`, a `<section data-view="<name>" x-show="view === '<name>'" x-cloak class="view">` with an `<h1 tabindex="-1">`, and `static/app/<name>.js` (loaded after `core.js` and before `backtests.js`) that calls `Dashboard.register('<name>', {…})`. Load its data with `pollWhileShown` and add the view to `dashboardViews` in `internal/server/frontend_shell_test.go`.

**Script order.** All scripts are `defer`, so they run in document order: Chart.js, Lightweight Charts, `core.js`, the view files, `settings.js`, `backtests.js`, Alpine last (so every view is registered before Alpine starts the component).

---

## Navigation

`_applyRoute()` reads `location.hash` on start-up and on every `hashchange`. `navigate(name, arg)` sets the hash, so the back button works and every view, tab and market can be bookmarked. `_showView()` calls the old view's `on<Name>Hidden`, the new view's `on<Name>Shown`, runs the new view's polls and, when the user navigated, scrolls to the top and moves focus to the view's heading.

Navigation is a left sidebar from 1200 px, an icon rail (76 px) from 768 px, and a bottom tab bar below 768 px. The current item has `aria-current="page"`.

---

## Design system (`styles.css`)

**Tokens** (`:root`, `color-scheme: dark`):

| Group | Tokens |
|---|---|
| Surfaces | `--surface-page #0b0e14`, `--surface-panel #11151d`, `--surface-raised #171c26`, `--surface-overlay #1e2430` |
| Borders | `--border #252c39`, `--border-strong #353e4f` |
| Text | `--text #e6e9ef`, `--text-secondary #aab2c0`, `--text-muted #7d8799` (all at least 4.3:1 on the panel) |
| Accent | `--accent #5b9dff`, `--accent-strong #2f6fdb` |
| Meaning | `--profit #3ccf8e`, `--loss #ff6b6b`, `--warning #f5c451`, `--info #5bc8e8`, `--live #ff8a3d` (real money), `--danger #d13438` (emergency stop) |
| Type | 12 / 13 / 14 / 16 / 20 / 28 px; tabular figures for numbers (`.num`) |
| Spacing, radius, motion | `--space-1…8`, `--radius`, `--duration` |

**Components:** app shell (`.app`, `.app-header`, `.app-nav`, `.app-main`), views (`.view`, `.view-head`), panels (`.panel`, `.panel-head`, `.panel-body`, `.panel-toolbar`), key figures (`.kpi-grid`, `.kpi`), status strip, tables (`.table`, `.table-wrap`, sticky header, zebra rows, right-aligned `td.num`, `tr.is-live`), pills (`.pill-profit/-loss/-warning/-info/-accent/-live/-outline`), buttons (`.btn-primary/-secondary/-danger/-danger-quiet/-ghost/-small/-icon`, `.btn-stop`, `.btn-resume`), switches (`role="switch"` + `aria-checked`), tabs (`role="tab"` + `aria-selected`), filter chips (`aria-pressed`), dialogs (`.modal-backdrop`, `.modal-panel`), banners and toasts, two-line clamps (`.is-clamped` with a `.more-toggle` button; `isClamped(el)` in `core.js` tells whether the text is cut off, measured with `afterReveal()` because `x-show` reveals elements one animation frame after `$nextTick`), lists that scroll inside their panel (`.scroll-list`), and visibility helpers for phones (`.hide-sm`, `.show-sm`, `.sr-only-sm`).

The current nav item's indicator never hides the keyboard focus ring (`.app-nav .nav-item:focus-visible`). A key-figure tile that is a link (`a.kpi`) keeps neutral text colour. Banners hidden by `x-show` take no space, so every view starts at the same height.

**Breakpoints:** under 1440 px the Markets detail moves under the table; under 1200 px the navigation becomes an icon rail; under 768 px the navigation is a bottom tab bar, the header is two rows, the gutter is 16 px, multi-column grids stack, Markets becomes list → detail, tab strips wrap instead of scrolling out of sight, lists grow with the page instead of scrolling inside it, and a wide table's empty message starts at the left edge so it is on screen. Tables scroll inside their panel; the page never scrolls sideways. Under 768 px or on a coarse pointer every control is at least 36 px tall (switches keep their look and get an invisible 36 px tap area). `prefers-reduced-motion` turns off animations.

**Rules:**

- Elements toggled with `x-show` must not set `display` in an inline `style`: when Alpine shows an element it removes the inline `display`, so an inline `display:flex` becomes `display:block`. Put layout in a class (`.modal-backdrop` centres every dialog).
- No utility-class shims (`.flex`, `.gap-2`, …): use the component classes.
- Colours only through tokens; profit/loss colour comes from `pnlClass(value)`.

---

## Safety rules

`TestFrontendSafety`, `TestFrontendDashboard` and `TestFrontendShell` (in `internal/server/`) check these.

- **Text, not HTML.** Every server, LLM and news string is bound with `x-text`. The dashboard has no `x-html` and never assigns `innerHTML`. `esc()` (escapes `& < > " '`) stays in `core.js` for any view that must build HTML, and `approvals.html` uses it for its cards.
- **Links** go through `safeUrl()`: only `http://` / `https://`, otherwise `#`.
- **No server values in inline handlers.** Alpine `@click` calls component methods with the row object; `approvals.html` carries ids in `data-*` attributes with one delegated listener.
- **Timestamps** go through `parseUtc()` (stored times are naive UTC; it appends `Z` when no zone is present).
- **Prices** use `fmtPrice()` (2 decimals from 1 upwards, otherwise 4 significant digits; also the candle price scale via `priceDecimals`). **Money** (equity, cash, value, P&L) uses `fmtMoney()` / `fmtSignedMoney()`: exactly 2 decimals.
- **Every action is confirmed** (`confirm()`): emergency stop, resume, trading mode, paper (and typed `LIVE` for live), strategy, per-market live switch, close, forget (*No order is sent*), reset, settings save (lists every change). Market on/off is a single click, as before.
- **Stale data.** Every action except the emergency stop starts with `_actionAllowedWhileStale()`, which asks *Send the request anyway?* while the connection is lost.
- **Emergency stop / resume** show the new state only after the server confirms it; a failure is an `alert()` starting *EMERGENCY STOP FAILED* / *RESUME FAILED*. Other failures are sticky error toasts (`notify('error', …)`); success toasts disappear after 6 s.
- **Approvals.** `503` → *Trade NOT executed – the approval is still pending*; `409` → the risk refusal; a `rejected` order names its environment. Buttons are disabled while a decision is being sent and while the emergency stop is active.
- **Live positions** carry a LIVE badge and their status (*entry pending* / *exit pending*); Close is disabled while a Kraken order is pending; a live close says the sell was sent and the P&L follows once Kraken confirms; *Forget* removes a live position from the bot without sending an order.
- **401 → `/login`.** `core.js` wraps `fetch` before anything else runs, as does `approvals.html`.

---

## Connection lost (stale data)

The page keeps the last data it received but makes clear it is old:

- A banner reads *Connection to the bot lost — showing data from HH:MM* (or *no data has been received yet*), the views are dimmed, the header shows *Connection lost* and the *Live* indicator is hidden.
- Triggers: the service worker's `API_OFFLINE` message for `/api/dashboard`, a failed or non-OK dashboard fetch, and `_checkStale()` (every 5 s) when the last applied response is older than `STALE_AFTER_MS` (20 s). The time-based check works without a service worker and catches requests that hang.
- Dashboard requests are numbered; a response older than the one shown is dropped, and the failure of an old request does not hide a newer success. `_applyDashboard` is called from one place only.
- The next applied dashboard response clears the state (`_markConnected()`).

---

## Charts

**Equity graph** (Overview, Chart.js): one instance on `#equity-chart`, found again with `Chart.getChart(canvas)` and updated in place with `update('none')`. Hover uses index mode with `intersect: false`, a 12 px hit radius, a tooltip with date, time and equity, and the `equityCursorLine` plugin (dashed vertical guide). The y-axis autoscales with `grace: '35%'`. Colours come from the CSS tokens (`cssVar()`).

**Backtest equity curve** (Backtests, Chart.js): one instance on `#bt-equity-chart`, held in `backtestChart` outside Alpine. Equity and drawdown share one chart on two stacked y-axes (drawdown, subdued, beneath). `_renderBacktestChart()` destroys the previous chart before drawing; opening another run, returning to the list and leaving the view destroy it too.

**Candle charts** (Markets, Lightweight Charts): only the selected market has charts, in the two panes of `#candle-detail`.

- Chart objects live outside Alpine in `candleCharts` (market → charts, series, resize observers) and `chartRequests` (market → request in flight), so they are not wrapped in reactive proxies.
- Selecting another market removes the old charts (`chart.remove()`, observers disconnected) before the new market loads; leaving Markets removes them too.
- `_loadChart(market)` allows one request per market: a second call shares the running promise.
- `_fetchAndRender(market)` fetches both intervals in parallel, checks both responses are OK before parsing, and drops the result if the market is no longer selected or Markets is no longer shown. `toCandleSeries` keeps one candle per timestamp and sorts by time. The price scale's `priceFormat` (`precision`, `minMove`) follows the last price.
- After `setData` each chart calls `timeScale().fitContent()`, so the candles fill its width; the resize observer refits after every size change (window resize, the sidebar becoming a rail, the layout stacking). The captions give the time each series covers (*5 min candles · 8h 20m*, from `candleSpan()`).

---

## Dialogs

All three dialogs use `.modal-backdrop` + `.modal-panel`, close on Escape and on a backdrop click, move focus into the panel when they open (one frame after Alpine reveals them), keep Tab inside (`trapTab`) and give focus back to the control that opened them.

**Trading settings** (`#trading-settings`, `openTradingSettings()`): three sections in order — **Trading mode**, **Strategy**, **Money**. Strategy cards split `Name (description)` with `_strategyLabelParts()`. Each choice asks `confirm()` first; *Live — real money* opens a panel whose *Switch to live* stays disabled until `LIVE` is typed, and `setTradingEnvironment('live')` sends that text as `confirmation`. A refusal (for example missing Kraken API keys) shows the API's `detail` in the dialog (`tradingSettingsError`).

**Settings** (`#settings-dialog`, `openSettings()`): reloads `GET /api/settings` and fills a draft (`resetSettingsDraft(data.fields)` before `settingsFields`, so list rows always have a draft). A side navigation lists the groups (`settingsGroups`); a dot marks groups with unsaved changes. Each row has the label, *Live* / *Restart* tag, *Restart pending* / *Unsaved* tags and help on the left and a control sized for its kind on the right:

| Kind | Control |
|---|---|
| `int`, `float` | Narrow right-aligned text box (`is-number`; the server validates) |
| `string` | Text box |
| `bool` | Switch |
| `list` | Removable chips, an add box (Enter or **Add**; market symbols upper-cased, duplicates ignored), entry count and **Remove all** |
| secret | `type="password"`, `autocomplete="new-password"`, never pre-filled; **Clear** / **Keep** |
| `DISABLED_NEWS_SOURCES` | **News feeds** panel: one switch per source, grouped by category (`newsFeedGroups`: the API's `news_feeds`, then *Your feeds* from the `NEWS_SOURCES` draft), each with its host; an *n of m on* count and **All on** / **All off**. A switch edits the list of switched-off names (`toggleNewsFeed`); removing an extra feed also drops its name from that list |

`saveSettings()` confirms with one line per change (lists show `+` added and `−` removed; news feeds *switched off …* / *switched on …*; secrets only *new value* / *cleared*), then posts the changes. Closing with unsaved changes asks first. While `settingsRestartRequired` is not empty, a banner lists the keys: *Settings saved. Restart the bot to apply: …*.

**Signal detail** (`.signal-panel`, `showSignal(id)`): strategy, time, market and direction, final confidence, thesis, score breakdown (momentum base, indicator consensus, news and LLM sentiment, ATR penalty, strategy confidence, then the LLM multiplier and the learner in the order the backend applies them, final; the LLM row shows the applied scale and, when `LLM_WEIGHT` is not 1, the raw scale and weight; signals stored before 0.22.0 have their LLM step rebuilt from the raw scale), indicator votes, raw signal data, risk decision, LLM assessment (sentiment, raw scale, weight and applied scale, reasoning), news in context, entry and exit plans.

---

## Desktop notifications

`initIntelligence()` asks for notification permission once. `onIntelligenceData()` seeds the IDs of the signals present on the first response without notifying, then raises one notification per new `trade_idea_id` (title *BTC/EUR ▲ LONG — 72% confidence*, body: thesis shortened to 120 characters plus the risk result, tag: the signal id). It runs on every view.

---

## Service worker (`sw.js`)

- Versioned cache `trading-bot-v<app version>`; old caches are deleted on activation and open tabs are claimed at once.
- HTML (navigations and `text/html`) is **network-first**, as are the dashboard's own code files `/static/app/*.js` and `/static/styles.css`, so a deploy is picked up on the next load and old scripts never run against new markup. The cached copy is used only when the bot is down. The service worker's `fetch()` still goes through the browser's HTTP cache, so the bot sends app code with `Cache-Control: no-cache` (since 0.22.1); without it the browser could reuse an old script for hours after a deploy (new HTML with old JS showed `NaN%` in the signal card). Other static files use stale-while-revalidate.
- A response is cached only when it is OK and not redirected (`cacheable()`), so the login page is never cached as the dashboard.
- API responses are never cached. When a polled endpoint (`/api/dashboard`, `/api/trades`, `/api/closed-trades`, `/api/rejected-trades`, `/api/news`, `/api/pnl-summary`) fails because the bot is unreachable, the worker answers `503` and posts `{type: 'API_OFFLINE', endpoint, ts}` to the requesting page only.

---

## Approvals and login pages

`approvals.html` lists every pending approval (refreshed every 3 s) with the same confirmations and failure messages as the Overview, using the shared styles; its back link opens `/#/overview/approvals`. A load failure replaces *Loading…* with the reason. Loading, empty and error messages sit in a panel like the dashboard's; the header badge is neutral (*None pending*), amber only while trades wait and red on an error. `login.html` is the access-token form used when the bot listens beyond this computer; it loads only `/static/styles.css`, the one static file served without a session.

---

## Backtests view

Code: `static/app/backtests.js`, markup in `<section data-view="backtests">` of `index.html`. A read-only viewer of the result folders that `go run ./cmd/backtest … --output results/<name>` writes, through `GET /api/backtests` and `GET /api/backtests/{id}` ([08-api-endpoints.md](08-api-endpoints.md#backtests)). It only sends GET requests and never recomputes a metric or derives a verdict: every number and sentence is read from the files.

**List** (`#/backtests`). Loaded each time the view is entered and with *Refresh*, never on a timer. Columns: when (`summary.json` modification time), run id with a warning pill when `has_warnings`, kind, strategy, markets, period (UTC), net return (signed, coloured), round trips, first verdict line with its rule id. Newest first; kind filter chips and a text filter (id, strategy, market, verdict). A multi-run parent's *Show N runs* lists its child runs. `skipped` > 0 shows "N folder(s) could not be read". With no results, two lines say how to write one (`--output results/<name>`, `docs/14-backtesting.md` as text, not a link).

**Detail** (`#/backtests/<id>`, the id's segments URL-encoded; child runs have ids such as `split_btc/runs/01_in_sample` and a *Part of …* link to their parent). In reading order:

1. **Read this first**: a single run's `honesty.lines` as written; lines with an upper-case prefix (`NOT THE LIVE STRATEGY:`, `ACCOUNTING WARNING:`, `DATA WARNING:`, `DATA:`, `STALE POSITIONS:`, `RISK SIZING:`) are styled as warnings. A multi-run summary has no honesty block: the view shows the warnings of its runs' rows (accounting, data, stale positions), the two fixed lines every multi-run `summary.md` starts with (`header()` in `render_multi.go`; a test keeps them in step) and the summary's `notes`. Always shown, never collapsible, and before any number.
2. **Verdict**: each line's text and rule id as produced. Green only for the positive rules `positive_not_validated`, `above_buy_and_hold`, `positive_in_and_out_of_sample`, `positive_at_every_tested_fee`; amber for `accounting_warning`, `data_coverage_warning`, `inconclusive_stale_positions`; neutral otherwise (descriptive rules such as `test_windows_profitable` also fire for losing runs).
3. **Key figures** (single runs): starting capital, ending equity, net P&L and %, worst-case net P&L when there are stale positions, fees, execution costs, round trips, win rate, profit factor, max drawdown (amount and %), exposure, annualised return, Sharpe. A metric the backtester wrote as `null` shows "n/a" with the note it gave (`profit_factor_note`, `annualised_note`, `ratio_note`), never 0.
4. **Equity and drawdown**: `part=equity` (at most 2000 points; "downsampled from N points" when the server downsampled).
5. **Benchmarks**: the strategy beside each benchmark (final value, net return, max drawdown, strategy minus benchmark in percentage points), the primary marked, with the benchmark notes.
6. **Costs**: the gross → net bridge, all costs with % of traded value and of capital, and per-market costs from `costs.json`.
7. **Reconciliation** (exact vs engine totals and differences, as written; a banner when it failed) and **Signals** (ideas, orders, fills, reasons not traded).
8. **Trades / Orders**: `part=trades` and `part=orders` in server-side pages of 100 with the total; Orders has a status filter (filled, rejected, cancelled) showing reject reasons. Clicking a column sorts the shown page only.
9. **Assumptions and reproducibility** (`run.json`): assumptions with their bias pill (pessimistic, optimistic, neutral, unknown), command line, tool and build, seed, data provenance (cache file, source file, checksum, import time), universe selections, settings and exchange model (disclosures), and *Raw summary*: `part=summary_md` loaded on opening and shown as plain preformatted text (`x-text`), never rendered as Markdown.

**Multi-run kinds.** `split`: in-sample and out-of-sample side by side. `walkforward`: test-window aggregate, tuning when present, and a per-window table. `regimes`: the whole run, the trend × volatility / trend / volatility tables with thin cells flagged, and the buckets. `sensitivity`: the break-even fee (labelled an estimate), sign survival per dimension and the variant table with the baseline marked. Rows link to their child run when one exists (matched by the `runs/NN_<label>` directory name). A summary whose shape the view does not know falls back to the Read this first block, the verdict and the raw summary, opened.

**Requests and safety.** Every request goes through `_btFetch`, which numbers it per resource; opening another run, returning to the list or leaving the view bumps those numbers, so a late response is dropped instead of rendering into another run's page. A 404 for the run returns to the list with a warning toast. URLs are built with `backtestUrl()` (each id segment through `encodeURIComponent`). All server text is rendered with `x-text`; runs open through buttons that set the hash, so no server value becomes an `href`.

