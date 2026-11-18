# Operations Guide

---

## Prerequisites

| Requirement | Version | Notes |
|-------------|---------|-------|
| Go | 1.27+ | Required to build; `setup.bat` and `launch.bat` check for it |
| LM Studio (or another OpenAI-compatible server) | Optional | Required for LLM features; bot runs without it |
| SQLite | Built-in | Embedded via the pure-Go `modernc.org/sqlite` driver; no separate install or CGO needed |
| Kraken account | Optional | Paper mode works without API credentials |

---

## First-Time Setup

### 1. Clone and enter the project

```powershell
cd C:\dev\LLMTradingBot
```

### 2. Run the setup script

```powershell
.\setup.bat
```

`setup.bat` checks that Go is on `PATH`, runs `go mod download`, builds `bin\tradingbot.exe` (from `./cmd/tradingbot`) and `bin\tradingbot-cli.exe` (from `./cmd/cli`), There is no configuration file to create: settings live in the database and are edited in the dashboard.

The equivalent manual steps on any platform:

```bash
go mod download
go build -o bin/tradingbot ./cmd/tradingbot
go build -o bin/tradingbot-cli ./cmd/cli
```

### 3. Settings

The first start uses safe built-in defaults: paper trading, manual mode, €500 starting capital, the 50 biggest EUR pairs on Kraken by 24h volume (stablecoins excluded, picked at each start), LM Studio at `http://localhost:1234/v1`. After starting the bot, open **⚙ Settings** in the dashboard to change anything (markets, risk limits, LLM endpoints, Kraken keys, news feeds). Use the PAPER/mode badges for the trading mode. Full reference: `docs/01-configuration.md`.

Upgrading from 0.7 or earlier: `.env` is no longer read. Re-enter your settings in the dashboard.

### 4. Configure an LLM backend

The primary backend is LM Studio's OpenAI-compatible server. In LM Studio, open the Developer tab, start the server on port 1234 and load a model. Leave **LM Studio model** (Settings → LLM) blank to use the first model LM Studio reports as loaded, or set it to a model ID from `GET /v1/models`.

Optionally, set the **Fallback LLM** URL, model and API key (Settings → LLM) to a remote OpenAI-compatible endpoint. It is used only while LM Studio is unreachable; the bot switches back to LM Studio when it returns.

The bot starts without an LLM backend; LLM features are disabled until one is reachable.

### 5. Start the LLM server

Start LM Studio's server before the bot and confirm the base URL matches **LM Studio URL** (`LMSTUDIO_BASE_URL`). If it starts later, the bot picks it up on the next news cycle (every 5 minutes).

---

## Starting the Bot

On Windows, the preferred path is to run the repository launcher from `C:\dev\LLMTradingBot`:

```powershell
.\launch.bat
```

The launcher checks for Go, builds `bin\tradingbot.exe`, starts it in its own console window, waits three seconds and opens the dashboard in a browser.

From the project root, you can also run the server directly:

```bash
go run ./cmd/tradingbot
```

Or run a built binary:

```powershell
.\bin\tradingbot.exe
```

Flags:

| Flag | Default | Purpose |
|------|---------|---------|
| `-db` | `./trading_bot.db` | SQLite database (trades, control state and every dashboard setting) |
| `-host` | `127.0.0.1` | Bind address (`0.0.0.0` for LAN access, which turns on the access token; see **Access from other computers** below) |
| `-port` | `8000` | HTTP port (1–65535) |
| `-log` | `trading_bot.log` | Log file; `llm.log` is written beside it |
| `-frontend` | `frontend` | Directory holding `index.html`, `approvals.html`, `login.html`, `sw.js` and `static/` |

The bot finds its files whatever folder it is started from. Its **home folder** is the first of these that holds `frontend/index.html`: the working directory, the folder holding the executable, and the folder above that (the executable is built into `bin/`). The dashboard is served from the home folder, and the database and log file are kept there when `-db` and `-log` are left at their defaults. So double-clicking `bin	radingbot.exe` uses the same dashboard and database as `launch.bat`. Paths given with `-db`, `-log` or `-frontend` are used exactly as given (relative ones against the working directory). If no dashboard is found the bot refuses to start and lists where it looked. The start-up log line records the `database` and `frontend` paths in use. `-h` prints the flags. No environment variables are read.

The operator CLI can also start the bot. It looks for `tradingbot.exe` next to itself or on `PATH`:

```powershell
.\bin\tradingbot-cli.exe run
.\bin\tradingbot-cli.exe -db data\bot.db -port 9000 run
.\bin\tradingbot-cli.exe status -port 9000
```

The CLI takes `-host`, `-port`, `-log`, `-db` and `-token`, before or after the command (`cli status -port 9000` works); `run` passes the first four through to `tradingbot`. The other commands talk to the running bot at `-host`/`-port`: `status` prints its version, mode, environment, equity and cash (from `/api/dashboard`), and `approve ID` / `reject ID` call the approval API. When the database at `-db` holds an access token, it is sent as `Authorization: Bearer <token>` (`-token` overrides it); the CLI never creates a database. If the bot is not running they print *Cannot reach bot at …* and exit 1; a `401` says the token is needed. An unknown or missing command prints the usage and exits 2. The trading mode and environment are changed in the dashboard only.

Stop the bot with Ctrl+C. It shuts down gracefully: HTTP requests get up to 10 seconds to finish, and the background loops and any in-flight briefing get up to 30 seconds (then `Background loops did not stop in time; exiting anyway` is logged and the bot exits). A second Ctrl+C during shutdown quits at once.

In the live environment, a Kraken order that is already being sent when shutdown starts is not abandoned: the `AddOrder` call runs detached from shutdown with its own 30-second timeout, and the order row and live position are written whatever the shutdown state (reconciliation writes are detached the same way), so the order is recorded and reconciled after the next start. An order not yet sent when shutdown starts is not sent. A second Ctrl+C skips this.

**What happens on startup:**

1. Logging is set up at INFO (stdout, the `-log` file, and `llm.log`).
2. The database is opened (SQLite file created if absent; schema migrations applied).
3. Settings are loaded: built-in defaults, then the values saved from the dashboard, then the start-up flags. A saved value that no longer parses or is out of range (or a secret that cannot be decrypted) is skipped with a `Saved setting ignored` warning. The log level then switches to `LOG_LEVEL`. If no markets were saved, the 50 biggest pairs are fetched from Kraken (`Default markets picked from Kraken by 24h volume`), or the built-in list is used with a warning if Kraken is unreachable.
4. Pending approvals, risk rejections, equity history and the latest cash balance are restored.
5. Open positions and the daily risk counters are restored from the database.
6. The news cache is seeded from the database (and marked as already briefed).
7. The learner is seeded with historical signal outcomes.
8. Control state, the latest LLM briefing and reflection, and the activity log are restored.
9. The LLM backends are probed (LM Studio first, then the remote fallback). LLM availability is logged.
10. When `-host` is not a loopback address, a warning is logged and the access token is loaded (generated on the first such start) and printed to the console.
11. Five background goroutines start: strategy loop, news loop, OHLC loop, reflection loop and equity ticker loop. The strategy loop validates the market list with Kraken, restores recent price history and starts the WebSocket ticker stream. If Kraken confirms none of the configured markets (its pair list could not be loaded), it logs *No markets could be validated - waiting for Kraken* and retries after 5 s, doubling up to 5 minutes, until a market validates or the bot stops.
12. The HTTP server begins serving requests.

The dashboard is available at: `http://127.0.0.1:8000`

### Access from other computers

With a loopback `-host` (the default) nothing needs logging in. To open the dashboard from another machine, start the bot with `-host 0.0.0.0` (or the computer's LAN address):

1. The console prints a block marked `DASHBOARD ACCESS TOKEN` at every start. Copy the token.
2. Open `http://<computer's IP or name>:8000`. The browser is sent to `/login`; paste the token. The session lasts until the browser closes.
3. Scripts send `Authorization: Bearer <token>`. The CLI reads the token from the database itself.

The Host check accepts IP addresses, `localhost` and this computer's name. To use another name (for example `trading-pc.lan`), add it under **Settings → Advanced → Allowed host names** and restart. To replace the token, call `POST /api/access-token/rotate` with the current token, or delete the `ACCESS_TOKEN` row from the `settings` table while the bot is stopped. There is no HTTPS, so only do this on a trusted network. See [01-configuration.md](01-configuration.md#access-from-other-computers).

---

## Running Tests

The test suite uses the standard `go test` runner with BDD-style `GIVEN … WHEN … THEN …` subtest names.

From the project root:

```bash
go test ./...
```

Or use the Windows test runner, which runs `go vet ./...` followed by `go test -v ./...`:

```powershell
.\run_tests.bat
```

See `docs/13-testing.md` for the coverage map and test-writing rules.

---

## Trading Modes

Change the mode in the dashboard: click the mode badge in the header to open **Trading settings**, pick a mode and confirm. The choice is saved and survives restarts. A fresh database starts in manual mode.

| Mode | Behaviour |
|------|-----------|
| `manual` | Signals appear in dashboard only. No orders placed. |
| `semi_automated` | Approved signals are queued. User clicks Approve/Reject within 30 minutes. |
| `fully_automated` | Signals execute immediately after passing risk checks. Signals below `MIN_SIGNAL_CONFIDENCE` (default 65%) are skipped and logged, except a 4-hour strategy's exit of a held long, which always executes. No human step. |

Start in `manual` mode to observe signal quality before enabling execution.

---

## Monitoring

### Dashboard

`http://127.0.0.1:8000` — Refreshes every 5 seconds. Key panels to watch:

- **Equity curve** — Overall account health. A flat or declining curve warrants attention.
- **Positions** — Any open position showing large negative unrealised P&L may be approaching stop-loss.
- **Activity log** — Real-time feed of bot events. `STOP-LOSS` entries indicate positions that were auto-closed.
- **Risk rejections** — Repeated rejections for the same market indicate the risk engine is blocking signals. Check `daily_loss` limit or position sizing.
- **LLM status** — Shows the model and whether LM Studio or the remote fallback is active. Market briefing updates after each new news batch. Reflection updates hourly once ≥5 closed trades exist.

`GET /health` and `GET /api/health` give a quick liveness and LLM-backend check.

### Log Files

The bot writes structured JSON logs (Go `log/slog`) to stdout and to `trading_bot.log` (start-up flag `-log`). Records from the LLM client and analyser are also written to `llm.log` in the same directory as that file, so LLM traffic can be followed on its own. Useful for forensic analysis after unexpected behaviour.

```powershell
# Tail the log files
Get-Content trading_bot.log -Wait -Tail 50
Get-Content llm.log -Wait -Tail 50
```

Log level is configurable via `LOG_LEVEL` (`DEBUG`, `INFO`, `WARNING`, `ERROR`; default `INFO`). Set it to `DEBUG` for verbose output, including the full LLM prompts and raw responses.

---

## Managing Markets

Markets are the **Fixed markets** setting (`FIXED_MARKETS`). Until you save it, the bot trades the 50 biggest pairs on Kraken by 24h volume, re-picked at every start. Saving the list fixes it: from then on Kraken's ranking is no longer used. To add or remove a market:

1. Open **⚙ Settings → Markets**, type the symbol (e.g. `SOL/EUR`) in the add box and press **Add** or Enter, or click **×** on a market chip to remove it. Then save.
2. Restart the bot (the dashboard shows a restart banner until you do).
3. On startup, `ValidateSymbols()` checks Kraken recognises the symbol. Invalid symbols are dropped with a warning.

To temporarily disable a market without restarting: use the **Disable** button on the dashboard Markets panel. Re-enable without restart using the **Enable** button. The toggle is persisted and survives restarts.

---

## Managing Positions

### View open positions

Dashboard → Positions panel (updates every 5 seconds). Paper and live positions are listed together; live ones carry a **LIVE** badge and show `entry pending` / `exit pending` while a Kraken order is open.

### View closed trades

Dashboard → Closed Trades panel (updates every 30 seconds). `GET /api/export/trades.csv` downloads the full closed-trade history as CSV.

### Manually close a position

Use the **✕ Close** button on the Positions panel in the dashboard. This calls `POST /api/positions/{position_id_full}/close`. A paper position is closed at the current market price, the P&L outcome is recorded, and an equity snapshot is saved immediately.

A live position is closed by sending one Kraken sell order for the executed size, in either environment and also during the emergency stop. The response has `status: "closing"` and `pnl: null`, because the sale is not complete yet. The P&L is recorded when the order is reconciled on a later tick. A second close while that sell is pending returns `409`, as does closing one whose entry is still pending. A position smaller than Kraken's minimum order is removed as dust with nothing sent (`409`). A Kraken rejection returns `502`.

### Forget a live position

If a live position was already sold on Kraken by hand (or is too small to sell) the bot cannot close it. Use the **Forget** button next to it in the Positions panel, or `POST /api/positions/{position_id}/forget`. This removes the position from the bot's book only: **no order is sent** and the holding on Kraken is untouched. Check Kraken first — forgetting a position you still hold leaves it with no stop.

Alternatively, via the API:

```bash
curl -X POST http://127.0.0.1:8000/api/positions/3fa85f64-5717-4562-b3fc-2c963f66afa6/close
```

The 8-character position ID prefix is also accepted:

```bash
curl -X POST http://127.0.0.1:8000/api/positions/3fa85f64/close
```

To close every open position at once (e.g., after a testing session), use `POST /api/positions/reset` (paper mode only) — see below.

### Reset stale positions

If positions remain open after a crash or during testing:

```bash
curl -X POST http://127.0.0.1:8000/api/positions/reset
```

Or use the **Reset Positions** button (paper mode only) on the dashboard. Each position is closed and recorded with `exit_reason = "operator_reset"` at the last known price (or its entry price); each close removes that position's `open_positions` row. Nothing else is cleared, so a position opened while the reset runs stays open. Live positions are not affected.

---

## Database Management

The SQLite database file is `trading_bot.db` in the repository root (or as configured in `DATABASE_PATH`). The schema is compatible with the one the Python version created, so an existing `trading_bot.db` from the Python bot opens and keeps working.

### Schema migrations

Migrations are applied automatically on startup by `storage.Open()` (`internal/storage/database.go`). The migration strategy is:
- **New tables and indexes**: created if they do not exist.
- **Additive changes** (new nullable columns): `ALTER TABLE ADD COLUMN` for each column still missing from an existing table.
- **Structural changes**: a legacy `open_positions` table that still uses `market` as its primary key is dropped and recreated.

The applied schema version is recorded in a `schema_version` table. No manual migration step is required.

### Inspect the database

```bash
sqlite3 trading_bot.db

# Useful queries:
.tables
SELECT * FROM order_records ORDER BY timestamp DESC LIMIT 20;
SELECT * FROM signal_outcomes ORDER BY exit_at DESC LIMIT 20;
SELECT * FROM open_positions;
SELECT equity, timestamp FROM equity_snapshots ORDER BY timestamp DESC LIMIT 10;
```

### Backup

Simply copy `trading_bot.db` while the bot is stopped. The file is self-contained.

```powershell
Copy-Item trading_bot.db trading_bot_backup_$(Get-Date -Format 'yyyyMMdd').db
```

### Reset for fresh start

Stop the bot, delete the database file, and restart. The bot will create a new empty database and start with `STARTING_CAPITAL` as the initial cash balance.

```powershell
Remove-Item trading_bot.db
```

---

## Emergency Stop

To immediately halt all trading:

1. Dashboard → click **Emergency Stop** button (top of page), or:
2. API: `curl -X POST http://127.0.0.1:8000/api/control/emergency-stop`

**Effect:**
- All strategy evaluation and execution is paused at the next tick (within one strategy interval, 60 seconds by default).
- All pending approvals are cleared.
- Approving trades is refused until you resume. **Manually closing a position is still allowed** (paper or live) — use it to get out while automation is halted.
- Existing open positions are **not** automatically closed.
- **Live positions have no automatic stop while the emergency stop is active.** Stop-losses and trailing stops do not send. The bot still prices their markets, reconciles pending Kraken orders (a fill during the stop is recorded) and moves trailing highs, and the activity feed reminds you every 10 minutes ("Emergency stop: N live position(s) have no automatic stop"). If you hold live positions, either close them by hand or resume soon.
- The stop is persisted, so it stays active across a restart.

To resume: click **Resume** on the dashboard or:

```bash
curl -X POST http://127.0.0.1:8000/api/control/resume
```

---

## Switching to Live Trading

**Warning:** Live trading sends real orders to Kraken with real funds. Ensure thorough paper trading validation before switching.

1. Open **⚙ Settings → Kraken**, enter the API key and secret, save, and restart the bot. The keys are encrypted with Windows DPAPI for your Windows user and are never shown again (the dialog only says whether they are set).

2. In the dashboard, open **Trading settings** (click the PAPER badge), choose **Live — real money**, type `LIVE` and click **Switch to live**. The switch is refused if the API keys are missing, or were saved but the bot has not been restarted since.
3. Start in **Manual** mode to observe signals without execution.
4. Promote to **Semi-automated** for human-gated execution.
5. Promote to **Fully automated** only after extended validation.

To go back, choose **Paper** in the same dialog and confirm. This is refused (`409`) while any live position is open: close them first, or forget any you already sold on Kraken. Turning a market's live flag off is refused the same way while that market holds a live position. The choice is saved, so a restart keeps the last environment chosen in the dashboard.

The risk engine still runs before live execution. In the live environment, per-market live toggles route that market to the Kraken `ExecutionEngine` (`internal/kraken/execution.go`), which submits Kraken `AddOrder` requests through the official Go SDK and records the returned transaction ID in the trade ledger. The engine refuses orders when the API key or secret is missing. Markets without the live flag continue to use paper execution, so you can promote one market at a time. Per-market live toggles are set with the **Live / Paper** button in the Markets panel (live environment) or `POST /api/control/markets/{market}/live-toggle`. In the paper environment the live toggles are ignored for execution (orders stay on paper and a warning is logged). In the live environment, cash and equity on the dashboard come from Kraken balances and pending live orders are reconciled on every tick.

Live orders are submitted as Kraken spot market orders unless the `ExecutionIntent` carries a limit price. Local paper positions are not changed by live orders. Live positions are tracked in their own book (`live_positions`, see [Execution Engines → Live Position Book](05-execution-engine.md#live-position-book)):

- **One order per decision.** A repeated buy signal does not send a second buy while a live long exists on that market (including one whose order has not been confirmed), also after a restart. Each `AddOrder` carries a client order id; it is retried only when Kraken proves the order was not placed, and an order whose outcome is unknown is kept `pending` and looked up by that id.
- **Intent first.** The position row naming the order is saved before the order is sent, so a crash at any moment leaves something to reconcile. If that save fails, a buy is not sent; a sell is sent anyway.
- **No shorts.** The spot account cannot open shorts. A SELL signal sells an existing live long; with none it is logged as "not acted on" and no order is sent.
- **Partial fills.** The position holds only the volume Kraken executed (`vol_exec`), and P&L uses that size and the average price. A remainder below Kraken's minimum order is dropped as dust.
- **Stops.** Stop-loss and trailing stop run on live longs every tick — in either environment, and also for markets you disabled or that left the market list — and send one sell each. They are paused during the emergency stop. The trailing high is saved and survives a restart.
- **Rejected sells.** If Kraken rejects a sell, automatic exits for that position pause for 5 minutes, doubling up to 1 hour, and the first rejection raises a `live_exit_rejected` alert. A manual close is not held back.
- **Stale prices.** A market whose last streamed price is older than two strategy intervals is re-fetched over REST; if that fails too it is skipped (no stop or strategy on an old price) and the activity feed says so.
- **Approvals** are re-checked by the risk engine when you approve them (see [Approval and Control](10-approval-and-control.md)).
- **Alerts** are sent in the background and never delay a live decision. The daily-loss-limit webhook alert is sent at most once per UTC day, not once per refused signal.

### Troubleshooting live orders

- **An order stays `pending` with no transaction ID** — the reply to `AddOrder` was lost (timeout, Kraken busy). It is looked up by client order id every tick; if Kraken has no such order after 5 minutes it becomes `not_found` and its pending position is removed (entry) or returns to `open` (exit).
- **A position shows `entry_pending` or `exit_pending` for long** — check the order in `order_records` and on Kraken. Reconciliation runs every tick while any live position exists, including during the emergency stop.
- **A position the bot cannot sell** (sold by hand on Kraken, or dust) — use **Forget** (see [Managing Positions](#forget-a-live-position)).

### Known limits (live)

These are not handled automatically; know them before trading real money:

- **Emergency stop = no automatic stops.** While the stop is active, live positions are not protected by stop-loss or trailing stop. Close them by hand if needed.
- **Crash between P&L stamp and result.** If the bot crashes after a sell's P&L is written to the order but before the trade result is recorded, that result is missing from daily loss and learning. It is under-counted, never counted twice.
- **Failed P&L stamp.** If writing the P&L to the order fails, a restart could count that sell's result a second time in the statistics. The holding itself is not affected.
- **Old orphan buys.** A live buy placed by an older version that has no position row is not rebuilt; it is visible on Kraken only.
- **Environment switch race.** A signal that read the environment as live just before you switch to paper can still send one live entry; that position is then protected by the live stops and blocks the switch until closed.
- **Forget is trusted.** The bot does not check Kraken before forgetting a position; if you still hold it, it has no stop.

---

## Performance Tuning

### Signal frequency

The strategy loop runs every `STRATEGY_INTERVAL_SECONDS` (Settings → Signals → *Strategy interval*, default 60 s, 30–600 s; applies from the next tick). A longer interval eases the load on a slow LLM but makes the bot react more slowly: indicators, the warm-up and the momentum lookback count ticks, and stops are checked once per tick. Backtests always simulate 30-second ticks, so their results match a 30-second interval best.

### LLM response time

If LLM calls are too slow, either:
- Increase `LMSTUDIO_TIMEOUT` (default 60 s) or `OPENAI_TIMEOUT` (default 30 s) in Settings → LLM (restart needed).
- Load a smaller/faster model in LM Studio.
- Lower `LLM_ONLY_MAX_CONCURRENCY` if parallel per-market calls (made when a 1h candle closes) overload the local server.
- Disable LLM features by clearing `LMSTUDIO_BASE_URL` and `OPENAI_BASE_URL`.

After a timeout or transport failure, the LLM client opens a circuit breaker before retrying. The retry delay starts at 30 seconds, doubles on repeated failures, and caps at 5 minutes. While LM Studio's circuit is open, calls go to the remote fallback if one is configured. A malformed JSON response from the model does not mark the backend unavailable; it only skips that one LLM decision so the next prompt can still run.

### Stop-loss sensitivity

`STOP_LOSS_PCT` is a fraction of the entry value (default `0.05`) and applies as soon as it is saved. Reduce it to `0.03` for tighter stop-losses (more frequent closures, smaller losses). Increase it to `0.08` for wider stops (less frequent, larger losses).

Stop-losses are measured against the entry price and only close positions that are losing by at least this percentage. Retracements from a peak are handled separately by `TRAILING_STOP_PCT`.

This setting also affects the risk engine's per-trade loss estimate — changing it will affect how many signals pass the per-trade loss limit check.

---

## Adding a New Strategy

1. Create `internal/strategy/my_strategy.go` implementing the `strategy.Strategy` interface:

```go
package strategy

import (
	"context"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// MyID is the canonical id of the new strategy.
const MyID = "my_strategy"

// MyStrategy is an example strategy.
type MyStrategy struct{}

// ID returns the strategy id.
func (m *MyStrategy) ID() string { return MyID }

// UsesLLM reports whether the strategy needs news and the LLM analyser.
func (m *MyStrategy) UsesLLM() bool { return false }

// RequiredPriceTicks is the price-tick history the strategy needs first; 0 for a candle strategy.
func (m *MyStrategy) RequiredPriceTicks() int { return 0 }

// Evaluate returns zero or more trade ideas for the given input.
func (m *MyStrategy) Evaluate(ctx context.Context, in Input) []domain.TradeIdea {
	var ideas []domain.TradeIdea
	// ... inspect in.MarketData, in.News, in.Briefing ...
	return ideas
}
```

2. Register it in `All()` in `internal/strategy/strategy.go`, and add a dashboard label to `StrategyLabels` in `internal/app/app.go`:

```go
func All(s config.Settings) []Strategy {
	return []Strategy{NewBasic1h(s), /* ... */ NewLLMOnly(s), &MyStrategy{}}
}
```

3. Select it in the dashboard's **Trading settings** dialog (click the mode badges in the header; the Strategy section is below Trading mode), or `POST /api/control/strategies/my_strategy/select`. The learner will automatically track outcomes for the new strategy ID; no changes to the learner are needed.

---

## Troubleshooting

### "Tick #N - prices fetched (warming up N/M ticks)"

Shown at start-up only for a strategy without LLM that reads price ticks: the loop waits until the price history reaches its `RequiredPriceTicks()` (M). None of the registered strategies needs this since 0.19.0; the candle strategies evaluate from the first tick and wait for their own finished candles. The count is the longest history among the enabled markets that were priced this tick, so a disabled or unpriced market cannot hold it back. If price ticks were restored from the database, this message may not appear at all.

For strategies that use the LLM, this tick warm-up is not used. Instead the loop logs "LLM data warmup waiting for …" until at least one market has the strategy's `RequiredPriceTicks()` of history (0 for every LLM strategy since 0.24.0, `llm_only_strategy` included, which used 10; they wait instead for 40 finished candles of their own (the message then reads "waiting for market data"): about 41 hours for 1h, 7 days for 4h, usually already in the first Kraken fetch) and at least one news item is cached. Only markets whose price history is ready are evaluated.

### "No markets could be validated - waiting for Kraken"

At start-up Kraken confirmed none of the configured markets, usually because its pair list could not be loaded (network not up yet, Kraken returned an error). The strategy loop retries with a growing wait (5 s up to 5 minutes) and starts as soon as one market validates; no ticks run meanwhile. If it persists, check the network and `KRAKEN_REST_URL`, and that `FIXED_MARKETS` uses Kraken's `BASE/QUOTE` names.

### `403 Host not allowed` or `401 Access token required`

`403`: the browser used a host name the bot does not accept (see **Access from other computers**); use the IP address or add the name to **Allowed host names**. `401`: the bot listens on a non-loopback address and the request had no valid token; log in at `/login` or send the bearer token. After a token rotation, log in again.

### "Both LLM backends unavailable — LLM features disabled"

Neither LM Studio nor the remote fallback answered the startup probe. Check that LM Studio's server is running with a model loaded and that `LMSTUDIO_BASE_URL` is correct. If no backend is configured at all, the log says "No LLM configured — set the LM Studio or fallback LLM URL under Settings → LLM". The bot continues without LLM confidence adjustment — signals use the base strategy confidence only — and re-probes LM Studio on every news cycle.

### `'go' is not recognized` / "Go was not found on PATH"

Go is not installed or not on `PATH`. Install Go 1.27 or newer from https://go.dev/dl/, open a new terminal, and run `setup.bat` again.

### `'tlocal' is not recognized` (or `'EM'`, `'M'`) when running a `.bat` script

The script has Unix (LF) line endings, and `cmd.exe` misparses LF-only batch files, cutting the start off lines such as `setlocal` and `REM`. The repo's `.gitattributes` (`*.bat text eol=crlf`) makes git check the scripts out with CRLF; if a copy was saved with LF by an editor, run `git checkout -- launch.bat setup.bat run_tests.bat` to restore CRLF. `TestWindowsScriptsUseCRLF` (`cmd/tradingbot/scripts_test.go`) fails if any script loses its CRLF endings.

### `launch.bat` reports "build failed" while the bot is running

Windows locks a running executable, so `go build -o bin\tradingbot.exe` cannot overwrite it. Close the "Kraken Bot" window (or stop `tradingbot.exe`) and run `launch.bat` again.

### "Daily loss limit reached"

The bot has lost more than `MAX_DAILY_LOSS_PERCENT` (default 5%) of equity in the current UTC day. All new trades are blocked until midnight UTC when the limit resets. The daily counters are persisted, so a restart does not reset them. The limit can be raised in Settings → Risk & sizing if appropriate; it applies immediately.

### "Position already open for X - one position per pair"

A signal was generated for a market that already has an open position in the same direction. This is expected behaviour — the risk engine enforces one position per pair. The signal will be retried on the next tick once the position is closed.

### Positions not appearing after restart

Positions are restored from the database on startup. If the `open_positions` table is empty (e.g., after a reset or fresh DB), no positions will appear. Check `SELECT * FROM open_positions;` in SQLite, and check which database is in use: the start-up log line shows its path. Before 0.12.1 a bot started from another folder (for example by double-clicking `bin	radingbot.exe`) created a new empty `trading_bot.db` there; such a stray file can be deleted once the bot is stopped.

### LLM reflection not appearing

Requires ≥5 closed trades in the database. Check `SELECT COUNT(*) FROM signal_outcomes;`. If zero, no trades have been recorded as closed. Verify that `fully_automated` mode is producing fills and that stop-loss closures are being recorded.
