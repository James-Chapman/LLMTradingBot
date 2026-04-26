# Development Todo List

This document tracks all identified bugs, improvements, and enhancements in the order they will be implemented. Items are worked sequentially: each item must be fully implemented, tested, and linted before the next begins.

**Status key:** `[ ]` = pending · `[x]` = done · `[~]` = in progress · `[!]` = blocked

---

## QA Review — 2026-04-26

### New Bugs — 2026-04-26

- [x] **BUG-024: Division by zero crash in momentum calculation.**
  - **Where:** `backend/strategy/basic_strategy.py` line 101.
  - **Problem:** `momentum = (current_price - previous_price) / previous_price` has no guard for `previous_price == 0.0`. On startup the first tick uses `snap.price` as the fallback when history is short, and a misconfigured or zero-priced market snapshot would raise `ZeroDivisionError`, crashing the strategy loop for that tick.
  - **Expected:** Guard with `if not previous_price: return None` before the division.
  - **Suggested test:** GIVEN a market snapshot with `previous_price = 0.0` WHEN `_evaluate_market()` is called THEN it returns `None` instead of raising `ZeroDivisionError`.

- [x] **BUG-025: Trailing-stop watermark resets to entry price on bot restart.**
  - **Where:** `backend/execution/paper.py` lines 70–71.
  - **Problem:** When restoring positions from the database, `trailing_high` is initialised to `row.avg_price` (the entry price) for longs. If price moved favourably before the restart (e.g., opened at 50,000, peaked at 60,000), the watermark resets to 50,000 instead of 60,000. The trailing stop then uses the wrong reference, allowing price to fall from 60,000 to 47,500 (entry × 0.95) before triggering — far below the correct stop of 57,000 (peak × 0.95). Profits built before the restart are not protected.
  - **Expected:** On restore, watermarks should be `None` and lazily set to the first live price seen by `update_trailing_prices()`, rather than defaulting to the stale entry price.
  - **Suggested test:** GIVEN a long restored with `avg_price = 50,000` WHEN `update_trailing_prices(60,000)` is called THEN `trailing_high` is 60,000 and `trailing_stop_triggered(57,000, 0.05)` returns `True`.

- [x] **BUG-026: `ApprovalService.get()` returns expired approvals to the approve endpoint.**
  - **Where:** `backend/approval/service.py` line 107–108; `backend/main.py` line 1098.
  - **Problem:** `ApprovalService.get()` reads `_pending` directly without an expiry check. The `POST /api/approvals/{id}/approve` endpoint calls `get()` first and returns 404 with the message `"Approval not found or expired"` only if the result is `None` — but an expired approval that hasn't yet been purged is not `None`. It is returned to the caller, execution proceeds, and only the subsequent `approve()` call (which uses `_get_if_valid()`) rejects it with a second 404. The operator sees a misleading first 200-path through the endpoint before the actual rejection.
  - **Expected:** `get()` should call `_get_if_valid()` so that the expiry check is consistent with `approve()` and `reject()`.
  - **Suggested test:** GIVEN an approval whose TTL has elapsed WHEN `approval_service.get(id)` is called THEN it returns `None` and the approval is marked expired in the repository.

---

### Code Quality — 2026-04-26

- [x] **QUALITY-003: Synchronous SQLite calls block the async event loop in the strategy tick.**
  - **Where:** `backend/main.py` strategy loop — `repo.save_price_tick()`, `repo.trim_old_price_ticks()`, `repo.trim_old_activity()`, `repo.save_trade_idea()`, `repo.save_risk_rejection()`, etc.
  - **Problem:** All repository methods are synchronous and are called directly from `async` strategy loop code. SQLite I/O under load (write-ahead log flush, vacuum, large result sets) blocks the event loop, delaying the next tick, WebSocket messages, and API responses. Each synchronous DB call holds the event loop for its entire duration.
  - **Expected:** Wrap blocking repository calls with `await asyncio.get_event_loop().run_in_executor(None, ...)`, or convert the repository layer to use an async SQLite driver such as `aiosqlite`.
  - **Suggested test:** GIVEN a repository operation that takes >10 ms WHEN it is called in the strategy loop THEN other async tasks (e.g. API health-check responses) are not blocked during the operation.

- [x] **QUALITY-004: `previous_price` supplied to strategy is 17 minutes stale, not the prior tick.**
  - **Where:** `backend/main.py` line 344.
  - **Problem:** `prev = hist[-_LOOKBACK_TICKS - 1]` resolves to `hist[-35]` — the price from 35 ticks (≈17.5 minutes) ago. This value is passed as `previous_price` in `market_data` and used as the sole momentum baseline in `BasicStrategy` (`momentum = (current - previous) / previous`). The variable name implies it is the immediately preceding price; the actual value is a rolling 17-minute-old reference. This means the momentum signal is insensitive to short-term reversals within the lookback window, and the thesis logs display a momentum figure that operators would reasonably interpret as a 30-second change.
  - **Expected:** Either rename the field to `reference_price` with a comment documenting the lookback, or change the lookback to `hist[-2]` for genuine tick-over-tick momentum and rely on `price_changes()` in indicators for multi-minute percentage moves.
  - **Suggested test:** GIVEN 40 ticks of price history where the last tick is 10% above the prior tick but only 0.1% above `hist[-35]` WHEN the strategy evaluates THEN momentum reflects the 30-second move, not the 17-minute baseline.

---

## Open Todos — 2026-04-25

### Critical — Test Runner Broken

- [x] **CFG-001: pytest cannot discover tests — missing `pythonpath` config.**
  - **Where:** `pyproject.toml`.
  - **Problem:** `tests/bdd_helpers.py` is imported by every test file as a bare `from bdd_helpers import ...`. pytest does not add the `tests/` directory to `sys.path` automatically, so collection fails with `ModuleNotFoundError: No module named 'bdd_helpers'`. Every single test file is currently un-runnable via `pytest`.
  - **Fix:** Add `[tool.pytest.ini_options]` to `pyproject.toml` with `pythonpath = ["tests"]` (pytest ≥ 7) and `testpaths = ["tests"]`.
  - **Suggested test:** `pytest tests/ --collect-only` exits 0 with no import errors.

---

### New Bugs

- [x] **BUG-016: Strategy fires before MACD and Bollinger Bands have enough data.**
  - **Where:** `backend/main.py` line 169 (`_LOOKBACK_TICKS = 10`).
  - **Problem:** The warm-up guard allows strategy evaluation after only 10 ticks (5 minutes). MACD requires 34 bars, Bollinger 20 bars. For the first ~17 minutes both indicators return `None`, so strategy signals are driven only by RSI, EMA, and price changes — a materially different and weaker signal set than the configured strategy expects.
  - **Expected:** Warm-up threshold should be at least `slow_ema + signal_period - 1 = 34` ticks so all configured indicators are computable before the first trade idea is evaluated.
  - **Suggested test:** GIVEN fewer than 34 price ticks in history WHEN the strategy loop evaluates THEN no trade ideas are emitted.

- [x] **BUG-017: Live Kraken orders are never reconciled — status stays `"pending"` indefinitely.**
  - **Where:** `backend/execution/kraken.py`, `backend/main.py`.
  - **Problem:** `KrakenExecutionEngine.execute()` submits an order and stores the `txid`, but there is no follow-up `QueryOrders` poll or WebSocket fill event to update `status` to `"filled"` or `"rejected"`. A live order that fills on Kraken is invisible to the risk engine, the P&L ledger, and the stop-loss loop until the next restart (which never queries fills either).
  - **Expected:** A periodic reconciliation task should call `QueryOrders` for all outstanding `"pending"` live orders and update status, fill price, and fee accordingly.
  - **Suggested test:** GIVEN a live order with a known txid WHEN reconciliation runs and Kraken reports the order filled THEN the order record is updated to `status="filled"` with the actual fill price.

- [x] **BUG-018: `_price_history` buffer (60 ticks = 30 min) is too small for Wilder RSI to fully warm up.**
  - **Where:** `backend/main.py` line 170 (`maxlen=60`).
  - **Problem:** Wilder's RSI is accurate only when the SMMA has smoothed over many periods beyond the seed window. With at most 60 ticks, RSI has at most 46 bars of SMMA smoothing (60 − 14 seed bars). Standard trading platforms use 100–500 bars. The current buffer produces an RSI that is noticeably different from charting platforms and will over-weight early bars.
  - **Expected:** Increase `maxlen` to at least 200 (≈100 minutes at 30 s/tick) so indicators are properly seeded. The OHLC cache (100 candles × 5 min = 500 min) is already much deeper and is used where available.
  - **Suggested test:** GIVEN a 200-bar price series WHEN RSI is computed THEN the result is within 1 point of an independently calculated reference RSI.

- [x] **BUG-019: `CoinNewsAdapter` and `CoinWeekAdapter` silently return no news.**
  - **Where:** `backend/ingestion/news_adapter.py`.
  - **Problem:** Both adapters are documented as Phase 0 stubs and return empty lists. The news loop calls them without any indication to the operator that news is not actually being fetched. If the operator configures either adapter, they get silent zero-news results with no warning in the activity log.
  - **Expected:** Either implement the adapters or log a one-time `WARNING` at startup so the operator knows those news sources are inactive.
  - **Suggested test:** GIVEN `CoinNewsAdapter` is registered WHEN the news loop runs THEN the activity log contains a warning that this adapter is a stub.

- [x] **BUG-020: Portfolio Value graph can lag behind the latest market-data tick.**
  - **Where:** `backend/main.py`, `backend/portfolio/equity.py`, `frontend/index.html`.
  - **Problem:** The dashboard header computed equity from fresh prices, but the graph history could persist `_current_equity` from the previous snapshot during the strategy tick. The chart could therefore show a stale portfolio value until the separate equity ticker loop ran.
  - **Expected:** Every market-data tick should record a fresh portfolio snapshot equal to account cash plus the current marked value of open crypto/stock holdings.
  - **BDD tests:** GIVEN cash and holdings WHEN a portfolio snapshot is built THEN equity equals cash plus current holdings value. GIVEN the strategy loop receives fresh tick prices WHEN source is inspected THEN the tick path records a fresh graph snapshot instead of persisting stale `_current_equity`.
  - **Implementation:** Added `portfolio.equity.build_equity_snapshot()` and `_record_equity_snapshot(prices)`. The strategy loop now calls `_record_equity_snapshot(prices)` immediately after persisting the tick prices, and the equity ticker loop uses the same helper.

- [x] **BUG-021: LLM signal analysis crashes on naive restored context timestamps.**
  - **Where:** `backend/llm/analyser.py`.
  - **Problem:** SQLite can return `llm_briefings.generated_at` and `llm_reflections.generated_at` as offset-naive datetimes. `analyse_signal()` subtracted those values from `datetime.now(timezone.utc)`, raising `TypeError: can't subtract offset-naive and offset-aware datetimes` and breaking the strategy loop.
  - **Expected:** Briefing and reflection age calculations should accept both naive restored timestamps and aware UTC timestamps.
  - **BDD tests:** GIVEN persisted naive LLM context timestamps WHEN signal analysis builds prompt context THEN UTC age calculations do not crash and the briefing/reflection blocks are still injected.
  - **Implementation:** Added UTC timestamp normalisation helpers in `llm.analyser`, changed briefing/reflection default timestamps to `datetime.now(timezone.utc)`, normalised DB-loaded ISO timestamps, and routed all context age calculations through the safe helper.

- [x] **BUG-022: News loop crashes sorting mixed naive and aware article timestamps.**
  - **Where:** `backend/ingestion/news_adapter.py`, `backend/main.py`.
  - **Problem:** RSS articles could carry offset-naive UTC timestamps while JSON-backed articles, such as Fear & Greed, used timezone-aware UTC. `_news_loop()` sorted all articles by `published_at`, which raised `TypeError: can't compare offset-naive and offset-aware datetimes`.
  - **Expected:** Every news article should enter the news loop with a timezone-aware UTC `published_at` so cross-source sorting is deterministic.
  - **BDD tests:** GIVEN RSS and JSON news with mixed naive/aware timestamps WHEN items are normalised and sorted THEN no datetime comparison error is raised. GIVEN the news loop source WHEN inspected THEN publish times are normalised before sorting.
  - **Implementation:** Added `normalise_published_at()` and `normalise_news_item()`, kept RSS parser output timezone-aware UTC, and normalised the aggregate news list before sorting.

- [x] **BUG-023: Local LLM can stay unavailable after one malformed JSON response.**
  - **Where:** `backend/llm/client.py`, `backend/llm/analyser.py`.
  - **Problem:** Ollama could return HTTP 200 with malformed JSON when a prompt was truncated or the model ignored JSON formatting. The client marked the whole service unavailable, and analyser-level `available` guards prevented the circuit breaker from making its half-open retry, so the Local LLM panel could remain unavailable.
  - **Expected:** Malformed model JSON should fail only the current LLM task. Transport failures and timeouts should still open the circuit, and analyser workflows should allow half-open retry attempts when the cooldown has expired.
  - **BDD tests:** GIVEN Ollama returns HTTP 200 with malformed JSON WHEN JSON is expected THEN the service remains available. GIVEN the circuit is ready for a half-open retry WHEN signal analysis runs THEN the analyser attempts the LLM call even while `available` is still false.
  - **Implementation:** Added `OllamaClient.can_attempt`, stopped treating `JSONDecodeError` as an availability failure, and routed analyser availability checks through the half-open-aware helper.

---

### Code Quality

- [x] **QUALITY-001: Background loops catch bare `Exception` — programming errors are silently swallowed.**
  - **Where:** `backend/main.py` lines 669, 709, 764, 783, 807, 818, 846.
  - **Problem:** Every background loop wraps its inner body in `except Exception as e: logger.warning(...)`. This correctly prevents one bad tick from crashing the bot, but it also masks `AttributeError`, `TypeError`, `KeyError`, and other programming mistakes. A bug introduced in the strategy or indicator code would log a warning and continue rather than surfacing as a visible failure.
  - **Expected:** Narrow catches to `(httpx.RequestError, asyncio.TimeoutError, krakenex.APIError)` or equivalent. Re-raise `(KeyboardInterrupt, SystemExit)` and let unexpected exceptions propagate to the task, where FastAPI's exception handler can alert the operator.

- [x] **QUALITY-002: `features/` directory is empty.**
  - **Where:** `backend/features/`.
  - **Problem:** The directory exists but contains no files. If it was intended for Gherkin `.feature` files it should be populated; if it is unused it should be removed to avoid confusion.
  - **Expected:** Either add feature files or delete the directory.
  - **Implementation:** Verified no `features/` directory remains in the tracked project tree.

---

### Missing Features

- [x] **FEAT-001: Kraken ticker feed uses polling — no WebSocket support.**
  - **Where:** `backend/ingestion/kraken_adapter.py` line 157.
  - **Problem:** The ticker subscription comment explicitly flags WebSocket as a future enhancement. The current poll approach adds latency and costs one REST call per tick per market. At 30-second intervals with many markets this is tolerable, but a WebSocket feed would give real-time prices and reduce Kraken API rate-limit risk.
  - **Expected:** Add an optional `KrakenWebSocketAdapter` that subscribes to the `ticker` channel and pushes prices to `_price_history` without polling. The existing REST adapter remains as a fallback.
  - **BDD tests:** GIVEN a Kraken WebSocket ticker message WHEN it is parsed THEN the adapter returns the same `MarketSnapshot` contract used by polling. GIVEN the strategy loop source WHEN inspected THEN WebSocket snapshots are subscribed and used before REST polling fallback.
  - **Implementation:** `KrakenMarketAdapter.subscribe_ticker()` now starts a WebSocket ticker subscription and falls back to polling if streaming fails. `_strategy_loop` starts the subscription after market validation and consumes cached stream snapshots before calling `get_tickers_batch()` for missing symbols.

- [x] **FEAT-002: No rate-limit back-off on Kraken API errors.**
  - **Where:** `backend/ingestion/kraken_adapter.py`, `backend/execution/kraken.py`.
  - **Problem:** Neither the market data adapter nor the execution engine implements exponential back-off when Kraken returns a rate-limit error (`EGeneral:Too Many Requests`). A burst of retries after a rate-limit response will immediately trigger another rate-limit, compounding the problem.
  - **Expected:** Add a `_backoff_retry` wrapper that detects Kraken rate-limit responses and sleeps with exponential jitter before retrying.
  - **BDD tests:** GIVEN Kraken rate-limits a live order WHEN execution retries THEN the order is submitted after backoff. GIVEN Kraken temporarily rate-limits ticker calls WHEN batch prices are fetched THEN the adapter backs off and retries before returning market snapshots.
  - **Implementation:** Added `kraken_retry.call_with_kraken_backoff()` with exponential delay plus jitter, and wired it into Kraken ticker, OHLC, asset-pair, `AddOrder`, and `QueryOrders` calls.

---

## UI Layout and CSS Refactor Plan — 2026-04-25

> Analysis scope: `frontend/index.html`, `frontend/approvals.html`, and `docs/11-frontend.md`.
> Goal: keep the current dark theme and all existing dashboard functionality while reducing wasted
> space on widescreen monitors and preserving a clean layout on tablets and phones.
> Implemented with shared CSS in `frontend/static/styles.css`.

### Current whitespace findings

- The dashboard header and main content are hard-capped at `max-width:1280px`, so 1440p and
  ultrawide monitors show large unused side gutters even when panels contain dense data.
- The approvals page is capped at `max-width:900px`, which is readable for forms but wasteful
  when approval cards contain three independent detail columns.
- The main dashboard uses a mostly vertical stack. After the first Markets / Signals / Open
  Positions row, several dense panels render full-width one after another instead of using a
  wider multi-column workspace.
- Several grids are fixed rather than responsive:
  `repeat(3,1fr)` for Markets / Signals / Positions, `repeat(3,1fr)` for News, and
  `repeat(3,1fr)` inside approval cards.
- Chart sizing is fixed (`140px` equity chart, `210px` candle chart panes), so wide monitors do
  not gain much analytical area from the extra available width.
- CSS is duplicated and split across inline `style` attributes and local utility classes. This
  makes responsive layout changes risky and hard to test.

- [x] **UI-001: Establish responsive layout baselines before implementation.**
  - **Files:** `frontend/index.html`, `frontend/approvals.html`, `docs/11-frontend.md`
  - **Scope:** Record the current panel order, visible controls, and breakpoint behavior before
    changing layout.
  - **Breakpoints to verify:** `390x844` mobile, `768x1024` tablet, `1366x768` laptop,
    `1920x1080` desktop, and `2560x1440` widescreen.
  - **BDD checks:** GIVEN each breakpoint WHEN the dashboard loads THEN every existing panel,
    button, modal, chart, table, and approval action remains reachable.
  - **Implementation:** Current fixed-width constraints and panel/grid behavior are captured in
    the whitespace findings above and guarded by `tests/test_frontend_layout_bdd.py`.
  - **Acceptance:** Baseline screenshots or notes exist for each breakpoint; no implementation
    work begins until required current-state behavior is listed.

- [x] **UI-002: Move shared CSS into `frontend/static/styles.css`.**
  - **Files:** `frontend/static/styles.css`, `frontend/index.html`, `frontend/approvals.html`,
    `backend/main.py`, `docs/11-frontend.md`
  - **Scope:** Extract theme variables, card styles, pills, buttons, layout shells, panel headers,
    grids, tables, modals, scrollbars, and chart containers into a shared stylesheet.
  - **Implementation notes:** Tailwind's browser CDN has been removed and the remaining utility
    dependencies are local classes in `frontend/static/styles.css`. Replace repeated inline style blocks with semantic classes such as
    `.app-shell`, `.dashboard-grid`, `.panel`, `.panel-header`, `.metric-strip`,
    `.responsive-table`, `.chart-grid`, and `.approval-card`.
  - **BDD checks:** GIVEN the stylesheet is served by FastAPI WHEN `/` and `/approvals` load THEN
    computed colors, borders, button states, and visible controls match the current theme.
  - **Implementation:** Theme variables, shared pills/buttons, panel styling, responsive shells,
    content-aware grids, tables, and chart containers now live in `/static/styles.css`.
  - **Acceptance:** No embedded `<style>` blocks remain in dashboard pages; CSS is loaded from
    `/static/styles.css`.

- [x] **UI-003: Replace fixed page caps with adaptive application shells.**
  - **Files:** `frontend/static/styles.css`, `frontend/index.html`, `frontend/approvals.html`
  - **Scope:** Replace `max-width:1280px` and `max-width:900px` with responsive shells that use
    the viewport without becoming unreadable.
  - **Implementation notes:** Use CSS variables and `clamp()` for gutters, for example a shell
    width based on `min(100% - var(--page-gutter) * 2, 1800px)` for the dashboard and a wider
    approval workspace on large screens. Keep sensible line-length limits inside long text blocks
    instead of constraining the entire page.
  - **BDD checks:** GIVEN a `1920x1080` viewport WHEN the dashboard loads THEN side gutters are
    small and intentional, while content remains aligned and readable. GIVEN a mobile viewport
    WHEN the dashboard loads THEN no horizontal page scroll is introduced.
  - **Implementation:** Dashboard and approval pages now use `.app-shell` with responsive gutter
    and shell-width variables instead of fixed `1280px` / `900px` caps.
  - **Acceptance:** Desktop pages use more monitor width; mobile and tablet retain readable
    gutters and touch-friendly spacing.

- [x] **UI-004: Rebuild the dashboard as a responsive dense workspace.**
  - **Files:** `frontend/static/styles.css`, `frontend/index.html`, `docs/11-frontend.md`
  - **Scope:** Preserve every existing dashboard panel while increasing useful horizontal space
    for each panel.
  - **Implementation notes:** Initial implementation used a multi-column dashboard workspace, but
    the final requirement is a vertical stack where every block is full width.
  - **No functionality loss:** Keep collapsible panel state, emergency stop/resume, market
    toggles, strategy selector, reset positions, signal modal, approvals preview, tables,
    candlestick charts, news cards, LLM panels, and activity log.
  - **BDD checks:** GIVEN existing dashboard data WHEN each panel is expanded/collapsed THEN the
    same DOM controls and API actions remain available after the layout refactor.
  - **Implementation:** Dashboard panels now use `.dashboard-grid` as a full-width vertical stack.
  - **Acceptance:** At `1920x1080`, panels use full available width; at mobile widths, the same
    vertical order remains logical.

- [x] **UI-005: Make grid panels content-aware instead of fixed-column.**
  - **Files:** `frontend/static/styles.css`, `frontend/index.html`, `frontend/approvals.html`
  - **Scope:** Replace fixed `repeat(3,1fr)` layouts with full-width vertical stacks.
  - **Targets:** Markets / Signals / Positions, approval detail sections, P&L summary,
    signal modal grids, and raw signal data.
  - **Implementation notes:** Tables should keep horizontal scrolling inside their panel, not on
    the page. The only side-by-side content layout should be the 5-minute / 15-minute chart pair.
  - **BDD checks:** GIVEN narrow, laptop, and widescreen viewports WHEN grids render THEN cards do
    not become cramped, overly stretched, or hidden off-screen.
  - **Implementation:** Main dashboard groups, chart card list, approval details, and approval
    lists now stack vertically. Chart pairs retain side-by-side behavior where space allows.
  - **Acceptance:** Blocks fill available width naturally and do not sit side by side, except for
    the 5-minute and 15-minute chart panes and the News Feed's internal article-card grid.

- [x] **UI-006: Improve chart use of widescreen space.**
  - **Files:** `frontend/static/styles.css`, `frontend/index.html`
  - **Scope:** Give charts responsive dimensions that scale with their container and data density.
  - **Implementation notes:** Increase equity chart height on desktop, use `aspect-ratio` or
    `clamp()` for chart containers, and keep `ResizeObserver` behavior for Lightweight Charts.
    Preserve the existing 5-minute and 15-minute chart pairing per market.
  - **BDD checks:** GIVEN charts are opened or the browser is resized WHEN data is available THEN
    Chart.js and Lightweight Charts resize without blank canvases or overlap.
  - **Implementation:** Equity and candle chart containers now use CSS `clamp()` dimensions;
    generated candle chart cards use `.chart-card`, `.chart-pair-grid`, and `.chart-pane`.
  - **Acceptance:** Widescreen users get larger, more useful charts; mobile users still get
    readable charts without horizontal page scroll.

- [x] **UI-007: Make the sticky header wrap intelligently.**
  - **Files:** `frontend/static/styles.css`, `frontend/index.html`, `frontend/approvals.html`
  - **Scope:** Keep all header status, equity, cash, approvals, stop, and resume controls while
    preventing crowding on smaller widths.
  - **Implementation notes:** Use a responsive header grid or flex-wrap. On mobile, group metrics
    into a compact status row and keep emergency stop/resume visually prominent.
  - **BDD checks:** GIVEN mobile and desktop widths WHEN header content changes between paper/live
    and normal/emergency states THEN text and buttons do not overlap.
  - **Implementation:** Header layout now uses `.header-bar`, `.header-brand`, `.header-status`,
    `.metric-block`, and responsive wrapping rules shared by dashboard and approvals pages.
  - **Acceptance:** Header uses wide screens efficiently and remains usable on touch devices.

- [x] **UI-008: Update frontend documentation after the layout refactor.**
  - **Files:** `docs/11-frontend.md`, `docs/99-todo.md`
  - **Scope:** Document the new stylesheet, layout shells, breakpoints, panel grid areas, and
    browser verification checklist.
  - **Implementation:** `docs/11-frontend.md` documents the shared stylesheet, responsive shells,
    breakpoint expectations, and grid structure.
  - **Acceptance:** Documentation matches the implemented CSS file and completed UI layout items
    are marked done after BDD verification.

- [x] **UI-009: Stack every page block vertically except paired market charts.**
  - **Files:** `frontend/static/styles.css`, `frontend/index.html`, `frontend/approvals.html`,
    `docs/11-frontend.md`, `tests/test_frontend_layout_bdd.py`
  - **Scope:** Remove the multi-column dashboard workspace and make every dashboard and approval
    block full width. The 5-minute and 15-minute charts remain paired; News Feed article cards
    can also form a responsive card grid inside the full-width News Feed panel.
  - **BDD checks:** GIVEN the stylesheet is inspected WHEN layout primitives are checked THEN
    `.dashboard-grid`, `.core-grid`, `.approval-details`, and `.chart-grid` are vertical stacks,
    `.news-grid` is a responsive card grid, and no dashboard CSS uses 12-column grid spans.
  - **Implementation:** `.app-shell` now uses the full viewport width minus gutters; dashboard
    and content grid classes use vertical flex stacks; P&L summary tables stack vertically; chart
    cards stack by market while `.chart-pair-grid` keeps the 5-minute and 15-minute panes paired.
  - **Acceptance:** No top-level page blocks render side by side; chart pairs remain side by side
    when there is enough room.

- [x] **UI-010: Restore News Feed article cards as responsive blocks.**
  - **Files:** `frontend/static/styles.css`, `docs/11-frontend.md`,
    `tests/test_frontend_layout_bdd.py`
  - **Scope:** Keep the News Feed panel full width, but let individual news articles render as
    card blocks in a responsive grid rather than one full-width article per row.
  - **BDD checks:** GIVEN the stylesheet is inspected WHEN the News Feed layout is checked THEN
    `.news-grid` uses CSS Grid with responsive `auto-fit` / `minmax()` columns while the dashboard
    shell remains a vertical stack.
  - **Implementation:** `.news-grid` now uses `display: grid` and
    `repeat(auto-fit, minmax(min(100%, 280px), 1fr))`.
  - **Acceptance:** News items appear as compact blocks again without reintroducing side-by-side
    top-level dashboard panels.

- [x] **UI-011: Rename "Portfolio Value" to "Equity Graph".**
  - **Files:** `frontend/index.html`, `docs/11-frontend.md`, `tests/test_frontend_layout_bdd.py`.
  - **Scope:** Update the dashboard panel label and frontend documentation from "Portfolio Value"
    to "Equity Graph". Keep the existing chart DOM hook (`equity-chart`) unless a separate
    refactor explicitly renames internal IDs.
  - **BDD checks:** GIVEN the dashboard markup is inspected WHEN the equity chart panel is found
    THEN the visible label is "Equity Graph" and "Portfolio Value" is no longer used as the panel
    title.
  - **Acceptance:** Operators see "Equity Graph" consistently in the UI and docs.
  - **Implementation:** Dashboard panel and frontend docs now use "Equity Graph"; the existing `equity-chart` DOM hook is unchanged.

- [x] **UI-012: Ensure Total Equity matches the Equity Graph value.**
  - **Files:** `backend/main.py`, `backend/portfolio/equity.py`, `frontend/index.html`,
    `tests/test_portfolio_equity_bdd.py`, `docs/03-background-loops.md`, `docs/11-frontend.md`.
  - **Scope:** Verify the dashboard header `Total Equity` value and the latest Equity Graph point
    are sourced from the same mark-to-market snapshot. If they already match, add a regression
    test that locks the behavior in.
  - **BDD checks:** GIVEN cash and open holdings WHEN `/api/dashboard` is built from current
    prices THEN `equity` equals the last `equity_history` point and both equal cash plus current
    holdings value.
  - **Acceptance:** The header number cannot drift from the latest graph value after a market
    tick, manual close, reset, or restart.
  - **Implementation:** Added `align_equity_history_with_current()` and use it in `/api/dashboard` so the final graph point matches the same current mark-to-market equity used by the header.

- [x] **UI-013: Place Markets, Signals, and Open Positions side by side with equal fixed panel heights.**
  - **Files:** `frontend/static/styles.css`, `frontend/index.html`, `docs/11-frontend.md`,
    `tests/test_frontend_layout_bdd.py`.
  - **Scope:** Supersedes the UI-009/UI-005 vertical-stack rule for this specific dashboard row.
    Markets, Signals, and Open Positions should render as three side-by-side panels on desktop
    and collapse responsively on narrow screens.
  - **Layout requirement:** The three panels must occupy the same pixel height in the row. Each
    panel body must scroll internally so large market lists, signal lists, or position lists do
    not make the row excessively tall.
  - **BDD checks:** GIVEN desktop-width CSS WHEN the core grid is inspected THEN `.core-grid`
    supports a three-column layout, each child panel has equal row height, and the list areas use
    bounded internal scrolling. GIVEN mobile width WHEN inspected THEN the panels stack without
    horizontal page scroll.
  - **Acceptance:** The row is compact and balanced on widescreen monitors, with no functionality
    loss for market toggles, signal detail buttons, position close buttons, or reset controls.
  - **Implementation:** `.core-grid` now renders Markets, Signals, Open Positions, and Closed Positions as equal-height responsive desktop panels. Each panel body uses bounded internal scrolling. Closed Positions is included in the same row to satisfy UI-014 without duplicating Open Positions.

- [x] **UI-014: Place Open Positions and Closed Positions side by side.**
  - **Files:** `frontend/static/styles.css`, `frontend/index.html`, `docs/11-frontend.md`,
    `tests/test_frontend_layout_bdd.py`.
  - **Scope:** Add a responsive two-panel row for Open Positions and Closed Positions. Keep
    tables and action controls intact.
  - **Layout requirement:** On desktop, Open Positions and Closed Positions should sit side by
    side and use internal scrolling where table content exceeds the row height. On smaller
    screens, they should stack vertically.
  - **BDD checks:** GIVEN desktop-width CSS WHEN position layout primitives are inspected THEN
    open and closed position panels share a row. GIVEN narrow width WHEN inspected THEN the row
    collapses to a single column.
  - **Acceptance:** Operators can compare current exposure and closed-trade outcomes without
    scrolling between distant sections.
  - **Implementation:** Moved Closed Positions into the core status grid immediately beside Open Positions on desktop. Narrow screens collapse the grid responsively.

- [x] **UI-015: Render each Local LLM panel item on its own line.**
  - **Files:** `frontend/index.html`, `frontend/static/styles.css`, `docs/11-frontend.md`,
    `tests/test_frontend_layout_bdd.py`.
  - **Scope:** Update the Local LLM panel presentation so each visible item is on a separate
    line rather than compressed into inline text.
  - **Targets:** Availability/model status, market briefing fields, market outlook rows,
    reflection pattern, reflection suggestion, confidence, and generated-at metadata.
  - **BDD checks:** GIVEN Local LLM data is rendered WHEN the panel markup is inspected THEN each
    LLM item uses a block/list row structure with no inline run-on metadata.
  - **Acceptance:** The Local LLM panel is easier to scan and does not lose any briefing or
    reflection information.
  - **Implementation:** Added `.llm-status`, `.llm-card`, `.llm-row`, `.llm-key`, and `.llm-outlook` classes. Model/status, briefing fields, outlooks, reflection pattern, suggestion, and confidence now render as separate rows.

---

## numpy Integration Plan — 2026-04-25

> numpy 2.4.4 is already present in `requirements.txt` (transitive dep via pandas).
> No installation step is needed — the plan is purely about adopting it in application code.
> EMA calculations remain pure-Python loops: they are inherently sequential (each value depends
> on the previous one) and numpy does not provide a material benefit there.
> The biggest wins are in scalar reductions (mean, std, max, min, abs) and the backtest replay.

- [x] **NUMPY-001: Vectorise scalar reductions in `indicators.py`.**
  - **Files:** `backend/analysis/indicators.py`
  - **Scope:** Replace Python built-ins with numpy equivalents in the five functions where
    the whole array is consumed in one reduction step. EMA-series functions stay as loops.
  - **Changes per function:**

    | Function | Python today | numpy replacement |
    |---|---|---|
    | `bollinger_bands` | `sum()`, manual variance loop | `np.mean()`, `np.std()` |
    | `rsi` | list comprehensions for gains/losses + loop | `np.diff()`, `np.where()`, SMMA loop unchanged |
    | `atr` (close-to-close) | `abs()` inside list-comp + `sum()` | `np.abs(np.diff())`, `np.mean()` |
    | `williams_r` | `max()`, `min()` on window | `np.max()`, `np.min()` |
    | `stochastic` | `max()`, `min()` per k-window | `np.max()`, `np.min()` per slice |

  - **Entry-point convention:** Add `arr = np.asarray(prices, dtype=np.float64)` at the top of
    each function so callers can still pass plain `List[float]` without changes anywhere else.
  - **BDD tests:** GIVEN price lists WHEN the numpy-backed indicators are called THEN they match
    the pure-Python references within the documented tolerances.
  - **Implementation:** `bollinger_bands`, `rsi`, `atr`, `williams_r`, and `stochastic`
    now coerce price input with `np.asarray(..., dtype=np.float64)` and use numpy reductions.
  - **Acceptance:** All existing BDD tests pass; ruff reports no violations.

- [x] **NUMPY-002: Vectorise backtest replay metrics in `replay.py`.**
  - **Files:** `backend/backtest/replay.py`
  - **Scope:** The replay module processes arrays of equity values and P&L figures to produce
    summary statistics. These are batch operations over potentially thousands of rows.
  - **Changes:** Max drawdown uses `np.maximum.accumulate`; the replay stores equity as an
    `np.ndarray`; win rate and profit factor use numpy reductions; 5-minute to 15-minute candle
    resampling uses numpy bucket reductions.
  - **BDD tests:** GIVEN known equity/P&L inputs WHEN replay metrics are computed THEN drawdown,
    win rate, profit factor, and resampled candles match manual reference values.
  - **Implementation:** Replay now keeps the equity curve in an `np.ndarray`, computes max
    drawdown and trade stats with numpy, includes `profit_factor` in reports, and resamples
    5-minute candles with numpy bucket reductions.
  - **Acceptance:** Existing backtest BDD tests pass unchanged; no new ruff violations.

- [x] **NUMPY-003: numpy-backed rolling statistics in `PerformanceLearner`.**
  - **Files:** `backend/strategy/learner.py`
  - **Scope:** Outcome tracking for rolling win rate, average P&L, and P&L percentiles over
    `signal_outcomes` history.
  - **Changes:** Load P&L history into `np.ndarray` once per query; compute mean, median, and
    percentiles with numpy; calculate trailing-N win rate from the latest rolling slice.
  - **BDD tests:** GIVEN outcome histories WHEN rolling win rate and percentiles are requested
    THEN results match manually counted and `statistics.quantiles` reference values.
  - **Implementation:** `PerformanceLearner` keeps chronological P&L history per key and exposes
    numpy-backed `rolling_win_rate()` and `pnl_percentiles()` query helpers used by summaries.
  - **Acceptance:** All learner BDD tests pass; ruff reports no violations.

- [x] **NUMPY-004: Enforce numpy type hygiene across the boundary.**
  - **Files:** `backend/analysis/indicators.py`, `backend/backtest/replay.py`
  - **Scope:** Prevent numpy scalar types from leaking into FastAPI responses, JSON reports, or
    SQLAlchemy columns.
  - **Changes:** Wrap public indicator return values in plain Python values and add `_to_py(val)`
    for numpy scalars and arrays.
  - **BDD tests:** GIVEN `compute_all()` output WHEN it is serialised with `json.dumps` THEN no
    `TypeError` is raised.
  - **Implementation:** Indicator outputs pass through `_to_py()` so numpy scalars and arrays are
    converted before API/report boundaries, with JSON serialisation covered by BDD tests.
  - **Acceptance:** All BDD tests pass; ruff reports no violations.

---

## Bugs

- [x] **BUG-001: Realised P&L ignores actual fill price, slippage, and fees.**
  - **Where:** `backend/main.py` stop-loss, auto-close, manual-approve, and manual-close paths; `backend/execution/paper.py`.
  - **Problem:** `PaperExecutionEngine.close_position()` and `execute()` create slippage-adjusted fill prices and fees, but callers calculate realised P&L using the raw `market_price`. Closed-trade rows and `order_records.pnl` can therefore disagree with actual fills and account cash.
  - **Expected:** Realised P&L, closed-trade `exit_price`, learner outcomes, and risk daily loss should be based on the executed fill price minus fees.
  - **Suggested test:** GIVEN a long is closed with slippage and fee WHEN the close is recorded THEN ledger P&L, signal outcome P&L, and cash delta match the fill record.

- [x] **BUG-002: Daily risk state is not persisted for several close paths.**
  - **Where:** `backend/main.py` manual approval close, manual position close, and operator reset paths.
  - **Problem:** `_record_trade_result()` persists `risk_state`, but some paths call `risk_engine.record_trade_result()` directly. A restart after a manual close or reset can lose accumulated daily loss.
  - **Expected:** Every realised trade result should update in-memory risk state and persist `risk_state`.
  - **Suggested test:** GIVEN a losing manual close WHEN the bot records the result THEN `repo.save_risk_state()` is called with the updated daily loss.

- [x] **BUG-003: Successful live Kraken orders are hidden from the trade ledger.**
  - **Where:** `backend/execution/kraken.py`, `backend/storage/repository.py`.
  - **Problem:** `KrakenExecutionEngine.execute()` leaves accepted live orders with status `"pending"`, but `Repository.get_trade_ledger()` only returns rows where `status == "filled"`. Accepted live orders can disappear from `/api/trades`.
  - **Expected:** Accepted live orders should either be visible as pending/submitted orders or updated to filled/rejected after reconciliation.
  - **Suggested test:** GIVEN Kraken accepts an order WHEN the trade ledger is requested THEN the order appears with a live status.

- [x] **BUG-004: Risk approval does not check available cash/margin for short signals.**
  - **Where:** `backend/risk/engine.py`, `backend/execution/paper.py`, `backend/execution/kraken.py`.
  - **Problem:** `RiskEngine.evaluate_trade()` checks available cash only for `Direction.LONG`. Paper execution later rejects unsupported shorts if there is not enough cash, and live execution may submit a sell that Kraken rejects.
  - **Expected:** Risk should reject unsupported shorts when there is no closable long and insufficient configured margin/cash.
  - **Suggested test:** GIVEN no existing long and insufficient cash WHEN a short idea is risk-checked THEN risk rejects before execution.

- [x] **BUG-005: Trade ledger open/close classification is wrong when the opener is outside the page limit.**
  - **Where:** `backend/storage/repository.py`.
  - **Problem:** `get_trade_ledger(limit=200)` derives the opening order from only the limited result set. If the opening order is older than the limit but the close is inside it, the close row is treated as an open row.
  - **Expected:** Open/close classification should query the earliest order for each returned `position_id`, independent of the page limit.
  - **Suggested test:** GIVEN a close order is in the requested page and its open order is outside the limit WHEN the ledger is built THEN the close row is still labelled `"close"`.

- [x] **BUG-006: Expired approvals can still be rejected without expiry validation.**
  - **Where:** `backend/approval/service.py`.
  - **Problem:** `approve()` uses `_get_if_valid()`, but `reject()` reads `_pending` directly and does not purge/check expiry. An expired approval can be marked rejected instead of expired.
  - **Expected:** Reject should use the same expiry validation path as approve, or purge expired approvals first.
  - **Suggested test:** GIVEN an expired approval WHEN reject is called THEN it returns `None` and persists status `"expired"`.

- [x] **BUG-007: LLM-only strategy can overload the local LLM by launching one request per market concurrently.**
  - **Where:** `backend/strategy/llm_only_strategy.py`.
  - **Problem:** `asyncio.gather()` runs all market recommendations at once. With a broad market universe, this can saturate Ollama, trigger timeouts, and cause a whole tick to degrade.
  - **Expected:** LLM-only evaluation should enforce a concurrency limit and ideally a per-tick market cap or prioritisation.
  - **Suggested test:** GIVEN many markets WHEN LLM-only evaluates THEN no more than the configured number of recommendation calls are in flight at once.

- [x] **BUG-008: Manual close structured logs still say stop-loss.**
  - **Where:** `backend/execution/paper.py`.
  - **Problem:** `close_position()` now supports non-stop-loss close sources, but its structured log message is still `"Position closed (stop-loss)"` for every targeted close.
  - **Expected:** Log message/source should reflect the `approval_request_id` or close reason.
  - **Suggested test:** GIVEN `close_position(..., approval_request_id="manual_close")` WHEN it logs THEN the log event is not labelled stop-loss.

- [x] **BUG-009: Operator reset records learner/risk outcomes but does not stamp close-order P&L or persist risk state.**
  - **Where:** `backend/main.py`, `backend/storage/repository.py`.
  - **Problem:** `/api/positions/reset` writes signal outcomes and updates in-memory risk/learner state, but it does not create close orders, stamp `order_records.pnl`, save an equity snapshot, or persist `risk_state`.
  - **Expected:** Reset should either create explicit reset close records or document that it only writes signal outcomes, and it should persist risk/equity state consistently.
  - **Suggested test:** GIVEN open positions WHEN operator reset runs THEN closed-trade history, risk state, and equity snapshot are all updated consistently.

## Backtest / Historical Data Test Plan

- [x] **BT-001: Build a deterministic 48-hour replay harness for strategy, risk, and paper execution.**
  - **Goal:** Test the bot against historical candles without starting FastAPI background loops or touching the production SQLite database.
  - **Scope:** Add a pure replay module, for example `backend/backtest/replay.py`, that accepts candles, strategy ID, starting capital, trading mode, stop-loss settings, and fee/slippage settings. It should reuse `compute_all()`, the selected strategy implementation, `RiskEngine`, and `PaperExecutionEngine` rather than duplicating trading logic.
  - **Data shape:** Use 5-minute OHLC candles for the replay. A 48-hour run needs 576 candles per market. Keep a 15-minute derived stream by resampling every 3 candles so higher-timeframe indicators are tested the same way the live bot uses `_ohlc_cache_15`.
  - **No lookahead rule:** At candle `N`, the strategy may only receive prices and candles from `0..N`. The test must fail if any future candle is passed into indicator calculation or execution decisions.
  - **Execution rule:** Execute trades at the next candle open when possible. If the replay uses the current candle close as the signal price, record that explicitly and keep it consistent across all tests.
  - **Close rule:** Close open positions on stop-loss, opposite signal, or final candle. Final-candle close is required so realised P&L and ending equity are deterministic.
  - **Output:** Return a structured result containing starting equity, ending equity, realised P&L, unrealised P&L, fees, slippage-adjusted fills, max drawdown, trade count, win rate, orders, fills, and signal decisions.

- [x] **BT-002: Add frozen 48-hour historical fixtures for stable CI tests.**
  - **Goal:** Make profit tests repeatable and independent of Kraken availability.
  - **Fixture path:** Add files under `tests/fixtures/historical/`, for example `btc_eur_5m_profitable_48h.json` and optionally `eth_eur_5m_choppy_48h.json`.
  - **Fixture content:** Store candles as `{ "t": "...", "o": 100.0, "h": 101.0, "l": 99.5, "c": 100.8, "v": 12.3 }`. Include metadata with `market`, `interval_minutes`, `start`, `end`, `source`, and `expected_strategy`.
  - **Profit fixture requirement:** The main fixture should contain a realistic trend with pullbacks where the configured strategy can produce at least one profitable trade after fees and slippage. This can be captured from Kraken and pinned, or generated from realistic candle rules if captured data is not suitable.
  - **Reason:** A test that always fetches the latest 48 hours and requires profit will be flaky. The live market can be sideways or adverse even when the bot is correct.

- [x] **BT-003: Add BDD tests for replay profitability and determinism.**
  - **Test file:** Add `tests/test_backtest_replay_bdd.py`.
  - **Profit test:** GIVEN a pinned 48-hour profitable BTC/EUR candle fixture WHEN the replay runs the selected strategy with paper execution THEN ending equity is greater than starting equity after fees and slippage.
  - **Trade creation test:** GIVEN the same fixture WHEN the replay completes THEN at least one filled open order and one filled close order are produced.
  - **Determinism test:** GIVEN the same fixture and settings WHEN the replay runs twice THEN orders, fills, ending equity, and realised P&L are identical.
  - **No-lookahead test:** GIVEN a fixture with a known future price jump WHEN the replay is at an earlier candle THEN indicators and signals do not include candles after the current timestamp.
  - **Stop-loss test:** GIVEN a fixture where price moves below entry after a trade WHEN replay runs THEN stop-loss closes only losing positions, not positions that remain profitable versus entry.
  - **Strategy selection test:** GIVEN `indicator_only`, `combined`, and `llm` strategy IDs WHEN replay is configured THEN exactly that strategy is used for every signal decision.

- [x] **BT-004: Add an optional live Kraken 48-hour smoke backtest.**
  - **Goal:** Let us run `last 48 hours` against current Kraken data without making CI flaky.
  - **Command:** Add a CLI entry point such as `python -m backend.backtest.replay --market BTC/EUR --hours 48 --interval 5 --strategy combined --source kraken`.
  - **Network behaviour:** This test should be opt-in, for example guarded by `RUN_LIVE_BACKTESTS=1`, and skipped by default in unit test runs.
  - **Assertion:** The live smoke test should assert that data loads, replay completes, no exceptions are raised, all orders/fills are internally consistent, and a report is written. It should not require profit by default.
  - **Optional threshold mode:** Add an explicit `--require-profit` flag for manual acceptance runs. Use it only when the user intentionally wants to evaluate whether the current strategy made money over the latest 48 hours.

- [x] **BT-005: Add backtest reporting and review artifacts.**
  - **Report path:** Write JSON reports to `docs/backtests/` or `backtests/reports/`, for example `docs/backtests/2026-04-25_BTC-EUR_combined_48h.json`.
  - **Summary fields:** Include market, strategy, candle count, start/end timestamps, starting capital, ending equity, realised P&L, fees, number of signals, number of filled trades, win rate, max drawdown, and close reasons.
  - **Ledger fields:** Include every replay order with timestamp, market, direction, size, fill price, fee, position ID, status, and source signal ID.
  - **Review use:** The report should make it clear whether profit came from one large trade, several small trades, or accidental end-of-test liquidation.

- [x] **BT-006: Define profit acceptance criteria carefully.**
  - **CI criterion:** For the pinned profitable fixture, require `ending_equity > starting_capital` and `realised_pnl > 0` after fees and slippage.
  - **Minimum trade criterion:** Require at least one completed trade so profit cannot pass because no trades were taken and equity stayed flat.
  - **Drawdown criterion:** Add an initial generous max drawdown guard, for example drawdown must remain below the configured daily loss limit. Tighten only after the replay harness is trusted.
  - **Live-data criterion:** For latest-48-hour Kraken data, record profit/loss as a metric first. Do not fail normal CI because a specific 48-hour market window was not profitable.

- [x] **BT-007: Capture the 48-hour profitability goal as BDD strategy-quality requirements.**
  - **Product goal:** Any 48-hour market window should ideally create profit. If the bot does not have an edge in that window, it should avoid trading or produce an explainable low-confidence/no-trade result.
  - **Correctness requirement:** GIVEN any historical 48-hour candle window WHEN the bot replays the window THEN fills, fees, slippage, stop-losses, strategy selection, risk limits, and signal timestamps are internally consistent.
  - **Profit-or-explain requirement:** GIVEN a historical 48-hour candle window WHEN the selected strategy completes replay THEN the result should be one of: profitable after fees and slippage, no-trade because confidence/risk gates rejected the setup, or failed strategy-quality because the bot traded into avoidable losses.
  - **No-trade requirement:** GIVEN a flat or choppy 48-hour window WHEN the bot has no technical/news/LLM edge THEN it should preserve capital by taking no trades or only trades that remain inside risk limits.
  - **Loss accountability requirement:** GIVEN a losing 48-hour replay WHEN the bot created trades THEN the report must identify which gates allowed the trade, the indicator/news/LLM evidence at entry, the exit reason, realised P&L, fees, and whether risk rules behaved as expected.
  - **Regression requirement:** GIVEN a previously profitable pinned 48-hour fixture WHEN indicators, news scoring, LLM prompts, risk logic, or execution logic change THEN the replay should remain profitable unless the expected fixture outcome is deliberately updated with a documented reason.

- [x] **BT-008: Add rolling 48-hour performance BDD requirements.**
  - **Goal:** Test the strategy over many windows, not just one cherry-picked fixture.
  - **Rolling-window requirement:** GIVEN at least 30 days of historical 5-minute candles WHEN the backtest runs rolling 48-hour windows THEN the report should include every window's return, trade count, win rate, max drawdown, profit factor, and no-trade reason where applicable.
  - **Profitability-rate requirement:** GIVEN the rolling 48-hour report WHEN strategy quality is evaluated THEN a configurable minimum percentage of windows should be profitable after fees and slippage.
  - **Average-edge requirement:** GIVEN the rolling 48-hour report WHEN all windows are aggregated THEN average return and median return should be positive, or the strategy should be marked as not production-ready.
  - **Drawdown requirement:** GIVEN the rolling 48-hour report WHEN the worst window is inspected THEN max drawdown and largest realised loss should stay within configured risk limits.
  - **Consecutive-loss requirement:** GIVEN rolling windows ordered by time WHEN losses occur THEN max consecutive losing windows should be reported and checked against a configurable threshold.
  - **Non-deterministic LLM requirement:** GIVEN LLM-backed strategy replay WHEN tests need repeatability THEN LLM recommendations should be fixture-backed or stubbed; live LLM runs should be treated as performance experiments, not deterministic CI tests.

---

## Bug Analysis — 2026-04-25

All 95 tests passed. The following bugs were identified via static code analysis and cross-file review.

All bugs below have been fixed. 104 tests pass; ruff reports no violations.

- [x] **BUG-010: Timezone mismatch corrupts daily loss state restore on non-UTC systems.**
  - **Where:** `backend/main.py` line 183.
  - **Problem:** `risk_engine._last_reset_date` is always set with `datetime.utcnow().date()` (UTC), but the restore guard compares it against `date.today()` (local system time). On any host not running in UTC, the two values diverge around midnight: the saved state uses the UTC date, the check uses the local date. The state is therefore silently not restored, resetting the daily loss counter on every restart between local midnight and UTC midnight.
  - **Expected:** Both the save and the restore comparison must use the same timezone (UTC). Use `datetime.now(timezone.utc).date()` everywhere.
  - **Suggested test:** GIVEN a risk state saved with today's UTC date WHEN the bot restores the state in a simulated non-UTC environment THEN the daily loss is correctly reloaded.

- [x] **BUG-011: `datetime.utcnow()` deprecated — codebase-wide compatibility risk.**
  - **Where:** `backend/main.py`, `backend/risk/engine.py`, `backend/execution/paper.py`, `backend/execution/kraken.py`, `backend/approval/service.py`, `backend/storage/repository.py`, `backend/universe/resolver.py`, `tests/bdd_helpers.py`, and several test files.
  - **Problem:** `datetime.utcnow()` is deprecated as of Python 3.12 and scheduled for removal. Python 3.14 (the current runtime) already emits `DeprecationWarning` on every call — 4 896 warnings were generated in the test suite. When the function is removed in a future release the entire application will crash at startup.
  - **Expected:** Replace every `datetime.utcnow()` with `datetime.now(timezone.utc)` and every `datetime.utcnow().isoformat()` with `datetime.now(timezone.utc).isoformat()`. Import `timezone` from the standard `datetime` module.
  - **Suggested test:** GIVEN the test suite runs WHEN any module is imported THEN no `DeprecationWarning` referencing `utcnow` is emitted.

- [x] **BUG-012: Paper short margin is not reserved — cash increases when opening a short.**
  - **Where:** `backend/execution/paper.py` — `_has_sufficient_funds()` line 352, `_apply_fill()` line 386.
  - **Problem:** `_has_sufficient_funds()` gates a new paper short on `self.cash >= fill_value + fee`. However `_apply_fill()` then does `self.cash += fill_value - fee` — cash increases when a short opens. The margin is checked but never deducted, allowing effective leverage beyond configured starting capital.
  - **Expected:** When opening a paper short (no existing long to close), deduct `fill_value` from cash as margin, then credit it back on close, mirroring real margin accounting.
  - **Suggested test:** GIVEN starting cash of €1000 WHEN three paper shorts of €800 each are opened THEN the third is rejected for insufficient margin.

- [x] **BUG-013: Equity ticker snapshots are never persisted to the database.**
  - **Where:** `backend/main.py` — `_equity_ticker_loop()` around line 829.
  - **Problem:** `_equity_ticker_loop` updates `_equity_history` and `_current_equity` every 10 seconds but never calls `repo.save_equity_snapshot()`. DB persistence only occurs inside `_strategy_loop` every 30 seconds, so crashes or delayed strategy ticks can leave chart gaps.
  - **Expected:** The equity ticker should persist each snapshot to the DB, or the strategy loop should persist on every tick rather than every 30 seconds.
  - **Suggested test:** GIVEN the equity ticker fires three times WHEN the DB is inspected THEN three new equity snapshot rows are present.

- [x] **BUG-014: `zip()` without `strict=` in MACD allows silent length mismatch.**
  - **Where:** `backend/analysis/indicators.py` line 145.
  - **Problem:** `zip(fast_series[offset:], slow_series)` silently truncates to the shorter series if an off-by-one occurs in the alignment calculation, producing subtly wrong MACD values without any indication of failure.
  - **Expected:** Use `zip(fast_series[offset:], slow_series, strict=True)` so a length mismatch raises `ValueError` immediately.
  - **Suggested test:** GIVEN a price series where `slow - fast` offset is deliberately wrong WHEN `macd()` is called THEN a `ValueError` is raised rather than silently returning misaligned values.

- [x] **BUG-015: Missing newline at end of file in `models.py` and `logging.py`.**
  - **Where:** `backend/domain/models.py` line 142, `backend/observability/logging.py` line 72.
  - **Problem:** Ruff reports `W292 No newline at end of file` for both files. Some tools treat a missing trailing newline as malformed text and create noisy diffs.
  - **Expected:** Add a single trailing newline to both files.
  - **Suggested test:** GIVEN ruff runs with `W292` enabled WHEN both files are checked THEN no `W292` violations are reported.

---

## Bug Analysis — 2026-04-24

## Phase 1 — Bugs (highest priority: correctness before features)

- [x] **B1: `equity` NameError in strategy loop.**
  - **File:** `backend/main.py` lines 288, 328, 477.
  - **Problem:** The strategy loop referenced the local variable `equity` after it was moved to `_equity_ticker_loop`. Three call sites (`evaluate()`, `analyse_signal()`, `size_base`) would raise `NameError` at runtime.
  - **Fix:** Replace all three occurrences with `_current_equity`, the global maintained by the equity ticker.

- [x] **B2: Missing DB schema columns cause silent data loss on restart.**
  - **File:** `backend/storage/database.py`
  - **Problem:** Three columns referenced by ORM models could be absent unless the ad-hoc `ALTER TABLE` block ran cleanly: `open_positions.trade_idea_id`, `signal_outcomes.closing_trade_idea_id`, and `signal_outcomes.position_id`.
  - **Fix:** Replace the raw SQL migration block with idempotent guards, or migrate to Alembic.

- [x] **B3: `LLMOnlyStrategy` fires one LLM call per market per tick.**
  - **File:** `backend/strategy/llm_only.py`
  - **Problem:** Each 30-second strategy tick called `recommend_trade()` for every active market sequentially, allowing broad universes to block a tick for longer than the tick interval.
  - **Fix:** Batch calls, run them with bounded concurrency, or throttle to one market per tick on a round-robin.

- [x] **B4: `_briefed_news_ids` grows without bound.**
  - **File:** `backend/main.py`
  - **Problem:** Every article ID sent to the LLM was added to `_briefed_news_ids` and never pruned.
  - **Fix:** Cap the set to the last N IDs with a deque-backed structure or periodically discard IDs older than the news retention window.

- [x] **B5: `daily_loss` is not persisted across restarts.**
  - **File:** `backend/risk/engine.py`
  - **Problem:** `RiskEngine.daily_loss` was in-memory only, so a restart mid-day reset the daily loss guard.
  - **Fix:** Persist `daily_loss` and `daily_start_equity`, then restore them on startup when the stored date matches today.

- [x] **B6: Daily loss limit does not include the current trade's estimated loss.**
  - **File:** `backend/risk/engine.py` lines 102-104.
  - **Problem:** The guard checked accumulated historical loss only, allowing a trade that would push the total over the configured daily limit.
  - **Fix:** Change the guard to include the current trade's estimated loss before approval.

- [x] **B7: RSI uses simple average instead of Wilder's smoothed MA.**
  - **File:** `backend/analysis/indicators.py`
  - **Problem:** The RSI implementation used a simple mean over the period instead of Wilder's exponential smoothing formula.
  - **Fix:** Implement Wilder's smoothed moving average for both average gain and average loss.

- [x] **B8: `ApprovalService._pending` is accessed directly during emergency stop.**
  - **File:** `backend/main.py`, `backend/approval/service.py`
  - **Problem:** The emergency-stop handler cleared `approval_service._pending` directly, bypassing lifecycle logic in `ApprovalService`.
  - **Fix:** Add a public `clear_pending()` method to `ApprovalService` and call that instead.

- [x] **B9: `UniverseResolver` produces duplicate markets.**
  - **File:** `backend/universe/resolver.py`
  - **Problem:** Markets appearing in both fixed and dynamic lists could be processed twice per tick.
  - **Fix:** De-duplicate with `list(dict.fromkeys(fixed + dynamic))` in `resolve_universe()`.

- [x] **B10: ATR and Stochastic use close price as high/low proxy.**
  - **File:** `backend/analysis/indicators.py`
  - **Problem:** Ticker-only high/low proxies made ATR collapse to 0 and Stochastic %K collapse to 50.
  - **Fix:** Source true OHLC for indicator computation, or replace ATR/Stochastic with indicators meaningful on tick data.

---

## Phase 2 — Improvements (quality, reliability, performance)

- [x] **I1: Replace ad-hoc ALTER TABLE migrations with idempotent schema management.**
  - **File:** `backend/storage/database.py`
  - **Problem:** Hand-rolled `ALTER TABLE ... ADD COLUMN` statements fail if the column already exists and provide no rollback or version tracking.
  - **Fix:** Introduce Alembic, or at minimum wrap every `ALTER TABLE` in safe idempotent guards and add schema version tracking.

- [x] **I2: Add exponential back-off and circuit breaker to the Ollama client.**
  - **File:** `backend/llm/client.py`
  - **Problem:** A flat 5-minute cooldown can miss early recovery and still causes repeated failed strategy-loop calls while Ollama remains down.
  - **Fix:** Implement closed, open, and half-open circuit-breaker states with exponential back-off from 30 seconds to 300 seconds.

- [x] **I3: Persist `ApprovalService` state across restarts.**
  - **File:** `backend/approval/service.py`
  - **Problem:** Pending approvals lived only in memory and were lost during restart.
  - **Fix:** Persist the approval queue to the DB and reload it on startup.

- [x] **I4: Replace `OFFSET`-based price-tick trimming with a subquery.**
  - **File:** `backend/storage/repository.py`
  - **Problem:** `trim_old_price_ticks()` scanned too much data by using `OFFSET N` to locate the cutoff row.
  - **Fix:** Delete rows not included in a latest-N subquery ordered by timestamp.

- [x] **I5: Improve LLM market-name matching robustness.**
  - **File:** `backend/llm/analyser.py`
  - **Problem:** LLM-returned market variants such as `BTC-EUR` or `btc eur` could fail to match and silently discard briefing data.
  - **Fix:** Mandate exact symbols in the prompt and add a fuzzy fallback with warning logs.

- [x] **I6: Extract confidence threshold constants from `basic_strategy.py`.**
  - **File:** `backend/strategy/basic.py`
  - **Problem:** Magic numbers for confidence adjustments were scattered inline.
  - **Fix:** Extract thresholds and weights to named constants with comments.

- [x] **I7: Enforce strict `.env` case sensitivity.**
  - **File:** `backend/config/settings.py`
  - **Problem:** Case-insensitive settings could shadow production values with development overrides.
  - **Fix:** Set `case_sensitive=True` and update `.env.example` to use consistent `UPPER_SNAKE_CASE`.

- [x] **I8: Replace hardcoded `£` currency symbols.**
  - **Files:** `backend/main.py`, `frontend/index.html`
  - **Problem:** UI and activity logs used GBP symbols even though exchange markets and `settings.base_currency` use EUR.
  - **Fix:** Use a backend currency helper and pass the symbol to the frontend dashboard response.

- [x] **I9: Add loss-distribution awareness to `PerformanceLearner`.**
  - **File:** `backend/strategy/learner.py`
  - **Problem:** The learner tracked win rate but not win/loss magnitude.
  - **Fix:** Record `mean_win_pnl`, `mean_loss_pnl`, and derive a magnitude-aware quality score per strategy, market, and direction.

- [x] **I10: Add liquidity and volume filter to risk engine.**
  - **File:** `backend/risk/engine.py`
  - **Problem:** Trades could be approved on near-zero-volume pairs where the slippage estimate was unrealistic.
  - **Fix:** Add a minimum 24-hour volume check sourced from Kraken ticker volume.

---

## Phase 3 — Enhancements (new capabilities)

- [x] **E1: Implement real dynamic universe resolution.**
  - **File:** `backend/universe/resolver.py`
  - **Problem:** `_resolve_dynamic_markets()` returned a hardcoded list and logged a warning.
  - **Fix:** Query CoinMarketCap or CoinGecko for top ETH-ecosystem tokens, filter to Kraken-available pairs, and return them.

- [x] **E2: Add multi-timeframe signal confirmation.**
  - **Files:** `backend/strategy/basic.py`, `backend/main.py`
  - **Problem:** Signals were generated from 30-second tick data only.
  - **Fix:** Use fetched 5-minute and 15-minute OHLC data to compute higher-timeframe EMA direction and add a confirmation gate.

- [x] **E3: Add trailing stop-loss.**
  - **File:** `backend/main.py`
  - **Problem:** Fixed stop-losses could give back profit after a favourable move.
  - **Fix:** Track `trailing_high` for longs and `trailing_low` for shorts, then trigger stops when price retraces beyond the threshold.

- [x] **E4: Feed sentiment score into signal confidence.**
  - **Files:** `backend/llm/analyser.py`, `backend/strategy/basic.py`
  - **Problem:** LLM market briefing sentiment was displayed but did not affect signal confidence.
  - **Fix:** Expose briefing sentiment as a numeric confidence adjustment in `BasicStrategy.evaluate()`.

- [x] **E5: Add strategy hot-reload endpoint.**
  - **Files:** `backend/main.py`, `backend/strategy/`
  - **Problem:** Strategy parameter changes required a full restart.
  - **Fix:** Add `POST /api/control/strategies/reload` to reload strategy parameters without stopping the strategy loop.

- [x] **E6: Add Kelly and volatility-based position sizing.**
  - **Files:** `backend/risk/engine.py`, `backend/strategy/basic.py`
  - **Problem:** All trades used a fixed `position_sizing_proposal` fraction of equity.
  - **Fix:** Implement a capped Kelly-fraction sizer using `PerformanceLearner` win rate and ATR.

- [x] **E7: Add multi-timeframe P&L dashboard panel.**
  - **File:** `frontend/index.html`
  - **Problem:** The dashboard showed total equity but not P&L by day, week, or market.
  - **Fix:** Add `/api/pnl-summary` and a collapsible "P&L Summary" dashboard panel.

- [x] **E8: Add webhook alerts for significant events.**
  - **File:** `backend/main.py`
  - **Problem:** There was no out-of-band notification for daily loss limits, stop-losses, emergency stop, or restart.
  - **Fix:** Add `settings.alert_webhook_url` and POST event payloads when significant events occur.

- [x] **E9: Add trade history CSV export.**
  - **File:** `backend/main.py`
  - **Problem:** There was no way to download trade data for external analysis.
  - **Fix:** Add `GET /api/export/trades.csv` streaming `closed_trades` with table-matching headers.

- [x] **E10: Add paper trading vs live trading toggle per market.**
  - **Files:** `backend/config/settings.py`, `backend/execution/`
  - **Problem:** The bot ran entirely in paper or entirely live.
  - **Fix:** Add a per-market `live` flag and route execution per market to the paper or Kraken engine.

---
