# LLM Integration

The LLM subsystem consists of an `OpenAiClient` for external OpenAI-compatible chat-completions servers, a `TransformersClient` fallback that loads a Hugging Face model locally (`llm/transformers_client.py`), a `SwitchingLLMClient` that routes between them, and an analyser that builds prompts and parses responses (`llm/analyser.py`). A pure-Python technical indicator library (`analysis/indicators.py`) feeds the prompts.

The LLM acts in two modes. For `basic_and_llm_strategy` it adjusts signal confidence, annotates theses, and can veto signals entirely when its `confidence_scale` falls below `LLM_VETO_THRESHOLD` (default 0.70). For `llm_only_strategy` it directly recommends `long`, `short`, or `hold`; only `long` and `short` become trade ideas. The `basic_strategy` does not use the LLM at all.

---

## OpenAI-Compatible Client (`backend/llm/openai_client.py`)

**Class:** `OpenAiClient`

Connects to any server that implements `POST /v1/chat/completions` such as LM Studio, llama.cpp server, Ollama OpenAI compatibility, OpenAI, or Azure OpenAI. It is enabled when `OPENAI_BASE_URL` and `OPENAI_MODEL` are configured.

### `chat()`

Sends a standard chat-completions JSON object:

```json
{
  "model": "google/gemma-4-e4b",
  "messages": [
    {"role": "system", "content": "Respond with valid JSON only."},
    {"role": "user", "content": "Analyse BTC/EUR."}
  ]
}
```

The flattened prompt shown in debug logs is diagnostic only; it is not used as the HTTP request body. This keeps `chat()` aligned with the same OpenAI-compatible payload shape used by `probe()`.

### Circuit Breaker

HTTP and transport failures open the circuit with exponential backoff. Malformed model JSON returns `None` without opening the circuit, because a bad completion should not mark the backend unavailable.

---

## Transformers Client (`backend/llm/transformers_client.py`)

**Class:** `TransformersClient`

Loads a Hugging Face model locally via the `transformers` pipeline API. The model is downloaded on first `probe()` call and kept resident in memory (GPU VRAM when available, system RAM otherwise) for the lifetime of the process. No separate server is required.

### Constructor

```python
TransformersClient(
    model: str,    # HuggingFace model ID, e.g. "Qwen/Qwen2.5-1.5B-Instruct"
    timeout: int,  # per-call timeout in seconds (default 60)
)
```

> **Important:** Only standard HuggingFace repos with a `config.json` and `model_type` are supported. GGUF-only repos (e.g. anything on `ggml-org/`) cannot be loaded via `pipeline()` and will produce a clear error message in the log directing you to switch models.

### `probe()`

Called once at startup. Downloads and initialises the model pipeline in a thread pool worker so the event loop is not blocked. On success, `self.available = True` and the pipeline is cached in `self._pipeline`. On failure, the error is logged and the circuit opens.

The pipeline is loaded with:
```python
pipeline(
    "text-generation",
    model=self.model,
    device_map="auto",   # places layers on GPU when VRAM is available
    dtype="auto",        # uses the model's native bfloat16/float16, halving VRAM vs float32
)
```

### `chat()`

Converts the OpenAI-style `messages` list into a single prompt string, runs inference in a thread pool worker, and returns a parsed dict (when `expect_json=True`) or a raw string. Returns `None` on failure without opening the circuit — a single malformed response does not take the LLM offline.

### Circuit Breaker

Transport failures and model errors open the circuit. The retry window starts at 30 seconds, doubles on repeated failures, and caps at 5 minutes. When the window expires, `can_attempt` returns `True` so the next LLM call makes a half-open retry.

### GPU Acceleration

PyTorch must be installed with CUDA support for GPU acceleration. `setup.bat` installs the CUDA 12.8 wheel (`--index-url https://download.pytorch.org/whl/cu128`) automatically, with a graceful fallback to the CPU build if the CUDA index is unreachable. Verify GPU is active after setup:

```python
import torch
print(torch.cuda.is_available())   # True
print(torch.cuda.get_device_name(0))
```

---

## LLM Analyser (`backend/llm/analyser.py`)

**Class:** `LLMAnalyser`

Holds cached state and implements the three LLM workflows.

```python
self._llm: SwitchingLLMClient       # wired in at startup
self.latest_reflection: Optional[Reflection]    # updated hourly
self.latest_briefing:   Optional[MarketBriefing]  # updated on new news
```

---

## Shared Client Helpers (`backend/llm/common.py`)

`common.py` contains behavior shared by the OpenAI-compatible and Transformers clients:

- `loads_model_json()` parses model output as a JSON object, stripping Markdown fences, extracting the first embedded object, and repairing obvious missing commas between object fields.
- `messages_to_prompt()` renders OpenAI-style chat messages into the plain-text prompt format used by local text-generation models and debug logs.
- `CircuitBreakerMixin` owns the shared `available`, `can_attempt`, `circuit_state`, retry-delay, `_should_attempt()`, `_mark_success()`, and `_mark_failed()` behavior. Each client keeps its own `_log_failure()` so log wording remains backend-specific.
- `utc_now()` provides the timezone-aware clock used by circuit retry timing.

Endpoint-specific behavior stays in each concrete client: OpenAI-compatible HTTP payloads and headers remain in `OpenAiClient`, while Transformers pipeline loading and unloading remain in `TransformersClient`.

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
  BTC/EUR: €85,420 | 5m: +0.26% | 15m: +0.61% | RSI 58.3 | EMA bullish | BB 62.0% | MACD bullish | Stoch 54.2 | WR -38.1
  ETH/EUR: €1,842  | 5m: -0.12% | 15m: -0.31% | RSI 44.1 | EMA bearish | BB 38.5% | MACD bearish | Stoch 32.0 | WR -67.5

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

Market keys from the LLM may not exactly match the internal format (e.g. `"btceur"` vs `"BTC/EUR"`). The normaliser strips non-alphanumeric characters and lowercases both sides for matching, logging a warning when a key is remapped.

#### Outlook Shape Normalisation

The LLM is prompted to return each outlook as an object, but malformed or older persisted rows may contain a plain string such as `"bullish"`. The analyser normalises every market outlook to:

```json
{"bias": "bullish", "score": 0.0, "note": ""}
```

Invalid or missing scores are clamped/defaulted to `0.0`, so the strategy loop treats malformed briefing data as neutral instead of crashing.

---

### Workflow 2: Signal Analysis (`analyse_signal`)

**Trigger:** `BasicAndLLMStrategy` — called after a signal survives the indicator consensus pipeline  
**Returns:** `SignalAnalysis`

#### Indicator Consensus Pipeline (runs before the LLM is called)

`BasicAndLLMStrategy` applies nine sequential steps. The signal is discarded at any failed step; the LLM is only reached if all pass.

| Step | Check | Threshold |
|------|-------|-----------|
| 1 | **Momentum gate** — `\|price change\|` must exceed the dynamic threshold | max(0.2%, ATR × 1.0) |
| 2 | **Higher-timeframe filter** — 15-min EMA cross must agree with direction (neutral is allowed) | EMA9 vs EMA21 on 15m candles |
| 3 | **RSI hard block** — extreme RSI kills the trade outright | LONG blocked if RSI ≥ 80; SHORT if RSI ≤ 20 |
| 4 | **BB hard block** — price at band extreme kills the trade | LONG blocked if BB ≥ 95%; SHORT if BB ≤ 5% |
| 5 | **Indicator vote** — each of 9 indicators votes +1 / −1 / 0 | see thresholds below |
| 6 | **Support gate** — enough indicators must agree | ≥ 5 supporting votes |
| 7 | **Consensus gate** — net vote must be positive | net votes ≥ +1 |
| 8 | **Confidence floor** — base confidence must be actionable | ≥ 0.20 |
| 9 | **LLM veto** — LLM confidence_scale must clear the threshold | ≥ `LLM_VETO_THRESHOLD` (default 0.70) |

**Indicator voting thresholds** (each casts +1 for direction, −1 against, 0 if neutral):

| Indicator | Bullish (supports LONG) | Bearish (supports SHORT) |
|-----------|------------------------|--------------------------|
| RSI | < 40 | > 60 |
| EMA cross | = "bullish" | = "bearish" |
| BB position | < 30% | > 70% |
| MACD bias | = "bullish" | = "bearish" |
| MACD signal bias | = "bullish" | = "bearish" |
| Stochastic %K | < 20 | > 80 |
| Williams %R | ≤ −80 | ≥ −20 |
| 5m price change | > 0 | < 0 |
| 15m price change | > 0 | < 0 |

**Base confidence formula:**

```
base         = min(|momentum| × 100, 0.50)
ind_bonus    = clamp(net_votes × 0.05, −0.20, +0.40)
atr_penalty  = −min(0.10, max(0, (ATR% − 1.0) × 0.05))   # only when ATR% > 1.0
base_conf    = clamp(base + ind_bonus + atr_penalty, 0.10, 0.95)
final_conf   = min(0.95, base_conf × llm_confidence_scale)
```

#### System Prompt

```
You are a concise crypto trading analyst.
You receive live market data, technical indicators, a market briefing,
portfolio state, and recent news.
Assess whether the signal is supported or contradicted by the evidence.
Respond ONLY with a valid JSON object — no prose, no markdown.
```

#### Full Prompt Example (BTC/EUR LONG)

This is the message sent to the LLM after a BTC/EUR LONG signal has cleared all 8 indicator steps (momentum gate, hard filters, 6/9 supporting votes, base confidence 0.68):

```
Signal: BTC/EUR LONG
Confidence: 68% | Momentum: +0.54% | Price: €83,420.00

Latest market briefing (14m ago, 3 new article(s)):
  Key insight: Fed rate hold boosts risk assets; BTC holding key support.
  Overall sentiment: +0.42
  BTC/EUR outlook: bullish (score +0.60) — Strong momentum, watch resistance at 85k.

Your most recent self-reflection (47m ago, confidence 72%):
  Pattern:    Losing longs had RSI > 72 at entry with bearish MACD histogram.
  Suggestion: Avoid longs when RSI above 70 and MACD histogram is negative.
Apply this advice when assessing the current signal.

Technical indicators (30s ticks):
  Change — 1h: +0.54% | 4h: +1.23% | 24h: +3.81%
  RSI(14): 34.2 — oversold
  EMA9/21: €83,312.00 / €82,890.00 — bullish crossover
  BB: pos 38% of band | upper €85,100.00 lower €80,400.00 | width 5.5%
  MACD: +124.4000 (bullish) | signal +97.8000 (bullish) | histogram +26.6000
  Stoch: %K 17 / %D 14 — oversold
  Williams %R: -84 — oversold
  ATR: 430.00 (0.516% of price)

Portfolio:
  Equity: €512.40 | Cash: €312.40 | Exposure: 39.0%
Open positions:
  ETH/EUR LONG × 0.041200 @ €2,187.50 unrealised €+4.20

Recent news (BTC):
  [CoinDesk] Bitcoin holds $88k as macro sentiment improves — Fed signals pause lifts
    risk appetite across crypto markets; traders eye $90k resistance…
  [Reuters] BlackRock Bitcoin ETF sees record inflows — Institutional demand
    accelerating; $500M net inflow logged in past 48 hours…
  [CryptoSlate] On-chain data shows long-term holders accumulating — Glassnode:
    LTH supply at 6-month high as short-term holders reduce exposure…

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
    "sentiment": 0.72,
    "confidence_scale": 1.30,
    "reasoning": "Oversold RSI and Stoch with bullish EMA and strong ETF inflows; strong long case."
}
```

In this example:
- Base confidence was **0.68** (momentum 0.54%, 6/9 indicators supporting, ATR 0.52% above threshold → small penalty)
- LLM scale **1.30** → final confidence = min(0.95, 0.68 × 1.30) = **0.88**
- Signal passes the veto threshold (1.30 ≥ 0.70) and is forwarded to RiskEngine

#### Effect on Signal

When the LLM is available (`llm_used = True`):

1. **Veto check** — if `confidence_scale < LLM_VETO_THRESHOLD` (default 0.70), the signal is discarded:
   ```
   BTC/EUR LONG vetoed by LLM — scale 0.55 < threshold 0.70: contradicted by bearish macro outlook
   ```
2. **Confidence adjustment** — if the veto does not fire, final confidence is calculated and the LLM reasoning is appended to the thesis:
   ```python
   idea.confidence = min(0.95, base_confidence * llm_analysis.confidence_scale)
   idea.thesis    += f" | LLM: {llm_analysis.reasoning}"
   ```

If the LLM is unavailable, `confidence_scale = 1.0` (no change), `llm_used = False`, and no veto fires.

#### Briefing Injection

If `latest_briefing` is set, a briefing block is prepended to the user message. This gives the LLM cross-market context (e.g. "overall market is bearish — be conservative on this LONG"). The briefing's age in minutes is included so the LLM can weight stale data appropriately.

#### Reflection Injection

If `latest_reflection` is set, the LLM's own most recent self-reflection is injected into the prompt immediately after the briefing block. This closes the feedback loop — the LLM's hourly pattern-finding advice directly influences every subsequent trade decision.

#### Indicator Reuse

Indicators are computed once per tick inside `_strategy_loop` and stored in `market_data[sym]["indicators"]`. The signal analysis block reads them directly from `market_data` instead of recomputing.

---

### Workflow 2b: LLM-Only Trade Recommendation (`recommend_trade`)

**Trigger:** Every market evaluated by `LLMOnlyStrategy`  
**Returns:** `LLMTradeRecommendation`

The LLM receives all the same contextual inputs as signal analysis (price, indicators, briefing, reflection, portfolio, news) but there is no pre-existing trade direction — it must originate the action itself. Indicators are passed as context; the strategy applies no indicator consensus gate before calling the LLM.

#### System Prompt

```
You are a concise crypto trading decision engine.
You receive live market data, technical indicators, a market briefing,
portfolio state, and recent news.
Recommend exactly one action: long, short, or hold.
Use indicators as context, but do not require indicator consensus.
Respond ONLY with a valid JSON object — no prose, no markdown.
```

#### Full Prompt Example (BTC/EUR)

```
Market: BTC/EUR
Price: €83,420.00 | Previous: €83,105.00 | Momentum: +0.38%

Latest market briefing (14m ago, 3 new article(s)):
  Key insight: Fed rate hold boosts risk assets; BTC holding key support.
  Overall sentiment: +0.42
  BTC/EUR outlook: bullish (score +0.60) — Strong momentum, watch resistance at 85k.

Your most recent self-reflection (47m ago, confidence 72%):
  Pattern:    Losing longs had RSI > 72 at entry with bearish MACD histogram.
  Suggestion: Avoid longs when RSI above 70 and MACD histogram is negative.
Apply this advice when choosing the current action.

Technical indicators (context only, do not require consensus):
  Change — 1h: +0.54% | 4h: +1.23% | 24h: +3.81%
  RSI(14): 58.3 — neutral
  EMA9/21: €83,312.00 / €82,890.00 — bullish crossover
  BB: pos 61% of band | upper €85,100.00 lower €80,400.00 | width 5.5%
  MACD: +124.4000 (bullish) | signal +97.8000 (bullish) | histogram +26.6000
  Stoch: %K 64 / %D 58 — bullish
  Williams %R: -38 — neutral
  ATR: 430.00 (0.516% of price)

Portfolio:
  Equity: €512.40 | Cash: €312.40 | Exposure: 39.0%
Open positions:
  ETH/EUR LONG × 0.041200 @ €2,187.50 unrealised €+4.20

Recent news (BTC):
  [CoinDesk] Bitcoin holds $88k as macro sentiment improves — Fed signals pause lifts
    risk appetite across crypto markets; traders eye $90k resistance…
  [Reuters] BlackRock Bitcoin ETF sees record inflows — Institutional demand
    accelerating; $500M net inflow logged in past 48 hours…
  [CryptoSlate] On-chain data shows long-term holders accumulating — Glassnode:
    LTH supply at 6-month high as short-term holders reduce exposure…

Return JSON with exactly these keys:
  action     — string: long, short, or hold
  confidence — float 0.0 to 0.95, only high when the trade is actionable
  sentiment  — float -1.0 to 1.0
  reasoning  — string, max 20 words
```

#### Expected Response

```json
{
    "action": "long",
    "confidence": 0.71,
    "sentiment": 0.58,
    "reasoning": "Bullish EMA cross, RSI not overbought, strong institutional inflows support upside."
}
```

#### Notes on prompt content

- The **briefing block** is absent until `brief_market()` has run at least once (triggered by new news arriving).
- The **reflection block** is absent until `reflect_on_outcomes()` has run, which requires at least 5 closed trades.
- If price history is too short for indicators, the indicators section reads `Insufficient price history for indicators`.
- The **news block** caps at 5 relevant articles, padded with general news if fewer than 3 match the asset's keywords.

#### Parsing Rules

- `buy` / `bullish` are normalised to `long`; `sell` / `bearish` to `short`.
- Invalid or missing actions become `hold`.
- Confidence is clamped to `0.0..0.95`.
- On parse failure or LLM unavailability, the result is `hold` and no trade idea is emitted.

LLM-only trade ideas still pass through `RiskEngine` before approval or execution.

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

Each trade row includes the indicator state at entry time, joined from `trade_ideas.indicators` via `get_closed_trades()`. Trades without a linked `trade_idea_id` show `no indicators`.

```
Performance summary (12 trades):
  Win rate: 42% | Total P&L: €-14.30
  Avg win: €+8.20 | Avg loss: €-12.40

Individual trades with entry-time indicators:
  BTC/EUR LONG | entry €85,420 exit €83,100 | P&L €-23.20 (-2.7%) conf 68% | stop_loss      | at entry: RSI 72 | EMA bullish | MACD bullish | Stoch 81 | WR -12 | ATR 0.08%
  ETH/EUR LONG | entry €1,842  exit €1,891   | P&L €+4.90  (+2.6%) conf 74% | manual_approve | at entry: RSI 44 | EMA bearish | MACD bearish | Stoch 31 | WR -71 | ATR 0.05%
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
3. **Injected into every subsequent `analyse_signal()` and `recommend_trade()` call** so the LLM's own advice actively influences trade decisions going forward

---

## Technical Indicators (`backend/analysis/indicators.py`)

Pure Python. No external dependencies. All functions accept a `List[float]` of prices oldest-to-newest and return `None` (or an empty dict) if there is insufficient data.

### `rsi(prices, period=14) → Optional[float]`

Wilder RSI. Uses the last `period+1` prices. Returns `None` if fewer than `period+1` ticks are available.

Signal labels: ≥70 = `"overbought"`, ≤30 = `"oversold"`, else `"neutral"`.

### `ema_pair(prices, fast=9, slow=21) → (Optional[float], Optional[float])`

SMA-seeded EMA. Returns `("bullish", ...)` if EMA9 > EMA21, else `"bearish"`.

### `bollinger_bands(prices, period=20) → Optional[Dict]`

20-period, 2-standard-deviation bands.

Returns:
- `upper`, `middle`, `lower` — band values
- `position` — 0–100% where the current price sits (0 = lower band, 100 = upper band)
- `width_pct` — `(upper - lower) / middle × 100` — measures volatility/squeeze

### `macd(prices, fast=12, slow=26, signal=9) → Optional[Dict]`

Full MACD with signal line and histogram. Requires at least `slow + signal − 1 = 34` ticks (~17 min at 30 s/tick).

Returns: `line`, `signal`, `histogram`, `bias`, `signal_bias`.

### `atr(prices, period=14) → Optional[float]`

Average True Range approximated from close prices. Also exposed in `compute_all` as `atr_pct` (ATR as a percentage of the current price).

### `stochastic(prices, k_period=14, d_period=3) → Optional[Dict]`

Returns `k`, `d`, and `bias` (`"oversold"` if K < 20, `"overbought"` if K > 80, else `"neutral"`). Requires `k_period + d_period − 1 = 16` ticks.

### `williams_r(prices, period=14) → Optional[float]`

Range: −100 (most oversold) to 0 (most overbought). Signal labels: ≤−80 = `"oversold"`, ≥−20 = `"overbought"`, else `"neutral"`.

### `price_changes(prices, tick_seconds=30) → Dict[str, float]`

Returns percentage change for `"1h"`, `"4h"`, and `"24h"` windows. Windows exceeding available history are omitted.

### `compute_all(prices, tick_seconds=30, ohlc_candles=None) → Dict`

Convenience wrapper. Returns all available indicators in a single flat dict. Called once per market per tick in the strategy loop and stored in `market_data[sym]["indicators"]`.

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

- Missing fields use safe defaults
- Out-of-range values are clamped
- `float()` wrapping handles string numbers from verbose models
- Any exception in parsing falls back to `_neutral()` / `_hold()` — no adjustment, no trade

The JSON parser also handles common LLM formatting mistakes: Markdown code fences are stripped, embedded JSON objects are extracted from surrounding prose, and missing field-separator commas are repaired.

---

## LLM Status in Dashboard

```json
{
    "llm": {
        "available": true,
        "status": "available",
        "model": "Qwen/Qwen2.5-1.5B-Instruct",
        "briefing": {
            "key_insight": "...",
            "overall_sentiment": 0.45,
            "market_outlooks": {"BTC/EUR": {"bias": "bullish", "score": 0.7, "note": "..."}, ...},
            "article_count": 4,
            "generated_at": "2026-04-28T14:32:00"
        },
        "reflection": {
            "pattern": "...",
            "suggestion": "...",
            "confidence": 0.72,
            "generated_at": "2026-04-28T14:00:00"
        }
    }
}
```

`status` is one of `"available"`, `"unavailable"`, or `"not_configured"` (when `TRANSFORMERS_LLM_MODEL` is blank).
