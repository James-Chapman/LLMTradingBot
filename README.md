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
| Settings | `.env` file in the repository root, overridden by real environment variables |
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
|   `-- backtest/            Historical replay against Kraken OHLC data
|-- internal/                Application packages (app, server, strategy, risk, paper,
|                            kraken, llm, news, storage, ...)
|-- frontend/                Dashboard HTML, approvals page, service worker
|-- docs/                    Detailed project documentation
|-- .env.example             Configuration template
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

`setup.bat` checks for Go, downloads the modules, builds `bin\tradingbot.exe` and `bin\tradingbot-cli.exe`, and creates `.env` from `.env.example` if it does not exist.

Manually, on any platform:

```bash
go mod download
go build -o bin/tradingbot ./cmd/tradingbot
go build -o bin/tradingbot-cli ./cmd/cli
cp .env.example .env
```

Then edit `.env` for your local settings. For a first run, keep:

```ini
TRADING_MODE=manual
TRADING_ENVIRONMENT=paper
```

If you want LLM analysis, open LM Studio, load a model, and start its local server (Developer tab) on port 1234. Leave `LMSTUDIO_MODEL` blank to use the first loaded model. Optionally set `OPENAI_BASE_URL`, `OPENAI_MODEL` and `OPENAI_API_KEY` for a remote fallback that is used only while LM Studio is unreachable. The full configuration reference is in [docs/01-configuration.md](docs/01-configuration.md).

## Run

On Windows, use the launcher from the project root:

```powershell
.\launch.bat
```

The launcher checks for Go, creates `.env` from the template if it is missing, builds `bin\tradingbot.exe`, starts the bot in its own window, and opens the dashboard.

Manual startup is also supported:

```bash
go run ./cmd/tradingbot
```

The server accepts `-env` (settings file, default `.env`) and `-frontend` (dashboard directory, default `frontend`). The CLI offers the same start-up with mode overrides:

```powershell
.\bin\tradingbot-cli.exe --mode semi_automated --environment paper run
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
- Review risk settings in `.env`.
- Add Kraken credentials only to your local `.env`.
- Start with `TRADING_MODE=manual`, then move to `semi_automated` before considering `fully_automated`.
- Set `TRADING_ENVIRONMENT=live`, then enable live execution one market at a time with the dashboard's per-market live toggle. In paper mode the toggle never sends real orders.

Never commit `.env`, database files, API keys, or logs containing secrets.
