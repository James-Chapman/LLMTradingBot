# Background Loops

`app.App.Run(ctx)` (`internal/app/app.go`) starts five background loops, each in its own goroutine. They share runtime state through `App` fields guarded by a single `sync.Mutex` that is never held across network, LLM, or exchange calls. Every loop sleeps with `sleepCtx`, so cancelling the root context (Ctrl+C or SIGTERM in `cmd/tradingbot`) stops them promptly; `Run` then waits for all loops and any in-flight briefing goroutines, and stops the Kraken ticker stream.

```go
// internal/app/app.go
func (a *App) Run(ctx context.Context) {
	loops := []func(context.Context){
		a.strategyLoop,
		a.newsLoop,
		a.ohlcLoop,
		a.reflectionLoop,
		a.equityTickerLoop, // supplemental 10-s equity snapshots
	}
	var wg sync.WaitGroup
	for _, loop := range loops {
		wg.Add(1)
		go func(run func(context.Context)) {
			defer wg.Done()
			run(ctx)
		}(loop)
	}
	<-ctx.Done()
	wg.Wait()
	a.market.Stop()
	a.Wait() // in-flight market briefing tasks
}
```

The LLM backend is probed once in `app.New` (`probeLLM`) before the loops start. Loop timings live in `app.Intervals`; zero values select the defaults shown below, and tests inject shorter intervals through `app.Options`. Each iteration runs inside `App.guarded`, which converts a panic into an error, logs it at ERROR level with the stack trace, and lets the loop continue.

---

## Strategy Loop (`strategyLoop`)

**Interval:** 30 seconds  
**Purpose:** Fetch live prices, evaluate the selected strategy, enforce risk rules, and route to execution.

On first entry, `bootStrategyLoop` resolves the market universe, validates the symbols against Kraken's tradable pairs, restores up to 60 price ticks per market from SQLite, logs "Bot started", sends a `bot_started` alert, and starts the Kraken ticker stream with `market.Start(active, a.cacheMarketSnapshot)`.

### Full Execution Sequence Per Tick (`strategyTick`)

```
1.  Emergency stop check           → skip tick (wait a full 30s) if active
2.  Market filter                  → only process enabled markets; skip if none
3.  Kraken ticker snapshot         → WebSocket cache first, REST GetTickersBatch for missing markets
4.  Update price history           → append to per-market slice (max 200 ticks), increment tick counter
5.  Build market data              → indicators (5-min candles) + 15-min higher-timeframe indicators + briefing sentiment
6.  Update mark prices             → paper.UpdateMarkPrices / UpdateTrailingPrices
7.  Persist price ticks            → repo.SavePriceTick() per symbol
8.  Record portfolio value         → recordEquitySnapshot (cash + marked value of open positions)
9.  Trim old rows                  → every 10 ticks (~5 min): price ticks, equity snapshots, activity
10. Stop checks                    → close positions at stop-loss or trailing stop
11. Warm-up guard                  → indicator-only: wait for 35 ticks; LLM strategies: wait for price ticks and news
12. Reconcile live orders          → every 2 ticks (~60s), live environment only
13. Strategy evaluation            → selected strategy only (LLM analysis/veto happens inside the strategy)
14. Learner adjustment             → learner.AdjustConfidence() per idea
15. Risk evaluation + persistence  → risk.EvaluateTrade(), repo.SaveTradeIdea() with full signal context
16. Mode routing                   → manual / semi_automated / fully_automated
17. Dashboard state update         → merge into latestSignals (max 12), bump dashboard version
```

A gated tick (emergency stop, no markets, no prices, warm-up) waits a full interval; an evaluated tick subtracts its own run time so the cadence stays at 30 seconds.

> **Note:** `recordEquitySnapshot(ctx, prices)` runs during every strategy tick immediately after fresh market prices arrive. In paper mode it builds the snapshot from the paper engine (`portfolio.BuildEquitySnapshot`); in live mode it reads Kraken balances through `kraken.ExecutionEngine.GetAccountSnapshot()`, keeping the last-known live values if Kraken is unavailable. It updates `currentEquity`, `currentCash`, and the risk engine (`risk.UpdateEquity`), appends the point to `equityHistory`, and persists `total_equity`, `cash`, and `positions_value` to SQLite. The Equity Graph therefore records the same account source used by the dashboard header.

`kraken.MarketAdapter.Start(symbols, callback)` runs the WebSocket v2 ticker subscription in a background goroutine. Every streamed update is stored by `cacheMarketSnapshot` into `latestSnapshots` and `currentPrices`. The 30-second strategy tick consumes these cached snapshots and only calls `GetTickersBatch()` for symbols that have not produced a stream update yet. If the stream itself fails, the adapter switches to REST ticker polling, so the cache keeps filling either way.

### Warm-Up Period

The indicator-only strategy will not generate signals until `strategy.LookbackTicks = 34` historical samples plus the latest tick have been collected. This prevents MACD and other indicators from running on insufficient data. The first signal can fire after approximately 17.5 minutes on a cold start (35 ticks × 30 seconds).

LLM-backed strategies use a data-driven check instead: each declares `RequiredPriceTicks()`, and the tick waits ("LLM data warmup waiting for … price ticks and news") until at least one market has that many ticks and the news cache is non-empty. `app.TradeWarmupTicks` is `0`; there is no fixed post-start delay.

On restart, up to 60 price ticks per market are restored from the database, so warm-up may complete immediately.

### Stop-Loss and Trailing-Stop Check

Before strategy evaluation every tick (`checkStops`):

```go
for _, pos := range a.paper.OpenPositions() {
	marketPrice, ok := prices[pos.Market]
	if !ok {
		continue
	}
	stopHit := a.paper.StopLossTriggered(pos.PositionID, marketPrice, a.risk.StopLossAssumption())
	trailHit := !stopHit && a.paper.TrailingStopTriggered(pos.PositionID, marketPrice, a.settings.TrailingStopPct)
	if !stopHit && !trailHit {
		continue
	}
	closeReason := "trailing_stop"
	if stopHit {
		closeReason = "stop_loss"
	}
	order, ok := a.paper.ClosePosition(pos.PositionID, marketPrice, paper.CloseOptions{})
	if !ok {
		continue
	}
	pnl, ok := a.paper.RecordClosedTrade(pos.PositionID, order.Price, closeReason, "")
	if !ok {
		continue
	}
	strategyID, direction := a.metaStrategyDirection(pos.PositionID)
	a.recordClosedOutcome(ctx, order.ID, strategyID, pos.Market, direction, pnl) // risk + order P&L + learner
	a.sendAlert(ctx, closeReason, map[string]any{"market": pos.Market, "position_id": pos.PositionID, "pnl": pnl})
}
```

The stop-loss monitor is entry-price based and only fires when the position is losing by at least `STOP_LOSS_PCT`. The trailing stop fires when price retraces `TRAILING_STOP_PCT` from the position's peak watermark and is recorded as `trailing_stop`, not `stop_loss`.

`OpenPositions()` returns a copy of the positions slice, and each position is re-checked with `paper.Position()` before closing, so a concurrent close from the HTTP API cannot be processed twice.

### Strategy Evaluation

The loop evaluates the single strategy selected in control state (`evaluateStrategy`):

- `basic_strategy` uses technical indicators only and requires the indicator consensus gates to pass.
- `basic_and_llm_strategy` uses `BasicAndLLMStrategy.Evaluate()` with indicator consensus, briefing sentiment, and the LLM signal-analysis/veto pass, which runs inside the strategy.
- `llm_only_strategy` uses `LLMOnlyStrategy.Evaluate()` and asks the LLM directly for `long`, `short`, or `hold`; indicators are passed into the LLM prompt but do not gate the trade locally.

Only one strategy is active at a time. The UI strategy selector writes `control.State.SelectStrategy()`, and the next tick applies that strategy to all markets. If the selected ID is unknown, the loop falls back to `basic_and_llm_strategy`. LLM strategies receive the news cache, the analyser, the latest briefing, and the current portfolio in `strategy.Input`.

### LLM Signal Analysis

For `basic_and_llm_strategy`, each market that passes the indicator gates is sent to the analyser from inside the strategy:

```go
// internal/strategy/basic_and_llm.go
analysis := analyser.AnalyseSignal(ctx, llm.SignalRequest{
	Market:         symbol,
	Direction:      string(sig.Direction),
	MomentumPct:    sig.Momentum * 100,
	BaseConfidence: baseConfidence,
	News:           in.News,
	CurrentPrice:   data.Price,
	Indicators:     data.Indicators,
	Equity:         in.Portfolio.Equity,
	Cash:           in.Portfolio.Cash,
	OpenPositions:  in.Portfolio.OpenPositions,
})
if analysis.ConfidenceScale < b.settings.LLMVetoThreshold {
	return nil // vetoed
}
finalConfidence := math.Min(calc.MaxConfidence, baseConfidence*analysis.ConfidenceScale)
if analysis.LLMUsed {
	thesis = append(thesis, "LLM: "+analysis.Reasoning)
}
```

If no LLM backend can answer (LM Studio down and no remote fallback, timeout, or error), `LLMUsed` is false, the scale is neutral, confidence is unchanged, and no veto fires.

For `basic_strategy`, no LLM pass runs. For `llm_only_strategy`, the strategy has already called `LLMAnalyser.RecommendTrade()` and stored the LLM decision in the trade idea. The loop rebuilds the LLM metadata from `SupportingSignals` (`signalLLMAnalysis`) for persistence but does not run a second LLM veto/adjustment pass.

### Mode Routing

After risk evaluation (`handleIdea`). A rejected decision is logged, stored in `risk_rejections`, and triggers a `daily_loss_limit_hit` alert when the reason mentions the daily loss limit. Approved decisions are routed by mode:

**`manual`:** Log to activity feed. No action.

**`semi_automated`:**
1. Check if a same-direction position already exists for this market → skip if so
2. Otherwise call `approvals.Submit(idea, decision)`; it returns `false` when an approval is already pending for this market → skip

**`fully_automated`:**
1. Skip (and drop from the signal panel) if confidence is below `MIN_SIGNAL_CONFIDENCE`
2. Check if a same-direction position already exists → skip silently
3. Skip with a warning if there is no live price
4. Compute position size (`sizeInBase`): `decision.ProposedSize / marketPrice`, or `PositionSizingProposal * equity / marketPrice` when the risk engine proposed no amount
5. Route the intent (`executeIntent`): `kraken.ExecutionEngine.Execute()` if the environment is `live` and the market is flagged live, otherwise `paper.Engine.Execute()` (a live-flagged market in the paper environment logs a warning and trades on paper)
6. If paper, SHORT and filled with a position ID: `paper.RecordClosedTrade`, then `recordClosedOutcome` (risk result, order P&L, learner)
7. If paper and filled: persist an equity snapshot immediately so a crash before the next tick does not lose the trade

---

## News Loop (`newsLoop`)

**Interval:** 5 minutes  
**Purpose:** Fetch news, persist new articles, update the in-memory cache, trigger a market briefing if new articles are detected, and re-probe LM Studio.

### News Sources

| Adapter | Feed |
|---------|------|
| `news.RSSAdapter` (one per `NEWS_SOURCES` entry) | CoinDesk, Cointelegraph, The Block, Decrypt, Bitcoin Magazine, CryptoSlate, The Defiant, CryptoPotato, NewsBTC (defaults) |
| `news.FearGreedAdapter` | Alternative.me Fear & Greed JSON API (always included) |

RSS adapters are built from `NEWS_SOURCES` by `news.BuildRSSAdapters`; there are no per-feed adapter types.

### Execution Sequence (`newsCycle`)

```
1.  Fetch from all adapters       → sequentially; each adapter logs its own failures
2.  Normalise all PublishedAt values to UTC, then sort descending
3.  Upsert each item to DB        → repo.UpsertNewsItem() (INSERT OR IGNORE)
4.  Rebuild latestNews cache      → 60 most recent articles across all sources (no time cutoff), 200-char summaries
5.  Compute fresh articles        → cached IDs not in briefedNewsIDs; briefedNewsIDs is replaced by the cached IDs
6.  If fresh articles exist       → go a.marketBriefingTask(ctx, fresh) (tracked by a WaitGroup)
7.  Log "News fetched" to activity feed
8.  llmClient.RecheckPrimary(ctx) → re-probe LM Studio; switch backends if its availability changed
```

### `marketBriefingTask`

Runs in its own goroutine so it does not delay the news loop:

```
1.  Collect current price + indicators for each active market
2.  Call analyser.BriefMarket(ctx, articles, markets)
3.  The analyser caches the result as its latest briefing
4.  The analyser persists it to llm_briefings (including the briefed news IDs)
5.  Log to activity feed: key insight + per-market bias
```

The briefing only runs when at least one active market has a current price (i.e. the strategy loop has received prices). If news loads before prices arrive, those articles are still marked as seen, so they are not briefed later; only articles fetched in later cycles trigger a briefing.

### ID Tracking

`briefedNewsIDs` is a `map[string]bool` seeded at start-up with the IDs of the 60 most recent news items in the database. This prevents re-briefing on articles that were already processed before the restart. Only genuinely new articles fetched after the bot starts trigger a new briefing. The set is replaced on every cycle so it stays bounded to the cache size.

The latest briefing is also restored from the `llm_briefings` table at start-up (via `LLMAnalyser.LoadFromDB()`), so signal analysis has LLM market context available immediately — it does not have to wait for the next news cycle.

---

## OHLC Loop (`ohlcLoop`)

**Interval:** 120 seconds per full cycle, with 1 second between the two interval fetches per market, and 2.5 seconds between markets  
**Purpose:** Populate both candlestick chart caches (5-min and 15-min) without exceeding Kraken's public API rate limit.

### Design Rationale

The OHLC data serves the dashboard charts and the indicator calculations (5-minute candles for ATR/volume indicators, 15-minute candles for the higher-timeframe filter). Fetching it inside the strategy loop would either slow each tick significantly or rate-limit the exchange ticker calls. A separate slow loop decouples candle refresh from signal generation.

### Execution Sequence

```
1.  Wait until activeMarkets is populated (polls every 2s; strategy loop must boot first)
2.  For each enabled market:
    a. refreshOHLC(market, 5)  → market.GetOHLC(ctx, market, 5, 100)  → ohlc5[market]
    b. Sleep 1 second (inter-interval gap)
    c. refreshOHLC(market, 15) → market.GetOHLC(ctx, market, 15, 100) → ohlc15[market]
    d. Sleep 2.5 seconds (inter-market gap)
3.  Sleep 120 seconds
4.  Repeat
```

Failed or empty fetches leave the previous cache entry in place. The `GET /api/ohlc/{market}?interval=5` and `?interval=15` endpoint reads from `ohlc5` and `ohlc15` respectively. If a market has not yet been cached, an empty candle list is returned and the chart waits gracefully.

---

## Reflection Loop (`reflectionLoop`)

**Interval:** 1 hour (after initial 2-minute delay)  
**Purpose:** Ask the LLM to find patterns in recent closed trades and surface an actionable suggestion.

### Execution Sequence

```
1.  Sleep 120s on start-up (let first trades accumulate)
2.  Fetch last 50 closed trades    → repo.GetClosedTrades(ctx, 50)
3.  Convert to []llm.TradeOutcome  → including the entry-time indicator snapshot
4.  Call analyser.ReflectOnOutcomes(ctx, outcomes)
    → requires ≥5 closed trades and an LLM that can attempt a call; returns nil otherwise
5.  If a reflection is returned:
    → the analyser caches it as its latest reflection
    → the analyser persists it to llm_reflections
    → log pattern + suggestion to activity feed
6.  Sleep 3600s
7.  Repeat
```

### Why `GetClosedTrades` Not `GetSignalOutcomes`

`Repository.GetClosedTrades()` left-joins `trade_ideas` on `trade_idea_id` and includes an `Indicators` map per trade — the full indicator snapshot stored at entry time. This lets the LLM find patterns like "overbought RSI at entry correlated with stop-loss outcomes". Trades without a linked `trade_idea_id` have no indicators and are still included. `GetSignalOutcomes()` returns only the outcome rows and is used to seed the learner.

---

## Equity Ticker Loop (`equityTickerLoop`)

**Interval:** 10 seconds  
**Purpose:** Recompute total portfolio equity from the latest known prices and push a supplemental snapshot to `equityHistory` between 30-second strategy ticks.

### Execution Sequence

```
1.  Sleep 10s
2.  If currentPrices is empty → skip (prices not yet fetched)
3.  Record portfolio value      → recordEquitySnapshot(ctx, prices)
4.  Compute account snapshot    → paper engine in paper mode, Kraken balances in live mode
5.  Update currentEquity/currentCash → shared state read by strategy sizing and risk
6.  Update risk engine          → risk.UpdateEquity(equity)
7.  Append and persist snapshot → equityHistory + repo.SaveEquitySnapshot(...)
8.  Repeat
```

`equityHistory` is capped at 1,440 points (`equityHistoryMax`), giving up to 4 hours of rolling history at 10-second supplemental resolution. Start-up loads the same 1,440-point window from SQLite, rather than only the most recent short slice, so a restarted dashboard keeps the wider graph range. Authoritative market-tick snapshots are recorded by the strategy loop as soon as fresh prices arrive, so the graph does not wait for the ticker loop to catch up. Errors are logged at WARN level and the loop continues.

---

## Start-up Sequence

`cmd/tradingbot` runs these steps before any request handler or loop starts:

```
1.  parseFlags()                        → -db, -host, -port, -log, -frontend
2.  observability.SetupLogging("INFO")  → JSON logs to stdout + the -log file (+ llm.log)
3.  storage.Open(-db)                   → opens SQLite and creates/migrates the schema
    app.LoadSettings() + Validate()     → defaults → saved dashboard settings → flags
    observability.SetLevel(LOG_LEVEL)   → DEBUG when the DEBUG switch is on
4.  app.New(ctx, settings, repo):
    a. learner.New()
    b. llm.NewSwitchingClientFromSettings() → LM Studio primary + remote OpenAI-compatible fallback
    c. kraken.NewMarketAdapter(), kraken.NewExecutionEngine(), universe resolver, news adapters
    d. strategy.All(settings)
    e. llm.NewAnalyser(), risk.NewEngine(), approval.NewService(), paper.NewEngine(), control.NewState()
    f. Wire repo into services          → activity.SetStore, control.SetRepo, analyser.SetStore
    g. Reload pending approvals         → approvals.LoadPendingFromRepository()
    h. Restore risk rejections          → last 50, newest first
    i. Load equity history              → last 1,440 points; restore paper cash from GetLatestCash()
                                          (header equity/cash restored from history in paper mode only)
    j. Restore open positions           → paper.RestoreFromDB()
    k. Restore daily risk counters      → repo.LoadRiskState() → risk.RestoreState()
    l. Seed news cache                  → latestNews = repo.GetRecentNews(60); briefedNewsIDs from those IDs
    m. Seed learner                     → learner.LoadFromOutcomes(repo.GetSignalOutcomes("", 1000))
    n. Restore control state            → control.LoadFromDB() (emergency stop, disabled markets/strategies,
                                          live markets, selected strategy)
    o. Restore LLM state                → analyser.LoadFromDB() (latest briefing, latest reflection)
    p. Seed activity log                → activity.SeedFromStore() (last 200 entries)
    q. Probe LLM                        → llmClient.Probe() (LM Studio first, then remote)
5.  Build the HTTP server               → server.New(bot, server.Options{FrontendDir: ...})
6.  go bot.Run(ctx)                     → start the five loops
7.  srv.ListenAndServe()                → serve HOST:PORT until shutdown
```

Steps 4f–4q ensure the bot resumes from its complete operational state after any restart. No data is lost: positions, equity, LLM context, control toggles, pending approvals, and the activity history are all immediately available when the dashboard first loads.

On shutdown, the root context is cancelled, the HTTP server is given 10 seconds to finish in-flight requests, and `main` waits for the loops to exit before returning.
