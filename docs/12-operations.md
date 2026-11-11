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

`setup.bat` checks that Go is on `PATH`, runs `go mod download`, builds `bin\tradingbot.exe` (from `./cmd/tradingbot`) and `bin\tradingbot-cli.exe` (from `./cmd/cli`), and creates `.env` from `.env.example` if it does not exist yet. An existing `.env` is left untouched.

The equivalent manual steps on any platform:

```bash
go mod download
go build -o bin/tradingbot ./cmd/tradingbot
go build -o bin/tradingbot-cli ./cmd/cli
cp .env.example .env
```

### 3. Edit `.env`

`.env` lives in the repository root. Real environment variables override values in `.env`. The template's defaults are safe (paper trading, manual mode). Minimum viable configuration for paper trading:

```ini
TRADING_MODE=semi_automated
TRADING_ENVIRONMENT=paper
STARTING_CAPITAL=500.0
FIXED_MARKETS=["BTC/EUR","ETH/EUR"]

LMSTUDIO_BASE_URL=http://localhost:1234/v1
LMSTUDIO_MODEL=

DATABASE_PATH=./trading_bot.db
```

List-valued settings such as `FIXED_MARKETS` are JSON arrays; a plain comma-separated list is also accepted. Full configuration reference: `docs/01-configuration.md`.

### 4. Configure an LLM backend

The primary backend is LM Studio's OpenAI-compatible server. In LM Studio, open the Developer tab, start the server on port 1234 and load a model. Leave `LMSTUDIO_MODEL` blank to use the first model LM Studio reports as loaded, or set it to a model ID from `GET /v1/models`.

Optionally, set `OPENAI_BASE_URL`, `OPENAI_MODEL` and `OPENAI_API_KEY` to a remote OpenAI-compatible endpoint. It is used only while LM Studio is unreachable; the bot switches back to LM Studio when it returns.

The bot starts without an LLM backend; LLM features are disabled until one is reachable.

### 5. Start the LLM server

Start LM Studio's server before the bot and confirm the base URL matches `LMSTUDIO_BASE_URL`. If it starts later, the bot picks it up on the next news cycle (every 5 minutes).

---

## Starting the Bot

On Windows, the preferred path is to run the repository launcher from `C:\dev\LLMTradingBot`:

```powershell
.\launch.bat
```

The launcher checks for Go, copies `.env.example` to `.env` if `.env` is missing, builds `bin\tradingbot.exe`, starts it in its own console window, waits three seconds and opens the dashboard in a browser.

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
| `-env` | `.env` | Path to the settings file |
| `-frontend` | `frontend` | Directory holding `index.html`, `approvals.html`, `sw.js` and `static/` |

Both defaults are relative to the working directory, so start the bot from the repository root (`launch.bat` does this).

The operator CLI can also start the bot. It looks for `tradingbot.exe` next to itself or on `PATH`:

```powershell
.\bin\tradingbot-cli.exe run
.\bin\tradingbot-cli.exe --mode semi_automated --environment paper run
```

`--mode` and `--environment` override `TRADING_MODE` and `TRADING_ENVIRONMENT` for that run. The CLI's other commands are `status` (prints the effective configuration) and `approve ID` / `reject ID`, which call the running bot's approval API.

Stop the bot with Ctrl+C. It shuts down gracefully: HTTP requests get up to 10 seconds to finish, and the background loops and any in-flight briefing are waited for.

**What happens on startup:**

1. Settings are loaded from `.env` and the environment, and validated.
2. Logging is set up (stdout, `LOG_FILE`, and `llm.log`).
3. The database is opened (SQLite file created if absent; schema migrations applied).
4. Pending approvals, risk rejections, equity history and the latest cash balance are restored.
5. Open positions and the daily risk counters are restored from the database.
6. The news cache is seeded from the database (and marked as already briefed).
7. The learner is seeded with historical signal outcomes.
8. Control state, the latest LLM briefing and reflection, and the activity log are restored.
9. The LLM backends are probed (LM Studio first, then the remote fallback). LLM availability is logged.
10. Five background goroutines start: strategy loop, news loop, OHLC loop, reflection loop and equity ticker loop. The strategy loop validates the market list with Kraken, restores recent price history and starts the WebSocket ticker stream.
11. The HTTP server begins serving requests.

The dashboard is available at: `http://127.0.0.1:8000`

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

Set via `TRADING_MODE` in `.env` (or the CLI's `--mode` override). The mode is read-only on the dashboard.

| Mode | Behaviour |
|------|-----------|
| `manual` | Signals appear in dashboard only. No orders placed. |
| `semi_automated` | Approved signals are queued. User clicks Approve/Reject within 30 minutes. |
| `fully_automated` | Signals execute immediately after passing risk checks. Signals below `MIN_SIGNAL_CONFIDENCE` (default 65%) are skipped and logged. No human step. |

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

The bot writes structured JSON logs (Go `log/slog`) to stdout and to `trading_bot.log` (configurable via `LOG_FILE`). Records from the LLM client and analyser are also written to `llm.log` in the same directory as `LOG_FILE`, so LLM traffic can be followed on its own. Useful for forensic analysis after unexpected behaviour.

```powershell
# Tail the log files
Get-Content trading_bot.log -Wait -Tail 50
Get-Content llm.log -Wait -Tail 50
```

Log level is configurable via `LOG_LEVEL` (`DEBUG`, `INFO`, `WARNING`, `ERROR`; default `INFO`). Set it to `DEBUG` for verbose output, including the full LLM prompts and raw responses.

---

## Managing Markets

Markets are defined in `FIXED_MARKETS` in `.env`. To add a new market:

1. Add the symbol to `FIXED_MARKETS` (e.g., `FIXED_MARKETS=["BTC/EUR","ETH/EUR","SOL/EUR"]`).
2. Restart the bot.
3. On startup, `ValidateSymbols()` checks Kraken recognises the symbol. Invalid symbols are dropped with a warning.

To temporarily disable a market without restarting: use the **Disable** button on the dashboard Markets panel. Re-enable without restart using the **Enable** button. The toggle is persisted and survives restarts.

---

## Managing Positions

### View open positions

Dashboard → Positions panel (live, updates every 5 seconds).

### View closed trades

Dashboard → Closed Trades panel (updates every 30 seconds). `GET /api/export/trades.csv` downloads the full closed-trade history as CSV.

### Manually close a position

Use the **✕ Close** button on the Positions panel in the dashboard. This calls `POST /api/positions/{position_id_full}/close`, closes the paper position at the current market price, records the P&L outcome, and saves an equity snapshot immediately.

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

Or use the **Reset Positions** button (paper mode only) on the dashboard. Each position is closed and recorded with `exit_reason = "operator_reset"` at the last known price (or its entry price), then all `open_positions` rows are removed and in-memory state is cleared.

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
- All strategy evaluation and execution is paused at the next tick (within 30 seconds).
- All pending approvals are cleared.
- Approving trades and manually closing positions are refused until you resume.
- Existing open positions are **not** automatically closed.
- The stop is persisted, so it stays active across a restart.

To resume: click **Resume** on the dashboard or:

```bash
curl -X POST http://127.0.0.1:8000/api/control/resume
```

---

## Switching to Live Trading

**Warning:** Live trading sends real orders to Kraken with real funds. Ensure thorough paper trading validation before switching.

1. Set Kraken API credentials in `.env`:

```ini
KRAKEN_API_KEY=your_key_here
KRAKEN_API_SECRET=your_secret_here
TRADING_ENVIRONMENT=live
```

2. Start with `TRADING_MODE=manual` to observe signals without execution.
3. Promote to `semi_automated` for human-gated execution.
4. Promote to `fully_automated` only after extended validation.

The risk engine still runs before live execution. With `TRADING_ENVIRONMENT=live`, per-market live toggles route that market to the Kraken `ExecutionEngine` (`internal/kraken/execution.go`), which submits Kraken `AddOrder` requests through the official Go SDK and records the returned transaction ID in the trade ledger. The engine refuses orders when the API key or secret is missing. Markets without the live flag continue to use paper execution, so you can promote one market at a time. In the paper environment the live toggles are ignored for execution (orders stay on paper and a warning is logged). With `TRADING_ENVIRONMENT=live`, cash and equity on the dashboard come from Kraken balances and pending live orders are reconciled every two ticks.

Live orders are submitted as Kraken spot market orders unless the `ExecutionIntent` carries a limit price. Live fills and balances remain exchange-owned; local paper positions are not mutated by live orders.

---

## Performance Tuning

### Signal frequency

The strategy loop runs every 30 seconds (the `Intervals.Strategy` default in `internal/app/app.go`). Reducing this increases Kraken API call frequency — stay above 10 seconds to avoid rate limiting.

### LLM response time

If LLM calls are too slow, either:
- Increase `LMSTUDIO_TIMEOUT` (default 60 s) or `OPENAI_TIMEOUT` (default 30 s) in `.env`.
- Load a smaller/faster model in LM Studio.
- Lower `LLM_ONLY_MAX_CONCURRENCY` if parallel per-market calls overload the local server.
- Disable LLM features by clearing `LMSTUDIO_BASE_URL` and `OPENAI_BASE_URL`.

After a timeout or transport failure, the LLM client opens a circuit breaker before retrying. The retry delay starts at 30 seconds, doubles on repeated failures, and caps at 5 minutes. While LM Studio's circuit is open, calls go to the remote fallback if one is configured. A malformed JSON response from the model does not mark the backend unavailable; it only skips that one LLM decision so the next prompt can still run.

### Stop-loss sensitivity

`STOP_LOSS_PCT` is a fraction of the entry value (code default `0.05`; `.env.example` sets `0.07`). Reduce it to `0.03` for tighter stop-losses (more frequent closures, smaller losses). Increase it to `0.08` for wider stops (less frequent, larger losses).

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

// RequiredPriceTicks is 0 for non-LLM strategies, which wait for indicator warm-up.
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
	return []Strategy{NewBasic(s), NewBasicAndLLM(s), NewLLMOnly(s), &MyStrategy{}}
}
```

3. Select it from the dashboard (or `POST /api/control/strategies/my_strategy/select`). The learner will automatically track outcomes for the new strategy ID; no changes to the learner are needed.

---

## Troubleshooting

### "Tick #N - prices fetched (warming up N/35 ticks)"

Normal at startup for indicator-only strategy evaluation. The loop waits for `LookbackTicks = 34` price ticks of history (plus the current one), approximately 17 minutes at 30 seconds per tick, so MACD and the other configured indicators have enough history. If price ticks were restored from the database, this message may not appear at all.

For strategies that use the LLM, the indicator warm-up is not used. Instead the loop logs "LLM data warmup waiting for …" until at least one market has the strategy's `RequiredPriceTicks()` of history (10 for `llm_only_strategy`, 35 for `basic_and_llm_strategy`, which still applies local indicator gates) and at least one news item is cached. Only markets whose price history is ready are evaluated.

### "Both LLM backends unavailable — LLM features disabled"

Neither LM Studio nor the remote fallback answered the startup probe. Check that LM Studio's server is running with a model loaded and that `LMSTUDIO_BASE_URL` is correct. If no backend is configured at all, the log says "No LLM configured — set LMSTUDIO_BASE_URL or OPENAI_BASE_URL in .env". The bot continues without LLM confidence adjustment — signals use the base strategy confidence only — and re-probes LM Studio on every news cycle.

### `'go' is not recognized` / "Go was not found on PATH"

Go is not installed or not on `PATH`. Install Go 1.27 or newer from https://go.dev/dl/, open a new terminal, and run `setup.bat` again.

### "Daily loss limit reached"

The bot has lost more than `MAX_DAILY_LOSS_PERCENT` (default 5%) of equity in the current UTC day. All new trades are blocked until midnight UTC when the limit resets. The daily counters are persisted, so a restart does not reset them. The limit can be raised in `.env` if appropriate.

### "Position already open for X - one position per pair"

A signal was generated for a market that already has an open position in the same direction. This is expected behaviour — the risk engine enforces one position per pair. The signal will be retried on the next tick once the position is closed.

### Positions not appearing after restart

Positions are restored from the database on startup. If the `open_positions` table is empty (e.g., after a reset or fresh DB), no positions will appear. Check `SELECT * FROM open_positions;` in SQLite, and check that `DATABASE_PATH` points at the file you expect: it is resolved relative to the working directory.

### LLM reflection not appearing

Requires ≥5 closed trades in the database. Check `SELECT COUNT(*) FROM signal_outcomes;`. If zero, no trades have been recorded as closed. Verify that `fully_automated` mode is producing fills and that stop-loss closures are being recorded.
