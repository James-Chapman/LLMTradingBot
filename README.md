# LLMTradingBot

### // This project is still in development

---

LLMTradingBot is a self-hosted cryptocurrency trading bot for Kraken spot markets, written in Go. It collects Kraken market data, generates strategy signals, optionally asks a local LLM (LM Studio) to analyse or veto those signals, applies risk controls, and serves a browser dashboard for monitoring and operator control.

The bot is designed to run safely in paper mode first. Live trading is supported for Kraken, but only after you provide Kraken API credentials and explicitly enable live execution for a market.

> This project is trading software, not financial advice. Use paper mode until you understand the strategy, risk settings, and failure modes.

## What It Does

- Monitors configured Kraken markets such as `BTC/EUR` and `ETH/EUR`.
- Generates trade ideas from technical indicators, momentum, market context, and optional LLM analysis.
- Enforces risk checks before any order can be executed.
- Supports `manual`, `semi_automated`, and `fully_automated` trading modes.
- Supports paper execution and live Kraken execution.
- Persists signals, orders, positions, equity, news, risk rejections, approvals, and control state to SQLite.
- Serves a browser dashboard for positions, equity, market status, activity, approvals, and emergency stop.

## Tech Stack

| Area | Technology |
|---|---|
| Backend | Go 1.27, standard-library `net/http` server, background loops as goroutines |
| Database | SQLite via `modernc.org/sqlite` (pure Go, no CGO) |
| Settings | Edited in the dashboard's Settings dialog and stored in the SQLite database (API keys encrypted with Windows DPAPI); start-up flags `-db -host -port -log` |
| Exchange | Official Kraken Go SDK `github.com/krakenfx/api-go/v2` (REST + WebSocket v2) |
| LLM | LM Studio's OpenAI-compatible API (primary), optional remote OpenAI-compatible fallback |
| Logging | `log/slog` JSON to stdout, `trading_bot.log` and `llm.log` |
| Frontend | Alpine.js, local CSS, Chart.js, Lightweight Charts |
| Tests and linting | `go test` with BDD-named subtests, `go vet`, `gofmt` |

## Repository Layout

```text
.
|-- cmd/
|   |-- tradingbot/          Server entry point (dashboard, API, background loops)
|   |-- cli/                 Operator CLI: status, approve, reject, run
|   `-- backtest/            Historical backtesting on Kraken trade history (docs/14-backtesting.md)
|-- internal/                Application packages (app, server, strategy, risk, paper,
|                            kraken, llm, news, storage, ...)
|-- frontend/                Dashboard HTML, approvals page, service worker
|-- docs/                    Detailed project documentation
|-- go.mod                   Go module definition
|-- setup.bat                Windows first-time setup
|-- launch.bat               Windows launcher
|-- run_tests.bat            Windows test runner
`-- README.md                Project entry point
```

## Prerequisites

- Go 1.27 or newer.
- Windows PowerShell or Command Prompt for the provided batch scripts (the Go commands work on any platform).
- LM Studio, optional but recommended for LLM-backed analysis.
- A Kraken account and API key only if you plan to use live trading.

SQLite is embedded in the binary; there is no separate database server to install. Existing `trading_bot.db` files from the earlier Python version are schema-compatible and open as-is.

## Install

On Windows, from the project root:

```powershell
.\setup.bat
```

`setup.bat` checks for Go, downloads the modules, and builds `bin\tradingbot.exe` and `bin\tradingbot-cli.exe`.

Manually, on any platform:

```bash
go mod download
go build -o bin/tradingbot ./cmd/tradingbot
go build -o bin/tradingbot-cli ./cmd/cli
```

There is no configuration file. The first start uses safe defaults (paper trading, manual mode); change settings in the dashboard with **⚙ Settings** in the header. Each save is confirmed first, and the dialog shows which changes apply immediately and which need a restart.

If you want LLM analysis, open LM Studio, load a model, and start its local server (Developer tab) on port 1234. Leave **LM Studio model** blank to use the first loaded model. Optionally set a **Fallback LLM** URL, model and API key for a remote endpoint that is used only while LM Studio is unreachable.

Upgrading from 0.7 or earlier: `.env` is no longer read; re-enter your settings in the dashboard. The full configuration reference is in [docs/01-configuration.md](docs/01-configuration.md).

## Run

On Windows, use the launcher from the project root:

```powershell
.\launch.bat
```

The launcher checks for Go, builds `bin\tradingbot.exe`, starts the bot in its own window, and opens the dashboard.

Manual startup is also supported:

```bash
go run ./cmd/tradingbot
```

The server's only flags are `-db` (database, default `./trading_bot.db`), `-host` (default `127.0.0.1`), `-port` (default `8000`), `-log` (default `trading_bot.log`) and `-frontend` (default `frontend`). No environment variables are read. The CLI can start the bot with the same flags, or ask a running bot for its status:

```powershell
.\bin\tradingbot-cli.exe -port 8000 run
.\bin\tradingbot-cli.exe status
```

After startup, open:

```text
http://127.0.0.1:8000
```

API health check:

```text
http://127.0.0.1:8000/health
```

Operational details are in [docs/12-operations.md](docs/12-operations.md).

## Run Tests

From the project root:

```powershell
go vet ./...
go test ./...
```

Or on Windows, `.\run_tests.bat` runs `go vet` and `go test -v ./...`. CI (`.github/workflows/go.yml`) also checks formatting with `gofmt` and runs the tests with the race detector.

Testing conventions and coverage notes are in [docs/13-testing.md](docs/13-testing.md).

## Documentation

- [Project overview](docs/00-overview.md)
- [Configuration reference](docs/01-configuration.md)
- [Domain models](docs/02-domain-models.md)
- [Background loops](docs/03-background-loops.md)
- [Risk engine](docs/04-risk-engine.md)
- [Execution engine](docs/05-execution-engine.md)
- [Strategy and learning](docs/06-strategy-and-learning.md)
- [LLM integration](docs/07-llm-integration.md)
- [API endpoints](docs/08-api-endpoints.md)
- [Ingestion adapters](docs/09-ingestion-adapters.md)
- [Approval and control](docs/10-approval-and-control.md)
- [Frontend](docs/11-frontend.md)
- [Operations guide](docs/12-operations.md)
- [Testing guide](docs/13-testing.md)
- [Todo and implementation notes](docs/99-todo.md)

## Live Trading Notes

Live trading sends real orders to Kraken. Before enabling it:

- Run in paper mode long enough to validate behavior.
- Review the risk settings (⚙ Settings → Risk & sizing).
- Enter the Kraken API key and secret under ⚙ Settings → Kraken, then restart the bot. They are stored encrypted for your Windows user and never shown again.
- Start in manual mode, then move to semi-automated before considering fully automated. Change the mode in the dashboard's **Trading settings** dialog (click the mode badge); every change asks for confirmation and is saved across restarts.
- Switch to live in the same dialog: you must type `LIVE`, and the switch is refused until Kraken API keys are saved and loaded (restart after saving them). Then enable live execution one market at a time with the per-market live toggle (`POST /api/control/markets/{market}/live-toggle`; no dashboard control yet). In paper mode the toggle never sends real orders.

Never commit database files (they hold your settings), API keys, or logs containing secrets.
