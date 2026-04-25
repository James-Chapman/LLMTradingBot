# Bug Todo

## Open Bugs

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

## numpy Integration Plan — 2026-04-25

> numpy 2.4.4 is already present in `requirements.txt` (transitive dep via pandas).
> No installation step is needed — the plan is purely about adopting it in application code.
> EMA calculations remain pure-Python loops: they are inherently sequential (each value depends
> on the previous one) and numpy does not provide a material benefit there.
> The biggest wins are in scalar reductions (mean, std, max, min, abs) and the backtest replay.

---

### NUMPY-001: Vectorise scalar reductions in `indicators.py`

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
- **BDD tests (write first):**
  - GIVEN a price list WHEN `bollinger_bands` is called THEN the result matches the
    pure-Python reference implementation to 8 decimal places.
  - GIVEN a price list WHEN `rsi` is called THEN the result matches the reference to 1 dp.
  - GIVEN a price list WHEN `atr`, `williams_r`, and `stochastic` are called THEN results
    match the reference values within floating-point tolerance.
- **Acceptance:** All existing BDD tests pass; ruff reports no violations.

---

### NUMPY-002: Vectorise backtest replay metrics in `replay.py`

- **Files:** `backend/backtest/replay.py`
- **Scope:** The replay module processes arrays of equity values and P&L figures to produce
  summary statistics. These are batch operations over potentially thousands of rows — the ideal
  numpy use case.
- **Changes:**
  - Max drawdown: replace Python loop with `np.maximum.accumulate` peak tracking and `np.min`.
  - Equity curve: store as `np.ndarray` during replay, convert to list only for JSON output.
  - Win rate / profit factor: `np.sum(pnl > 0)`, `np.sum(pnl[pnl > 0])` etc.
  - Candle resampling (5 m → 15 m): replace manual slice loops with `np.array_split` grouping
    and `np.max`/`np.min`/`np.sum` per group.
- **BDD tests (write first):**
  - GIVEN an equity series with a known drawdown WHEN max drawdown is computed THEN the
    numpy result equals the pure-Python reference value.
  - GIVEN a list of P&L values WHEN win rate and profit factor are computed THEN results
    match manually calculated expected values.
- **Acceptance:** Existing backtest BDD tests pass unchanged; no new ruff violations.

---

### NUMPY-003: numpy-backed rolling statistics in `PerformanceLearner`

- **Files:** `backend/strategy/performance_learner.py` (or wherever the learner lives)
- **Scope:** Outcome tracking — rolling win rate, average P&L, loss-distribution percentiles —
  over the `signal_outcomes` history. Currently computed with Python loops each time the
  learner is queried.
- **Changes:**
  - Load outcome P&L column into `np.ndarray` once per query.
  - Compute `np.mean`, `np.median`, `np.percentile([25, 75, 95])` in one pass.
  - Use `np.convolve` or a rolling-window slice for the trailing-N-trade win rate.
- **BDD tests (write first):**
  - GIVEN 20 outcome records WHEN the learner computes rolling win rate (last 10) THEN the
    result equals the manually counted value.
  - GIVEN outcome P&L values WHEN percentiles are requested THEN 25th, 50th, and 75th
    percentile outputs match `statistics.quantiles` reference.
- **Acceptance:** All learner BDD tests pass; ruff reports no violations.

---

### NUMPY-004: Enforce numpy type hygiene across the boundary

- **Files:** `backend/analysis/indicators.py`, `backend/backtest/replay.py`
- **Scope:** numpy scalar types (`np.float64`, `np.int64`) are not JSON-serialisable. Any
  place that returns a numpy scalar directly to a FastAPI response or SQLAlchemy column will
  silently cause a serialisation error in production.
- **Changes:**
  - Wrap every public indicator return value in `float()` or `round()` (already done for most)
    to guarantee plain Python types at the boundary.
  - Add a `_to_py(val)` helper that calls `.item()` on numpy scalars and is a no-op for Python
    types — use it on all indicator return values.
  - Add a JSON encoder test: GIVEN indicator output WHEN passed to `json.dumps` THEN no
    `TypeError` is raised.
- **BDD tests (write first):**
  - GIVEN `compute_all()` is called with a plain list WHEN the result is serialised with
    `json.dumps` THEN no `TypeError` is raised.
- **Acceptance:** All BDD tests pass; ruff reports no violations.

---

## Bug Analysis — 2026-04-25

All 95 tests passed. The following bugs were identified via static code analysis and cross-file review.

All bugs below have been fixed. 104 tests pass; ruff reports no violations.

---

### ✅ BUG-010: Timezone mismatch corrupts daily loss state restore on non-UTC systems

- **Where:** `backend/main.py` line 183.
- **Problem:** `risk_engine._last_reset_date` is always set with `datetime.utcnow().date()` (UTC), but the restore guard compares it against `date.today()` (local system time). On any host not running in UTC, the two values diverge around midnight: the saved state uses the UTC date, the check uses the local date. The state is therefore silently not restored, resetting the daily loss counter on every restart between local midnight and UTC midnight — effectively disabling the daily loss limit for those hours.
- **Expected:** Both the save and the restore comparison must use the same timezone (UTC). Use `datetime.now(timezone.utc).date()` everywhere.
- **Suggested test:** GIVEN a risk state saved with today's UTC date WHEN the bot restores the state in a simulated non-UTC environment THEN the daily loss is correctly reloaded.

---

### ✅ BUG-011: `datetime.utcnow()` deprecated — codebase-wide compatibility risk

- **Where:** `backend/main.py`, `backend/risk/engine.py`, `backend/execution/paper.py`, `backend/execution/kraken.py`, `backend/approval/service.py`, `backend/storage/repository.py`, `backend/universe/resolver.py`, `tests/bdd_helpers.py`, and several test files.
- **Problem:** `datetime.utcnow()` is deprecated as of Python 3.12 and scheduled for removal. Python 3.14 (the current runtime) already emits `DeprecationWarning` on every call — 4 896 warnings were generated in the test suite. When the function is removed in a future release the entire application will crash at startup.
- **Expected:** Replace every `datetime.utcnow()` with `datetime.now(timezone.utc)` and every `datetime.utcnow().isoformat()` with `datetime.now(timezone.utc).isoformat()`. Import `timezone` from the standard `datetime` module.
- **Suggested test:** GIVEN the test suite runs WHEN any module is imported THEN no `DeprecationWarning` referencing `utcnow` is emitted.

---

### ✅ BUG-012: Paper short margin is not reserved — cash increases when opening a short

- **Where:** `backend/execution/paper.py` — `_has_sufficient_funds()` line 352, `_apply_fill()` line 386.
- **Problem:** `_has_sufficient_funds()` gates a new paper short on `self.cash >= fill_value + fee`. However `_apply_fill()` then does `self.cash += fill_value - fee` — cash *increases* when a short opens. The margin is checked but never deducted: the bot can open unlimited simultaneous short positions each time adding the short proceeds to cash, allowing effective leverage far beyond the configured starting capital. A subsequent buyback (short close) decreases cash, but by then unrealised losses can already exceed actual equity with no guard to prevent it.
- **Expected:** When opening a paper short (no existing long to close), deduct `fill_value` from cash as margin, then credit it back on close, mirroring real margin accounting.
- **Suggested test:** GIVEN starting cash of €1000 WHEN three paper shorts of €800 each are opened THEN the third is rejected for insufficient margin.

---

### ✅ BUG-013: Equity ticker snapshots are never persisted to the database

- **Where:** `backend/main.py` — `_equity_ticker_loop()` around line 829.
- **Problem:** `_equity_ticker_loop` updates `_equity_history` (in-memory deque) and `_current_equity` every 10 seconds but never calls `repo.save_equity_snapshot()`. DB persistence only occurs inside `_strategy_loop` every 30 seconds. If the bot crashes between 30-second strategy ticks, up to 30 seconds of equity snapshots are lost. On restart the Portfolio Value chart is pre-loaded from DB (`limit=288`), so the gap appears as a flat line. Under heavy load where the strategy loop is delayed, the gap can be larger.
- **Expected:** The equity ticker should persist each snapshot to the DB, or the strategy loop should persist on every tick rather than every 30 seconds. Alternatively, load the in-memory deque from the last DB write and acknowledge the gap as acceptable by design.
- **Suggested test:** GIVEN the equity ticker fires three times WHEN the DB is inspected THEN three new equity snapshot rows are present.

---

### ✅ BUG-014: `zip()` without `strict=` in MACD allows silent length mismatch

- **Where:** `backend/analysis/indicators.py` line 145.
- **Problem:** `zip(fast_series[offset:], slow_series)` silently truncates to the shorter series if an off-by-one occurs in the alignment calculation. If `offset` is computed incorrectly, the MACD line values are misaligned by one bar with no error raised, producing subtly wrong MACD line, signal, and histogram values without any indication of failure.
- **Expected:** Use `zip(fast_series[offset:], slow_series, strict=True)` so a length mismatch raises `ValueError` immediately, making alignment bugs visible.
- **Suggested test:** GIVEN a price series where `slow - fast` offset is deliberately wrong WHEN `macd()` is called THEN a `ValueError` is raised rather than silently returning misaligned values.

---

### ✅ BUG-015: Missing newline at end of file in `models.py` and `logging.py`

- **Where:** `backend/domain/models.py` line 142, `backend/observability/logging.py` line 72.
- **Problem:** Ruff reports `W292 No newline at end of file` for both files. Some tools (git diff, cat, POSIX utilities) treat a missing trailing newline as a malformed text file. This also causes noisy git diffs where the last line of the file appears modified whenever another tool writes it.
- **Expected:** Add a single trailing newline to both files.
- **Suggested test:** GIVEN ruff runs with `W292` enabled WHEN both files are checked THEN no `W292` violations are reported.
