# Strategy & Learning

## Combined Strategy (`backend/strategy/basic_strategy.py`)

**Class:** `BasicStrategy`  
**Strategy ID:** `"combined"`

An indicator-consensus signal generator with contextual sentiment adjustments. It compares the current price against a price from `_LOOKBACK_TICKS` (10) ticks ago for the momentum signal, confirms or rejects that signal using up to 9 additional technical indicators, then applies news sentiment, LLM briefing sentiment, learner adjustment, and the full LLM signal-analysis/veto pass in the strategy loop.

## Indicator Only Strategy (`backend/strategy/indicator_only_strategy.py`)

**Class:** `IndicatorOnlyStrategy`  
**Strategy ID:** `"indicator_only"`

Uses the same momentum, hard-filter, higher-timeframe, coverage, and consensus rules as `BasicStrategy`, but disables news sentiment and LLM sentiment inputs. The strategy loop also skips the LLM signal-analysis/veto pass for `indicator_only`, so technical indicators determine whether the signal is acted upon.

---

### Signal Generation Logic

```python
async def evaluate(
    self,
    market_data: Dict[str, Any],
    news_signals: List[Dict],
    learner=None,
) -> List[TradeIdea]:
```

For each market in `market_data`:

#### 1. Momentum Gate

```python
momentum = (current_price - previous_price) / previous_price
if abs(momentum) < 0.002:
    continue    # < 0.2% move — discard
```

`previous_price` is the price `_LOOKBACK_TICKS` (10) ticks ago:

```python
prev = hist[-_LOOKBACK_TICKS - 1] if len(hist) > _LOOKBACK_TICKS else snap.price
```

The momentum gate is purely for noise suppression. The momentum value determines the initial hypothesis direction (LONG if positive, SHORT if negative) but does not win by itself — the indicator consensus must confirm.

#### 2. Hard Filters

Block trades into extreme conditions where the probability of reversal is high:

| Filter | Condition | Effect |
|--------|-----------|--------|
| RSI overbought | RSI ≥ 80 and LONG | Skip |
| RSI oversold | RSI ≤ 20 and SHORT | Skip |
| BB upper extreme | BB position ≥ 95% and LONG | Skip |
| BB lower extreme | BB position ≤ 5% and SHORT | Skip |

These fire before the consensus vote and cannot be overridden by other indicators.

#### 3. Indicator Consensus Voting

Each indicator casts a vote: `+1` (agrees with signal direction), `−1` (opposes), `0` (neutral or absent).

| # | Indicator | Bullish threshold | Bearish threshold |
|---|-----------|------------------|------------------|
| 1 | RSI | < 40 (oversold) | > 60 (overbought) |
| 2 | EMA cross | `"bullish"` (EMA9 > EMA21) | `"bearish"` |
| 3 | BB position | < 30% (near lower band) | > 70% (near upper band) |
| 4 | MACD line | bias `"bullish"` (line > 0) | bias `"bearish"` |
| 5 | MACD signal crossover | `signal_bias "bullish"` (line > signal) | `signal_bias "bearish"` |
| 6 | Stochastic %K | < 20 (oversold) | > 80 (overbought) |
| 7 | Williams %R | ≤ −80 (oversold) | ≥ −20 (overbought) |
| 8 | 5m price change | positive | negative |
| 9 | 15m price change | positive | negative |

`votes` = sum of all individual votes (range: −9 to +9)  
`available` = count of indicators that cast a non-neutral opinion

#### 4. Indicator Support Gate

```python
if supporting < 6:
    continue    # not enough indicators agree with the signal direction
```

At least 6 indicators must agree with the proposed trade direction before the strategy can emit a trade idea. A signal with 9 available indicators but only 5 supporting and 4 opposing is blocked.

#### 5. Consensus Gate

```python
if votes < 1:
    continue    # more indicators oppose than support
```

Once the 6-indicator support gate is met, the net vote must also be positive (`>= 1`).

#### 6. Confidence Calculation

```
base  = min(abs(momentum) × 100, 0.50)          ← capped at 0.50
bonus = clamp(votes × 0.05, −0.20, +0.40)       ← indicator agreement
news  = +0.10 if aligned, −0.10 if opposed, 0   ← news sentiment
atr   = −min(0.10, (atr_pct − 1.0) × 0.05)      ← volatility penalty if ATR > 1% of price
─────────────────────────────────────────────────
final = clamp(base + bonus + news + atr, 0.10, 0.95)
```

With all 9 indicators agreeing, maximum confidence = 0.50 + 0.40 + 0.10 = **1.00 → capped at 0.95**.  
With 6 agreeing and 0 opposing: 0.50 + 0.30 + 0.10 = **0.90**.

#### 7. Learner Adjustment

```python
if learner is not None:
    confidence = learner.adjust_confidence(
        self.strategy_id, symbol, direction.value, confidence
    )
```

See the Learner section below.

#### 8. Minimum Confidence Filter

```python
if confidence < 0.20:
    continue
```

Signals below 20% are discarded. In `fully_automated` mode, a further `MIN_SIGNAL_CONFIDENCE` (default 0.65) is applied after LLM adjustment — see `03-background-loops.md`.

#### 9. Position Sizing

All signals use a fixed `position_sizing_proposal = 0.20` (20% of current equity per trade). The risk engine validates this against available cash.

---

### `TradeIdea` Output

```python
TradeIdea(
    strategy_id = "combined",
    market      = symbol,
    direction   = direction,
    thesis      = "Momentum +0.42% | RSI 38 | EMA bullish | MACD bullish | support 6/7 (net +5)",
    supporting_signals = {
        "momentum":             0.00421,
        "news_sentiment":       0.0,
        "current_price":        85420.0,
        "previous_price":       85061.0,
        "indicator_votes":      4,
        "indicators_supporting": 6,
        "indicators_opposing":  1,
        "indicators_available": 7,
        "rsi_14":               38.0,
        "ema_cross":            "bullish",
        "bb_position":          34.2,
        "macd_bias":            "bullish",
        "macd_signal_bias":     "bullish",
        "macd_histogram":       0.0387,
        "stoch_k":              None,       # not enough history yet
        "stoch_d":              None,
        "williams_r":           -62.5,
        "atr_pct":              0.097,
        "price_change_5m":      0.35,
        "price_change_15m":     0.79,
    },
    confidence  = 0.73,
    entry_plan  = "Enter at market on indicator consensus confirmation",
    exit_plan   = "Exit on momentum reversal or stop-loss at 7% loss",
    stop_or_invalidation    = "Abandon if unrealised loss reaches 7%",
    position_sizing_proposal = 0.20,
    mode_eligibility = [MANUAL, SEMI_AUTOMATED, FULLY_AUTOMATED],
)
```

---

## Performance Learner (`backend/strategy/learner.py`)

**Class:** `PerformanceLearner`

Tracks historical trade outcomes per (strategy, market, direction) tuple and adjusts future signal confidence using win rate and P&L magnitude. Recent outcomes are weighted more heavily than old ones, while rolling statistics and percentiles use numpy-backed vectors over the stored P&L history.

---

### Data Structure

```python
# Internal state
_stats: Dict[Tuple[str, str, str], _Stats]

_Stats.pnl_history: List[float]     # chronological realised P&L values
_Stats.weighted_wins: float         # exponentially decayed winning weight
_Stats.weighted_total: float        # exponentially decayed total weight
```

Key format: `(strategy_id, market, direction)`  
Example: `("combined", "BTC/EUR", "long")`

---

### `record_outcome()`

```python
def record_outcome(self, strategy_id: str, market: str, direction: str, pnl: float) -> None:
    key = (strategy_id, market, direction)
    stats = self._stats[key]
    stats.weighted_wins *= DECAY
    stats.weighted_total *= DECAY
    stats.weighted_total += 1.0
    stats.weighted_wins += 1.0 if pnl > 0 else 0.0
    stats.record_pnl(pnl)
```

Called from `main.py` in three places:
- Stop-loss closure
- Fully-automated SHORT execution (closing a long)
- Manual approval SHORT execution (closing a long)

---

### `adjust_confidence()`

```python
def adjust_confidence(self, strategy_id: str, market: str, direction: str, confidence: float) -> float:
    stats = self._stats.get((strategy_id, market, direction))
    if stats is None or stats.raw_count < 5:
        return confidence   # insufficient data — no adjustment

    scale = 1.0 + (stats.quality_score() * 0.5)
    return min(0.95, confidence * scale)
```

**`DECAY = 0.92`** — each older trade is worth 92% of the next more recent one. After 10 trades, the oldest has weight `0.92^9 ≈ 0.47` relative to the newest.

**Scale interpretation:** `quality_score()` blends decayed win rate with average win/loss magnitude. A strongly negative expectancy can halve confidence, neutral expectancy leaves it unchanged, and strongly positive expectancy can boost confidence by up to 50%.

Confidence is clamped to [0.1, 0.95] after adjustment.

---

### `load_from_outcomes()`

```python
def load_from_outcomes(self, outcomes: List[SignalOutcomeModel]) -> None:
```

Called at startup with all historical `signal_outcomes` from the database. Populates `_stats` and chronological P&L history so the learner has history from previous sessions without requiring live trades.

---

### Rolling Statistics

```python
def rolling_win_rate(self, strategy_id: str, market: str, direction: str, n: int = 10) -> float:
    pnl = np.asarray(stats.pnl_history, dtype=np.float64)
    return float(np.mean(pnl[-n:] > 0.0))

def pnl_percentiles(self, strategy_id: str, market: str, direction: str) -> Dict[str, float]:
    pnl = np.asarray(stats.pnl_history, dtype=np.float64)
    p25, p50, p75, p95 = np.percentile(pnl, [25, 50, 75, 95], method="weibull")
```

The summary API exposes `rolling_win_rate_10`, `average_pnl`, `median_pnl`, `pnl_p25`, `pnl_p75`, and `pnl_p95` alongside the existing win/loss averages and quality score.

---

### `summary()`

Returns rows for the `GET /api/learning` endpoint and dashboard:

```python
[
    {
        "strategy": "combined",
        "market": "BTC/EUR",
        "direction": "long",
        "trades": 15,
        "win_rate": 0.63,
        "rolling_win_rate_10": 0.70,
        "average_pnl": 4.25,
        "median_pnl": 3.80,
        "pnl_p25": -1.50,
        "pnl_p75": 8.90,
        "pnl_p95": 15.20,
        "quality_score": 0.18,
    }
]
```

---

## LLM Confidence Adjustment

After the learner adjustment, the LLM analyser applies an additional multiplier:

```python
if llm_analysis.llm_used:
    idea.confidence = min(0.95, idea.confidence * llm_analysis.confidence_scale)
```

`confidence_scale` is returned by the LLM in range [0.5, 2.0]. A value of 2.0 means the LLM strongly supports the signal and can double the base confidence. The final confidence is always capped at 0.95.

If `confidence_scale` is below `LLM_VETO_THRESHOLD` (default 0.70) and the LLM was used, the signal is skipped before reaching the risk engine — see `07-llm-integration.md` for details.

### Combined Adjustment Chain

```
Strategy base confidence
    (momentum base ≤ 0.50)
    + indicator consensus bonus (−0.20 to +0.40)
    + news sentiment bonus (±0.10)
    − ATR volatility penalty (0 to −0.10)
    → clamp to [0.10, 0.95]

    × Learner scale (0.5–1.5, requires ≥5 samples)
    × LLM confidence_scale (0.5–2.0, when available)
    → veto if scale < LLM_VETO_THRESHOLD (default 0.70)
    → clamp to [0.10, 0.95]
```

---

## Signal Threshold Summary

| Stage | Threshold | Effect |
|---|---|---|
| Momentum minimum | 0.2% | No signal generated |
| Hard filter | RSI ≥80 / ≤20, BB ≥95% / ≤5% | Signal blocked |
| Indicator support gate | 6 agreeing indicators | Signal discarded if fewer support the trade direction |
| Consensus gate | net votes < 1 | Signal discarded |
| Confidence minimum (strategy) | 20% | Signal discarded |
| Confidence minimum (fully_automated) | 65% (configurable) | Signal skipped |
| LLM veto threshold | 0.70 (configurable) | Signal skipped if LLM scale < threshold |
| LLM max confidence | 95% | Hard cap regardless of scale |
| Risk min trade size | £50 | Risk engine rejects if equity is very low |
| Position sizing | 20% of equity | Fixed; all signals use this |

---

## Indicator Warm-up Times

At 30 s/tick, indicators become available after the following number of ticks:

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

During the warmup period (controlled by `_LOOKBACK_TICKS = 10` in `main.py`), the strategy loop skips signal evaluation entirely. After warmup, indicators that still have insufficient history are simply omitted from the vote. A trade idea is not emitted until at least 6 indicators have a non-neutral opinion.

---

## LLM Only Strategy (`backend/strategy/llm_only_strategy.py`)

**Class:** `LLMOnlyStrategy`  
**Strategy ID:** `"llm"`

The LLM-only strategy does not apply local indicator gates, indicator voting, or learner confidence adjustment. It asks the LLM for an explicit `long`, `short`, or `hold` recommendation for each market and converts only `long`/`short` responses into `TradeIdea` objects.

Indicators are still passed into the LLM prompt as context. They are not counted, voted, or required by local strategy code.

### Recommendation Flow

```python
recommendation = await analyser.recommend_trade(
    market=symbol,
    current_price=current_price,
    previous_price=previous_price,
    indicators=indicators,
    news=news_signals,
    equity=equity,
    cash=cash,
    open_positions=open_positions,
)
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

`hold`, invalid actions, parse failures, or unavailable LLM state produce no signal. A valid `long`/`short` recommendation uses the LLM confidence directly as the trade confidence. The normal risk engine, approval flow, position limits, and fully automated confidence threshold still apply after the strategy emits the trade idea.
