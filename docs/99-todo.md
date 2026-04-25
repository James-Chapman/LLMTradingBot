# Development Todo List

This document tracks all identified bugs, improvements, and enhancements in the order they will be implemented. Items are worked sequentially: each item must be fully implemented, tested, and linted before the next begins.

**Status key:** `[ ]` = pending · `[x]` = done · `[~]` = in progress · `[!]` = blocked

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
