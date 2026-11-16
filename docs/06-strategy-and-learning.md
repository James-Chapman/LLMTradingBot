# Strategy & Learning

## Shared Calculation Packages

Strategy calculations are split by responsibility:

- `internal/analysis` owns raw technical indicator formulas such as RSI, EMA, Bollinger Bands, MACD, ATR, Stochastic, Williams %R, and time-window price changes. `analysis.ComputeAll(prices, tickSeconds, candles)` returns an `analysis.Indicators` map (`map[string]any`) with nested groups such as `bb`, `macd`, `stoch` and `price_changes`. When 5-minute OHLC candles are cached, ATR and Stochastic use real high/low data instead of the close-price approximation.
- `internal/strategy/calc` owns strategy decision formulas: price momentum (`CalculateMomentum`), ATR-adjusted momentum thresholding (`EvaluateMomentumGate`), higher-timeframe alignment (`CheckHigherTimeframeAlignment`), RSI/Bollinger hard filters (`CheckHardFilters`), indicator voting (`VoteIndicators`), confidence components (`CalculateConfidence`), and volatility-adjusted position sizing (`CalculatePositionSizing`). All thresholds are named constants in `calc/constants.go`.

The eight basic strategies (`BasicStrategy`, `BasicAndLLMStrategy`) share one gate pipeline (`evaluateIndicatorGates` in `internal/strategy/indicator_signal.go`); `LLMOnlyStrategy` and the LLM recommendation prompt path use the same `calc` helpers instead of duplicating formulas locally.

Every strategy implements the `strategy.Strategy` interface:

```go
type Strategy interface {
	ID() string               // canonical strategy id
	UsesLLM() bool            // needs news + analyser
	RequiredPriceTicks() int  // price-history depth an LLM strategy needs; 0 otherwise
	Evaluate(ctx context.Context, in Input) []domain.TradeIdea
}
```

`strategy.All(settings)` returns one instance of each (21 strategies), and `strategy.ByID()` looks one up (legacy IDs `basic_strategy`, `basic_and_llm_strategy`, `basic_trend` and `llm_only` are mapped to their canonical IDs by `control.CanonicalStrategyID`). Markets are evaluated in sorted order so results are deterministic.

## Basic Strategies (`internal/strategy/basic.go`, `basic_and_llm.go`)

Since 0.18.0 the basic indicator rules read **finished 1-hour or 4-hour candles** instead of 30-second price ticks. There are eight versions:

| Strategy ID | Constant | Candles | LLM | Shorts |
|---|---|---|---|---|
| `basic_strategy_1h` | `strategy.Basic1hID` | 1 h | — | paper shorts |
| `basic_strategy_long_1h` | `strategy.BasicLong1hID` | 1 h | — | long only |
| `basic_strategy_4h` | `strategy.Basic4hID` | 4 h | — | paper shorts |
| `basic_strategy_long_4h` | `strategy.BasicLong4hID` | 4 h | — | long only |
| `basic_and_llm_strategy_1h` | `strategy.BasicAndLLM1hID` (also `strategy.DefaultID`) | 1 h | reviews each entry | paper shorts |
| `basic_and_llm_strategy_long_1h` | `strategy.BasicAndLLMLong1hID` | 1 h | reviews each entry | long only |
| `basic_and_llm_strategy_4h` | `strategy.BasicAndLLM4hID` | 4 h | reviews each entry | paper shorts |
| `basic_and_llm_strategy_long_4h` | `strategy.BasicAndLLMLong4hID` | 4 h | reviews each entry | long only |

The two types are `BasicStrategy` (no LLM) and `BasicAndLLMStrategy`; both embed `basicCore`, which does the candle work:

1. **Candle view.** The newest 200 finished candles (the forming last row is dropped) become a `MarketData` view: `Price` is the last close, `PreviousPrice` the close before it, `PriceHistory` the closes, and `Indicators = analysis.ComputeAll(closes, candleSeconds, candles)`. The 5m/15m price changes are meaningless on hour bars, so `price_changes` is replaced by `analysis.CandleChanges`: the change over **10 and 30 candles**. On 30-second ticks the momentum compares two 5-tick windows (10 ticks, 5 minutes) and the price-change votes span 5 and 15 minutes, i.e. 1× and 3× the momentum span; on candles the momentum spans 10 candles, hence 10 and 30. The higher timeframe is the finished 4-hour candles for the 1h versions and **whole UTC days** (six finished 4-hour candles merged) for the 4h versions. Fewer than `BasicMinClosedCandles` (40) finished candles: no reading (warm-up).
2. **Reading.** The view goes through the same gate pipeline as before (`evaluateIndicatorGates`, below). The reading also records whether the consensus has **turned** (`consensusTurns`): bearish when the candle momentum fell past the momentum gate or the net indicator vote is `BASIC_EXIT_VOTES` or more against a long; bullish the other way round. Before 0.20.0 any negative net vote counted, which closed most positions within hours. It also records the **trend**: the last close against `trend_ema`, the `BASIC_TREND_EMA`-period EMA of the higher timeframe (4-hour candles for 1h, days for 4h), added to `HigherTimeframe` by the candle view; unknown while the higher timeframe is shorter than the period. A reading is memoised per market until a new candle closes; at each new candle the core counts the closed candles in a row with a turn (`bearRun`, `bullRun`) and whether a strong-enough entry signal is new on this candle (`freshSignal`).
3. **Action** (per tick, per market, by what is held):
   - **Long held:** a **rule exit** (a sell with `TradeIdea.Exit` set: it skips `MIN_SIGNAL_CONFIDENCE`, so the learner cannot block it; confidence `CandleConfidence`, 0.95) when the live price hits the trailing stop or the profit target (below), when the consensus has turned bearish on `BASIC_EXIT_CONFIRM_CANDLES` closed candles in a row, or at once on a full short signal. Repeated every tick until sold.
   - **Paper short held:** the same rules mirrored emit a covering long (confidence 0.95).
   - **Nothing held:** a long signal emits a long entry; a short signal emits a paper short, except in the long-only versions, which never open a short. The entry must pass the entry rules below. Each entry is emitted **once per candle**: an entry rejected by the risk engine (often below `MIN_SIGNAL_CONFIDENCE`) or vetoed by the LLM waits for the next candle instead of being re-proposed on every tick.

   **Exit and entry rules (0.20.0).** All are live settings in the Signals group, shared by the eight versions; 0 (or false) switches a rule off.

   | Setting | Default | Rule |
   |---|---|---|
   | `BASIC_EXIT_VOTES` | 3 | Net indicator votes against a position that count as a turn (a momentum move past its gate also counts) |
   | `BASIC_EXIT_CONFIRM_CANDLES` | 3 | Closed candles in a row the turn must hold before the rule exit; each candle counts once however many ticks read it, and a candle without a turn starts the count again |
   | `BASIC_TREND_EMA` | 50 | No long below, no short above this EMA of the higher timeframe; an unknown trend blocks nothing |
   | `BASIC_TRAIL_ATR` | 5 | Close once the live price gives back this many candle ATRs from the best price since entry: the entry price, the live price and the highs (lows for a short) of the candles that began after the position opened |
   | `BASIC_COOLDOWN_CANDLES` | 6 | After a position in a market is seen closed (by any exit, the stop-loss included), wait this many closed candles before entering it again |
   | `BASIC_ENTRY_VOTES` | 1 | Net indicator votes an entry signal needs (1 = the old consensus gate) |
   | `BASIC_FRESH_SIGNAL` | false | Enter only on the first closed candle of a strong-enough signal |
   | `BASIC_MIN_ATR_COST` | 1 | Skip entries whose candle `atr_pct` is below this many times `FEE_AND_SLIPPAGE`: too quiet for a typical move to pay the costs |
   | `BASIC_TAKE_PROFIT_ATR` | 0 | Close once the live price is this many candle ATRs (the current ATR) in profit from the entry price |

   The state behind these rules (turn counts, the side last held, the candle a position closed on) lives in the strategy and restarts when settings are saved (the strategies are rebuilt) or the bot restarts. `BASIC_MIN_ATR_COST` compares against `FEE_AND_SLIPPAGE`: live defaults to 0.9 % since 0.21.0 (Kraken Pro's Tier 1 taker fee, 0.80 % on 2026-11-16, plus 0.10 % slippage), while the backtest sets it to the run's taker fee plus the expected half-spread (1.3 % by default), so live filters somewhat less than the tuning runs did.
4. **Idea.** Entries carry the gate confidence, the indicator signals, `candle_interval` ("1h"/"4h") and `current_price` = the **live** price (not the last close); sizing comes from `calc.CalculatePositionSizing(atr_pct)`. The 10 % stop-loss and 15 % trailing stop still apply.

**LLM versions.** `BasicAndLLMStrategy` sends each new entry (long or paper short) to `Analyser.AnalyseSignal()` with the candle indicators, the live price and `TickSeconds` = the candle length, so the prompt says "Technical indicators (1h candles)" or "(4h candles)". Because entries are emitted once per candle, the LLM is asked at most once per market per candle. The reply's `confidence_scale` multiplies the entry's confidence (capped at 0.95); below `LLM_VETO_THRESHOLD` (0.70) the entry is dropped for that candle. An unavailable LLM passes the entry unscaled, with the `llm_*` signals nil. Exits and covers never wait for the LLM. With no analyser supplied (tests) it uses `strategy.NeutralAnalyser`.

**Old IDs.** A saved selection of `basic_strategy` (or the older `basic_trend`) loads as `basic_strategy_1h`, and `basic_and_llm_strategy` as `basic_and_llm_strategy_1h` (`control.LegacyStrategyIDs`). Trades, signals and learner outcomes recorded before 0.18.0 keep their old IDs, so the learner starts fresh for the new strategies; the dashboard labels the old IDs "(ticks, retired)".

**Backtest replay** (`internal/backtest`). The replay feeds non-LLM strategies the same `strategy.MarketData` as live, built by the shared `marketdata.Build` (including the 1-hour candles), and runs the risk engine, learner, stops and fully automated gates in the live order; LLM strategies are refused. The replay is spot only, so `basic_strategy_1h` and `basic_strategy_long_1h` trade identically there (a sell with nothing held is ignored). See [14-backtesting.md](14-backtesting.md).

Two indicator details: RSI on a flat series (no gains and no losses, e.g. a pegged pair) is a neutral 50, not 100; and `atr_pct` is computed from the unrounded ATR, because the published `atr` is rounded to 6 decimal places and is 0 for sub-cent coins.

---

### Signal Generation Logic

The gates below were written for 30-second ticks and are applied unchanged to the candle view, so "price history" means candle closes and the higher timeframe is the 4-hour (1h versions) or daily (4h versions) indicators.

```go
func (b *BasicStrategy) Evaluate(ctx context.Context, in strategy.Input) []domain.TradeIdea
```

`in.MarketData` is a `map[string]strategy.MarketData` built by the strategy loop with `Price`, `PreviousPrice`, `PriceHistory`, `Volume`, `LLMSentiment`, `Indicators` (strategy ticks plus 5-minute candles) and `HigherTimeframe` (indicators computed from 15-minute candles). For each market:

#### 1. Momentum Gate

```go
// calc.CalculateMomentum with MomentumWindowPoints = 5
baseline := clean[len(clean)-10 : len(clean)-5]
recent := clean[len(clean)-5:]
momentum := (windowAverage(recent) - windowAverage(baseline)) / windowAverage(baseline)

// calc.EvaluateMomentumGate
dynamic := (atrPct / 100) * MomentumATRMultiplier // 1.0
effective := math.Max(dynamic, MomentumThreshold)  // floor 0.002
passes := math.Abs(momentum) >= effective
```

The rolling price history is passed into every strategy and the LLM recommendation prompt path. When at least 10 valid (finite, positive) prices are available, momentum is the percentage change between the previous 5-price average and the latest 5-price average. This reduces sensitivity to one sampled price that happens to be unusually far from the surrounding ticks.

`PreviousPrice` is the sparse-data fallback while the bot is warming up. It comes from `MOMENTUM_LOOKBACK_TICKS` ticks back when available:

```go
lookback := a.settings.MomentumLookbackTicks + 1
prev := ps.snap.Price
if len(hist) >= lookback {
	prev = hist[len(hist)-lookback]
}
// fallback momentum = (currentPrice - previousPrice) / previousPrice
```

The momentum must exceed the larger of the fixed 0.2% floor and the ATR-derived noise threshold (ATR as a percentage of price). The momentum gate is purely for noise suppression. The momentum value determines the initial hypothesis direction (LONG if positive, SHORT if negative) but does not win by itself — the indicator consensus must confirm.

#### 2. Higher-Timeframe Alignment

```go
cross, _ := higherTimeframe.Str("ema_cross") // from 15-minute candles
```

The signal is blocked only when the 15-minute EMA cross opposes the proposed direction (`bearish` for a LONG, `bullish` for a SHORT). Missing or `neutral` higher-timeframe data is allowed.

#### 3. Hard Filters

Block trades into extreme conditions where the probability of reversal is high:

| Filter | Condition | Effect |
|--------|-----------|--------|
| RSI overbought | RSI ≥ 80 and LONG | Skip |
| RSI oversold | RSI ≤ 20 and SHORT | Skip |
| BB upper extreme | BB position ≥ 95% and LONG | Skip |
| BB lower extreme | BB position ≤ 5% and SHORT | Skip |

These fire before the consensus vote and cannot be overridden by other indicators.

#### 4. Indicator Consensus Voting

Each indicator casts a vote: `+1` (agrees with signal direction), `−1` (opposes), `0` (neutral or absent).

| # | Indicator | Bullish threshold | Bearish threshold |
|---|---|------------------|------------------|
| 1 | RSI | < 40 (oversold) | > 60 (overbought) |
| 2 | EMA cross | `"bullish"` (EMA9 > EMA21) | `"bearish"` |
| 3 | BB position | < 30% (near lower band) | > 70% (near upper band) |
| 4 | MACD line | bias `"bullish"` (line > 0) | bias `"bearish"` |
| 5 | MACD signal crossover | `signal_bias "bullish"` (line > signal) | `signal_bias "bearish"` |
| 6 | Stochastic %K | < 20 (oversold) | > 80 (overbought) |
| 7 | Williams %R | ≤ −80 (oversold) | ≥ −20 (overbought) |
| 8 | 5m price change | positive | negative |
| 9 | 15m price change | positive | negative |

`calc.VoteSummary` holds:  
`Votes` = sum of all individual votes (range: −9 to +9)  
`Available` = count of indicators that cast a non-neutral opinion  
`Supporting` / `Opposing` = counts of agreeing and opposing votes

#### 5. Indicator Support Gate

```go
if votes.Supporting < calc.MinIndicatorsForSignal { // 5
	return nil // not enough indicators agree with the signal direction
}
```

At least 5 indicators must agree with the proposed trade direction before the strategy can emit a trade idea. A signal with 9 available indicators but only 4 supporting and 4 opposing is blocked.

#### 6. Consensus Gate

```go
if votes.Votes < calc.MinConsensusVotes { // 1
	return nil // more indicators oppose than support
}
```

Once the 5-indicator support gate is met, the net vote must also be positive (`>= 1`).

#### 7. Confidence Calculation

```
base  = min(|momentum| × 100, 0.50)              ← capped at 0.50
bonus = clamp(votes × 0.05, −0.20, +0.40)        ← indicator agreement (0 when none available)
atr   = −min(0.10, (atr_pct − 1.0) × 0.05)       ← volatility penalty if ATR > 1% of price
────────────────────────────────────────────────
final = clamp(base + bonus + atr, 0.10, 0.95)
```

With all 9 indicators agreeing, maximum confidence = 0.50 + 0.40 = **0.90**.  
With 5 agreeing and 0 opposing: 0.50 + 0.25 = **0.75**.

#### 8. Minimum Confidence Filter

```go
if confidence.Confidence < calc.MinActionableConfidence { // 0.20
	return nil
}
```

Signals below 20% are discarded inside the strategy. In `fully_automated` mode, a further `MIN_SIGNAL_CONFIDENCE` (default 0.65) is applied in the strategy loop after the LLM and learner adjustments — see `03-background-loops.md`.

#### 9. Learner Adjustment

Applied by the strategy loop, not the strategy, to every idea returned by the selected strategy (including `llm_only_strategy`):

```go
// internal/app/loops.go
for i := range ideas {
	ideas[i].Confidence = a.learner.AdjustConfidence(
		ideas[i].StrategyID, ideas[i].Market, string(ideas[i].Direction), ideas[i].Confidence)
}
```

See the Learner section below.

#### 10. Position Sizing

`PositionSizingProposal` comes from `calc.CalculatePositionSizing(atrPct)`: 20% of equity, scaled down by `max(0.25, 1/atr_pct)` when ATR exceeds 1% of price, then bounded to 5–25%. With the default `TARGET_TRADE_AMOUNT` the risk engine trades a fixed notional instead and uses this fraction only when `TARGET_TRADE_AMOUNT` is `0`.

---

### `TradeIdea` Output

```go
domain.TradeIdea{
	ID:         domain.NewID(),
	StrategyID: "basic_strategy_1h",
	Market:     symbol,
	Direction:  direction,
	Thesis:     "Momentum 0.42% | RSI 38.0 | EMA bullish | MACD bullish | MACD-X bullish | WR -62.5 | support 6/7 (net +5)",
	SupportingSignals: map[string]any{
		"momentum":              0.00421,
		"current_price":         85420.0,
		"previous_price":        85061.0,
		"indicator_votes":       5,
		"indicators_supporting": 6,
		"indicators_opposing":   1,
		"indicators_available":  7,
		"rsi_14":                38.0,
		"ema_cross":             "bullish",
		"bb_position":           34.2,
		"macd_bias":             "bullish",
		"macd_signal_bias":      "bullish",
		"macd_histogram":        0.0387,
		"stoch_k":               nil, // not enough history yet (JSON null)
		"stoch_d":               nil,
		"williams_r":            -62.5,
		"atr_pct":               0.097,
		"price_change_5m":       0.35,
		"price_change_15m":      0.79,
	},
	Confidence:             0.73,
	EntryPlan:              "Enter at market on indicator consensus confirmation",
	ExitPlan:               "Exit on momentum reversal or stop-loss at 5% loss",
	StopOrInvalidation:     "Abandon if unrealised loss reaches 5%",
	PositionSizingProposal: 0.20,
	ModeEligibility:        []domain.TradingMode{domain.ModeManual, domain.ModeSemiAutomated, domain.ModeFullyAutomated},
}
```

The stop-loss percentage in the plan text is `int(STOP_LOSS_PCT × 100)`. `BasicAndLLMStrategy` adds `llm_sentiment`, `llm_confidence_scale` and `llm_reasoning` (all `nil` when the LLM was not used), appends `LLM: <reasoning>` to the thesis, and uses the entry plan "Enter at market on indicator consensus and LLM validation".

---

## Performance Learner (`internal/learner/learner.go`)

**Type:** `learner.PerformanceLearner`

Tracks historical trade outcomes per (strategy, market, direction) tuple and adjusts future signal confidence using win rate and P&L magnitude. Recent outcomes are weighted more heavily than old ones, while rolling statistics and percentiles are computed over the stored P&L history. All methods take an internal mutex, so the learner is safe to call from the strategy loop and HTTP handlers concurrently.

---

### Data Structure

```go
type key struct {
	strategyID string
	market     string
	direction  string
}

type stats struct {
	rawCount     int       // number of outcomes
	winPnlTotal  float64   // raw totals for the dashboard's mean win/loss columns
	winCount     int
	lossPnlTotal float64
	lossCount    int
	pnlHistory   []float64 // chronological realised P&L in currency, oldest first
	returns      []float64 // the same outcomes as P&L / notional, oldest first
}

type PerformanceLearner struct {
	mu    sync.Mutex
	stats map[key]*stats
	order []key // insertion order
}
```

Example key: `{"basic_and_llm_strategy_1h", "BTC/EUR", "long"}`

---

### `RecordOutcome()`

```go
func (l *PerformanceLearner) RecordOutcomeWithNotional(strategyID, market, direction string, pnl, notional float64)
func (l *PerformanceLearner) RecordOutcome(strategyID, market, direction string, pnl float64) // notional unknown
```

Each outcome is appended to the key's history. `notional` is the trade's entry price × size; the learner stores the trade's return `pnl / notional` for the quality score. When the notional is unknown (`0`, or `RecordOutcome`), the currency P&L is stored instead; the score's normalisation still removes its scale, but trades of different sizes then weigh by size.

Called through `App.recordClosedOutcome()` with the closed trade's notional for every close:
- Paper stop-loss and trailing-stop closures
- Fully-automated SHORT execution (closing a paper long)
- Manual approval SHORT execution (closing a paper long)
- Manual UI close of a paper position
- A live sell that fills (stop, signal or manual close), with notional = entry price × executed size from the live book

For paper closes the notional is `AvgPriceAtClose × SizeAtClose` from `paper.Engine.Meta()`. `paper.ClosePositionsForOperatorReset()` passes it the same way for each position closed by the operator reset. At start-up the learner is seeded from `signal_outcomes` with notional = `entry_price × |size|` per row, so a restart scores the same returns. `RecordOutcome` remains for callers with no notional.

---

### `AdjustConfidence()`

```go
func (l *PerformanceLearner) AdjustConfidence(strategyID, market, direction string, baseConfidence float64) float64 {
	s, ok := l.stats[key{strategyID, market, direction}]
	if !ok || s.rawCount < MinSamples { // 5
		return baseConfidence // insufficient data — no adjustment
	}
	quality := s.qualityScore()
	return math.Min(0.95, baseConfidence*(1.0+quality*0.5))
}
```

**`Decay = 0.92`** — each older trade is worth 92% of the next more recent one. After 10 trades, the oldest has weight `0.92^9 ≈ 0.47` relative to the newest.

**Scale interpretation:** `qualityScore()` computes, with the same recency weights (newest 1.0, then `Decay` per older trade), the win rate and the mean return of winning and of losing trades, where a return is P&L as a fraction of the trade's notional. The expectancy `winRate·meanWin + (1−winRate)·meanLoss` is divided by the larger of `|meanWin|` and `|meanLoss|` and clamped to [−1, 1] (0 when every trade broke even). Because it works on returns and is normalised, the same sequence of percentage outcomes gives the same score on a 100-unit and a 10,000-unit account; previously a floor of 1 currency unit on the normaliser kept the score near 0 for small trades. A strongly negative expectancy can halve confidence, neutral expectancy leaves it unchanged, and strongly positive expectancy can boost confidence by up to 50%.

Adjusted confidence is capped at 0.95. There is no lower clamp in the learner; the strategy's own floor (0.10) applies before adjustment.

---

### `LoadFromOutcomes()`

```go
func (l *PerformanceLearner) LoadFromOutcomes(outcomes []learner.Outcome)
```

Called at start-up with up to 1,000 of the most recent `signal_outcomes` rows (newest first) from the database. Populates the stats and chronological history so the learner has history from previous sessions without requiring live trades. `learner.Outcome.Notional` (entry price × size of the row) gives the trade's return; rows seeded this way score exactly as if they had been recorded one by one. Weights start at 1.0 for the newest outcome per key and decay by `Decay` for each older one.

---

### Rolling Statistics

```go
func (l *PerformanceLearner) RollingWinRate(strategyID, market, direction string, n int) float64
func (l *PerformanceLearner) PnlPercentiles(strategyID, market, direction string) (PnlStats, bool)
```

`RollingWinRate` is the share of winning trades among the latest `n` outcomes. `PnlPercentiles` returns mean, median, and the 25th/50th/75th/95th percentiles using the same "weibull" interpolation that the Python version took from numpy (virtual index `(n+1)·q − 1`, clipped to the data range).

The summary exposes `rolling_win_rate_10`, `average_pnl`, `median_pnl`, `pnl_p25`, `pnl_p75`, and `pnl_p95` alongside the win/loss averages and quality score.

---

### `Summary()`

Returns `[]learner.SummaryRow` for the `GET /api/learning` endpoint and dashboard, sorted by market:

```json
[
    {
        "strategy": "basic_and_llm_strategy_1h",
        "market": "BTC/EUR",
        "direction": "long",
        "trades": 15,
        "win_rate": 0.63,
        "rolling_win_rate_10": 0.70,
        "mean_win_pnl": 7.10,
        "mean_loss_pnl": -3.20,
        "average_pnl": 4.25,
        "median_pnl": 3.80,
        "pnl_p25": -1.50,
        "pnl_p75": 8.90,
        "pnl_p95": 15.20,
        "quality_score": 0.18
    }
]
```

---

## LLM Confidence Adjustment

Inside `BasicAndLLMStrategy`, the LLM analyser applies a multiplier to the strategy's base confidence:

```go
finalConfidence := math.Min(calc.MaxConfidence, baseConfidence*analysis.ConfidenceScale)
```

`ConfidenceScale` is returned by the LLM in range [0.5, 2.0]. A value of 2.0 means the LLM strongly supports the signal and can double the base confidence. The final confidence is always capped at 0.95. When the LLM was not used the scale is a neutral 1.0.

If `ConfidenceScale` is below `LLM_VETO_THRESHOLD` (default 0.70) the strategy drops the signal before it reaches the risk engine — see `07-llm-integration.md` for details.

### Combined Adjustment Chain

```
Strategy base confidence
    (momentum base ≤ 0.50)
    + indicator consensus bonus (−0.20 to +0.40)
    − ATR volatility penalty (0 to −0.10)
    → clamp to [0.10, 0.95]
    → discard if < 0.20

    × LLM ConfidenceScale (0.5–2.0, basic_and_llm_strategy_* only, when available)
    → veto if scale < LLM_VETO_THRESHOLD (default 0.70)
    → cap at 0.95

    × Learner scale (0.5–1.5, requires ≥5 samples; applied in the strategy loop)
    → cap at 0.95
```

---

## Signal Threshold Summary

| Stage | Threshold | Effect |
|---|---|---|
| Momentum minimum | max(0.2%, ATR % of price) | No signal generated |
| Higher timeframe | 15-min EMA cross opposes direction | Signal blocked |
| Hard filter | RSI ≥80 / ≤20, BB ≥95% / ≤5% | Signal blocked |
| Indicator support gate | 5 agreeing indicators | Signal discarded if fewer support the trade direction |
| Consensus gate | net votes < 1 | Signal discarded |
| Confidence minimum (strategy) | 20% | Signal discarded |
| Confidence minimum (fully_automated) | 65% (configurable) | Signal skipped |
| LLM veto threshold | 0.70 (configurable) | Signal skipped if LLM scale < threshold |
| LLM max confidence | 95% | Hard cap regardless of scale |
| Risk min trade size | 50 (`BASE_CURRENCY`) | Floor for the order notional; rejected if cash cannot cover it |
| Position sizing | 5–25% of equity (20% base, ATR-scaled) | Used when `TARGET_TRADE_AMOUNT` is 0 |

---

## Indicator Warm-up Times

At the default 60 s/tick (`STRATEGY_INTERVAL_SECONDS`), indicators computed from the tick history become available after the following number of ticks. The times scale with the interval: at 30 s they halve.

| Indicator | Ticks needed | Time at 60 s |
|-----------|-------------|------|
| Williams %R (14) | 14 | ~14 min |
| RSI (14) | 15 | ~15 min |
| Stochastic (14, 3) | 16 | ~16 min |
| EMA (fast=9) | 9 | ~9 min |
| EMA (slow=21) | 21 | ~21 min |
| Bollinger Bands (20) | 20 | ~20 min |
| ATR (14) | 15 | ~15 min |
| MACD signal line | 34 | ~34 min |

Once 5-minute candles are cached by the OHLC loop, ATR and Stochastic are computed from those candles instead and do not depend on tick count.

A strategy without LLM is blocked at start-up until the price history reaches its `RequiredPriceTicks()`. Every such strategy now reads candles (0), so none waits for ticks; each waits for its own finished candles instead. There is no fixed post-start trade delay (`app.TradeWarmupTicks = 0`).

Strategies that include the LLM use data readiness instead of a fixed warm-up time, via `RequiredPriceTicks()`. `LLMOnlyStrategy` needs 10 price-history ticks (`calc.MomentumWindowPoints × 2`), enough for averaged momentum. The `basic_and_llm_strategy_*` versions and the 4-hour LLM versions need 0: they read candles. In both cases the tick waits until at least one market has enough history and the news cache contains at least one item. Markets without enough price history are excluded from that LLM evaluation pass.

---

## LLM Only Strategy (`internal/strategy/llm_only.go`)

**Type:** `LLMOnlyStrategy`  
**Strategy ID:** `"llm_only_strategy"` (`strategy.LLMOnlyID`; legacy `llm_only` accepted)

The LLM-only strategy does not apply local indicator gates or indicator voting. It asks the LLM for an explicit `long`, `short`, or `hold` recommendation for each market and converts only `long`/`short` responses into `TradeIdea` values. The strategy loop still applies the learner adjustment to its ideas.

Indicators are still passed into the LLM prompt as context. They are not counted, voted, or required by local strategy code.

Markets are evaluated concurrently: one goroutine per market, bounded by a semaphore of `LLM_ONLY_MAX_CONCURRENCY` (minimum 1). Each goroutine writes only its own result slot, so ideas keep sorted market order. A panic while evaluating one market is recovered, logged, and skips only that market.

### Recommendation Flow

```go
rec := in.Analyser.RecommendTrade(ctx, llm.RecommendationRequest{
	Market:        symbol,
	CurrentPrice:  data.Price,
	PreviousPrice: previous, // falls back to the current price
	PriceHistory:  data.PriceHistory,
	Indicators:    data.Indicators,
	News:          in.News,
	Equity:        in.Portfolio.Equity,
	Cash:          in.Portfolio.Cash,
	OpenPositions: in.Portfolio.OpenPositions,
})
```

`in.Portfolio.OpenPositions` comes from `App.strategyPositions()`: for a live-routed market (live environment and the market toggled live) it holds that market's live-book positions, for every other market the paper positions. The positions only appear in the LLM prompt's portfolio block; the indicator strategies do not read them.

The LLM response must contain:

```json
{
  "action": "long | short | hold",
  "confidence": 0.0,
  "sentiment": 0.0,
  "reasoning": "short rationale"
}
```

`hold`, invalid actions, parse failures, or unavailable LLM state (`rec.LLMUsed == false`) produce no signal. A valid `long`/`short` recommendation uses the LLM confidence directly as the trade confidence, with `PositionSizingProposal` from `calc.CalculatePositionSizing(0)` (20%). The normal risk engine, approval flow, position limits, and fully automated confidence threshold still apply after the strategy emits the trade idea.

---

## Trend Strategy (`internal/strategy/trend.go`)

**Type:** `TrendStrategy`  
**Strategy ID:** `"trend_strategy"` (`strategy.TrendID`)

A slow, indicator-only trend follower on 4-hour candles, added because the backtest showed that every faster rule loses to the fees (see *Why 4-hour candles* below). No LLM, no news. It reads only `MarketData.TrendCandles`, the 4-hour candles the OHLC loop fetches from Kraken (720 rows, 120 days; see [03-background-loops.md](03-background-loops.md#ohlc-loop-ohlcloop)).

For each market, in sorted order:

1. Drop the newest candle: Kraken's OHLC response always ends with the forming one, and the replay reproduces that, so only finished candles are used.
2. With fewer than `TrendMinClosedCandles` (160) closed candles, emit nothing (warm-up).
3. Compute the EMA of the closes over `TrendFastEMA` (50 candles, about 8 days) and `TrendSlowEMA` (150 candles, 25 days) with `analysis.EMA`.
4. Fast above slow and no long held → a **long** idea; fast below slow and a long held → a **sell** marked as a rule exit (`TradeIdea.Exit`). Otherwise (already held in an uptrend, nothing held in a downtrend, or equal EMAs) → nothing. The strategy reads the open positions from `Input.Portfolio`, which live and the replay both fill. Before 0.16.0 it emitted a long or a short every tick whatever was held; the loop ignored the repeats, but they filled the idea log, and in paper mode a sell with nothing held opened a paper short.

Every idea has confidence `TrendConfidence` = 0.95 and `PositionSizingProposal` 0.20; the risk engine sizes it as usual (`TARGET_TRADE_AMOUNT`). The confidence is fixed and high on purpose: the learner can scale a confidence to between 0.5× and 1.5× from recent outcomes, and live records a signal exit under the closing idea's direction (`short`), so after a run of losing exits a lower confidence could fall below `MIN_SIGNAL_CONFIDENCE` (0.65) and the exit would be dropped, leaving the position open in a downtrend. At 0.95 that needs a learner quality below −0.63, and the 10-pair backtest showed it happens: exits were blocked on up to 448 market-days. Since 0.16.0 a rule exit of a held long is exempt from `MIN_SIGNAL_CONFIDENCE` (see *4-hour candle strategies* below).

The strategy keeps no state of its own: after a stop-out nothing is held, so it buys again on the next tick if the trend is still up, so **a tight trailing stop multiplies fees instead of limiting losses**. Use wider stops with this strategy: the backtest used `STOP_LOSS_PCT` 0.10 and `TRAILING_STOP_PCT` 0.15 (`docs/backtest-settings/trend_strategy.json`). These are dashboard settings shared by all strategies; with the defaults (5 % and 3 %) the strategy was not tested.

### Why 4-hour candles

A screen of rule families on BTC/EUR and ETH/EUR 30-second bars (January–August 2026 to choose, September–November to check; 80 bp taker fee and 5 bp half-spread per side, so a round trip costs about 1.7 %) found:

| Family | Result |
|---|---|
| EMA crosses and breakouts on 15-minute and 1-hour candles | lost on every variant: holding times of 3–27 hours do not move prices more than 1.7 % often enough |
| Mean reversion (RSI below 25–35 and below the lower Bollinger band, exit at the middle band) on 15-minute to 4-hour candles | lost on every variant |
| EMA 9/21 and 20/50 on 4-hour candles | negative out of sample; with a 3 % trailing stop, −85 % in sample from re-entries |
| EMA 50/150 on 4-hour candles, wide stops | best in sample on **both** markets (BTC +1.5 %, ETH +23.8 %), positive out of sample on both (one trade each) |

The parameters were chosen on the in-sample months only.

### Backtest results (2026-01-01 to 2026-11-14, BTC/EUR and ETH/EUR)

Settings: built-in defaults plus the stops above; capital 500 EUR, `TARGET_TRADE_AMOUNT` 100 EUR per trade, 80 bp fee (`kraken-base`). Over the period an equal-weight buy-and-hold of the two coins lost 30.17 %.

| Run | Net P&L | Round trips | Notes |
|---|---|---|---|
| `basic_strategy` (tick-based, retired in 0.18.0) | −34.96 EUR (−6.99 %) | 11 | fees 17.47 EUR |
| `trend_strategy`, whole period | **+40.31 EUR (+8.06 %)** | 10 | fees 16.46 EUR; 8 losses of 1.4–7.1 %, then ETH Jul–Oct +44.90 and BTC Aug–Oct +19.61 |
| `trend_strategy`, in sample (Jan–Aug) | +23.94 EUR (+4.79 %) | 10 | profit factor 1.82 |
| `trend_strategy`, out of sample (Sep–Nov) | −10.26 EUR (−2.05 %) | 2 | both losses; see below |

Sensitivity of the whole-period run (`backtest sensitivity`, one input varied at a time):

| Varied | Values → net % | Sign survives |
|---|---|---|
| Fee (bp) | 26 → +10.28, 40 → +9.71, 80 → +8.06, 120 → +6.41 | yes; break-even above 120 bp |
| Latency (ticks of 30 s) | 1 → +8.06, 2 → +7.97, 4 → +8.11, 10 → +8.10 | yes |
| Minimum half-spread (bp) | 2 → +8.10, 5 → +8.06, 10 → +8.00, 20 → +7.89 | yes |
| `TRAILING_STOP_PCT` | 0.03 → **−9.64** (24 trips), 0.08 → +3.96, 0.15 → +8.06, 0.25 → +8.40 | **no** at 0.03, the default |

How to read this:

- **The stops matter more than the costs.** Fees, latency and spread barely move the result because the strategy trades rarely; the default 3 % trailing stop turns it into a loss.
- **Not statistically meaningful.** Ten round trips; the profit comes from two of them. The verdict says so, and a single different trend could flip the sign.
- **The out-of-sample loss is mostly a restart effect, and also a real weakness.** The split's out-of-sample run starts flat with an empty 4-hour history, so it only leaves warm-up on 26 September, buys both coins at the end of the summer trend, and sells at the October reversals that the whole-period run sold for a profit. A live bot started late in a trend would do the same: the strategy enters whenever the trend is up, however late.
- **It beats holding the coins** because it was in cash during most of the falls (January–February, May–June, late October–November), not because it predicts them.

## 4-Hour Candle Strategies (`internal/strategy/candle_strategy.go`, `rotation.go`)

Five more non-LLM strategies trade the same 4-hour candles as the trend strategy (`MarketData.TrendCandles`, 720 rows from Kraken). Each has an LLM version (below).

| Strategy ID | Rule (finished 4-hour candles only) | Warm-up |
|---|---|---|
| `donchian_strategy` | Buy a close above the highest high of the previous 90 candles (15 days); sell a close below the lowest low of the previous 30 (5 days). | 91 candles, ~16 days |
| `rsi_dip_strategy` | Buy when the 14-candle RSI is below 20 **and** the close is at least 10 % under the 20-candle EMA (a capitulation); sell when RSI recovers above 50, or after 18 candles (3 days). | 40 candles, ~7 days |
| `macd_strategy` | MACD 36/78/27 (the classic 12/26/9 stretched three times): buy while the MACD line is above zero and above its signal line; sell when the histogram turns negative. | 115 candles, ~20 days |
| `squeeze_strategy` | Bollinger bands of 40 candles at 2 SD: after the band width falls to its lowest 20 % of the last 120 candles (within the 6 candles before), buy a close above the upper band that is also above the 100-candle EMA; sell a close below the middle band. | 160 candles, ~27 days |
| `rotation_strategy` | Rank every watched market by its 14-day (84-candle) return; hold the 3 strongest while their return is positive. Rank 4 is a buffer (no idea, so a market at the edge is not bought and sold on every swap); a held market below rank 4 or with a negative return is sold. | 85 candles, ~15 days |

Shared behaviour (`CandleStrategy`, `actionable` in `candle_strategy.go`):

- **Finished candles only.** The newest candle is Kraken's forming one and is dropped, as in the trend strategy.
- **No stored state.** Rules that need "in a trade" state (breakout, dip, squeeze) rebuild it from the candle history on every evaluation (`latch`). A rule's decision is memoised per market until a new candle closes (`decide`), so evaluating every 30-second tick costs nothing; each idea still reports the current price.
- **Only actionable ideas.** A long is emitted only for a market with no long held, and a sell only for a held long. Every sell is marked `TradeIdea.Exit` (a *rule exit*). Open positions come from `Input.Portfolio`, which live and the replay both fill. The trend strategy does the same since 0.16.0.
- **Rule exits skip `MIN_SIGNAL_CONFIDENCE`.** All ideas have confidence 0.95 (`CandleConfidence`), but the learner can scale it to 0.475. In the first 10-pair backtest that blocked exits of held positions on up to 448 market-days, leaving them to the stop-loss. The live loop and the replay now execute a rule exit of a held long whatever its confidence, and ignore one with nothing held (never a paper short). Entries still need the threshold.
- **Wide stops.** Like the trend strategy, these re-enter on the next tick after a stop-out while the rule is still long. They were tested with `STOP_LOSS_PCT` 0.10 and `TRAILING_STOP_PCT` 0.15 (`docs/backtest-settings/candle_strategies.json`).

**How the parameters were chosen.** A Python screen of each family (`research4.py`/`research5.py`, not in the repository; 80 bp fee, 5 bp half-spread, fills on the next bar, the bot's stops) on BTC/EUR, ETH/EUR and HYPE/EUR over 2026-01-01..2026-11-15 picked one variant per family; for rotation the screen was meaningless on three markets and the parameters are a common default. So for those three markets the whole of 2026 is in-sample; the other seven pairs were never looked at while choosing. Nothing was re-tuned after the 10-pair runs below.

## LLM Versions (`internal/strategy/llm_version.go`)

Every 4-hour strategy has an LLM version: `trend_and_llm_strategy`, `donchian_and_llm_strategy`, `rsi_dip_and_llm_strategy`, `macd_and_llm_strategy`, `squeeze_and_llm_strategy` and `rotation_and_llm_strategy`. `LLMVersion` wraps the base strategy:

1. The base strategy evaluates as usual; its ideas are relabelled with the LLM version's id.
2. **Exits are passed on unchanged, without an LLM call**, so an LLM outage or veto can never keep a position open. So are entries for a market already held.
3. Each new entry is sent to `LLMAnalyser.AnalyseSignal` with the strategy's **own prompt** (`llm.StrategyPrompt`): its name, its instructions (what the signal means, what would make it more or less likely to work, when to oppose it), and its data (the idea's supporting signals: channel levels, RSI and EMA gap, MACD values, band width, rank and return). The prompt also carries what the `basic_and_llm_strategy_*` versions send: the market briefing, the latest reflection, the tick indicators, the portfolio and up to five recent news items for the asset. "Momentum" in the prompt is the 24-hour change of the 4-hour candles.
4. The reply's `confidence_scale` (0.5–2.0) multiplies the entry's confidence, capped at 0.95; a scale below `LLM_VETO_THRESHOLD` (0.70) drops the entry. The `llm_sentiment`, `llm_confidence_scale` and `llm_reasoning` supporting signals are set as for `basic_and_llm_strategy` (nil when the LLM was not used), and the reasoning is appended to the thesis.
5. **One LLM call per market per closed 4-hour candle.** The verdict (including a veto) is reused until the market's last finished candle changes, so a 60-second strategy interval does not ask the LLM 240 times about the same signal. A reply made without the LLM (unavailable, unparsable) is not kept, so it is retried on the next tick; meanwhile the base entry stands with its own confidence.

Like every LLM strategy, the LLM versions wait for news before evaluating (`llmStrategyDataReady`) and **cannot be backtested**: a replay cannot reproduce what the LLM would have said on a past date. Their only difference from the base strategy is which entries the LLM lets through, so judge them against the base strategy's backtest and on paper.

### Backtest results: 10 pairs, 2026-01-01 to 2026-11-14

Markets: the top 10 EUR pairs by volume on 2026-11-14 (BTC, ETH, SOL, XRP, ZEC, HYPE, NEAR, AVAX, LINK, ADA), chosen today, so the selection is **survivorship-biased** (pairs that did well are more likely to be in today's top 10). HYPE/EUR starts at its listing on 28 January. 500 EUR capital, `TARGET_TRADE_AMOUNT` 100 EUR, 80 bp fee; the 4-hour strategies use the stops above, `basic_strategy` the defaults. An equal-weight buy-and-hold of the ten returned −0.4 %. "Jan–Aug" and "Sep–Nov" are the two halves of `backtest split --split 2026-09-01`, each starting flat with an empty history.

| Strategy | Net % (30 s) | Net % (120 s) | Round trips | Win % | Profit factor | Max DD % | Fees EUR | Pairs positive | Jan–Aug % (trips) | Sep–Nov % (trips) |
|---|---|---|---|---|---|---|---|---|---|---|
| `basic_strategy` (ticks, retired) | −23.4 | −33.4 | 62 | 19 | 0.26 | 25.9 | 99 | 1/10 | −23.4 (62) | −11.1 (68) |
| `basic_strategy_1h` (0.18.0) | −47.8 | — | 172 | 15 | 0.31 | 48.4 | 274 | 2/10 | −43.0 (131) | −4.9 (41) |
| `basic_strategy_4h` (0.18.0) | −4.0 | — | 31 | 19 | 0.72 | 7.9 | 50 | 4/10 | +0.2 (17) | −4.2 (14) |
| `basic_strategy_1h` (0.20.0 rules) | −3.8 | — | 22 | 14 | 0.71 | 9.9 | 35 | 2/10 | −6.7 (13) | +2.9 (9) |
| `basic_strategy_4h` (0.20.0 rules) | −0.5 | — | 15 | 20 | 0.96 | 9.9 | 24 | 2/10 | +4.5 (6) | −5.1 (9) |
| `trend_strategy` | −4.8 | +19.5 | 87 | 26 | 0.94 | 30.5 | 135 | 3/10 | +4.4 (69) | −34.3 (26) |
| `donchian_strategy` | **+42.6** | **+52.0** | 85 | 31 | 1.72 | 17.5 | 134 | 5/10 | +27.2 (58) | +1.9 (19) |
| `rsi_dip_strategy` | −6.5 | −8.4 | 27 | 41 | 0.72 | 18.9 | 42 | 4/10 | −6.5 (27) | 0.0 (0) |
| `macd_strategy` | +5.5 | +19.8 | 140 | 26 | 1.06 | 33.1 | 218 | 6/10 | +1.2 (109) | −10.4 (23) |
| `squeeze_strategy` | −3.4 | −2.7 | 100 | 28 | 0.95 | 25.1 | 157 | 4/10 | +4.6 (76) | −13.5 (12) |
| `rotation_strategy` | −56.8 | −41.9 | 225 | 19 | 0.66 | 56.8 | 352 | 1/10 | −45.2 (171) | −8.7 (57) |

The two 0.18.0 basic rows are the candle versions (one 30 s run each, same settings; the replay is spot only, so the long-only versions trade identically; Jan–Aug / Sep–Nov here are the trades entered in each part of the same run, not separate runs). Both are positive before costs (gross +53.7 EUR for 1h, +33.8 EUR for 4h) and lose on fees: 1h makes 172 round trips with a median hold of 8.6 h and 15 % winners, nearly all closed by the rule exit, which fires as soon as the net indicator vote turns bearish; 4h makes 31 with a median hold of 36 h. 4h beats the tick version (−23.4 %) but trails equal-weight buy and hold (−0.4 %). The 1h run also logs 1,798 orders rejected for insufficient liquidity on thin 30-second bars: 1,171 rule-exit sells, retried each tick until one fills, and 637 entries, which then wait for the next candle.

The two 0.20.0 rows run the new default exit and entry rules (`BASIC_EXIT_VOTES` 3, `BASIC_EXIT_CONFIRM_CANDLES` 3, `BASIC_TREND_EMA` 50, `BASIC_TRAIL_ATR` 5, `BASIC_COOLDOWN_CANDLES` 6, `BASIC_MIN_ATR_COST` 1). They were chosen on **Jan–Aug only**, from 28 configurations per timeframe (each rule switched off or varied on its own, then the entry rules on top of the best), and Sep–Nov was run once afterwards as the held-out test. The rules mostly work by trading far less (1h: 22 round trips instead of 172), so the 1h loss falls from −47.8 % to −3.8 % and the held-out months turn from −4.9 % to +2.9 %, but on 9 trades with 2 winners. The 4h version looked best in tuning (+4.5 % on 6 trades) and lost −5.1 % held out (9 trades, 1 winner): with so few trades neither timeframe shows an edge over equal-weight buy and hold (−0.4 %) or cash. In tuning, stricter entries removed profitable trades as well as losing ones (profit before costs fell to about zero), and a profit target made both timeframes worse by cutting the few large winners, so it is off. Limit (maker) entries at Kraken Pro's 0.40 % Tier 1 maker fee were estimated on the tuning trades as if every entry filled: 1h would still lose (−22 % instead of −32 %), so they were not built. A longer history (the 2022–2026 archive) is needed before trusting either timeframe.

Net P&L per pair (EUR, 30 s run):

| Strategy | ADA | AVAX | BTC | ETH | HYPE | LINK | NEAR | SOL | XRP | ZEC |
|---|---|---|---|---|---|---|---|---|---|---|
| `basic_strategy` (ticks, retired) | −14.2 | −11.9 | −17.7 | −17.3 | −13.8 | −8.4 | −7.6 | −10.5 | +0.9 | −16.5 |
| `basic_strategy_1h` | −27.5 | −12.5 | −91.3 | −49.3 | +2.4 | −4.5 | +29.9 | −44.0 | −29.9 | −12.3 |
| `basic_strategy_4h` | −2.9 | −4.6 | −15.7 | −17.4 | +17.2 | +2.3 | +21.1 | +1.3 | −21.2 | 0.0 |
| `trend_strategy` | −41.9 | −3.1 | −3.7 | +38.1 | −16.7 | +20.2 | −26.3 | +17.9 | −7.4 | −1.3 |
| `donchian_strategy` | −41.0 | −7.8 | −5.6 | +17.9 | +64.3 | −1.6 | +85.5 | +10.1 | −24.8 | +116.0 |
| `rsi_dip_strategy` | −14.8 | −6.7 | +7.3 | +3.6 | 0.0 | +10.2 | −5.6 | −17.5 | +9.5 | −18.7 |
| `macd_strategy` | −41.6 | +6.0 | −25.0 | −28.8 | +46.7 | +1.4 | +45.3 | +0.2 | −35.6 | +58.8 |
| `squeeze_strategy` | +1.4 | +11.3 | −20.7 | −14.0 | +40.0 | −7.6 | −20.0 | −13.7 | −31.8 | +38.1 |
| `rotation_strategy` | −72.8 | −14.1 | −28.8 | −49.6 | −16.7 | −57.3 | +87.4 | −59.8 | −16.3 | −56.2 |

How to read this:

- **No strategy wins on every pair, and only Donchian is positive in both halves.** Its profit comes from three pairs (ZEC, NEAR, HYPE); it lost on five. The Sep–Nov half is +1.9 % on 19 trades, which is not evidence of anything.
- **The tick interval should not matter for 4-hour rules, but it does (trend −4.8 % vs +19.5 %).** With 500 EUR and 100 EUR per trade the bot can hold about four positions; whichever market signals first takes the cash, and stops are checked on a different sample of prices. One trade decides a lot: trend's 18 August ZEC buy was stopped out on 22 August for +33 EUR when stops were checked every 30 s, and rode to 10 September for +114 EUR when checked every 120 s. Treat differences of this size as noise.
- **Rotation churns.** 225 round trips paid 352 EUR in fees on 500 EUR of capital; ranks change too often at a 14-day window for an 80 bp fee.
- **RSI dip rarely trades** (27 trips, none after June) and loses on average: dips in this falling year kept falling.
- **A single period of 10.5 months, with today's top 10, is not enough.** The plan is to repeat these runs over 2022–2026 with each year's point-in-time top 20 EUR pairs before drawing conclusions.
- **Thin pairs fill late.** A market order is rejected when the next 30-second bar has no trade and retried on the next tick; AVAX/EUR traded in only 16 % of 30-second intervals. This delays entries and exits on the quieter pairs (pessimistic).

## LLM Shadow Reviews (`internal/strategy/shadow.go`, `internal/shadow`, `internal/app/shadow.go`)

The LLM versions cannot be backtested, so the only way to learn whether their vetoes help is to watch them live. Shadow reviews do that without letting the LLM touch a trade. While `LLM_SHADOW_REVIEW` is on (the default) and the selected strategy is one of the six 4-hour strategies **without** LLM:

1. After each evaluation, `ShadowReviewer.Claim` picks the entries the strategy's LLM version would have sent to the LLM: longs for a market not held, with a closed 4-hour candle, not yet reviewed for that market's last closed candle. Exits and other strategies are never reviewed.
2. The review runs **in the background** (`queueShadowReviews`, tracked by `App.Wait`): the tick goes on to trade the entry exactly as before and does not wait for the LLM. One batch asks the LLM at a time.
3. The LLM is asked through `askLLM`, the same function the LLM versions use, with the strategy's own prompt, the news, the briefing, the indicators and the portfolio as they were before the entry. So the verdict is exactly what the LLM version would have been told.
4. The answer is stored in `llm_shadow_reviews`, one row per strategy, market and closed candle, and noted in the activity feed (*Shadow review BTC/EUR - LLM would veto (scale 0.55 < 0.70), trade unaffected*). A scale below `LLM_VETO_THRESHOLD` is a *would-veto*. If the LLM cannot answer, the row says so and the claim is released, so a repeat of the entry on a later tick is asked again; a later answer replaces the unanswered row, but an answer is never replaced.

No review is made without news (the LLM version would still be waiting for it), without an LLM that can be tried, or while an LLM strategy is selected (that strategy asks the LLM itself).

**Scoring** (`shadow.Summarise`, read through `GET /api/shadow-reviews`). Each closed long of the reviewed strategy (`signal_outcomes`) is matched to the newest review of its strategy and market whose candle had closed before the entry and opened at most 12 hours before it (`MatchWindow`: the candle, the next one in which the entry is made, and 4 hours of slack for late candle data and approvals). Several trades entered under one verdict (a stop-out and re-entry) all count for it, since the LLM version would have kept the same verdict for the whole candle. Trades without a matching review, and reviews the LLM could not answer, are not scored. Per strategy:

| Field | Meaning |
|---|---|
| Veto accuracy | Share of the vetoed trades that lost money (P&L ≤ 0, after fees). |
| Random veto (`loser_rate`) | Share of all scored trades that lost money: what vetoes picked at random would hit. |
| Break-even | Average win ÷ (average win + average loss): the veto accuracy at which vetoes neither help nor hurt. |
| Vetoes would save | Minus the total P&L of the vetoed trades. |
| Verdict | *Too few* until 10 vetoed trades have closed (`DefaultMinScoredVetoes`); then *would help* when the vetoed trades lost money in total, else *would hurt*. |

The LLM is worth using with a strategy when its veto accuracy is above the break-even, and clearly above the random veto. From the 2026 backtest trades, the break-evens are about 51 % for RSI dip, 71–75 % for squeeze, trend, MACD and rotation, and 80 % for Donchian, whose profit came from a few large trades that a veto could remove. These are estimates: a vetoed trade would also have freed its cash for another one, which this scoring cannot see.

---

## Backtest Replay (`internal/backtest`)

`backtest.Run` replays cached 30-second bars through the strategy, the shared `marketdata.Build`, the risk engine, the learner (fed only trades the run has closed) and the paper engine, tick by tick in the live loop's order, with a simulated exchange that fills each order on the next tick's bar at taker-side prices plus Kraken fees and order rules. Stops are checked on each tick's last price, as live. The full description, every assumption and its bias are in [14-backtesting.md](14-backtesting.md).
