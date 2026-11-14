# Strategy & Learning

## Shared Calculation Packages

Strategy calculations are split by responsibility:

- `internal/analysis` owns raw technical indicator formulas such as RSI, EMA, Bollinger Bands, MACD, ATR, Stochastic, Williams %R, and time-window price changes. `analysis.ComputeAll(prices, tickSeconds, candles)` returns an `analysis.Indicators` map (`map[string]any`) with nested groups such as `bb`, `macd`, `stoch` and `price_changes`. When 5-minute OHLC candles are cached, ATR and Stochastic use real high/low data instead of the close-price approximation.
- `internal/strategy/calc` owns strategy decision formulas: price momentum (`CalculateMomentum`), ATR-adjusted momentum thresholding (`EvaluateMomentumGate`), higher-timeframe alignment (`CheckHigherTimeframeAlignment`), RSI/Bollinger hard filters (`CheckHardFilters`), indicator voting (`VoteIndicators`), confidence components (`CalculateConfidence`), and volatility-adjusted position sizing (`CalculatePositionSizing`). All thresholds are named constants in `calc/constants.go`.

`BasicStrategy` and `BasicAndLLMStrategy` share one gate pipeline (`evaluateIndicatorGates` in `internal/strategy/indicator_signal.go`); `LLMOnlyStrategy` and the LLM recommendation prompt path use the same `calc` helpers instead of duplicating formulas locally.

Every strategy implements the `strategy.Strategy` interface:

```go
type Strategy interface {
	ID() string               // canonical strategy id
	UsesLLM() bool            // needs news + analyser
	RequiredPriceTicks() int  // price-history depth an LLM strategy needs; 0 otherwise
	Evaluate(ctx context.Context, in Input) []domain.TradeIdea
}
```

`strategy.All(settings)` returns one instance of each, and `strategy.ByID()` looks one up (legacy IDs `basic_trend` and `llm_only` are mapped to their canonical IDs by `control.CanonicalStrategyID`). Markets are evaluated in sorted order so results are deterministic.

## Indicator-Only Strategy (`internal/strategy/basic.go`)

**Type:** `BasicStrategy`  
**Strategy ID:** `"basic_strategy"` (`strategy.BasicID`)

An indicator-consensus signal generator. It compares averaged recent price windows from the rolling price history for the momentum signal, then confirms or rejects that signal using the higher-timeframe trend, hard filters, and up to 9 technical indicator votes. No LLM pass runs. The strategy loop then applies the learner adjustment.

## Combined Indicator + LLM Strategy (`internal/strategy/basic_and_llm.go`)

**Type:** `BasicAndLLMStrategy`  
**Strategy ID:** `"basic_and_llm_strategy"` (`strategy.BasicAndLLMID`, also `strategy.DefaultID`)

Uses the same momentum, higher-timeframe, hard-filter, support, and consensus gates as `BasicStrategy`, then calls `Analyser.AnalyseSignal()` for each surviving market. The LLM adjusts signal confidence and can veto the signal entirely when `ConfidenceScale < LLM_VETO_THRESHOLD`. This is the default selected strategy. With no analyser supplied (for example in the backtest replay) it uses `strategy.NeutralAnalyser`, which behaves like an unavailable LLM.

---

### Signal Generation Logic

```go
func (b *BasicStrategy) Evaluate(ctx context.Context, in strategy.Input) []domain.TradeIdea
```

`in.MarketData` is a `map[string]strategy.MarketData` built by the strategy loop with `Price`, `PreviousPrice`, `PriceHistory`, `Volume`, `LLMSentiment`, `Indicators` (30-second ticks plus 5-minute candles) and `HigherTimeframe` (indicators computed from 15-minute candles). For each market:

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
	StrategyID: "basic_strategy",
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
	weightedWins  float64   // exponentially decayed winning weight
	weightedTotal float64   // exponentially decayed total weight
	rawCount      int       // number of outcomes
	winPnlTotal   float64
	winCount      int
	lossPnlTotal  float64
	lossCount     int
	pnlHistory    []float64 // chronological realised P&L values, oldest first
}

type PerformanceLearner struct {
	mu    sync.Mutex
	stats map[key]*stats
	order []key // insertion order
}
```

Example key: `{"basic_and_llm_strategy", "BTC/EUR", "long"}`

---

### `RecordOutcome()`

```go
func (l *PerformanceLearner) RecordOutcome(strategyID, market, direction string, pnl float64) {
	s := l.statsFor(key{strategyID, market, direction})
	s.weightedWins *= Decay
	s.weightedTotal *= Decay
	s.weightedTotal += 1.0
	if pnl > 0 {
		s.weightedWins += 1.0
	}
	s.recordPnl(pnl)
	s.rawCount++
}
```

Called through `App.recordClosedOutcome()` for every paper close:
- Stop-loss and trailing-stop closures
- Fully-automated SHORT execution (closing a long)
- Manual approval SHORT execution (closing a long)
- Manual UI close

and from `paper.ClosePositionsForOperatorReset()` for each position closed by the operator reset.

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

**Scale interpretation:** `qualityScore()` blends the decayed win rate with average win/loss magnitude into an expectancy normalised by the larger of the average win and average loss (at least 1), clamped to [−1, 1]. A strongly negative expectancy can halve confidence, neutral expectancy leaves it unchanged, and strongly positive expectancy can boost confidence by up to 50%.

Adjusted confidence is capped at 0.95. There is no lower clamp in the learner; the strategy's own floor (0.10) applies before adjustment.

---

### `LoadFromOutcomes()`

```go
func (l *PerformanceLearner) LoadFromOutcomes(outcomes []learner.Outcome)
```

Called at start-up with up to 1,000 of the most recent `signal_outcomes` rows (newest first) from the database. Populates the stats and chronological P&L history so the learner has history from previous sessions without requiring live trades. Weights start at 1.0 for the newest outcome per key and decay by `Decay` for each older one.

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
        "strategy": "basic_and_llm_strategy",
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

    × LLM ConfidenceScale (0.5–2.0, basic_and_llm_strategy only, when available)
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

At 30 s/tick, indicators computed from the tick history become available after the following number of ticks:

| Indicator | Ticks needed | Time |
|-----------|-------------|------|
| Williams %R (14) | 14 | ~7 min |
| RSI (14) | 15 | ~7.5 min |
| Stochastic (14, 3) | 16 | ~8 min |
| EMA (fast=9) | 9 | ~4.5 min |
| EMA (slow=21) | 21 | ~10.5 min |
| Bollinger Bands (20) | 20 | ~10 min |
| ATR (14) | 15 | ~7.5 min |
| MACD signal line | 34 | ~17 min |

Once 5-minute candles are cached by the OHLC loop, ATR and Stochastic are computed from those candles instead and do not depend on tick count.

Indicator-only strategy evaluation is blocked while `strategy.LookbackTicks = 34` (plus the current tick) is still warming up. There is no fixed post-start trade delay (`app.TradeWarmupTicks = 0`): a fixed 20-tick delay would be redundant because the 34-tick indicator lookback still blocks decisions after 20 ticks.

Strategies that include the LLM use data readiness instead of a fixed warm-up time, via `RequiredPriceTicks()`. `LLMOnlyStrategy` needs 10 price-history ticks (`calc.MomentumWindowPoints × 2`), enough for averaged momentum. `BasicAndLLMStrategy` needs the full `LookbackTicks + 1` depth because it includes local indicator gates. In both cases the tick waits until at least one market has enough history and the news cache contains at least one item. Markets without enough price history are excluded from that LLM evaluation pass.

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
