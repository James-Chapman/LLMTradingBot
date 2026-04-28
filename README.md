# LLMTradingBot

### // This project is still in development

---

LLMTradingBot is a self-hosted cryptocurrency trading bot for Kraken spot markets. It collects Kraken market data, generates strategy signals, optionally asks a local Ollama LLM to analyse or veto those signals, applies risk controls, and exposes a FastAPI-powered dashboard for monitoring and operator control.

The bot is designed to run safely in paper mode first. Live trading is supported for Kraken, but only after you provide Kraken API credentials and explicitly enable live execution.

> This project is trading software, not financial advice. Use paper mode until you understand the strategy, risk settings, and failure modes.

## What It Does

- Monitors configured Kraken markets such as `BTC/EUR` and `ETH/EUR`.
- Generates trade ideas from technical indicators, momentum, market context, and optional LLM analysis.
- Enforces risk checks before any order can be executed.
- Supports `manual`, `semi_automated`, and `fully_automated` trading modes.
- Supports paper execution and live Kraken execution.
- Persists signals, orders, positions, equity, news, risk rejections, and control state to SQLite.
- Serves a browser dashboard for positions, equity, market status, activity, approvals, and emergency stop.

## Tech Stack

| Area | Technology |
|---|---|
| Backend | Python, FastAPI, Uvicorn, async background tasks |
| Database | SQLite through SQLAlchemy 2.x |
| Settings | Pydantic v2 and pydantic-settings, loaded from `backend/.env` |
| Exchange | krakenex and pykrakenapi |
| LLM | Ollama local REST API, `phi3:mini` by default |
| HTTP client | httpx |
| Numeric compute | numpy and pandas |
| Frontend | Alpine.js, local CSS, Chart.js, Lightweight Charts |
| Tests and linting | unittest BDD tests, Ruff |

## Repository Layout

```text
.
|-- backend/                 FastAPI app, strategies, risk, execution, storage
|-- frontend/                Dashboard HTML, approvals page, service worker
|-- tests/                   BDD-style unittest suite
|-- docs/                    Detailed project documentation
|-- requirements.txt         Runtime Python dependencies
|-- requirements-dev.txt     Developer tools
|-- launch.bat               Windows launcher
`-- README.md                Project entry point
```

## Prerequisites

- Python 3.11 or newer.
- Windows PowerShell or Command Prompt for the provided launcher.
- Ollama, optional but recommended for LLM-backed analysis.
- A Kraken account and API key only if you plan to use live trading.

SQLite is included with Python; there is no separate database server to install.

## Install

From the project root:

```powershell
python -m venv .venv
.\.venv\Scripts\python.exe -m pip install --upgrade pip
.\.venv\Scripts\python.exe -m pip install -r requirements.txt
Copy-Item backend\.env.example backend\.env
```

Then edit `backend/.env` for your local settings. For a first run, keep:

```ini
TRADING_MODE=manual
TRADING_ENVIRONMENT=paper
```

If you want LLM analysis, install Ollama and pull the default model:

```powershell
ollama pull phi3:mini
```

If using Llama.cpp (recommended for local deployment), ensure the server is running and the model is pulled via its respective tool/CLI. The full configuration reference is in [docs/01-configuration.md](docs/01-configuration.md).

## Run

On Windows, use the launcher from the project root:

```powershell
.\launch.bat
```

The launcher creates `.venv` if missing, checks whether dependencies are current, updates the virtual environment when needed, starts Ollama if it is available, runs the backend with `.venv\Scripts\python.exe`, and opens the dashboard.

Manual startup is also supported:

```powershell
cd backend
..\.venv\Scripts\python.exe main.py
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
.\.venv\Scripts\python.exe -m unittest discover -s tests
.\.venv\Scripts\python.exe -m ruff check .
```

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
- Review risk settings in `backend/.env`.
- Add Kraken credentials only to your local `.env`.
- Start with `TRADING_MODE=manual`, then move to `semi_automated` before considering `fully_automated`.

Never commit `backend/.env`, database files, API keys, or logs containing secrets.
