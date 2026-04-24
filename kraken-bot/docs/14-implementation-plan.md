# Implementation Plan

This document tracks all identified bugs, improvements, and enhancements in the order they will be implemented. Items are worked sequentially: each item must be fully implemented, tested, and linted before the next begins.

**Status key:** `[ ]` = pending · `[x]` = done · `[~]` = in progress · `[!]` = blocked

---

## Phase 1 — Bugs (highest priority: correctness before features)

### B1 — `equity` NameError in strategy loop
**File:** `backend/main.py` ~lines 288, 328, 477  
**Problem:** The strategy loop referenced the local variable `equity` after it was moved to `_equity_ticker_loop`. Three call sites (`evaluate()`, `analyse_signal()`, `size_base`) would raise `NameError` at runtime.  
**Fix:** Replace all three occurrences with `_current_equity` (the global maintained by the equity ticker).  
**Status:** `[x]` — fixed as part of the Portfolio Value realtime work.

---

### B2 — Missing DB schema columns (silent data loss on restart)
**File:** `backend/storage/database.py`  
**Problem:** Three columns that the ORM models reference do not exist in the DB schema unless the ad-hoc `ALTER TABLE` migration block runs cleanly. If any column already exists the migration crashes, leaving the schema partially applied. Affected columns: `open_positions.trade_idea_id`, `signal_outcomes.closing_trade_idea_id`, `signal_outcomes.position_id`.  
**Fix:** Replace the raw-SQL migration block with idempotent `IF NOT EXISTS` guards, or migrate to Alembic.  
**Status:** `[x]`

---

### B3 — `LLMOnlyStrategy` fires one LLM call per market per tick (O(n) latency)
**File:** `backend/strategy/llm_only.py`  
**Problem:** Each 30-second strategy tick calls `recommend_trade()` for every active market sequentially. With 10+ markets and an Ollama round-trip of ~3 s each, a single tick can block for 30+ seconds — longer than the tick interval, causing ticks to pile up.  
**Fix:** Batch the LLM call into a single prompt covering all markets, or run calls concurrently with `asyncio.gather`, or throttle to one market per tick on a round-robin.  
**Status:** `[x]`

---

### B4 — `_briefed_news_ids` grows without bound (memory leak)
**File:** `backend/main.py`  
**Problem:** Every article ID that has been sent to the LLM is added to `_briefed_news_ids` (a plain Python `set`) and never pruned. After weeks of running the set could contain hundreds of thousands of IDs.  
**Fix:** Cap the set to the last N IDs (e.g. 1 000) using a deque-backed structure, or periodically discard IDs older than the news retention window.  
**Status:** `[x]`

---

### B5 — `daily_loss` not persisted across restarts (risk limit bypass)
**File:** `backend/risk/engine.py`  
**Problem:** `RiskEngine.daily_loss` is an in-memory float. A restart mid-day resets it to 0, allowing the bot to exceed the configured daily loss limit.  
**Fix:** Persist `daily_loss` and `daily_start_equity` to the DB (a single-row settings table or a dedicated `risk_state` table) and restore them on startup if the date matches today.  
**Status:** `[x]`

---

### B6 — Daily loss limit does not include the current trade's estimated loss
**File:** `backend/risk/engine.py` lines 102–104  
**Problem:** The guard `if self.daily_loss >= daily_limit` checks accumulated historical loss only. A trade that would push the total over the limit is still approved if `daily_loss` is just below the threshold.  
**Fix:** Change to `if self.daily_loss + estimated_loss >= daily_limit` so the full projected loss is checked before approval.  
**Status:** `[x]`

---

### B7 — RSI uses simple average, not Wilder's smoothed MA
**File:** `backend/analysis/indicators.py`  
**Problem:** The RSI implementation averages gains/losses over the period using a simple mean. Wilder's RSI uses an exponential smoothing formula (`prev_avg * (n-1)/n + new_value * 1/n`) which produces materially different values, especially over short windows.  
**Fix:** Implement Wilder's smoothed moving average for both average gain and average loss.  
**Status:** `[x]`

---

### B8 — `ApprovalService._pending` accessed directly during emergency stop (encapsulation breach)
**File:** `backend/main.py` / `backend/approval/service.py`  
**Problem:** The emergency-stop handler calls `approval_service._pending.clear()`, bypassing any locking or lifecycle logic in `ApprovalService`.  
**Fix:** Add a public `clear_pending()` method to `ApprovalService` and call that instead.  
**Status:** `[x]`

---

### B9 — `UniverseResolver` produces duplicate markets (fixed ∩ dynamic overlap)
**File:** `backend/universe/resolver.py`  
**Problem:** If a coin appears in both `fixed_markets` and the dynamic ETH-ecosystem list (e.g. `LINK/EUR` is in both), the dashboard market list and strategy loop will process it twice per tick.  
**Fix:** De-duplicate with `list(dict.fromkeys(fixed + dynamic))` in `resolve_universe()`.  
**Status:** `[x]`

---

### B10 — ATR and Stochastic use close price as high/low proxy
**File:** `backend/analysis/indicators.py`  
**Problem:** The bot only receives last-trade price from Kraken tickers, so ATR and Stochastic are computed with `high = low = close`. This makes ATR always 0 (no range) and Stochastic %K always 50 (midpoint of zero range).  
**Fix:** Either source true OHLC for indicator computation (the 5-min candle data already fetched by `_ohlc_loop` is available), or remove ATR/Stochastic from the indicator set and replace with indicators that are meaningful on tick data (e.g. Bollinger Bands, linear regression slope).  
**Status:** `[x]`

---

## Phase 2 — Improvements (quality, reliability, performance)

### I1 — Replace ad-hoc ALTER TABLE migrations with idempotent schema management
**File:** `backend/storage/database.py`  
**Problem:** Hand-rolled `ALTER TABLE … ADD COLUMN` statements fail if the column already exists. There is no rollback, no version tracking, and no way to apply migrations to a new DB cleanly.  
**Fix:** Introduce Alembic (or at minimum wrap every `ALTER TABLE` in a `try/except` and add a schema-version table). This overlaps with B2 — implement together.  
**Status:** `[x]`

---

### I2 — Add exponential back-off / circuit breaker to the Ollama client
**File:** `backend/llm/client.py`  
**Problem:** After an Ollama failure, the client applies a flat 5-minute cooldown. If the service recovers sooner, the bot misses it; if it stays down, the strategy loop still calls `_analyser` every 30 s and logs a failure each time.  
**Fix:** Implement a circuit-breaker with three states (closed / open / half-open) and exponential back-off (30 s → 60 s → 120 s → max 300 s).  
**Status:** `[x]`

---

### I3 — `ApprovalService` state not persisted (lost on restart)
**File:** `backend/approval/service.py`  
**Problem:** All pending approvals live in memory. A restart during a manual-approval window drops them silently.  
**Fix:** Persist the approval queue to the DB (new `pending_approvals` table) and reload on startup.  
**Status:** `[x]`

---

### I4 — Replace `OFFSET`-based price-tick trimming with a subquery
**File:** `backend/storage/repository.py`  
**Problem:** `trim_old_price_ticks()` uses `OFFSET N` to locate the cutoff row, which scans the entire table. On a busy instance with many markets and long uptime this becomes slow.  
**Fix:** Use `DELETE FROM price_ticks WHERE id NOT IN (SELECT id FROM price_ticks WHERE symbol = :sym ORDER BY timestamp DESC LIMIT :keep)`.  
**Status:** `[x]`

---

### I5 — Improve LLM market-name matching robustness
**File:** `backend/llm/analyser.py`  
**Problem:** The code normalises LLM-returned market keys by stripping slashes and lowercasing, but variants like `BTC-EUR` or `btc eur` still fail to match, silently discarding briefing data for that market.  
**Fix:** Pre-format the LLM prompt to mandate exact market symbols, and add a fuzzy-fallback match with a warning log when normalisation still fails.  
**Status:** `[x]`

---

### I6 — Confidence threshold constants extracted from `basic_strategy.py`
**File:** `backend/strategy/basic.py`  
**Problem:** Magic numbers for confidence adjustments (+0.1, -0.1, 0.45, 0.7, etc.) are scattered inline, making the strategy hard to tune or test.  
**Fix:** Extract all threshold and weight constants to a named-constant block at the top of the module (or to `config/settings.py`) with comments explaining each.  
**Status:** `[x]`

---

### I7 — Enforce strict `.env` case sensitivity
**File:** `backend/config/settings.py`  
**Problem:** `case_sensitive=False` means `KRAKEN_API_KEY` and `kraken_api_key` are both accepted, which can shadow production values with development overrides silently.  
**Fix:** Set `case_sensitive=True` and update `.env.example` to use consistent `UPPER_SNAKE_CASE` throughout.  
**Status:** `[x]`

---

### I8 — Currency symbol hardcoded as `£` throughout
**File:** `backend/main.py`, `frontend/index.html`  
**Problem:** The UI and activity log use `£` (GBP) even though the exchange operates in EUR and `settings.base_currency` exists.  
**Fix:** Replace all hardcoded `£` with a helper that reads `settings.base_currency` on the backend, and pass the symbol to the frontend via the dashboard API response.  
**Status:** `[x]`

---

### I9 — `PerformanceLearner` tracks win rate only — no loss distribution
**File:** `backend/strategy/learner.py`  
**Problem:** The learner records weighted win/loss counts but not the magnitude of wins and losses. A strategy with 50% win rate but 3:1 win-to-loss size is treated identically to one with the same win rate but 1:3 size.  
**Fix:** Record `mean_win_pnl`, `mean_loss_pnl`, and derive a Sharpe-like quality score per strategy+market combination.  
**Status:** `[x]`

---

### I10 — Add liquidity / volume filter to risk engine
**File:** `backend/risk/engine.py`  
**Problem:** A trade can be approved on a pair with near-zero volume, where the 0.1% slippage estimate is wildly optimistic and the actual fill would be far off market price.  
**Fix:** Add a minimum 24h-volume check to `evaluate_trade()`, sourced from the Kraken ticker `v` (volume) field already available in `TickerSnapshot`.  
**Status:** `[x]`

---

## Phase 3 — Enhancements (new capabilities)

### E1 — Real dynamic universe resolution (replace hardcoded placeholder)
**File:** `backend/universe/resolver.py`  
**Problem:** `_resolve_dynamic_markets()` returns a hardcoded list of 10 coins and logs a warning. The class was designed to query CoinMarketCap but never implemented.  
**Fix:** Implement a CoinMarketCap (or CoinGecko free-tier) API call to fetch the top N ETH-ecosystem tokens by market cap, filter to pairs available on Kraken, and return them.  
**Status:** `[x]`

---

### E2 — Multi-timeframe signal confirmation
**File:** `backend/strategy/basic.py`, `backend/main.py`  
**Problem:** All signals are generated from 30-second tick data. Higher-timeframe trend context (hourly / daily EMA direction) would reduce whipsaw entries.  
**Fix:** Use the already-fetched 5-min and 15-min OHLC candle data (available in `_ohlc_cache_5` / `_ohlc_cache_15`) to compute HTF EMA direction and add it as a confirmation gate in `BasicStrategy.evaluate()`.  
**Status:** `[x]`

---

### E3 — Trailing stop-loss
**File:** `backend/main.py` (stop-loss check section)  
**Problem:** The current stop-loss is a fixed percentage from entry. A position that has moved 10% in your favour still exits at the original 5%-below-entry stop, giving back all profit.  
**Fix:** Track a `trailing_high` (for longs) / `trailing_low` (for shorts) per position in `PaperExecutionEngine`. Each tick update the trail; trigger stop if price retraces beyond the trail threshold.  
**Status:** `[x]`

---

### E4 — Sentiment score fed into signal confidence
**File:** `backend/llm/analyser.py`, `backend/strategy/basic.py`  
**Problem:** The LLM market briefing produces a `sentiment` field (`bullish` / `bearish` / `neutral`) but it is displayed only on the dashboard; it does not influence signal confidence scores.  
**Fix:** Expose `_analyser.latest_briefing.sentiment` as a numeric adjustment (e.g. `+0.05` bullish, `-0.05` bearish) applied at the end of `BasicStrategy.evaluate()`.  
**Status:** `[x]`

---

### E5 — Strategy hot-reload endpoint
**File:** `backend/main.py`, `backend/strategy/`  
**Problem:** Changing strategy parameters (lookback, thresholds, confidence weights) requires a full bot restart, which resets in-memory state and loses the warm-up tick buffer.  
**Fix:** Add a `POST /api/control/strategies/reload` endpoint that reloads strategy parameters from `settings` (or a JSON config file) without stopping the strategy loop.  
**Status:** `[x]`

---

### E6 — Position-sizing based on Kelly / volatility
**File:** `backend/risk/engine.py`, `backend/strategy/basic.py`  
**Problem:** All trades are sized at a fixed `position_sizing_proposal` fraction of equity. Volatility (ATR once fixed by B10) and recent win rate (already in the learner) could drive dynamic sizing.  
**Fix:** Implement a Kelly-fraction position sizer that reads `PerformanceLearner` win rate and ATR, capped at the existing `max_loss_per_trade_percent` guard.  
**Status:** `[x]`

---

### E7 — Multi-timeframe P&L dashboard panel
**File:** `frontend/index.html`  
**Problem:** The dashboard shows total equity over time but no breakdown of P&L by day, week, or market.  
**Fix:** Add a new `/api/pnl-summary` endpoint that returns P&L grouped by day and by market. Add a collapsible "P&L Summary" panel to the dashboard with a small table and sparklines.  
**Status:** `[x]`

---

### E8 — Webhook / alert on significant events
**File:** `backend/main.py`  
**Problem:** There is no out-of-band notification mechanism. If the bot hits a daily loss limit or a stop-loss fires while the user is away from the browser, there is no alert.  
**Fix:** Add a configurable webhook URL (`settings.alert_webhook_url`) and POST a JSON payload on: daily loss limit hit, stop-loss triggered, emergency stop activated, bot restart.  
**Status:** `[x]`

---

### E9 — Export trade history to CSV
**File:** `backend/main.py` (new endpoint)  
**Problem:** There is no way to download trade data for external analysis (spreadsheets, backtesting tools, tax calculations).  
**Fix:** Add `GET /api/export/trades.csv` that streams `closed_trades` as a CSV with headers matching the Closed Positions table columns.  
**Status:** `[x]`

---

### E10 — Paper trading vs live trading toggle per market
**File:** `backend/config/settings.py`, `backend/execution/`  
**Problem:** The bot runs entirely in paper or entirely live. There is no way to go live on one market while keeping others in paper mode for comparison.  
**Fix:** Add a per-market `live` flag to the control state, and route execution to `PaperExecutionEngine` or `KrakenExecutionEngine` per market based on that flag.  
**Status:** `[x]`

---

## Summary Table

| # | Type | Item | Status |
|---|------|------|--------|
| B1 | Bug | `equity` NameError in strategy loop | `[x]` |
| B2 | Bug | Missing DB schema columns | `[x]` |
| B3 | Bug | `LLMOnlyStrategy` O(n) LLM calls per tick | `[x]` |
| B4 | Bug | `_briefed_news_ids` memory leak | `[x]` |
| B5 | Bug | `daily_loss` not persisted across restarts | `[x]` |
| B6 | Bug | Daily loss limit ignores current trade | `[x]` |
| B7 | Bug | RSI uses simple average not Wilder's SMMA | `[x]` |
| B8 | Bug | `_pending.clear()` bypasses `ApprovalService` | `[x]` |
| B9 | Bug | `UniverseResolver` duplicate markets | `[x]` |
| B10 | Bug | ATR/Stochastic use close-as-high/low proxy | `[x]` |
| I1 | Improvement | Idempotent schema migrations | `[x]` |
| I2 | Improvement | Ollama circuit breaker + exponential back-off | `[x]` |
| I3 | Improvement | Persist `ApprovalService` queue to DB | `[x]` |
| I4 | Improvement | Efficient price-tick trimming query | `[x]` |
| I5 | Improvement | Robust LLM market-name matching | `[x]` |
| I6 | Improvement | Extract strategy confidence constants | `[x]` |
| I7 | Improvement | Enforce `.env` case sensitivity | `[x]` |
| I8 | Improvement | Replace hardcoded `£` with config currency | `[x]` |
| I9 | Improvement | `PerformanceLearner` loss-distribution tracking | `[x]` |
| I10 | Improvement | Liquidity / volume filter in risk engine | `[x]` |
| E1 | Enhancement | Real dynamic universe resolution | `[x]` |
| E2 | Enhancement | Multi-timeframe signal confirmation | `[x]` |
| E3 | Enhancement | Trailing stop-loss | `[x]` |
| E4 | Enhancement | LLM sentiment fed into signal confidence | `[x]` |
| E5 | Enhancement | Strategy hot-reload endpoint | `[x]` |
| E6 | Enhancement | Kelly / volatility-based position sizing | `[x]` |
| E7 | Enhancement | Multi-timeframe P&L dashboard panel | `[x]` |
| E8 | Enhancement | Webhook alerts on significant events | `[x]` |
| E9 | Enhancement | Export trade history to CSV | `[x]` |
| E10 | Enhancement | Per-market paper vs live toggle | `[x]` |

---

*Last updated: 2026-04-24*
