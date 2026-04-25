# Background Loops

`main.py` starts five `asyncio` tasks in the FastAPI lifespan context manager. All run concurrently inside the single Uvicorn event loop. None of them block; all I/O is async or delegated to a thread pool.

```python
@asynccontextmanager
async def lifespan(app: FastAPI):
    await _ollama.probe()                           # warm-up LLM connection
    asyncio.create_task(_strategy_loop())
    asyncio.create_task(_news_loop())
    asyncio.create_task(_ohlc_loop())
    asyncio.create_task(_reflection_loop())
    asyncio.create_task(_equity_ticker_loop())      # supplemental 10-s equity snapshots
    yield
```

---

## Strategy Loop (`_strategy_loop`)

**Interval:** 30 seconds  
**Purpose:** Fetch live prices, evaluate momentum signals, run LLM analysis (including veto check), enforce risk rules, and route to execution.

### Full Execution Sequence Per Tick

```
1.  Emergency stop check           → sleep 30s and skip if active
2.  Market filter                  → only process enabled markets
3.  Kraken ticker snapshot         → WebSocket cache first, REST batch fallback for missing markets
4.  Update price history           → append to per-market deque (max 200 ticks)
5.  Warm-up guard                  → skip strategy evaluation until 35 ticks collected
6.  Persist price ticks            → repo.save_price_tick() per symbol
7.  Record portfolio value         → cash + marked value of open positions at tick prices
8.  Trim old price ticks           → every 10 ticks (~5 min)
9.  Stop-loss check                → close any position at ≥5% loss
10. Strategy evaluation            → selected strategy only
11. Per-signal LLM analysis        → _analyser.analyse_signal() for indicator-led signals
11a. LLM-only recommendation       → _analyser.recommend_trade() already made the LLM decision
11b. LLM veto check                → skip indicator-led signal if llm_used and confidence_scale < LLM_VETO_THRESHOLD
12. Risk evaluation                → risk_engine.evaluate_trade() with positions + cash
13. Mode routing                   → manual / semi_automated / fully_automated
14. Dashboard state update         → _latest_signals, _current_prices
```

> **Note:** `_record_equity_snapshot(prices)` runs during every strategy tick immediately after fresh market prices arrive. It computes `paper_engine.get_total_equity(prices)`, updates `_current_equity`, updates the risk engine, appends the point to `_equity_history`, and persists `total_equity`, `cash`, and `positions_value` to SQLite. The Equity Graph therefore records cash plus current holdings value on each market-data tick, and `/api/dashboard` aligns the final graph point with the same `Total Equity` value shown in the header.

`kraken_adapter.start_subscription(_active_markets, _cache_market_snapshot)` starts Kraken WebSocket ticker streaming after the active market list is validated. The 30-second strategy tick consumes cached WebSocket snapshots and only calls `get_tickers_batch()` for symbols that have not produced a stream update yet, preserving REST polling as a fallback.

### Warm-Up Period

The strategy will not generate signals until `_LOOKBACK_TICKS = 34` historical samples plus the latest tick have been collected. This prevents MACD and other indicators from running on insufficient data. The first signal can fire after approximately 17.5 minutes on a cold start (35 ticks × 30 seconds).

On restart, up to 60 price ticks are restored from the database, so warm-up may complete immediately.

### Stop-Loss Check

Before strategy evaluation every tick:

```python
for pos in paper_engine.open_positions():
    market_price = prices.get(pos.market)
    loss_pct = (pos.avg_price - market_price) / pos.avg_price   # for longs
    if paper_engine.stop_loss_triggered(pos.position_id, market_price, STOP_LOSS_ASSUMPTION):
        order = await paper_engine.close_position(pos.position_id, market_price)
        pnl = pos.size * (market_price - pos.avg_price)
        risk_engine.record_trade_result(pnl)
        repo.update_order_pnl(order.id, pnl)
        paper_engine.record_closed_trade(pos.position_id, market_price, "stop_loss")
        learner.record_outcome(...)
```

The stop-loss monitor is entry-price based and only fires when the position is losing by at least `STOP_LOSS_PCT`. A profitable retrace from a previous high/low is not recorded as `stop_loss`.

The iteration is over a list snapshot (`open_positions()` returns `list(self.positions.values())`), so concurrent modification of the positions dict during iteration is safe.

### Strategy Evaluation

The loop evaluates the single strategy selected in control state:

- `indicator_only` uses technical indicators only and requires at least six indicators to support the trade direction.
- `combined` uses `BasicStrategy.evaluate()` with indicator consensus, news sentiment, LLM briefing sentiment, and the LLM signal-analysis/veto pass.
- `llm` uses `LLMOnlyStrategy.evaluate()` and asks the LLM directly for `long`, `short`, or `hold`; indicators are passed into the LLM prompt but do not gate the trade locally.

Only one strategy is active at a time. The UI strategy selector writes `control.selected_strategy`, and the next signal tick applies that strategy to all markets.

### LLM Signal Analysis

For each generated signal:

```python
_hist = list(_price_history.get(idea.market, []))
_ind  = compute_indicators(_hist)
llm_analysis = await _analyser.analyse_signal(
    idea.market, idea.direction.value, _mom, idea.confidence, _latest_news,
    current_price   = _price,
    indicators      = _ind,
    equity          = equity,
    cash            = paper_engine.cash,
    open_positions  = paper_engine.open_positions(),
)
if llm_analysis.llm_used:
    if llm_analysis.confidence_scale < settings.llm_veto_threshold:
        # veto: log warning and skip this signal entirely
        continue
    idea.confidence = min(0.95, idea.confidence * llm_analysis.confidence_scale)
    idea.thesis    += f" · LLM: {llm_analysis.reasoning}"
```

If the LLM is unavailable (Ollama not running, timeout, or error), `llm_used = False`, confidence is unchanged, and no veto fires.

For `indicator_only`, no LLM signal-analysis pass runs. For `llm`, the strategy has already called `_analyser.recommend_trade()` and stored the LLM decision in the trade idea. The loop records that LLM metadata for persistence but does not run a second LLM veto/adjustment pass.

### Mode Routing

After risk evaluation (and only if `risk_decision.approved`):

**`manual`:** Log to activity feed. No action.

**`semi_automated`:**
1. Check if a same-direction position already exists for this market → skip if so
2. Check if a pending approval already exists for this market → skip if so
3. Otherwise call `approval_service.submit(idea, risk_decision)` → queued

**`fully_automated`:**
1. Check if a same-direction position already exists → skip silently
2. Capture existing FIFO long before execution (needed to compute P&L on close)
3. Compute position size: `(idea.position_sizing_proposal * equity) / market_price`
4. Call `paper_engine.execute(intent, market_price, ...)`
5. If SHORT and filled: call `record_closed_trade`, `risk_engine.record_trade_result`, `repo.update_order_pnl`, `learner.record_outcome`

---

## News Loop (`_news_loop`)

**Interval:** 5 minutes  
**Purpose:** Fetch RSS feeds, persist new articles, update the in-memory cache, trigger a market briefing if new articles are detected.

### News Sources

| Adapter | Feed |
|---------|------|
| CoinDeskAdapter | CoinDesk RSS |
| CoinTelegraphAdapter | Cointelegraph RSS |
| TheBlockAdapter | The Block RSS |
| DecryptAdapter | Decrypt RSS |
| BitcoinMagazineAdapter | Bitcoin Magazine RSS |
| CryptoSlateAdapter | CryptoSlate RSS |
| TheDefiantAdapter | The Defiant RSS |
| CryptoPotaroAdapter | CryptoPotato RSS |
| NewsBTCAdapter | NewsBTC RSS |
| ReutersBusinessAdapter | Reuters Business RSS |
| FearGreedAdapter | Alternative.me Fear & Greed JSON API |

### Execution Sequence

```
1.  Fetch from all adapters       → each runs in thread pool (urllib, synchronous)
2.  Normalise all `published_at` values to timezone-aware UTC, then sort descending
3.  Upsert each item to DB        → repo.upsert_news_item() (ignores duplicates)
4.  Rebuild _latest_news cache    → 60 most recent articles across all sources (no time cutoff)
5.  Compute new_ids               → current IDs minus _briefed_news_ids
6.  If new_ids not empty:
    a. Update _briefed_news_ids
    b. asyncio.create_task(_market_briefing_task(new_articles))
```

### `_market_briefing_task`

Spawned as a separate task so it does not delay the news loop:

```
1.  Collect current price + indicators for each active market
2.  Call _analyser.brief_market(new_articles, market_data)
3.  Cache result in _analyser.latest_briefing
4.  Persist to llm_briefings table (including briefed_news_ids list)
5.  Log to activity feed: key insight + per-market bias
```

The briefing task only fires when `_current_prices` is non-empty (i.e. the strategy loop has completed at least one tick). If the bot just started and news loads before prices arrive, no briefing is triggered until the next news cycle.

### ID Tracking

`_briefed_news_ids` is a module-level `set` pre-populated at startup with all news IDs currently in the database. This prevents re-briefing on articles that were already processed before the restart. Only genuinely new articles fetched after the bot starts will trigger a new briefing.

The latest briefing is also restored from the `llm_briefings` table at startup (via `_analyser.load_from_db()`), so signal analysis has LLM market context available immediately — it does not have to wait for the next news cycle.

---

## OHLC Loop (`_ohlc_loop`)

**Interval:** 120 seconds per full cycle, with 1 second between the two interval fetches per market, and 2.5 seconds between markets  
**Purpose:** Populate both candlestick chart caches (5-min and 15-min) without exceeding Kraken's public API rate limit.

### Design Rationale

The OHLC data is read-only and serves the dashboard charts. Fetching it inside the strategy loop would either slow each tick significantly or rate-limit the exchange ticker calls. A separate slow loop decouples chart refresh from signal generation.

### Execution Sequence

```
1.  Wait until _active_markets is populated (strategy loop must start first)
2.  For each enabled market:
    a. Call kraken_adapter.get_ohlc(market, interval=5,  candle_limit=100)
       → store in _ohlc_cache_5[market]
    b. Sleep 1.0 second (inter-interval gap)
    c. Call kraken_adapter.get_ohlc(market, interval=15, candle_limit=100)
       → store in _ohlc_cache_15[market]
    d. Sleep 2.5 seconds (inter-market gap)
3.  Sleep 120 seconds
4.  Repeat
```

The `GET /api/ohlc/{market}?interval=5` and `?interval=15` endpoint reads from `_ohlc_cache_5` and `_ohlc_cache_15` respectively. If a market has not yet been cached, an empty candle list is returned and the chart waits gracefully.

---

## Reflection Loop (`_reflection_loop`)

**Interval:** 1 hour (after initial 2-minute delay)  
**Purpose:** Ask the LLM to find patterns in recent closed trades and surface an actionable suggestion.

### Execution Sequence

```
1.  Sleep 120s on startup (let first trades accumulate)
2.  Fetch last 50 closed trades    → repo.get_closed_trades(limit=50)
3.  Call _analyser.reflect_on_outcomes(outcomes)
    → requires ≥5 closed trades; returns None otherwise
4.  If reflection returned:
    → cache in _analyser.latest_reflection
    → persist to llm_reflections table
    → log pattern + suggestion to activity feed
5.  Sleep 3600s
6.  Repeat
```

### Why `get_closed_trades` Not `get_signal_outcomes`

`get_closed_trades()` returns plain Python dicts with consistent key names (`"pnl"`, `"confidence"`, `"market"`, etc.). `get_signal_outcomes()` returns SQLAlchemy ORM model instances which must be accessed via attribute notation. The reflection code uses dict access, so `get_closed_trades()` is the correct call.

`get_closed_trades()` also left-joins `trade_ideas` on `trade_idea_id` and includes an `indicators` key per trade — the full indicator snapshot stored at entry time. This lets the LLM find patterns like "overbought RSI at entry correlated with stop-loss outcomes". Trades without a linked `trade_idea_id` have `indicators: null` and are still included.

---

## Equity Ticker Loop (`_equity_ticker_loop`)

**Interval:** 10 seconds  
**Purpose:** Recompute total portfolio equity from the latest known prices and push a supplemental snapshot to `_equity_history` between 30-second strategy ticks.

### Execution Sequence

```
1.  Sleep 10s
2.  If _current_prices is empty → skip (prices not yet fetched)
3.  Record portfolio value      → _record_equity_snapshot(_current_prices)
4.  Compute equity              → paper_engine.get_total_equity(_current_prices)
5.  Update _current_equity      → shared global read by strategy sizing
6.  Update risk engine          → risk_engine.update_equity(equity)
7.  Append and persist snapshot → _equity_history + repo.save_equity_snapshot(...)
8.  Repeat
```

`_equity_history` has `maxlen=1440`, giving up to 4 hours of rolling history at 10-second supplemental resolution. Startup loads the same 1,440-point window from SQLite, rather than only the most recent short slice, so a restarted dashboard keeps the wider graph range. Authoritative market-tick snapshots are recorded by `_strategy_loop` as soon as fresh prices arrive, so the graph does not wait for the ticker loop to catch up.

---

## Startup Sequence

When Uvicorn starts `main.py`, the following executes at module level (before any request handler runs):

```
1.  setup_logging()               → configures JSON log handler + file handler
2.  init_database()               → creates/migrates SQLite schema
3.  repo = Repository()
4.  learner = PerformanceLearner()
5.  _ollama = OllamaClient(...)
6.  _analyser = LLMAnalyser(_ollama)
7.  kraken_adapter = KrakenMarketAdapter(...)
8.  paper_engine = PaperExecutionEngine(starting_capital, repo)
9.  Wire repo into singletons     → activity.set_repo(repo)
                                     control.set_repo(repo)
                                     _analyser.set_repo(repo)
10. Restore risk rejections       → _risk_rejections deque pre-loaded from DB (last 50)
11. Load equity history from DB   → restore _equity_history deque (limit 1,440)
12. Restore cash from DB          → paper_engine.cash = repo.get_latest_cash()
13. Restore open positions from DB → paper_engine.restore_from_db()
14. Seed news cache from DB       → _latest_news = repo.get_recent_news(limit=60)
                                     _briefed_news_ids seeded from all current news IDs
15. Seed learner from DB          → learner.load_from_outcomes(repo.get_signal_outcomes())
16. Restore control state         → control.load_from_db()
                                     (emergency stop, disabled markets/strategies)
17. Restore LLM state             → _analyser.load_from_db()
                                     (latest_briefing, latest_reflection)
18. Seed activity log             → activity.seed_from_db() (last 200 entries)
```

Steps 9–18 ensure the bot resumes from its complete operational state after any restart. No data is lost: positions, equity, LLM context, control toggles, and the activity history are all immediately available when the dashboard first loads.
