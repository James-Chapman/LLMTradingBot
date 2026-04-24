# LLM Integration

The LLM subsystem consists of three components: an async Ollama client (`llm/client.py`), an analyser that builds prompts and parses responses (`llm/analyser.py`), and a pure-Python technical indicator library (`analysis/indicators.py`) that feeds the prompts.

The LLM acts in two modes. For the `combined` strategy, it adjusts signal confidence, annotates theses, and can veto signals entirely when its `confidence_scale` falls below `LLM_VETO_THRESHOLD` (default 0.70). For the `llm` strategy, it directly recommends `long`, `short`, or `hold`; only `long` and `short` become trade ideas. The `indicator_only` strategy does not use the LLM signal-analysis/veto pass.

---

## Ollama Client (`backend/llm/client.py`)

**Class:** `OllamaClient`

A thin async wrapper around the Ollama REST API.

### Constructor

```python
OllamaClient(
    base_url: str,     # e.g. "http://localhost:11434"
    model: str,        # e.g. "phi3:mini"
    timeout: int,      # per-request timeout in seconds (default 60)
)
```

### `probe()`

```python
async def probe(self) -> bool:
```

Called once at startup from the FastAPI lifespan. GETs `/api/tags` to check if Ollama is running and the model is loaded. Sets `self.available = True/False`. Does not raise; failures are logged as warnings.

### `chat()`

```python
async def chat(
    self,
    messages: List[Dict],   # OpenAI-compatible message list
    expect_json: bool = False,
) -> Optional[Dict]:
```

POSTs to `/api/chat` with `"stream": false`. If `expect_json=True`, the response content is parsed with `json.loads()`. Returns `None` on any failure (timeout, JSON parse error, network error).

**Retry cooldown:** After any failure, `_retry_after` is set to `now + 5 minutes`. Subsequent calls within this window return `None` immediately without hitting the network. This prevents a cascade of timeouts if the model is loading or unavailable.

```python
if datetime.utcnow() < self._retry_after:
    return None
```

The cooldown resets on the next successful call.

---

## LLM Analyser (`backend/llm/analyser.py`)

**Class:** `LLMAnalyser`

Holds cached state and implements the three LLM workflows.

```python
self._llm: OllamaClient
self.latest_reflection: Optional[Reflection]    # updated hourly
self.latest_briefing: Optional[MarketBriefing]  # updated on new news
```

---

### Workflow 1: Market Briefing (`brief_market`)

**Trigger:** New news articles detected in `_news_loop`  
**Returns:** `MarketBriefing | None`

#### System Prompt

```
You are a crypto market analyst.
You receive current prices, technical indicators, and breaking news.
Give a concise outlook for each watched trading pair.
Respond ONLY with a valid JSON object — no prose, no markdown.
```

#### User Message Structure

```
Breaking news update — N new article(s).

Current market prices:
  BTC/EUR: £85,420 | 5m: +0.26% | 15m: +0.61% | RSI 58.3 | EMA bullish | BB 62.0% | MACD bullish | Stoch 54.2 | WR -38.1
  ETH/EUR: £1,842  | 5m: -0.12% | 15m: -0.31% | RSI 44.1 | EMA bearish | BB 38.5% | MACD bearish | Stoch 32.0 | WR -67.5

New articles:
  [CoinDesk] Bitcoin ETF volumes surge — Institutional demand rising sharply ahead of Fed…
  [CoinTelegraph] BTC faces $88k resistance — Analysts expect consolidation before next…

Assess each pair: BTC/EUR, ETH/EUR

Return JSON with exactly these keys:
  market_outlooks   — object: each market key maps to
                      {"bias": "bullish|bearish|neutral", "score": float -1 to 1, "note": max 12 words}
  overall_sentiment — float -1.0 to 1.0
  key_insight       — string, max 20 words, most important takeaway
```

#### Expected Response

```json
{
    "market_outlooks": {
        "BTC/EUR": {"bias": "bullish", "score": 0.7, "note": "ETF inflows accelerating, supply tight"},
        "ETH/EUR": {"bias": "neutral", "score": 0.1, "note": "Awaiting BTC direction confirmation"}
    },
    "overall_sentiment": 0.45,
    "key_insight": "Institutional demand surge may push BTC past resistance this week"
}
```

#### Key Normalisation

Market keys from the LLM may not exactly match the internal format (e.g. `"btceur"` vs `"BTC/EUR"`). The normaliser strips slashes and lowercases both sides for matching:

```python
matched = next(
    (m for m in market_data if m.lower().replace("/", "") == k.lower().replace("/", "")),
    k,
)
```

---

### Workflow 2: Signal Analysis (`analyse_signal`)

**Trigger:** Every generated signal in the strategy loop  
**Returns:** `SignalAnalysis`

#### System Prompt

```
You are a concise crypto trading analyst.
You receive live market data, technical indicators, a market briefing,
portfolio state, and recent news.
Assess whether the signal is supported or contradicted by the evidence.
Respond ONLY with a valid JSON object — no prose, no markdown.
```

#### User Message Structure

```
Signal: BTC/EUR LONG
Confidence: 72% | Momentum: +1.23% | Price: £85,420.00

Latest market briefing (3m ago, 4 new article(s)):
  Key insight: Institutional demand surge may push BTC past resistance this week
  Overall sentiment: +0.45
  BTC/EUR outlook: bullish (score +0.70) — ETF inflows accelerating, supply tight

Technical indicators (30s ticks):
  Change — 5m: +0.26% | 15m: +0.61% | 30m: +0.37%
  RSI(14): 58.3 — neutral
  EMA9/21: £85,420.00 / £85,180.00 — bullish crossover
  BB: pos 62.0% of band | upper £86,100.00 lower £84,700.00 | width 1.65%
  MACD: +0.0024 (bullish) | signal +0.0018 (bullish) | histogram +0.0006
  Stoch: %K 54.2 / %D 51.8 — neutral
  Williams %R: -38.1 — neutral
  ATR: 48.92 (0.057% of price)

Portfolio:
  Equity: £487.20 | Cash: £350.00 | Exposure: 28.2%
Open positions:
  ETH/EUR LONG × 0.025000 @ £1,842.00 unrealised £+4.90

Recent news (BTC):
  [CoinDesk] Bitcoin ETF volumes surge — Institutional demand rising sharply ahead of Fed…
  [CoinTelegraph] BTC faces $88k resistance — Analysts expect consolidation before next leg up

Return JSON with exactly these keys:
  sentiment        — float -1.0 to 1.0
  confidence_scale — float 0.5 to 2.0 (multiplied by base confidence;
                     use >1.0 when evidence strongly supports the signal,
                     <1.0 when it contradicts, <0.7 only when you would actively oppose the trade)
  reasoning        — string, max 20 words
```

#### Expected Response

```json
{
    "sentiment": 0.65,
    "confidence_scale": 1.25,
    "reasoning": "Strong ETF inflows and bullish EMA crossover support long bias"
}
```

#### Effect on Signal

When the LLM is available (`llm_used = True`):

1. **Veto check** — if `confidence_scale < settings.llm_veto_threshold` (default 0.70), the signal is skipped entirely. A warning is written to the activity log:
   ```
   Signal {market} vetoed by LLM · Scale X.XX < threshold 0.70 — {reasoning}
   ```
2. **Confidence adjustment** — if the veto does not fire, confidence is scaled and the reasoning is appended to the thesis:
   ```python
   idea.confidence = min(0.95, idea.confidence * llm_analysis.confidence_scale)
   idea.thesis    += f" · LLM: {llm_analysis.reasoning}"
   ```

If the LLM is unavailable, `confidence_scale = 1.0` (no change), `llm_used = False`, and no veto can fire.

#### Briefing Injection

If `latest_briefing` is set, a briefing block is prepended to the user message. This gives the LLM cross-market context (e.g. "overall market is bearish — be conservative on this LONG"). The briefing's age in minutes is included so the LLM can weight stale data appropriately.

#### Reflection Injection

If `latest_reflection` is set, the LLM's own most recent self-reflection is injected into the prompt immediately after the briefing block:

```
Your most recent self-reflection (47m ago, confidence 72%):
  Pattern:    Stop-losses trigger frequently on BTC longs within 2 hours of entry
  Suggestion: Consider reducing position size or tightening entry criteria for BTC longs
Apply this advice when assessing the current signal.
```

This closes the feedback loop — the LLM's hourly pattern-finding advice now directly influences every subsequent trade decision. The reflection's age is shown so the LLM can weight it appropriately against fresher evidence.

#### Indicator Reuse

Indicators are computed once per tick inside `_strategy_loop` and stored in `market_data[sym]["indicators"]`. The signal analysis block reads them directly from `market_data` instead of recomputing, avoiding redundant work:

```python
_ind = md.get("indicators", {})
```

---

### Workflow 2b: LLM-Only Trade Recommendation (`recommend_trade`)

**Trigger:** Every market evaluated by `LLMOnlyStrategy`  
**Returns:** `LLMTradeRecommendation`

The recommender receives the same contextual inputs as signal analysis, but there is no pre-existing trade direction. The LLM must choose one action:

```json
{
  "action": "long | short | hold",
  "confidence": 0.0,
  "sentiment": 0.0,
  "reasoning": "max 20 words"
}
```

The prompt includes current and previous price, momentum, the full technical indicator snapshot, latest briefing/reflection if available, portfolio equity/cash/exposure, open positions, and recent relevant news.

Indicators are explicitly labelled as context only. The local strategy code does not require six indicators, consensus, RSI thresholds, or any other indicator condition for LLM-only signals.

Parsing rules:

- `buy` and `bullish` are normalised to `long`.
- `sell` and `bearish` are normalised to `short`.
- Invalid or missing actions become `hold`.
- Confidence is clamped to `0.0..0.95`.
- If Ollama is unavailable or the response cannot be parsed, the result is `hold` and no trade idea is emitted.

---

### Workflow 3: Outcome Reflection (`reflect_on_outcomes`)

**Trigger:** Hourly reflection loop (requires ≥5 closed trades)  
**Returns:** `Reflection | None`

#### System Prompt

```
You are a quantitative trading coach reviewing paper trade results.
Find one clear pattern and give one concrete, actionable improvement.
Respond ONLY with a valid JSON object.
```

#### User Message Structure

Each trade row now includes the indicator state at entry time, joined from `trade_ideas.indicators` via `get_closed_trades()`. Trades without a linked `trade_idea_id` (e.g. pre-linkage history or manual opens) show `no indicators`.

```
Performance summary (12 trades):
  Win rate: 42% | Total P&L: £-14.30
  Avg win: £+8.20 | Avg loss: £-12.40

Individual trades with entry-time indicators:
  BTC/EUR LONG | entry £85,420 exit £83,100 | P&L £-23.20 (-2.7%) conf 68% | stop_loss   | at entry: RSI 72 | EMA bullish | MACD bullish | Stoch 81 | WR -12 | ATR 0.08%
  ETH/EUR LONG | entry £1,842 exit £1,891   | P&L £+4.90  (+2.6%) conf 74% | manual_approve | at entry: RSI 44 | EMA bearish | MACD bearish | Stoch 31 | WR -71 | ATR 0.05%
  ...

Look for indicator-level patterns (e.g. RSI levels, EMA direction, MACD bias) that correlate
with wins or losses. Give one specific, actionable finding.

Return JSON with exactly these keys:
  pattern            — string, one pattern observed (max 25 words)
  suggestion         — string, one concrete adjustment (max 25 words)
  insight_confidence — float 0.0 to 1.0
```

#### Expected Response

```json
{
    "pattern": "All 5 losing BTC longs had RSI above 70 at entry — overbought entries consistently stopped out",
    "suggestion": "Skip LONG signals when RSI exceeds 70; wait for pullback below 65 before entry",
    "insight_confidence": 0.81
}
```

The reflection is:
1. Displayed in the LLM status card on the dashboard
2. Cached in `_analyser.latest_reflection` and persisted to `llm_reflections` in the DB
3. **Injected into every subsequent `analyse_signal()` call** so the LLM's own advice actively influences trade decisions going forward

---

## Technical Indicators (`backend/analysis/indicators.py`)

Pure Python. No external dependencies. All functions accept a `List[float]` of prices oldest-to-newest and return `None` (or an empty dict) if there is insufficient data.

### `rsi(prices, period=14) → Optional[float]`

Wilder RSI. Uses the last `period+1` prices. Returns `None` if fewer than `period+1` ticks are available.

```
gains = positive deltas in last N ticks
losses = negative deltas in last N ticks
RSI = 100 - 100 / (1 + avg_gain/avg_loss)
```

Signal labels: ≥70 = `"overbought"`, ≤30 = `"oversold"`, else `"neutral"`.

### `ema_pair(prices, fast=9, slow=21) → (Optional[float], Optional[float])`

SMA-seeded EMA. Seeded with the SMA of the first `period` values, then applies `val = price × k + val × (1-k)` where `k = 2 / (period + 1)`.

Returns `("bullish", ...)` if EMA9 > EMA21, else `"bearish"`.

### `bollinger_bands(prices, period=20) → Optional[Dict]`

20-period, 2-standard-deviation bands.

Returns:
- `upper`, `middle`, `lower` — band values
- `position` — 0–100% where the current price sits (0 = lower band, 100 = upper band)
- `width_pct` — `(upper - lower) / middle × 100` — measures volatility/squeeze

### `macd(prices, fast=12, slow=26, signal=9) → Optional[Dict]`

Full MACD with signal line and histogram. Requires at least `slow + signal − 1 = 34` ticks (~17 min at 30 s/tick).

Returns:
- `line` — MACD value (fast EMA − slow EMA)
- `signal` — 9-period EMA of the MACD line
- `histogram` — `line − signal`
- `bias` — `"bullish"` if line > 0, else `"bearish"`
- `signal_bias` — `"bullish"` if line > signal (bullish crossover), else `"bearish"`

### `atr(prices, period=14) → Optional[float]`

Average True Range approximated from close prices (no high/low available). True Range is approximated as `|close[i] − close[i−1]|`. ATR = average of the last `period` true ranges. Also exposed in `compute_all` as `atr_pct` (ATR as a percentage of the current price).

### `stochastic(prices, k_period=14, d_period=3) → Optional[Dict]`

Stochastic oscillator approximated from close prices (highest/lowest close in the window stands in for the true high/low). Returns:
- `k` — raw %K (0–100)
- `d` — 3-period SMA of %K (smoothed signal line)
- `bias` — `"oversold"` if K < 20, `"overbought"` if K > 80, else `"neutral"`

Requires `k_period + d_period − 1 = 16` ticks.

### `williams_r(prices, period=14) → Optional[float]`

Williams %R. Range: −100 (most oversold) to 0 (most overbought). Approximated from close prices. Signal labels: ≤−80 = `"oversold"`, ≥−20 = `"overbought"`, else `"neutral"`.

### `price_changes(prices, tick_seconds=30) → Dict[str, float]`

Returns percentage change from N ticks ago:
- `"5m"` — 10 ticks ago
- `"15m"` — 30 ticks ago
- `"30m"` — 60 ticks ago

Windows that exceed available history are omitted from the result.

### `compute_all(prices, tick_seconds=30) → Dict`

Convenience wrapper. Returns all available indicators in a single flat dict. Missing indicators (insufficient data) are omitted. Called once per market per tick in the strategy loop and stored in `market_data[sym]["indicators"]`.

Full output keys: `price_changes`, `rsi_14`, `rsi_signal`, `ema9`, `ema21`, `ema_cross`, `bb`, `macd`, `atr`, `atr_pct`, `stoch`, `williams_r`, `williams_r_signal`.

---

## News Relevance Filtering

`_relevant_news()` in `analyser.py` filters news for each market signal:

1. Extract base asset from market: `"BTC/EUR"` → `"BTC"` → `"btc"`
2. Look up aliases: `{"btc": ["bitcoin", "btc"], "eth": ["ethereum", "eth"], ...}`
3. Return articles where any alias appears in title or summary (case-insensitive)
4. If fewer than 3 relevant articles found, pad with most recent general articles up to `max_items=5`

---

## Response Parsing & Safety

All LLM responses are parsed defensively:

```python
sentiment = max(-1.0, min(1.0, float(result.get("sentiment", 0.0))))
scale     = max(0.5,  min(2.0,  float(result.get("confidence_scale", 1.0))))
```

- Missing fields use safe defaults (0.0 for sentiment, 1.0 for scale = no change)
- Out-of-range values are clamped
- `float()` wrapping handles string numbers from verbose models
- Any exception in parsing falls back to `_neutral()` (no adjustment)

---

## LLM Status in Dashboard

```json
{
    "llm": {
        "available": true,
        "model": "phi3:mini",
        "briefing": {
            "key_insight": "...",
            "overall_sentiment": 0.45,
            "market_outlooks": {"BTC/EUR": {"bias": "bullish", "score": 0.7, "note": "..."}, ...},
            "article_count": 4,
            "generated_at": "2026-04-24T14:32:00"
        },
        "reflection": {
            "pattern": "...",
            "suggestion": "...",
            "confidence": 0.72,
            "generated_at": "2026-04-24T14:00:00"
        }
    }
}
```
