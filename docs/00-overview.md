# Kraken Trading Bot — Project Overview

## Purpose

A self-hosted, news-aware cryptocurrency trading bot that operates against the Kraken spot exchange. The system evaluates momentum signals, enriches them with real-time news sentiment via a Large Language Model (LLM) served by LM Studio, enforces portfolio risk rules, and can execute trades in paper (simulated) or live mode.

The bot is written in Go (module `github.com/James-Chapman/LLMTradingBot`, Go 1.27). Version 0.6.0 is the Go rewrite of the earlier Python/FastAPI implementation; existing SQLite databases created by the Python version open and keep working.

The primary design goals are:

- **Auditability** — every signal, decision, and trade is persisted to a local SQLite database with a complete audit trail.
- **Safety** — a universal risk engine enforces hard position limits, cash sufficiency, and daily loss caps before any trade reaches execution.
- **Operator control** — a web dashboard exposes real-time state, manual overrides, market toggles, and an emergency stop at all times.
- **Adaptability** — an LLM (LM Studio first, a remote OpenAI-compatible endpoint as fallback) analyses each signal in full market context (indicators, portfolio, news, briefing, and its own prior reflection) and reflects hourly on closed trade outcomes including entry-time indicator snapshots, feeding patterns back into every subsequent signal decision.

---

## High-Level Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│              cmd/tradingbot — net/http server + app.App                 │
│              (background loops run as goroutines)                       │
├──────────────┬───────────────┬──────────────┬─────────────┬─────────────┤
│ Strategy     │  News         │  OHLC        │  Reflection │  Equity     │
│ Loop (30s)   │  Loop (5min)  │  Loop (2min) │  Loop (1hr) │  Ticker(10s)│
└──────┬───────┴───────┬───────┴──────┬───────┴──────┬──────┴─────────────┘
       │               │              │              │
       ▼               ▼              ▼              ▼
┌─────────────┐ ┌───────────┐ ┌─────────────┐ ┌──────────────────┐
│  Strategies │ │ RSS feeds │ │ Kraken OHLC │ │  LLM Analyser    │
│  Learner    │ │ Fear &    │ │  (cache)    │ │  (LM Studio →    │
│  Indicators │ │ Greed     │ └─────────────┘ │   remote API)    │
└──────┬──────┘ └─────┬─────┘                 └──────────────────┘
       │              │ new articles detected
       ▼              ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                         Risk Engine                                     │
│  position limit · cash check · min size · per-trade loss · daily loss  │
└──────────────────────────────────┬──────────────────────────────────────┘
                                   │ approved
       ┌───────────────────────────┼────────────────────────┐
       ▼                           ▼                        ▼
  Manual mode              Semi-automated            Fully automated
  (signal only)         (Approval queue)          (immediate execution)
                               │                        │
                               ▼                        ▼
                    ┌─────────────────────────────────────────┐
                    │   paper.Engine / kraken.ExecutionEngine │
                    └─────────────────────────────────────────┘
                                   │
                                   ▼
                    ┌─────────────────────────────────────────┐
                    │        SQLite / storage.Repository      │
                    │  orders · positions · outcomes ·        │
                    │  equity · news · price ticks ·          │
                    │  signals · activity · control state ·   │
                    │  risk rejections · LLM briefings        │
                    └─────────────────────────────────────────┘
```

Live prices reach the strategy loop through the Kraken WebSocket v2 ticker channel; if the stream fails the market adapter falls back to REST ticker polling.

---

## Technology Stack

| Layer | Technology | Version / Notes |
|---|---|---|
| Language | Go | 1.27; single static binary per command |
| Web server | `net/http` (`http.ServeMux`) | Package `internal/server`; Host-header check always on; access token when `-host` is not loopback; graceful shutdown on Ctrl+C / SIGTERM (a second Ctrl+C quits at once) |
| Concurrency | Goroutines + `context.Context` | One goroutine per background loop; cancellation via the root context |
| Frontend | Alpine.js + local CSS | No build step; a design system (tokens and components) in `/static/styles.css`, one script per view in `/static/app/` |
| Charts | Lightweight Charts (candles), Chart.js (equity) | CDN-loaded |
| Database | SQLite via `database/sql` + `modernc.org/sqlite` | Pure Go (no cgo); one shared connection (`SetMaxOpenConns(1)`); WAL journal; schema compatible with Python-created DBs |
| Settings | `internal/config` (`Settings` struct + field registry) | Built-in defaults → values saved from the dashboard's Settings dialog (SQLite `settings` table, secrets DPAPI-encrypted) → start-up flags `-db -host -port -log`. No `.env`, no environment variables |
| Exchange API | Official Kraken Go SDK `github.com/krakenfx/api-go/v2` | REST plus WebSocket v2 ticker; market data is public and key-free |
| LLM | LM Studio (OpenAI-compatible, preferred) → remote OpenAI-compatible API (fallback) | `llm.SwitchingClient` probes and routes |
| HTTP client | `net/http` | Used for LLM chat-completions, RSS, Fear & Greed, CoinGecko, alert webhook |
| Logging | `log/slog` JSON handler | stdout + the `-log` file; LLM components also write `llm.log` |
| Numeric compute | Go standard library (`math`) | Indicator reductions, replay metrics, learner statistics |

---

## Repository Layout

```text
LLMTradingBot/
|-- go.mod / go.sum                 Module github.com/James-Chapman/LLMTradingBot
|-- setup.bat                       Windows setup (Go toolchain check, module download, build)
|-- launch.bat                      Windows launcher (go build + run + open dashboard)
|-- run_tests.bat                   Runs go test
|-- docs/                           Project documentation
|-- cmd/
|   |-- tradingbot/main.go          Server entry point (flags -db, -host, -port, -log, -frontend)
|   |-- cli/main.go                 Operator CLI: status, approve, reject, run
|   `-- backtest/                   Backtest CLI: run, split, walkforward, regimes, sensitivity, compare, data
|-- internal/
|   |-- analysis/                   Technical indicators (pure Go)
|   |-- app/                        Wiring, runtime state, background loops, operations, views
|   |   `-- apptest/                Network-free fakes for tests
|   |-- approval/service.go         Approval queue with TTL
|   |-- backtest/                   Replay of cached Kraken trades, reports, robustness tests
|   |-- config/                     Settings struct, defaults, dashboard field registry, currency symbols
|   |-- control/state.go            Emergency stop, market toggles, strategy selection, trading mode/environment
|   |-- domain/models.go            Core value types and enums
|   |-- kraken/                     Kraken SDK wrapper: market data, live execution, retry
|   |-- learner/learner.go          Exponential-decay performance learner
|   |-- llm/
|   |   |-- client.go               Client / Backend interfaces and sentinel errors
|   |   |-- common.go               JSON parsing, prompt helpers, circuit breaker
|   |   |-- openai_client.go        OpenAI-compatible client (LM Studio and remote)
|   |   |-- switching_client.go     LM Studio → remote fallback router
|   |   |-- pyfmt.go                Python-compatible number formatting for prompts
|   |   `-- analyser.go             Signal analysis, briefing, reflection
|   |-- news/                       RSS/Atom feeds and Fear & Greed adapter
|   |-- observability/              slog JSON logging and the activity log
|   |-- paper/                      Paper trading engine and operator reset
|   |-- portfolio/equity.go         Mark-to-market equity snapshots
|   |-- risk/                       Universal risk management and state persistence
|   |-- server/server.go            HTTP routes, static frontend, Host check and access-token login
|   |-- storage/                    SQLite schema, migrations and the Repository
|   |-- strategy/
|   |   |-- strategy.go             Strategy interface, IDs, registry
|   |   |-- basic.go                Indicator-only momentum strategy
|   |   |-- basic_and_llm.go        Indicator consensus + LLM analysis strategy
|   |   |-- llm_only.go             LLM-led strategy with no indicator gating
|   |   |-- indicator_signal.go     Shared indicator-gated signal builder
|   |   `-- calc/                   Shared decision formulas and constants
|   `-- universe/resolver.go        Tradable market universe builder (fixed + CoinGecko coins in BASE_CURRENCY)
`-- frontend/
    |-- index.html                  Main dashboard: markup of the shell and its six views (Alpine.js)
    |-- approvals.html              Dedicated approval queue page
    |-- login.html                  Access-token form (only used when -host is not loopback)
    |-- static/styles.css           Design system: tokens, components, view layouts, breakpoints
    |-- static/app/                 Dashboard scripts: core.js plus one file per view (see 11-frontend.md)
    `-- sw.js                       Service Worker
```

---

## Operating Modes

### Trading Mode (`TRADING_MODE`)

Both can be changed at runtime from the dashboard's **Trading settings** dialog (click the PAPER/LIVE and mode badges in the header). A choice made there is saved in `control_state` and kept across restarts; a fresh database starts in manual mode on paper. See [10-approval-and-control.md](10-approval-and-control.md#trading-settings).

| Mode | Behaviour |
|---|---|
| `manual` | Signals are generated and displayed in the dashboard. No orders are placed. |
| `semi_automated` | Each approved signal is queued for manual approval via the dashboard or approvals page. Operator clicks Approve or Reject. |
| `fully_automated` | Approved signals are executed immediately without human intervention. Stop-losses fire automatically. |

### Trading Environment (`TRADING_ENVIRONMENT`)

| Environment | Behaviour |
|---|---|
| `paper` | All executions are simulated by `paper.Engine` with realistic fee and slippage modelling. No real money moves. |
| `live` | Live-flagged markets submit spot orders to Kraken through `kraken.ExecutionEngine`. Requires valid Kraken API credentials; without them the live engine refuses every order locally. |

The risk engine, one-position-per-pair rule, and cash sufficiency checks are enforced identically in both environments.

Order routing is per market, gated by environment: an order goes to `kraken.ExecutionEngine` only when the environment is `live` **and** the market is toggled live in the dashboard (`control_state.live_markets`). Every other order goes to `paper.Engine`; in the paper environment a live-toggled market trades on paper and logs a warning. The environment also selects where the dashboard's equity and cash come from (paper engine or Kraken balances) and whether live orders are reconciled.

---

## Key Design Decisions

### 1. Universal Risk Engine
Risk checks run before mode routing, not inside each mode's branch. This means no signal can bypass cash limits or position constraints regardless of whether it came from manual approval or fully-automated execution.

### 2. Position ID as Primary Key
Positions are keyed by UUID (`position_id`), not by market symbol. This allows complete independent tracking of every trade's lifecycle and pairs BUY and SELL orders in the trade ledger via a shared `position_id`.

### 3. LLM as an Advisor with Veto Power and Self-Improvement Loop
The LLM adjusts confidence (`confidence_scale` 0.5–2.0) and annotates the thesis. It can also veto a signal entirely when its `confidence_scale` falls below `LLM_VETO_THRESHOLD` (default 0.70). Vetoes only fire when an LLM backend is available. Final execution authority for all non-vetoed signals remains with the risk engine and the operator.

Every signal analysis prompt includes the LLM's own most recent hourly reflection (pattern + suggestion), closing the feedback loop so its advice influences subsequent decisions. The hourly reflection itself receives the full indicator state at the time of each trade entry (joined from `trade_ideas`), enabling indicator-level pattern detection rather than summary statistics alone.

### 4. Complete State Persistence
All dashboard-relevant state — positions, equity, price history, news, trade signals, risk rejections, activity log, control toggles, and LLM briefings/reflections — is stored in a local SQLite file. The bot can survive a restart and fully recover from the database with no loss of operational context.

### 5. Dependency-Free Indicators
Technical indicators (`internal/analysis`) are plain Go loops over `[]float64` using the `math` package. Standard deviation is the population form, matching the numpy results of the Python version. EMA series are computed iteratively because each EMA value depends on the previous value.

### 6. Shared State Behind One Mutex
The Python version kept runtime state in module globals touched only by the asyncio event loop. In Go, `app.App` owns that state behind a single `sync.Mutex` that is never held across network, LLM, or exchange calls; the component services (paper engine, control state, approvals, risk, learner, analyser, activity log) are each safe for concurrent use.
