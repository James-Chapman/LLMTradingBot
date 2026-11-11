# API Endpoints

All routes are registered on a standard-library `http.ServeMux` in `internal/server/server.go` (`routes()`), wrapped in a CORS middleware that allows the origins in `CORS_ORIGINS`. Handlers delegate to the `App` in `internal/app`. Paths, methods, status codes and JSON keys match the original FastAPI app, so the frontend works unchanged.

`cmd/tradingbot` binds the server to `HOST` (default `127.0.0.1`) on `PORT` (default `8000`). The frontend is served from the same process; no separate web server is required. The `-frontend` flag sets the directory the pages are served from (default `frontend`).

---

## Static Pages

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/` | Serves `frontend/index.html` — the main dashboard |
| `GET` | `/approvals` | Serves `frontend/approvals.html` — the approval management page |
| `GET` | `/sw.js` | Serves the service worker from the root scope |
| `GET` | `/static/...` | Serves files under `frontend/static/` (no directory listings) |

HTML pages and `sw.js` are sent with `Cache-Control: no-store, max-age=0` so frontend fixes apply immediately.

---

## Health

### `GET /health`

Returns a basic liveness check. Does not probe LLM or Kraken connectivity.

**Response:**

```json
{
    "status": "healthy",
    "version": "0.6.0"
}
```

### `GET /api/health`

Returns the LLM backend state.

**Response:**

```json
{
    "llm_available": true,
    "llm_backend":   "lmstudio | remote | none",
    "llm_model":     "qwen2.5-7b-instruct (LM Studio)"
}
```

---

## Dashboard

### `GET /api/dashboard`

Returns the full dashboard state in a single payload. Polled every 5 seconds by the frontend.

The response carries an `ETag` header holding the dashboard version counter, which is bumped on every state change. A request whose `If-None-Match` matches the current version gets `304 Not Modified` with no body. If building the payload panics, the handler logs the error and returns a reduced fallback payload instead of a 500.

**Response shape:**

```json
{
    "mode": "manual | semi_automated | fully_automated",
    "environment": "paper | live",
    "base_currency": "EUR",
    "currency_symbol": "€",
    "equity": "487.20",
    "cash": "312.50",
    "open_position_ids": ["3fa85f64-5717-4562-b3fc-2c963f66afa6"],
    "markets": [
        {"symbol": "BTC/EUR", "price": "85420.00", "enabled": true, "live": false},
        {"symbol": "ETH/EUR", "price": "-",        "enabled": true, "live": false}
    ],
    "signals": [
        {
            "strategy":      "llm_only_strategy",
            "market":        "BTC/EUR",
            "direction":     "long",
            "confidence":    0.72,
            "thesis":        "Price momentum: 0.82% | LLM: Strong ETF inflows support long bias",
            "risk_approved": true,
            "risk_reason":   "All risk checks passed",
            "trade_idea_id": "7c9e6679-..."
        }
    ],
    "positions": [
        {
            "position_id":      "3fa85f64",
            "position_id_full": "3fa85f64-5717-4562-b3fc-2c963f66afa6",
            "market":           "BTC/EUR",
            "direction":        "long",
            "opened_at":        "2026-04-23T14:32:10",
            "size":             "0.001140",
            "avg_price":        "85420.00",
            "unrealized_pnl":   "4.20",
            "strategy":         "llm_only_strategy",
            "source":           "",
            "status":           "open",
            "trade_idea_id":    "7c9e6679-..."
        }
    ],
    "approvals": [
        {
            "id":              "7c9e6679-...",
            "market":          "ETH/EUR",
            "direction":       "long",
            "strategy_id":     "llm_only_strategy",
            "confidence":      0.65,
            "size":            5.265,
            "expires_at":      "2026-04-23T15:02:00",
            "thesis":          "...",
            "entry_plan":      "Enter at market when momentum confirms trend",
            "exit_plan":       "Exit on momentum reversal or stop-loss at 5% loss",
            "risk_approved":   true,
            "risk_reason":     "All risk checks passed",
            "adjusted_sizing": null,
            "status":          "pending"
        }
    ],
    "equity_history": [
        {"timestamp": "2026-04-23T14:30:00", "equity": 487.20},
        ...
    ],
    "risk_rejections": [
        {
            "market":        "BTC/EUR",
            "direction":     "long",
            "confidence":    0.62,
            "thesis":        "...",
            "reason":        "Position already open for BTC/EUR — one position per pair",
            "trade_idea_id": "...",
            "timestamp":     "2026-04-23T14:31:45"
        }
    ],
    "activity": [
        {
            "timestamp": "2026-04-23T14:32:10Z",
            "level":     "success | info | warn | error",
            "message":   "STOP-LOSS: BTC/EUR closed at €83,100 (-2.7% loss)",
            "detail":    ""
        }
    ],
    "control": {
        "emergency_stop":      false,
        "stop_since":          null,
        "disabled_markets":    [],
        "disabled_strategies": [],
        "selected_strategy":   "llm_only_strategy",
        "live_markets":        []
    },
    "strategies": [
        {"id": "basic_strategy",         "label": "Basic Strategy (Just market indicators, no LLM)", "enabled": false, "selected": false},
        {"id": "basic_and_llm_strategy", "label": "Basic + LLM Combined (...)",                      "enabled": false, "selected": false},
        {"id": "llm_only_strategy",      "label": "All LLM (...)",                                   "enabled": true,  "selected": true}
    ],
    "warmup_ticks_remaining": 0,
    "learning": [
        {
            "strategy": "basic_and_llm_strategy", "market": "BTC/EUR", "direction": "long",
            "trades": 15, "win_rate": 0.60, "rolling_win_rate_10": 0.70,
            "mean_win_pnl": 3.10, "mean_loss_pnl": -2.40, "average_pnl": 0.90, "median_pnl": 1.20,
            "pnl_p25": -1.10, "pnl_p75": 2.80, "pnl_p95": 4.50, "quality_score": 0.63
        }
    ],
    "version": "0.6.0",
    "llm": {
        "available": true,
        "status":    "available | unavailable | not_configured",
        "model":     "qwen2.5-7b-instruct (LM Studio)",
        "backend":   "lmstudio | remote | none",
        "briefing": {
            "key_insight":       "Institutional demand surge may push BTC past resistance this week",
            "overall_sentiment": 0.45,
            "market_outlooks": {
                "BTC/EUR": {"bias": "bullish", "score": 0.7, "note": "ETF inflows accelerating"},
                "ETH/EUR": {"bias": "neutral",  "score": 0.1, "note": "Awaiting BTC direction"}
            },
            "article_count": 4,
            "generated_at":  "2026-04-23T14:32:00"
        },
        "reflection": {
            "pattern":      "Stop-losses trigger frequently on BTC longs within 2 hours of entry",
            "suggestion":   "Consider reducing position size or tightening entry criteria for BTC longs",
            "confidence":   0.72,
            "generated_at": "2026-04-23T14:00:00"
        }
    }
}
```

**Notes:**

- `equity`, `cash`, market `price` and the position numbers are pre-formatted strings, as in the Python API. A market that has not been priced yet shows `"-"`.
- `signals` holds the 12 most recent signals from the strategy loop. This is not a ledger; older entries roll off.
- `equity_history` is limited to the last 1 440 snapshots (4 hours at 10-second resolution, produced by `equityTickerLoop`). The last point is aligned with the current equity.
- `risk_rejections` returns the most recent 20 of the 50 rejections kept in memory.
- `activity` returns the most recent 60 entries.
- Approval `size` is the proposed position size as a percentage of equity.
- `learning` is the learner summary (see `GET /api/learning`).
- `llm.briefing` and `llm.reflection` are `null` if the LLM has not yet produced results.
- In paper mode, `cash` and `equity` come from the paper engine (`internal/paper`). In live mode, both come from Kraken account balances: `cash` is the selected quote-currency balance and `equity` is quote cash plus non-quote balances marked to the latest known prices.
- `open_position_ids` is a list of full UUIDs for all currently open positions. The frontend uses this to display OPEN/CLOSED status in the trade ledger without a separate API call.

---

### `GET /api/control`

Returns only the control state snapshot. Used during startup; the dashboard re-fetches from `/api/dashboard`.

**Response:** Same as `"control"` key in `/api/dashboard`.

---

## Approvals

### `GET /api/approvals`

Returns all currently pending (non-expired) approval requests.

**Response:** Array of approval objects (same shape as `"approvals"` in `/api/dashboard`).

---

### `POST /api/approvals/{approval_id}/approve`

Approves and immediately executes the pending trade. Markets toggled to live route to Kraken; all others go to the paper engine.

**Path parameter:** `approval_id` — UUID of the `ApprovalRequest`.

**Pre-conditions checked by the handler:**

1. Emergency stop is not active (`503`).
2. Approval exists and is not expired (`404`).
3. A live market price is available. Without one, the approval is consumed and logged but not executed (`200` with status `approved_no_price`).

**Response (success):**

```json
{
    "status":       "approved",
    "id":           "7c9e6679-...",
    "order_id":     "a3f1c2d4-...",
    "order_status": "filled"
}
```

**Response (emergency stop active, `503`):**

```json
{"detail": "Emergency stop is active - cannot execute trades"}
```

**Response (not found or expired, `404`):**

```json
{"detail": "Approval not found or expired"}
```

**Response (no price available, `200`):**

```json
{
    "status": "approved_no_price",
    "id":     "7c9e6679-...",
    "detail": "No live price for ETH/EUR; trade logged but not executed"
}
```

---

### `POST /api/approvals/{approval_id}/reject`

Rejects and removes the pending approval. Returns `404` (`"Approval not found"`) if not found.

**Response:**

```json
{"status": "rejected", "id": "7c9e6679-..."}
```

---

## Control Operations

### `POST /api/control/emergency-stop`

Activates the emergency stop. While active:
- Strategy loop skips all signal generation and execution.
- Approval queue is cleared (all pending approvals are discarded).
- Any attempt to approve a trade or close a position returns `503`.

An `emergency_stop_activated` alert is sent to `ALERT_WEBHOOK_URL` when configured.

**Response:**

```json
{"status": "stopped", "timestamp": "2026-04-23T14:40:00+00:00"}
```

---

### `POST /api/control/resume`

Deactivates the emergency stop and resumes normal operation. The strategy loop picks up at the next tick.

**Response:**

```json
{"status": "resumed", "timestamp": "2026-04-23T14:41:00+00:00"}
```

---

### `POST /api/control/markets/{market}/toggle`

Toggles a market on or off. Market symbols contain a slash, so the route is registered as `/api/control/markets/{market...}`: it captures the rest of the path and treats the last segment as the action. Send the symbol URL-encoded (`BTC%2FEUR`) or raw (`BTC/EUR`).

When a market is disabled:
- The strategy loop skips it.
- The OHLC loop stops refreshing its candles.
- Existing open positions for that market are **not** automatically closed.

**Response:**

```json
{"market": "ETH/EUR", "enabled": false}
```

---

### `POST /api/control/markets/{market}/live-toggle`

Toggles whether orders for a market route to Kraken live execution instead of the paper engine. The live engine still refuses orders when the Kraken API keys are not set (see [09-ingestion-adapters.md](09-ingestion-adapters.md)). The toggle is not gated by `TRADING_ENVIRONMENT`. The state persists in the control state (`live_markets`).

**Response:**

```json
{"market": "ETH/EUR", "live": true}
```

---

### `POST /api/control/strategies/{strategy_id}/select`

Selects the single strategy used for future signal evaluation. Current strategy IDs are `"basic_strategy"`, `"basic_and_llm_strategy"`, and `"llm_only_strategy"`; legacy IDs are mapped to their canonical form. Returns `404` (`"Strategy not found"`) for an unknown ID.

**Response:**

```json
{"strategy_id": "llm_only_strategy", "selected": true}
```

### `POST /api/control/strategies/{strategy_id}/toggle`

Legacy alias for `select`; only one strategy is active at a time.

**Response:**

```json
{"strategy_id": "llm_only_strategy", "enabled": true, "selected": true}
```

### `POST /api/control/strategies/reload`

Rebuilds the strategy instances from the current settings.

**Response:**

```json
{"status": "reloaded", "strategies": ["basic_strategy", "basic_and_llm_strategy", "llm_only_strategy"]}
```

---

## Data Retrieval

### `GET /api/learning`

Returns the performance learner's statistics per `(strategy, market, direction)` tuple as an array (`[]` when empty).

**Response:**

```json
[
    {
        "strategy":            "basic_and_llm_strategy",
        "market":              "BTC/EUR",
        "direction":           "long",
        "trades":              15,
        "win_rate":            0.60,
        "rolling_win_rate_10": 0.70,
        "mean_win_pnl":        3.10,
        "mean_loss_pnl":       -2.40,
        "average_pnl":         0.90,
        "median_pnl":          1.20,
        "pnl_p25":             -1.10,
        "pnl_p75":             2.80,
        "pnl_p95":             4.50,
        "quality_score":       0.63
    }
]
```

---

### `GET /api/ohlc/{market}`

Returns cached OHLC candlestick data for the requested market and interval. The backend refreshes both caches via `ohlcLoop` every 120 seconds, one Kraken call per (market, interval) pair, with gaps between calls to stay under Kraken's rate limit. Returns an empty candle list if the cache has not yet been populated.

**Path parameter:** `market` — e.g. `BTC/EUR` (URL-encoded as `BTC%2FEUR`). Captured with `{market...}` so the slash is preserved.

**Query parameter:** `interval` — candle width in minutes. `15` selects the 15-minute cache; anything else, including the default `5`, selects the 5-minute cache. A non-integer value returns `422` with a FastAPI-style validation body.

**Response:**

```json
{
    "symbol":   "BTC/EUR",
    "interval": 15,
    "candles": [
        {"t": 1745409000, "o": 85120.0, "h": 85680.0, "l": 85050.0, "c": 85420.0, "v": 1.234},
        ...
    ]
}
```

`t` is a Unix timestamp in seconds. Candles are ordered oldest-to-newest. 100 candles are requested per cycle (~8 hours at 5-min, ~25 hours at 15-min).

---

### `GET /api/trades`

Returns the trade ledger: visible (filled, pending, or submitted) orders sorted by timestamp descending, limited to 200 rows. Rejected execution intents are excluded and are available from `/api/rejected-trades`.

**Response:** Array of trade objects:

```json
[
    {
        "id":                "a3f1c2d4-...",
        "position_id":       "3fa85f64",
        "position_id_full":  "3fa85f64-5717-4562-b3fc-2c963f66afa6",
        "trade_idea_id":     "7c9e6679-...",
        "strategy":          "basic_and_llm_strategy",
        "market":            "BTC/EUR",
        "direction":         "long",
        "trade_type":        "open | close",
        "size":              0.001140,
        "price":             85505.42,
        "fee":               0.35,
        "value":             97.48,
        "pnl":               null,
        "status":            "filled",
        "environment":       "paper | live",
        "exchange_order_id": "",
        "source":            "auto | manual | stop-loss | system",
        "timestamp":         "2026-04-23T14:32:10"
    }
]
```

**Key fields:**
- `trade_type`: derived from position history. The earliest order for a `position_id` is `"open"`; any later order on that position is `"close"`.
- `strategy`: Strategy ID that generated the linked signal. A close without its own signal inherits the opening order's strategy.
- `pnl`: Non-null on `"close"` rows. Gross P&L before fees (fees are reported separately in `fee`).
- `position_id`: First 8 characters of the position UUID, for display only. Same value on the open and close rows.
- `position_id_full`: Full UUID of the position. Use this to match against `open_position_ids` from `/api/dashboard` to determine OPEN/CLOSED status.
- `source`: Origin of the trade, derived from the order's approval request ID — `"auto"` (fully-automated), `"manual"` (approved by user or closed from the UI), `"stop-loss"` (stop-loss closure), `"system"` (no approval ID).

---

### `GET /api/rejected-trades`

Returns execution-level rejected intents sorted by timestamp descending, limited to 100 rows. These rows never became trades and are intentionally kept out of `/api/trades`.

**Response:** Array of rejected-trade objects:

```json
[
    {
        "id":            12,
        "market":        "BTC/EUR",
        "direction":     "long",
        "size":          0.001140,
        "price":         85505.42,
        "confidence":    0.72,
        "reason":        "insufficient_funds",
        "trade_idea_id": "7c9e6679-...",
        "strategy":      "basic_and_llm_strategy",
        "timestamp":     "2026-04-23T14:32:10"
    }
]
```

**Key fields:**
- `reason`: Execution rejection reason. Paper orders use values such as `"insufficient_funds"`; live orders store the Kraken submission error or local credential failure.
- `strategy`: Strategy ID from the linked rejected signal, when available.
- `confidence`: Signal confidence when the rejected intent was attempted. May be `null` for non-signal execution paths.
- `trade_idea_id`: Optional link back to the signal detail modal.

---

### `GET /api/closed-trades`

Returns signal outcome records for all closed trades, sorted by exit time descending, limited to 200 rows. This is the source of truth for the performance learner and LLM reflection.

**Response:** Array of outcome objects:

```json
[
    {
        "strategy":              "basic_and_llm_strategy",
        "market":                "BTC/EUR",
        "direction":             "long",
        "entry_price":           85420.0,
        "exit_price":            83100.0,
        "size":                  0.00114,
        "pnl":                   -2.64,
        "pnl_pct":               -0.027,
        "confidence":            0.68,
        "exit_reason":           "stop_loss | auto | manual_approve | manual_close | operator_reset",
        "trade_idea_id":         "uuid-of-opening-signal",
        "closing_trade_idea_id": "uuid-of-closing-signal",
        "entry_at":              "2026-04-23T14:32:10",
        "exit_at":               "2026-04-23T15:01:05",
        "position_id":           "3fa85f64",
        "position_id_full":      "3fa85f64-5717-4562-b3fc-2c963f66afa6",
        "indicators":            {"rsi_14": 58.3, "ema_cross": "bullish", "...": "..."}
    }
]
```

- `confidence` — signal confidence at entry time (after LLM adjustment, before the trade was placed).
- `trade_idea_id` — UUID of the trade idea that opened this position. Empty string if not linked.
- `closing_trade_idea_id` — UUID of the SHORT signal that triggered the close (`auto` / `manual_approve` only). Empty string for stop-loss and manual UI closes.
- `indicators` — the indicator snapshot stored with the opening trade idea, or `null` when none is linked.

---

### `GET /api/pnl-summary`

Returns realised P&L totals (rounded to 2 dp) over the most recent 1 000 closed trades, grouped by exit day and by market.

**Response:**

```json
{
    "total_pnl": -14.30,
    "by_day":    {"2026-04-23": -2.64, "2026-04-24": 5.10},
    "by_market": {"BTC/EUR": -8.20, "ETH/EUR": -6.10}
}
```

---

### `GET /api/export/trades.csv`

Downloads up to 10 000 closed trades as `trades.csv` (`Content-Disposition: attachment`). Rows use CRLF line endings and Python-style float formatting.

Columns: `strategy, market, direction, entry_price, exit_price, size, pnl, pnl_pct, confidence, exit_reason, entry_at, exit_at, position_id, trade_idea_id, closing_trade_idea_id`.

---

### `GET /api/signals/{trade_idea_id}`

Returns the full context of one trade idea for the signal detail modal. Returns `404` (`"Signal not found"`) if the ID is unknown.

**Response:**

```json
{
    "id":                   "7c9e6679-...",
    "strategy_id":          "basic_and_llm_strategy",
    "market":               "BTC/EUR",
    "direction":            "long",
    "thesis":               "...",
    "supporting_signals":   {"momentum": 0.0054, "llm_confidence_scale": 1.3},
    "confidence":           0.88,
    "momentum_pct":         0.54,
    "indicators":           {"rsi_14": 34.2, "...": "..."},
    "llm_used":             true,
    "llm_sentiment":        0.72,
    "llm_confidence_scale": 1.3,
    "llm_reasoning":        "Oversold RSI and Stoch with bullish EMA...",
    "news_context":         [{"source": "CoinDesk", "title": "...", "url": "...", "summary": "..."}],
    "risk_approved":        true,
    "risk_reason":          "All risk checks passed",
    "entry_plan":           "...",
    "exit_plan":            "...",
    "stop_or_invalidation": "...",
    "created_at":           "2026-04-23T14:32:10"
}
```

---

### `GET /api/news`

Returns the in-memory news cache: the 60 most recent articles across all sources, newest first. The cache is refreshed every 300 seconds by `newsLoop`.

**Response:** Array of news items:

```json
[
    {
        "id":           "abc123-hash",
        "source":       "CoinDesk",
        "title":        "Bitcoin ETF volumes surge — Institutional demand rising sharply",
        "url":          "https://www.coindesk.com/...",
        "summary":      "First 200 characters of article content...",
        "published_at": "2026-04-23T14:15:00"
    }
]
```

`url` is `null` for items without a link. The Fear & Greed item links to the Alternative.me index page.

---

## Position Management

### `POST /api/positions/{position_id}/close`

Closes a named open paper position at the current market price. In addition to filling the order, the handler:
- Writes a `signal_outcomes` row (`exit_reason = "manual_close"`) for history and LLM learning
- Records the realised P&L in the risk engine and persists the daily counters, so the drawdown tracker reflects the close
- Records realised P&L on the order row
- Records the outcome in the performance learner so it adjusts future confidence
- Saves an equity snapshot immediately

**Path parameter:** `position_id` — either the full UUID (e.g. `3fa85f64-5717-4562-b3fc-2c963f66afa6`) or the 8-character prefix (e.g. `3fa85f64`). The prefix form is accepted for convenience, but the full UUID is preferred to avoid ambiguity.

**Pre-conditions checked by the handler:**

1. Emergency stop is not active (`503`).
2. The position ID matches an existing open position (`404`).
3. A live market price is available for the position's market (`503`).

**Response (success):**

```json
{
    "status":      "closed",
    "position_id": "3fa85f64-5717-4562-b3fc-2c963f66afa6",
    "order_id":    "a1b2c3d4-...",
    "fill_price":  83100.0,
    "pnl":         -23.20,
    "cash":        476.80
}
```

**Response (position not found, `404`):**

```json
{"detail": "Position not found"}
```

**Response (no price available, `503`):**

```json
{"detail": "No live price for BTC/EUR"}
```

---

### `POST /api/positions/reset`

Closes and clears all open paper positions from both the in-memory state and the database. **Paper mode only.** Returns `403` (`"Position reset is not allowed in live mode"`) when `TRADING_ENVIRONMENT=live`.

Before clearing, `paper.ClosePositionsForOperatorReset()` writes a complete audit trail for every open position so no trade disappears from history:

1. Each position is closed through the normal close-order accounting path at the last known market price, falling back to its entry price if no live price is cached.
2. A `signal_outcomes` row is written with `exit_reason = "operator_reset"`.
3. The realised P&L is recorded in the risk engine and the performance learner so in-memory state reflects the forced close.

Then clears:
- All rows from `open_positions` in SQLite
- The paper engine's in-memory positions and position metadata

Finally an equity snapshot is saved.

**Note:** Reset positions appear in the Closed Trades panel with `exit_reason = "operator_reset"` and are included in the LLM reflection data.

**Response:**

```json
{"cleared": 3}
```

`cleared` is the number of positions closed and recorded.

---

## Error Responses

All error responses use the FastAPI-compatible `{"detail": ...}` format. Operations return an `*app.StatusError` carrying the status and detail; any other error becomes a `500` and is logged.

```json
{"detail": "Human-readable error message"}
```

Common HTTP status codes:
- `304` — Dashboard unchanged since the `ETag` sent in `If-None-Match`
- `403` — Position reset called in live mode
- `404` — Resource not found (unknown route, approval, position, strategy, or signal)
- `422` — Invalid `interval` query parameter on `/api/ohlc`
- `500` — Unexpected error (`{"detail": "Internal Server Error"}`, logged to the application log)
- `503` — Emergency stop active, or no live price available for a manual close
