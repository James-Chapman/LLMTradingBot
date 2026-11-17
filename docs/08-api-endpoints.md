# API Endpoints

All routes are registered on a standard-library `http.ServeMux` in `internal/server/server.go` (`routes()`). Requests pass, in order, the Host check, the CORS middleware (origins in `CORS_ORIGINS`) and, when the bot listens on a non-loopback address, the access-token check. Handlers delegate to the `App` in `internal/app`. Paths, methods, status codes and JSON keys match the original FastAPI app, so the frontend works unchanged.

**Host check.** Every request whose `Host` header names a host the bot is not reached by gets `403 {"detail": "Host not allowed. ..."}`, which stops DNS rebinding. Bound to a loopback address, only `localhost`, `127.0.0.1` and `[::1]` (any port) pass; bound to any other address, that address, this computer's name, any IP address and the names in `ALLOWED_HOSTS` pass too (see [01-configuration.md](01-configuration.md#access-from-other-computers)).

**Access token.** Only when `-host` is not a loopback address. Every route except `GET /health`, `GET /login`, `GET /static/styles.css` and `POST /api/login` needs the token, sent as `Authorization: Bearer <token>` or in the `tradingbot_session` cookie set by `POST /api/login`. Without it, pages (`GET`/`HEAD` outside `/api/`) redirect with `303` to `/login`, and everything else gets `401 {"detail": "Access token required: ..."}` with `WWW-Authenticate: Bearer`. A wrong bearer token waits 1 second before the `401`. Loopback-bound instances need no token.

**Cross-site requests.** CORS headers only stop a foreign page *reading* a reply; a browser still delivers a plain cross-site `POST`. The middleware therefore refuses every state-changing request (any method other than `GET`, `HEAD`, `OPTIONS`) whose `Origin` header is neither in `CORS_ORIGINS` nor the host the request was sent to, with `403 {"detail": "Cross-origin request refused"}`. Requests without an `Origin` header (the CLI, scripts, `curl`) and the dashboard's own requests are unaffected. On its own this is not authentication; the access token above is what protects a bot reachable from other computers.

`cmd/tradingbot` binds the server to `-host` (default `127.0.0.1`) on `-port` (default `8000`). The frontend is served from the same process; no separate web server is required. The `-frontend` flag sets the directory the pages are served from (default `frontend`). The server's timeouts are: read header 10 s, read 30 s, write 2 min (room for an approval that places a live Kraken order), idle 2 min. No response streams.

**Request bodies.** Endpoints that take JSON read at most their limit (1 KB for trading mode, environment and login; 64 KB for settings). A larger body gets `413 {"detail": "Request body is larger than N bytes"}`; a missing or malformed body, or data after the JSON value, gets `400 {"detail": "Request body must be one JSON value"}`. Nothing changes in either case.

---

## Static Pages

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/` | Serves `frontend/index.html` — the main dashboard |
| `GET` | `/approvals` | Serves `frontend/approvals.html` — the approval management page |
| `GET` | `/login` | Serves `frontend/login.html` — the access-token form. Only used when the bot listens on a non-loopback address; otherwise it redirects to `/` |
| `GET` | `/sw.js` | Serves the service worker from the root scope |
| `GET` | `/static/...` | Serves files under `frontend/static/` (no directory listings) |

HTML pages and `sw.js` are sent with `Cache-Control: no-store, max-age=0` so frontend fixes apply immediately. The dashboard's own code (`/static/app/*.js`, `/static/styles.css`) is sent with `Cache-Control: no-cache`: the browser checks with the bot on every load (an unchanged file answers 304), so a cached script never runs against newer HTML. Other static files have no `Cache-Control`.

---

## Access

Only used when the bot listens on a non-loopback address. On a loopback-bound bot both endpoints return `404`.

### `POST /api/login`

**Request body** (1 KB limit): `{"token": "<access token>"}`

With the right token: `200 {"status": "ok"}` and the session cookie `tradingbot_session` (the token itself; HttpOnly, SameSite=Strict, Path=/, Secure over HTTPS, ends when the browser closes). With a wrong token: `401 {"detail": "Wrong access token"}` after a 1-second delay, and a `Failed dashboard login` warning in the log.

### `POST /api/access-token/rotate`

Needs the current token. Generates a new token, stores it sealed, and returns `200 {"token": "<new token>"}` with a new session cookie. The old token and every session using it stop working at once; scripts must use the new token.

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
        {"symbol": "BTC/EUR",  "price": "85420.00",    "price_value": 85420,     "enabled": true, "live": false},
        {"symbol": "PEPE/EUR", "price": "0.000008500", "price_value": 0.0000085, "enabled": true, "live": false},
        {"symbol": "ETH/EUR",  "price": "-",           "price_value": null,      "enabled": true, "live": false}
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
            "avg_price_value":  85420,
            "unrealized_pnl":   "4.20",
            "strategy":         "llm_only_strategy",
            "source":           "",
            "status":           "open",
            "environment":      "paper",
            "trade_idea_id":    "7c9e6679-..."
        },
        {
            "position_id":      "9b2e41d0",
            "position_id_full": "9b2e41d0-1c7a-4f0e-9a51-7d3c2b6e8f10",
            "market":           "ETH/EUR",
            "direction":        "long",
            "opened_at":        "2026-04-23T14:35:02",
            "size":             "0.030000",
            "avg_price":        "3012.40",
            "avg_price_value":  3012.4,
            "unrealized_pnl":   "0.00",
            "strategy":         "basic_strategy",
            "source":           "",
            "status":           "entry_pending",
            "environment":      "live",
            "trade_idea_id":    "5d1c0e7a-..."
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
        "live_markets":        [],
        "trading_mode":        "manual",
        "trading_environment": "paper"
    },
    "strategies": [
        {"id": "basic_strategy_1h",         "label": "Basic 1h (market indicators on 1-hour candles, no LLM)", "enabled": false, "selected": false},
        {"id": "basic_and_llm_strategy_1h", "label": "Basic + LLM 1h (...)",                                   "enabled": false, "selected": true},
        {"id": "llm_only_strategy",      "label": "All LLM (...)",                                   "enabled": true,  "selected": true},
        {"id": "trend_strategy",         "label": "Trend Strategy (4-hour EMA trend following, no LLM)", "enabled": false, "selected": false},
        {"id": "trend_and_llm_strategy", "label": "Trend + LLM (4-hour EMA trend; the LLM reviews each entry)", "enabled": false, "selected": false},
        {"id": "donchian_strategy", "label": "Donchian Breakout (4-hour 90/30 channel breakout, no LLM)", "enabled": false, "selected": false},
        {"id": "donchian_and_llm_strategy", "label": "Donchian Breakout + LLM (the LLM reviews each breakout)", "enabled": false, "selected": false},
        {"id": "rsi_dip_strategy", "label": "RSI Dip (buys 4-hour capitulation dips, sells the bounce, no LLM)", "enabled": false, "selected": false},
        {"id": "rsi_dip_and_llm_strategy", "label": "RSI Dip + LLM (the LLM reviews each dip)", "enabled": false, "selected": false},
        {"id": "macd_strategy", "label": "MACD Momentum (slow 4-hour MACD 36/78/27, no LLM)", "enabled": false, "selected": false},
        {"id": "macd_and_llm_strategy", "label": "MACD Momentum + LLM (the LLM reviews each entry)", "enabled": false, "selected": false},
        {"id": "squeeze_strategy", "label": "Bollinger Squeeze (4-hour squeeze breakout, no LLM)", "enabled": false, "selected": false},
        {"id": "squeeze_and_llm_strategy", "label": "Bollinger Squeeze + LLM (the LLM reviews each breakout)", "enabled": false, "selected": false},
        {"id": "rotation_strategy", "label": "Relative Strength Rotation (holds the 3 strongest markets by 14-day return, no LLM)", "enabled": false, "selected": false},
        {"id": "rotation_and_llm_strategy", "label": "Relative Strength Rotation + LLM (the LLM reviews each entry)", "enabled": false, "selected": false}
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
- Money amounts (`equity`, `cash`, `unrealized_pnl`) have 2 decimals. Prices (`price`, `avg_price`) have 2 decimals at or above 1 and 4 significant digits below 1, so sub-cent coins are not shown as `0.00`.
- `markets[].price_value` (a number, `null` while unpriced) and `positions[].avg_price_value` carry the raw prices; the dashboard formats prices from these.
- `markets[].live` is `true` when the market is toggled live (`control.live_markets`).
- `signals` holds the 12 most recent signals from the strategy loop. This is not a ledger; older entries roll off.
- `equity_history` is limited to the last 1 440 snapshots (4 hours at 10-second resolution, produced by `equityTickerLoop`). The last point is aligned with the current equity.
- `risk_rejections` returns the most recent 20 of the 50 rejections kept in memory.
- `activity` returns the most recent 60 entries.
- Approval `size` is the proposed position size as a percentage of equity.
- `learning` is the learner summary (see `GET /api/learning`).
- `llm.briefing` and `llm.reflection` are `null` if the LLM has not yet produced results.
- In paper mode, `cash` and `equity` come from the paper engine (`internal/paper`). In live mode, both come from Kraken account balances: `cash` is the selected quote-currency balance and `equity` is quote cash plus non-quote balances marked to the latest known prices.
- `positions` lists the open paper positions followed by the live positions (positions opened by real Kraken orders, tracked in `live_positions`). `environment` is `"paper"` or `"live"`. `status` is `"open"` for paper positions; a live position is `"entry_pending"` while its buy order is open on Kraken (its `size` is then the requested size and `unrealized_pnl` is `0.00`), `"open"` once the buy has filled (`size`, `avg_price` and the fee are the executed values), and `"exit_pending"` while its sell order is open. Live positions are always spot longs. Live positions stay listed in the paper environment, because they are still held on Kraken.
- `open_position_ids` is a list of full UUIDs for all currently open positions, paper and live (including live positions whose entry or exit is pending). The frontend uses this to display OPEN/CLOSED status in the trade ledger without a separate API call.

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

Approves and immediately executes the pending trade. In the live environment, markets toggled live route to Kraken; all others go to the paper engine.

**Path parameter:** `approval_id` — UUID of the `ApprovalRequest`.

**Pre-conditions checked by the handler:**

1. Emergency stop is not active (`503`; the approval stays pending).
2. Approval exists and is not expired (`404`).
3. A live market price is available. Without one nothing is executed and the approval **stays pending**, so it can be approved again once a price arrives (`503`).
4. On a live market, a short can only sell a filled live long that has no sell pending (`409`; the approval is removed when there is no live position at all, because a spot account cannot open a short).
5. The risk checks pass again with the current state (open positions of the route, cash, daily loss, price). If they no longer pass, the approval is removed and nothing is executed (`409`, the `detail` names the reason). The order is sized from this fresh decision.

**Response (success):**

```json
{
    "status":       "approved",
    "id":           "7c9e6679-...",
    "order_id":     "a3f1c2d4-...",
    "order_status": "filled",
    "environment":  "paper"
}
```

`environment` is `"live"` when the order went to Kraken. A live order's `order_status` is usually `"pending"`: the position appears in the dashboard as `entry_pending` (or `exit_pending` for a sell) until reconciliation sees the fill. `order_status` `"rejected"` means the order was refused (for example insufficient funds, or Kraken rejected it); the approval is still consumed.

**Response (emergency stop active, `503`):**

```json
{"detail": "Emergency stop is active - cannot execute trades"}
```

**Response (not found or expired, `404`):**

```json
{"detail": "Approval not found or expired"}
```

**Response (no price available, `503`):**

```json
{"detail": "No live price for ETH/EUR - the approval is still pending; try again when a price is available"}
```

**Response (refused by the risk re-check, `409`):**

```json
{"detail": "Risk check failed at approval time: Daily loss limit reached. The approval was removed."}
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

Only markets the bot knows can be toggled: an active market, a configured fixed market, or one that already has a saved toggle. Any other symbol gets `404 {"detail": "Market not found"}` and nothing is saved. The same applies to `live-toggle`.

When a market is disabled:
- The strategy loop skips it.
- The OHLC loop stops refreshing its candles.
- Existing open positions for that market are **not** automatically closed.

**Response:**

```json
{"market": "ETH/EUR", "enabled": false}
```

---

### `POST /api/control/trading-mode`

Changes the trading mode. The choice is saved in `control_state` and survives restarts (default `manual`). The dashboard asks for confirmation before calling it.

**Request body:**

```json
{"mode": "semi_automated"}
```

`mode` must be `manual`, `semi_automated` or `fully_automated`.

**Response:** `{"trading_mode": "semi_automated"}`

**Errors:** `400` for a malformed body or an unknown mode; nothing changes.

---

### `POST /api/control/trading-environment`

Switches between paper and live (real money). The choice is saved in `control_state` and survives restarts (default `paper`).

**Request body:**

```json
{"environment": "live", "confirmation": "LIVE"}
```

- Switching to `live` requires `"confirmation": "LIVE"` (the text the dashboard makes you type) **and** Kraken API keys in use. Keys saved in Settings but not yet loaded are refused with *Kraken API keys were saved but are not in use yet: restart the bot, then switch to live*; no keys gives *Kraken API keys are not set: add them under Settings → Kraken first*.
- Switching to `paper` needs no confirmation field; the dashboard still shows a confirm dialog. It is refused while any live position is open (entry or exit pending counts): close the live positions first, so the switch never leaves a real holding behind a paper dashboard.

**Response:** `{"trading_environment": "live"}`

**Errors:** `400` for a malformed body, an unknown environment, a missing or wrong confirmation, or missing Kraken API keys. `409` when switching to paper with live positions open: `{"detail": "Cannot switch to paper while 1 live position(s) are open: BTC/EUR [3fa85f64] open. Close them first, or use Forget for any sold on Kraken."}`. The `detail` field explains which; nothing changes.

Going live only affects markets toggled live (see below); all others keep trading on paper.

---

### `POST /api/control/markets/{market}/live-toggle`

Toggles whether orders for a market route to Kraken live execution instead of the paper engine. The live engine still refuses orders when the Kraken API keys are not set (see [09-ingestion-adapters.md](09-ingestion-adapters.md)). The flag only sends real orders while the trading environment is `live`; in the paper environment the order executes on paper and a warning is logged. The state persists in the control state (`live_markets`). In the live environment the dashboard's Markets panel has a confirmed **Live / Paper** button per market for this toggle.

**Response:**

```json
{"market": "ETH/EUR", "live": true}
```

**Errors:** `409` when turning the flag **off** for a market that holds a live position (any status); the `detail` names the position. Nothing changes. Close the position first, or forget it if it was already sold on Kraken.

---

### `POST /api/control/strategies/{strategy_id}/select`

Selects the single strategy used for future signal evaluation. Current strategy IDs are `"basic_strategy_1h"`, `"basic_strategy_long_1h"`, `"basic_strategy_4h"`, `"basic_strategy_long_4h"`, `"basic_and_llm_strategy_1h"`, `"basic_and_llm_strategy_long_1h"`, `"basic_and_llm_strategy_4h"`, `"basic_and_llm_strategy_long_4h"`, `"llm_only_strategy"`, `"trend_strategy"`, `"trend_and_llm_strategy"`, `"donchian_strategy"`, `"donchian_and_llm_strategy"`, `"rsi_dip_strategy"`, `"rsi_dip_and_llm_strategy"`, `"macd_strategy"`, `"macd_and_llm_strategy"`, `"squeeze_strategy"`, `"squeeze_and_llm_strategy"`, `"rotation_strategy"` and `"rotation_and_llm_strategy"`; legacy IDs are mapped to their canonical form. Returns `404` (`"Strategy not found"`) for an unknown ID.

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
{"status": "reloaded", "strategies": ["basic_strategy_1h", "basic_strategy_long_1h", "basic_strategy_4h", "basic_strategy_long_4h", "basic_and_llm_strategy_1h", "basic_and_llm_strategy_long_1h", "basic_and_llm_strategy_4h", "basic_and_llm_strategy_long_4h", "llm_only_strategy", "trend_strategy", "trend_and_llm_strategy", "donchian_strategy", "donchian_and_llm_strategy", "rsi_dip_strategy", "rsi_dip_and_llm_strategy", "macd_strategy", "macd_and_llm_strategy", "squeeze_strategy", "squeeze_and_llm_strategy", "rotation_strategy", "rotation_and_llm_strategy"]}
```

---

## Settings

Dashboard-managed settings (everything except the start-up flags and the trading mode / environment). See [01-configuration.md](01-configuration.md).

### `GET /api/settings`

Lists every setting in display order. Secret values are never sent.

**Response:**

```json
{
  "fields": [
    {
      "key": "MIN_SIGNAL_CONFIDENCE",
      "label": "Minimum signal confidence",
      "group": "Signals",
      "help": "Fully automated mode only trades signals at or above this (0–1).",
      "kind": "float",
      "live": true,
      "secret": false,
      "value": "0.65",
      "is_set": true,
      "restart_pending": false
    },
    {
      "key": "KRAKEN_API_KEY",
      "label": "Kraken API key",
      "group": "Kraken",
      "help": "Needed for live trading. Stored encrypted.",
      "kind": "string",
      "live": false,
      "secret": true,
      "value": "",
      "is_set": true,
      "restart_pending": true
    }
  ],
  "restart_required": ["KRAKEN_API_KEY"],
  "news_feeds": [
    {"name": "CoinDesk", "url": "https://www.coindesk.com/arc/outboundfeeds/rss/", "category": "Crypto news", "kind": "feed"},
    {"name": "Fear & Greed", "url": "https://alternative.me/crypto/fear-and-greed-index/", "category": "Market sentiment", "kind": "fear_greed"}
  ]
}
```

| Field | Description |
|---|---|
| `kind` | `string`, `int`, `float`, `bool` or `list`. Lists are JSON-array text (`["BTC/EUR","ETH/EUR"]`) |
| `live` | `true` when a change applies immediately; otherwise after a restart |
| `value` | Saved value as text. Always `""` for secrets |
| `is_set` | Whether a value is saved (the only thing reported for secrets) |
| `restart_pending` | Saved value differs from the one the running bot uses |
| `restart_required` | Every key with `restart_pending` |
| `news_feeds` | The built-in news sources (`config.BuiltInNewsFeeds()`), which the dashboard lists with a switch each. `kind` is `feed` (RSS/Atom) or `fear_greed`. Switched-off names are the `DISABLED_NEWS_SOURCES` setting |

### `POST /api/settings`

Saves changed settings. The dashboard lists the changes in a confirm dialog before calling it.

**Request body** (64 KB limit):

```json
{"changes": {"MIN_SIGNAL_CONFIDENCE": "0.7", "FIXED_MARKETS": "[\"BTC/EUR\",\"SOL/EUR\"]", "KRAKEN_API_KEY": "…"}}
```

Values are text in the same form `GET` returns. An empty value clears a secret.

**Response:**

```json
{"applied_live": ["MIN_SIGNAL_CONFIDENCE"], "restart_required": ["FIXED_MARKETS", "KRAKEN_API_KEY"]}
```

`applied_live` lists the changed keys already in use; `restart_required` lists every key waiting for a restart.

**Errors:** `400` for malformed JSON, a missing or empty `changes` map, an unknown key (including start-up-only keys such as `PORT`), or an invalid value. The `detail` names the key, e.g. `MIN_SIGNAL_CONFIDENCE: "high" is not a number`. Every value is validated first, so a refused request saves nothing.

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

### `GET /api/shadow-reviews`

The LLM shadow reviews (`docs/06-strategy-and-learning.md`, *LLM Shadow Reviews*): whether they are on, and each reviewed strategy's verdicts scored against its closed trades. Ratios are `null` until there is something to divide. `recent` is the newest 20 reviews with the trades entered under them (`pnl` is `null` while none has closed). `500` with a `detail` when the database cannot be read.

**Response:**

```json
{
    "enabled": true,
    "veto_threshold": 0.7,
    "min_scored_vetoes": 10,
    "strategies": [
        {
            "strategy_id":     "rotation_strategy",
            "reviewed":        120,
            "llm_unavailable": 0,
            "would_veto":      30,
            "scored_trades":   90,
            "losing_trades":   70,
            "scored_vetoes":   22,
            "vetoed_losers":   19,
            "veto_accuracy":   0.864,
            "loser_rate":      0.778,
            "break_even":      0.73,
            "vetoed_pnl":      -41.0,
            "kept_pnl":        -80.0,
            "verdict":         "ahead"
        }
    ],
    "recent": [
        {
            "strategy_id":      "rotation_strategy",
            "market":           "ZEC/EUR",
            "candle":           "2026-11-16T08:00:00Z",
            "reviewed_at":      "2026-11-16T12:00:40Z",
            "trade_idea_id":    "4f6c…",
            "base_confidence":  0.95,
            "llm_used":         true,
            "confidence_scale": 0.55,
            "sentiment":        -0.4,
            "reasoning":        "Rally driven by a one-off listing rumour",
            "would_veto":       true,
            "veto_threshold":   0.7,
            "trades":           1,
            "pnl":              -4.2
        }
    ]
}
```

- `candle` is the start of the last closed 4-hour candle the entry was decided on.
- `verdict` is `too_few` (fewer than `min_scored_vetoes` vetoed trades closed), `ahead` (the vetoed trades lost money in total) or `behind`.

---

### `GET /api/ohlc/{market}`

Returns cached OHLC candlestick data for the requested market and interval. The backend refreshes both caches via `ohlcLoop` every 120 seconds (and just after each hour boundary), one Kraken call per (market, interval) pair, with gaps between calls to stay under Kraken's rate limit. Returns an empty candle list if the cache has not yet been populated.

**Path parameter:** `market` — e.g. `BTC/EUR` (URL-encoded as `BTC%2FEUR`). Captured with `{market...}` so the slash is preserved.

**Query parameter:** `interval` — candle width in minutes. `15`, `60` and `240` select the 15-minute, 1-hour and 4-hour caches (the last two are the strategies' candles, refreshed once per interval); anything else, including the default `5`, selects the 5-minute cache. A non-integer value returns `422` with a FastAPI-style validation body.

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

`t` is a Unix timestamp in seconds. Candles are ordered oldest-to-newest. 100 candles are requested per cycle (~8 hours at 5-min, ~25 hours at 15-min); the 1-hour and 4-hour caches keep 720 candles (30 and 120 days).

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
    "created_at":           "2026-04-23T14:32:10",
    "candle_minutes":       60
}
```

`candle_minutes` (since 0.23.0) is the candle length the signal's strategy reads (`strategy.CandleMinutes`): 60 for the 1-hour basic strategies, 240 for the 4-hour ones, 15 for `llm_only_strategy` and unknown ids. The dialog's chart opens on it.

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

## Backtests

Read-only viewer of the result directories written by `go run ./cmd/backtest ... --output results/<name>` (see [14-backtesting.md](14-backtesting.md#reports-and-metrics)). Code: `internal/server/backtests.go`. The server never imports `internal/backtest`: it reads the files as files, decodes only the fields the list shows, and passes `summary.json`, `run.json` and `costs.json` through unchanged, so new report fields reach the dashboard without server changes. Nothing is ever written.

The results folder is `Options.BacktestResultsDir` (default `results`, relative to the working directory, like the frontend folder; no start-up flag). A missing folder is not an error (the list is empty) and is not created. The routes sit behind the same Host, CORS and access-token checks as the rest of `/api/`.

**Ids.** A run's `id` is its directory relative to the results folder, with `/` separators (`btc_q1`, `2026/split_btc`, `split_btc/runs/02_out_of_sample`). Ids from the URL are refused with `404 {"detail": "Backtest not found"}` unless every segment is a plain name: no `..`, `.`, empty segment, leading `/`, backslash, `:` (drive letters), NUL or other control character, Windows reserved name (`NUL`, `CON`, `COM1`, ... — refused on every platform, so an id is valid or invalid everywhere alike), and at most 512 bytes. Every file is then opened through an `os.Root` on the results folder, which refuses any path, including a symlink or junction target, that resolves outside it. Only the fixed names `summary.json`, `run.json`, `costs.json`, `summary.md`, `equity.csv`, `trades.csv`, `orders.csv` are ever opened; an id must name a directory holding `summary.json`. Error details never contain a filesystem path. (ServeMux itself redirects a path containing `.`/`..` segments to its cleaned form, which then gets the same checks.)

**Limits.** CSV files over 256 MB and JSON/Markdown files over 64 MB get `413 {"detail": "<file> is larger than N bytes"}`. CSV files are parsed as a stream (`encoding/csv`); a malformed file gets `422 {"detail": "<file> is not a valid ..."}`.

### `GET /api/backtests`

Lists the run directories found under the results folder (up to 8 levels deep), newest `summary.json` modification time first, ties by `id`. A directory holding any file the backtester writes is one entry and is not descended into: a multi-run parent (`split`, `walkforward`, `regimes`, `sensitivity`) is one entry with its `runs/NN_label` directories as `children` (each addressable by id, not listed at top level). Folders without report files only group runs. Symlinks and junctions are not followed. A run directory whose `summary.json` is missing, unreadable, not JSON or has no `kind` is left out and counted in `skipped`.

```json
{
    "backtests": [
        {
            "id": "split_btc",
            "kind": "split",
            "strategy": "basic",
            "markets": ["BTC/EUR"],
            "start": "2024-01-01T00:00:00Z",
            "end": "2025-12-31T00:00:00Z",
            "net_return_pct": 0.42,
            "net_pnl": 42,
            "round_trips": 31,
            "verdict": "The out-of-sample result keeps its sign.",
            "verdict_rule": "oos_holds",
            "has_warnings": false,
            "modified": "2026-11-02T00:00:00Z",
            "children": ["split_btc/runs/01_in_sample", "split_btc/runs/02_out_of_sample"]
        },
        {
            "id": "btc_q1",
            "kind": "run",
            "strategy": "basic_strategy",
            "markets": ["BTC/EUR", "ETH/EUR"],
            "start": "2025-01-01T00:00:00Z",
            "end": "2025-03-31T00:00:00Z",
            "net_return_pct": 1.2,
            "net_pnl": 120,
            "round_trips": 3,
            "verdict": "Too few round trips to judge.",
            "verdict_rule": "too_few_trades",
            "has_warnings": false,
            "modified": "2026-11-01T00:00:00Z",
            "children": []
        }
    ],
    "skipped": 0
}
```

| Field | Source |
|---|---|
| `kind`, `strategy` (`strategy_id`), `start`, `end` | `summary.json` |
| `markets` | `summary.json` `markets`, else `run.json` `markets` (multi-run) or `initial_markets` |
| `net_return_pct`, `net_pnl`, `round_trips` | the headline row: `metrics` (run), `out_of_sample` (split), `run` (regimes), `baseline` (sensitivity), `aggregate.compounded_return_pct` / `aggregate.round_trips` (walkforward, `net_pnl` is `null`); `null` when absent |
| `verdict`, `verdict_rule` | text and rule of the first verdict line (`""` when none) |
| `has_warnings` | `reconciliation.warning`, an honesty line containing `WARNING` (accounting or data warning), the headline row's `reconcile_warning` or `data_warnings`; a multi-run parent also when any child has one |
| `modified` | `summary.json` modification time, UTC RFC 3339 |
| `children` | sorted ids of `runs/*` directories; `[]` for a single run |

### `GET /api/backtests/{id...}`

Without `part`: the run's stored JSON.

```json
{
    "id": "btc_q1",
    "kind": "run",
    "summary": { "kind": "run", "strategy_id": "basic_strategy", "honesty": {"lines": ["..."]}, "verdict": [{"rule": "...", "text": "..."}], "metrics": {"net_pnl": 120}, "...": "summary.json as stored" },
    "run": { "seed": 1, "...": "run.json as stored" },
    "costs": { "...": "costs.json as stored" },
    "children": []
}
```

`run` and `costs` are `null` when the file is missing (a multi-run parent has no `costs.json`). Unknown id, an id naming a file, or a directory without `summary.json`: `404`.

The query parameter `part` selects one part instead; any other value of `part` (including empty) is `400`. Integer parameters that are not integers or are below their minimum are `400`; values above the cap are lowered to it (the response states the value used). Other query parameters are ignored. A missing file is `404 {"detail": "<file> not found for this backtest"}`.

**`?part=equity[&max_points=N]`** — `equity.csv` as columns and rows. `max_points` defaults to 2000, minimum 2, cap 20000. Longer curves are downsampled: the first and last points are kept and the interior is cut into `(max_points − 2) / 2` equal buckets, of which the lowest-equity and highest-equity point each survive, in time order, so a drawdown inside a bucket is never smoothed away.

```json
{
    "id": "btc_q1",
    "columns": ["time", "equity", "cash", "positions_value", "peak", "drawdown", "drawdown_pct", "min_equity", "open_positions"],
    "points": [
        ["2025-01-01T00:00:00Z", 10000, 10000, 0, 10000, 0, 0, 10000, 0],
        ["2025-03-31T00:00:00Z", 10100, 10100, 0, 10100, 0, 0, 10050, 0]
    ],
    "downsampled": false,
    "original_points": 2,
    "max_points": 2000
}
```

**`?part=trades[&limit=&offset=]`** and **`?part=orders[&limit=&offset=&status=]`** — `trades.csv` / `orders.csv` rows as objects keyed by column. `limit` defaults to 500 (minimum 1, cap 5000), `offset` to 0. `status` (orders only) keeps the orders with that exact status (`filled`, `rejected`, `cancelled`). `total` counts every row (matching `status`). Typing: the text columns (`time`, `id`, `position_id`, `market`, `side`, `reason`, `strategy_id`, `idea_id`, `*_time`, `price_source`, `status`, `reject_reason`, `exit_reason`) stay strings; every other column is a number, or `null` when empty (e.g. `realised_pnl` of a buy).

```json
{
    "id": "btc_q1",
    "part": "orders",
    "columns": ["id", "market", "side", "...", "realised_pnl", "status", "reject_reason"],
    "rows": [
        {"id": "o3", "market": "ETH/EUR", "side": "buy", "requested_qty": 5, "filled_qty": 0, "position_id": "", "realised_pnl": null, "status": "rejected", "reject_reason": "insufficient_funds", "...": "..."}
    ],
    "total": 1,
    "limit": 500,
    "offset": 0
}
```

**`?part=summary_md`** — `summary.md` as `text/markdown; charset=utf-8` with `X-Content-Type-Options: nosniff`. The dashboard shows it as preformatted text, never as HTML.

---

## Position Management

### `POST /api/positions/{position_id}/close`

Closes a named open position. A **paper** position is closed at the current market price. In addition to filling the order, the handler:
- Writes a `signal_outcomes` row (`exit_reason = "manual_close"`) for history and LLM learning
- Records the realised P&L (entry and exit fees included) in the risk engine and persists the daily counters, so the drawdown tracker reflects the close
- Records realised P&L on the order row
- Records the outcome in the performance learner (as a return on the trade's entry price × size) so it adjusts future confidence
- Saves an equity snapshot immediately

A **live** position (one listed with `"environment": "live"` in the dashboard) is closed by sending one real Kraken market sell of its executed size. Nothing is realised yet: the response has `"status": "closing"` and `"pnl": null`, and the P&L, outcome and learner update are recorded when reconciliation sees the sell fill on a later tick. Until then the position shows as `exit_pending`.

**Path parameter:** `position_id` — either the full UUID (e.g. `3fa85f64-5717-4562-b3fc-2c963f66afa6`) or the 8-character prefix (e.g. `3fa85f64`). The prefix form is accepted for convenience, but the full UUID is preferred to avoid ambiguity.

**Pre-conditions checked by the handler:**

1. The position ID matches an existing open paper or live position (`404`).
2. A live market price is available for the position's market (`503`).
3. Live positions only: the position is `open` — no buy or sell order is pending (`409`).
4. Live positions only: the size is at least Kraken's minimum order. A smaller position is removed from the book as dust and nothing is sent (`409`, "below Kraken's minimum order and was removed as dust").

A manual close is allowed **during the emergency stop** and, for a live position, in either environment: it is the operator's way out while automation is halted. It is also not held back by the back-off that pauses automatic exits after a rejected sell.

**Response (paper position closed):**

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

**Response (live sell sent):**

```json
{
    "status":       "closing",
    "position_id":  "9b2e41d0-1c7a-4f0e-9a51-7d3c2b6e8f10",
    "order_id":     "c4d5e6f7-...",
    "order_status": "pending",
    "environment":  "live",
    "fill_price":   3050.10,
    "pnl":          null,
    "cash":         412.30
}
```

`fill_price` is the reference price the sell was sent at; the real fill price is recorded on reconciliation. A Kraken rejection of the sell returns `502` and the position stays open.

**Response (position not found, `404`):**

```json
{"detail": "Position not found"}
```

**Response (no price available, `503`):**

```json
{"detail": "No live price for BTC/EUR"}
```

---

### `POST /api/positions/{position_id}/forget`

Removes a **live** position from the bot's book **without sending any order**. Use it when the holding was already sold on Kraken by hand, or is too small to sell, and the bot can no longer close it. The holding on Kraken is not touched; any order still pending for the position is no longer tracked. The dashboard asks for confirmation first. The action is written to the activity log ("Live position forgotten: … - no order sent").

**Path parameter:** `position_id` — full UUID or the 8-character prefix.

**Response:**

```json
{"status": "forgotten", "position_id": "3fa85f64-5717-4562-b3fc-2c963f66afa6", "market": "BTC/EUR"}
```

**Errors:** `404` for an unknown position; `409` for a paper position ("Only live positions can be forgotten - close a paper position instead"); `500` if the row could not be deleted from the database (the position is gone until the next restart, then returns).

---

### `POST /api/positions/reset`

Closes every open paper position, one by one. **Paper environment only.** Returns `403` (`"Position reset is not allowed in live mode"`) when the trading environment is `live`. Live positions are never touched.

`paper.ClosePositionsForOperatorReset()` writes a complete audit trail for every open position so no trade disappears from history:

1. Each position is closed through the normal close-order accounting path at the last known market price, falling back to its entry price if no live price is cached. That close removes the position from memory and deletes its own `open_positions` row.
2. A `signal_outcomes` row is written with `exit_reason = "operator_reset"`.
3. The realised P&L is recorded in the risk engine, on the close order and in the performance learner so in-memory state reflects the forced close.

Nothing is blanket-cleared afterwards, so a position opened while the reset runs stays open instead of vanishing without a close. Finally an equity snapshot is saved.

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
- `303` — Page requested without a session on a non-loopback bot (redirect to `/login`)
- `400` — Malformed request body or invalid value (including a backtest `part` or paging parameter)
- `401` — Access token missing or wrong (non-loopback bot only)
- `403` — Host not allowed, cross-origin state change, or position reset called in live mode
- `404` — Resource not found (unknown route, approval, position, strategy, signal, market, backtest id or backtest file)
- `409` — Approval refused by the risk re-check, a live short with nothing to sell, a live position whose order is still pending or that was removed as dust, forgetting a paper position, or switching to paper / turning a market's live flag off while a live position is open
- `502` — Kraken rejected the sell for a live position close
- `413` — Request body over the endpoint's limit, or a backtest file over its size cap
- `422` — Invalid `interval` query parameter on `/api/ohlc`, or a malformed backtest file
- `500` — Unexpected error (`{"detail": "Internal Server Error"}`, logged to the application log)
- `503` — Emergency stop active (approvals), or no live price available for an approval (it stays pending) or a manual close
