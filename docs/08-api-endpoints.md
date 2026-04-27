# API Endpoints

All routes are mounted on a FastAPI application in `backend/main.py`. The server binds to `settings.host` (default `127.0.0.1`) on `settings.port` (default `8000`). The frontend is served from the same process — no separate web server is required.

---

## Static Pages

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/` | Serves `frontend/index.html` — the main dashboard |
| `GET` | `/approvals` | Serves the approval management page |

---

## Health

### `GET /health`

Returns a basic liveness check. Does not probe Ollama or Kraken connectivity.

**Response:**

```json
{
    "status": "ok",
    "version": "0.1.0"
}
```

---

## Dashboard

### `GET /api/dashboard`

Returns the full dashboard state in a single payload. Polled every 5 seconds by the frontend.

**Response shape:**

```json
{
    "mode": "manual | semi_automated | fully_automated",
    "environment": "paper | live",
    "equity": 487.20,
    "cash": 312.50,
    "open_position_ids": ["3fa85f64-5717-4562-b3fc-2c963f66afa6"],
    "markets": [
        {"symbol": "BTC/EUR", "price": 85420.0, "enabled": true},
        {"symbol": "ETH/EUR", "price": 1842.0,  "enabled": true}
    ],
    "signals": [
        {
            "strategy":     "llm_only_strategy",
            "market":       "BTC/EUR",
            "direction":    "long",
            "confidence":   0.72,
            "thesis":       "Price momentum: 0.82%, news sentiment: 0.00 · LLM: Strong ETF inflows support long bias",
            "risk_approved": true,
            "risk_reason":  "All risk checks passed"
        }
    ],
    "positions": [
        {
            "position_id":   "3fa85f64-...",
            "market":        "BTC/EUR",
            "direction":     "long",
            "size":          0.001140,
            "avg_price":     85420.0,
            "unrealized_pnl": 4.20
        }
    ],
    "approvals": [
        {
            "id":          "7c9e6679-...",
            "market":      "ETH/EUR",
            "direction":   "long",
            "strategy_id": "llm_only_strategy",
            "confidence":  0.65,
            "size":        0.05265,
            "expires_at":  "2026-04-23T15:02:00",
            "thesis":      "...",
            "entry_plan":  "Enter at market when momentum confirms trend",
            "exit_plan":   "Exit on momentum reversal or stop-loss at 5% loss",
            "risk_approved": true,
            "risk_reason": "All risk checks passed",
            "adjusted_sizing": null,
            "status":      "pending"
        }
    ],
    "equity_history": [
        {"timestamp": "2026-04-23T14:30:00", "equity": 487.20},
        ...
    ],
    "risk_rejections": [
        {
            "market":     "BTC/EUR",
            "direction":  "long",
            "confidence": 0.62,
            "thesis":     "...",
            "reason":     "Position already open for BTC/EUR — one position per pair",
            "timestamp":  "2026-04-23T14:31:45"
        }
    ],
    "activity": [
        {
            "timestamp": "2026-04-23T14:32:10",
            "level":     "success | info | warn | error",
            "message":   "STOP-LOSS: BTC/EUR closed at £83,100 (-2.7% loss)",
            "detail":    ""
        }
    ],
    "control": {
        "emergency_stop":     false,
        "stop_since":         null,
        "disabled_markets":   [],
        "disabled_strategies": [],
        "selected_strategy": "llm_only_strategy"
    },
    "strategies": [
        {"id": "basic_strategy", "label": "Indicator only", "enabled": false, "selected": false},
        {"id": "llm_only_strategy", "label": "LLM Only strategy", "enabled": true, "selected": true},
        {"id": "llm", "label": "LLM", "enabled": false, "selected": false}
    ],
    "learning": {
        "combined:BTC/EUR:long": {
            "count":            15,
            "win_rate":         0.60,
            "weighted_win_rate": 0.63,
            "scale":            1.13
        }
    },
    "llm": {
        "available": true,
        "model": "phi3:mini",
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
            "pattern":    "Stop-losses trigger frequently on BTC longs within 2 hours of entry",
            "suggestion": "Consider reducing position size or tightening entry criteria for BTC longs",
            "confidence": 0.72,
            "generated_at": "2026-04-23T14:00:00"
        }
    }
}
```

**Notes:**

- `signals` contains the most recent signal per market from the last strategy tick. This is not a ledger — it is overwritten each tick.
- `equity_history` is limited to the last 1 440 snapshots (4 hours at 10-second resolution, produced by `_equity_ticker_loop`).
- `activity` returns the most recent 60 entries from the 200-entry rolling buffer.
- `llm.briefing` and `llm.reflection` are `null` if the LLM has not yet produced results.
- `cash` is the current available paper cash balance (distinct from total equity, which includes unrealised P&L on open positions).
- `open_position_ids` is a list of full UUIDs for all currently open positions. The frontend uses this to display OPEN/CLOSED status in the trade ledger without a separate API call.

---

### `GET /api/control`

Returns only the control state snapshot. Used during startup; the dashboard re-fetches from `/api/dashboard`.

**Response:** Same as `"control"` key in `/api/dashboard`.

---

## Approvals

### `GET /api/approvals`

Returns all currently pending (non-expired) approval requests. Expired requests are purged on read.

**Response:** Array of approval objects (same shape as `"approvals"` in `/api/dashboard`).

---

### `POST /api/approvals/{approval_id}/approve`

Approves and immediately executes the pending trade. Returns `404` if the approval does not exist or has expired.

**Path parameter:** `approval_id` — UUID of the `ApprovalRequest`.

**Pre-conditions checked by the handler:**

1. Emergency stop is not active.
2. Approval exists and is not expired.
3. A live market price is available.

**Response (success):**

```json
{
    "status":       "executed",
    "id":           "7c9e6679-...",
    "order_id":     "a3f1c2d4-...",
    "order_status": "filled"
}
```

**Response (emergency stop active):**

```json
{"detail": "Emergency stop is active"}
```

**Response (no price available):**

```json
{"detail": "No live price available for ETH/EUR"}
```

---

### `POST /api/approvals/{approval_id}/reject`

Rejects and removes the pending approval. Returns `404` if not found.

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
- Any attempt to approve a trade returns an error.

**Response:**

```json
{"status": "stopped", "timestamp": "2026-04-23T14:40:00"}
```

---

### `POST /api/control/resume`

Deactivates the emergency stop and resumes normal operation. The strategy loop picks up at the next tick.

**Response:**

```json
{"status": "resumed", "timestamp": "2026-04-23T14:41:00"}
```

---

### `POST /api/control/markets/{market:path}/toggle`

Toggles a market on or off. The `{market:path}` parameter captures the full path segment, allowing market symbols that contain `/` (e.g., `BTC/EUR`).

When a market is disabled:
- It is removed from `_active_markets` in the strategy loop.
- Its prices are no longer fetched or cached.
- Existing open positions for that market are **not** automatically closed.

**Response:**

```json
{"market": "ETH/EUR", "enabled": false}
```

---

### `POST /api/control/strategies/{strategy_id}/select`

Selects the single strategy used for future signal evaluation. Current strategy IDs are `"basic_strategy"`, `"basic_and_llm_strategy"`, and `"llm_only_strategy"`.

**Response:**

```json
{"strategy_id": "llm", "selected": true}
```

---

## Data Retrieval

### `GET /api/learning`

Returns the performance learner's win-rate statistics per `(strategy, market, direction)` tuple.

**Response:**

```json
{
    "combined:BTC/EUR:long": {
        "count":            15,
        "win_rate":         0.60,
        "weighted_win_rate": 0.63,
        "scale":            1.13
    },
    "combined:ETH/EUR:long": {
        "count":            8,
        "win_rate":         0.375,
        "weighted_win_rate": 0.41,
        "scale":            0.91
    }
}
```

`scale` is the multiplier that would be applied to future signals for that combination. Scale < 1.0 means the learner is reducing confidence; > 1.0 means it is boosting it.

---

### `GET /api/ohlc/{market:path}`

Returns cached OHLC candlestick data for the requested market and interval. The backend refreshes both caches via `_ohlc_loop` every 120 seconds — one Kraken call per (market, interval) pair, with a small gap between calls to avoid rate limits. Returns an empty candle list if the cache has not yet been populated.

**Path parameter:** `market` — e.g. `BTC/EUR` (URL-encoded as `BTC%2FEUR`).

**Query parameter:** `interval` — candle width in minutes. `5` (default) or `15`.

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

Returns the trade ledger - filled paper orders and accepted live orders sorted by timestamp descending, limited to 200 rows. Rejected execution intents are excluded and are available from `/api/rejected-trades`.

**Response:** Array of trade objects:

```json
[
    {
        "timestamp":        "2026-04-23T14:32:10",
        "market":           "BTC/EUR",
        "strategy":         "combined",
        "direction":        "long",
        "trade_type":       "open | close",
        "size":             0.001140,
        "price":            85505.42,
        "value":            97.48,
        "pnl":              null,
        "source":           "auto | manual | stop_loss",
        "status":           "filled",
        "position_id":      "3fa85f64",
        "position_id_full": "3fa85f64-5717-4562-b3fc-2c963f66afa6"
    },
    {
        "timestamp":        "2026-04-23T15:01:05",
        "market":           "BTC/EUR",
        "strategy":         "combined",
        "direction":        "short",
        "trade_type":       "close",
        "size":             0.001140,
        "price":            83100.00,
        "value":            94.73,
        "pnl":              -2.68,
        "source":           "stop_loss",
        "status":           "filled",
        "position_id":      "3fa85f64",
        "position_id_full": "3fa85f64-5717-4562-b3fc-2c963f66afa6"
    }
]
```

**Key fields:**
- `trade_type`: `"open"` for LONG orders (opening a position), `"close"` for SHORT orders (closing).
- `strategy`: Strategy ID that generated the linked signal. Stop-loss/manual close rows fall back to the opening signal's strategy when the close row has no direct signal.
- `pnl`: Non-null on `"close"` rows. Gross P&L before fees (fees captured separately in order_records).
- `position_id`: First 8 characters of the position UUID — for display only. Same value on the open and close rows.
- `position_id_full`: Full UUID of the position. Use this to match against `open_position_ids` from `/api/dashboard` to determine OPEN/CLOSED status.
- `source`: Origin of the trade — `"auto"` (fully-automated), `"manual"` (approved by user), `"stop_loss"` (stop-loss closure).

---

### `GET /api/rejected-trades`

Returns execution-level rejected intents sorted by timestamp descending, limited to 100 rows. These rows never became trades and are intentionally kept out of `/api/trades`.

**Response:** Array of rejected-trade objects:

```json
[
    {
        "id":              12,
        "timestamp":       "2026-04-23T14:32:10",
        "market":          "BTC/EUR",
        "strategy":        "combined",
        "direction":       "long",
        "size":            0.001140,
        "price":           85505.42,
        "confidence":      0.72,
        "reason":          "insufficient_funds",
        "trade_idea_id":   "7c9e6679-..."
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
        "market":                  "BTC/EUR",
        "direction":               "long",
        "entry_price":             85420.0,
        "exit_price":              83100.0,
        "pnl":                     -2.64,
        "pnl_pct":                 -0.027,
        "exit_reason":             "stop_loss | auto | manual_approve",
        "entry_at":                "2026-04-23T14:32:10",
        "exit_at":                 "2026-04-23T15:01:05",
        "confidence":              0.68,
        "trade_idea_id":           "uuid-of-opening-signal",
        "closing_trade_idea_id":   "uuid-of-closing-signal"
    }
]
```

- `confidence` — signal confidence at entry time (after LLM adjustment, before the trade was placed).
- `trade_idea_id` — UUID of the `TradeIdeaModel` that opened this position. Empty string if not linked.
- `closing_trade_idea_id` — UUID of the SHORT signal that triggered the close (`auto` / `manual_approve` only). Empty string for stop-loss and manual UI closes.

---

### `GET /api/news`

Returns all news articles published in the last 12 hours from the in-memory cache. Cache is refreshed every 300 seconds by `_news_loop`. No item cap — all articles within the window are returned.

**Response:** Array of news items:

```json
[
    {
        "id":           "abc123-hash",
        "source":       "CoinDesk | CoinTelegraph",
        "title":        "Bitcoin ETF volumes surge — Institutional demand rising sharply",
        "url":          "https://www.coindesk.com/...",
        "summary":      "First 200 characters of article content...",
        "published_at": "2026-04-23T14:15:00"
    }
]
```

---

## Position Management

### `POST /api/positions/{position_id}/close`

Closes a named open position at the current market price. In addition to filling the order, the handler:
- Writes a `signal_outcomes` row (`exit_reason = "manual_close"`) for history and LLM learning
- Calls `risk_engine.record_trade_result(pnl)` so the drawdown tracker reflects the close
- Calls `repo.update_order_pnl(order_id, pnl)` to record realised P&L on the order row
- Calls `learner.record_outcome(...)` so the performance learner adjusts future confidence
- Saves an equity snapshot immediately

**Path parameter:** `position_id` — either the full UUID (e.g. `3fa85f64-5717-4562-b3fc-2c963f66afa6`) or the 8-character prefix (e.g. `3fa85f64`). The 8-char prefix form is accepted for convenience but the full UUID is preferred to avoid ambiguity.

**Pre-conditions checked by the handler:**

1. The position ID matches an existing open position.
2. A live market price is available for the position's market.

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

**Response (position not found):**

```json
{"detail": "Position 3fa85f64 not found"}
```

**Response (no price available):**

```json
{"detail": "No live price available for BTC/EUR"}
```

---

### `POST /api/positions/reset`

Clears all open positions from both the in-memory state and the database. **Paper mode only.** Returns a `403` error if called in live mode.

Before clearing, the handler writes a complete audit trail for every open position so no trade disappears from history:

1. For each open position, sets `avg_price_at_close` and `size_at_close` in `_position_meta`.
2. Calls `paper_engine.record_closed_trade(position_id, exit_price, "operator_reset")` — writes a `signal_outcomes` row. Exit price is the last known market price, falling back to `avg_price` if no live price is cached.
3. Calls `risk_engine.record_trade_result(pnl)` and `learner.record_outcome(...)` so in-memory state reflects the forced close.

Then clears:
- All rows from `open_positions` in SQLite
- `paper_engine.positions` dict
- `paper_engine._position_meta` dict

**Note:** Does not affect cash balance, order history, or the equity ledger. Reset positions appear in the Closed Trades panel with `exit_reason = "operator_reset"` and are included in the LLM reflection data.

**Response:**

```json
{"cleared": 3}
```

`cleared` is the number of position rows deleted from the database.

---

## Error Responses

All error responses use FastAPI's standard `HTTPException` format:

```json
{"detail": "Human-readable error message"}
```

Common HTTP status codes:
- `400` — Bad request (e.g., reset called in live mode)
- `404` — Resource not found (approval ID doesn't exist or expired)
- `500` — Unhandled exception (logged to activity feed and application log)
