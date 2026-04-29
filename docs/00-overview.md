# Kraken Trading Bot — Project Overview

## Purpose

A self-hosted, news-aware cryptocurrency trading bot that operates against the Kraken spot exchange. The system evaluates momentum signals, enriches them with real-time news sentiment via a local Large Language Model (LLM), enforces portfolio risk rules, and can execute trades in paper (simulated) or live mode.

The primary design goals are:

- **Auditability** — every signal, decision, and trade is persisted to a local SQLite database with a complete audit trail.
- **Safety** — a universal risk engine enforces hard position limits, cash sufficiency, and daily loss caps before any trade reaches execution.
- **Operator control** — a web dashboard exposes real-time state, manual overrides, market toggles, and an emergency stop at all times.
- **Adaptability** — an OpenAI-compatible or local Transformers LLM analyses each signal in full market context (indicators, portfolio, news, briefing, and its own prior reflection) and reflects hourly on closed trade outcomes including entry-time indicator snapshots, feeding patterns back into every subsequent signal decision.

---

## High-Level Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                          FastAPI Application                            │
│                              (main.py)                                  │
├──────────────┬───────────────┬──────────────┬───────────────────────────┤
│ Strategy     │  News         │  OHLC        │  Reflection               │
│ Loop (30s)   │  Loop (5min)  │  Loop (2min) │  Loop (1hr)               │
└──────┬───────┴───────┬───────┴──────┬───────┴───────────┬───────────────┘
       │               │              │                   │
       ▼               ▼              ▼                   ▼
┌─────────────┐ ┌───────────┐ ┌─────────────┐ ┌──────────────────┐
│  BasicStrat │ │ RSS feeds │ │ Kraken OHLC │ │  LLM Analyser    │
│  Learner    │ │ CoinDesk  │ │  (cache)    │ │  (LMStudio/      │
│  Indicators │ │ CoinTele  │ └─────────────┘ └──────────────────┘
└──────┬──────┘ └─────┬─────┘
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
                    │ PaperExecutionEngine / KrakenExecutionEngine │
                    └─────────────────────────────────────────┘
                                   │
                                   ▼
                    ┌─────────────────────────────────────────┐
                    │        SQLite / Repository              │
                    │  orders · positions · outcomes ·        │
                    │  equity · news · price ticks ·          │
                    │  signals · activity · control state ·   │
                    │  risk rejections · LLM briefings        │
                    └─────────────────────────────────────────┘
```

---

## Technology Stack

| Layer | Technology | Version / Notes |
|---|---|---|
| Web framework | FastAPI + Uvicorn | ASGI, async throughout |
| Frontend | Alpine.js + local CSS | No build step; utility styling in `/static/styles.css` |
| Charts | Lightweight Charts (candles), Chart.js (equity) | CDN-loaded |
| Database | SQLite via SQLAlchemy 2.x | `StaticPool`; single file |
| Settings | Pydantic v2 / pydantic-settings | `.env` file |
| Exchange API | krakenex + pykrakenapi | Read-only in paper mode |
| LLM | OpenAI-compatible API (preferred) → Transformers (fallback) | `SwitchingLLMClient` probes and routes |
| HTTP client | httpx (async) | Used for OpenAI-compatible chat-completions calls |
| Logging | Python `logging` → JSON + file | Structured; module-level |
| Numeric compute | numpy | Indicator reductions, replay metrics, learner statistics |

---

## Repository Layout

```text
LLMTradingBot/
|-- launch.bat                      Windows launcher (venv + uvicorn)
|-- requirements.txt                Runtime dependencies
|-- requirements-dev.txt            Developer tooling
|-- docs/                           Project documentation
|-- tests/                          BDD-style unittest coverage
|-- backend/
|   |-- main.py                     FastAPI app, background loops, all endpoints
|   |-- .env                        Runtime configuration (not committed)
|   |-- analysis/
|   |   `-- indicators.py           numpy-assisted technical indicators
|   |-- approval/
|   |   `-- service.py              Approval queue with TTL
|   |-- config/
|   |   `-- settings.py             Pydantic settings model
|   |-- control/
|   |   `-- state.py                Emergency stop, market toggles, strategy selection
|   |-- domain/
|   |   `-- models.py               Pydantic domain models
|   |-- execution/
|   |   |-- paper.py                Paper trading engine
|   |   `-- kraken.py               Live Kraken order engine
|   |-- ingestion/
|   |   |-- kraken_adapter.py       Kraken ticker and OHLC adapter
|   |   `-- news_adapter.py         RSS news ingestion
|   |-- llm/
|   |   |-- common.py               Shared JSON parsing, prompt, and circuit helpers
|   |   |-- openai_client.py        OpenAI-compatible chat-completions client
|   |   |-- switching_client.py     OpenAI/Transformers backend router
|   |   |-- transformers_client.py  Hugging Face Transformers async client
|   |   `-- analyser.py             Signal analysis, briefing, reflection
|   |-- observability/
|   |   |-- activity.py             Event log, memory and DB persisted
|   |   `-- logging.py              JSON structured logging setup
|   |-- risk/
|   |   `-- engine.py               Universal risk management
|   |-- storage/
|   |   |-- database.py             Engine creation and auto-migrations
|   |   |-- models.py               SQLAlchemy ORM table definitions
|   |   `-- repository.py           All DB read/write operations
|   |-- strategy/
|   |   |-- basic_strategy.py       Indicator-only momentum strategy
|   |   |-- basic_and_llm_strategy.py  Indicator consensus + LLM analysis strategy
|   |   |-- llm_only_strategy.py    LLM-led strategy with no indicator gating
|   |   `-- learner.py              Exponential-decay performance learner
|   `-- universe/
|       `-- resolver.py             Tradable market universe builder
`-- frontend/
    |-- index.html                  Main dashboard (Alpine.js SPA)
    |-- approvals.html              Dedicated approval queue page
    `-- sw.js                       Service Worker
```

---

## Operating Modes

### Trading Mode (`TRADING_MODE`)

| Mode | Behaviour |
|---|---|
| `manual` | Signals are generated and displayed in the dashboard. No orders are placed. |
| `semi_automated` | Each approved signal is queued for manual approval via the dashboard or approvals page. Operator clicks Approve or Reject. |
| `fully_automated` | Approved signals are executed immediately without human intervention. Stop-losses fire automatically. |

### Trading Environment (`TRADING_ENVIRONMENT`)

| Environment | Behaviour |
|---|---|
| `paper` | All executions are simulated by `PaperExecutionEngine` with realistic fee and slippage modelling. No real money moves. |
| `live` | Live-flagged markets submit spot orders to Kraken through `KrakenExecutionEngine`. Requires valid Kraken API credentials. |

The risk engine, one-position-per-pair rule, and cash sufficiency checks are enforced identically in both environments.

---

## Key Design Decisions

### 1. Universal Risk Engine
Risk checks run before mode routing, not inside each mode's branch. This means no signal can bypass cash limits or position constraints regardless of whether it came from manual approval or fully-automated execution.

### 2. Position ID as Primary Key
Positions are keyed by UUID (`position_id`), not by market symbol. This allows complete independent tracking of every trade's lifecycle and pairs BUY and SELL orders in the trade ledger via a shared `position_id`.

### 3. LLM as an Advisor with Veto Power and Self-Improvement Loop
The LLM adjusts confidence (`confidence_scale` 0.5–2.0) and annotates the thesis. It can also veto a signal entirely when its `confidence_scale` falls below `LLM_VETO_THRESHOLD` (default 0.70). Vetoes only fire when the active LLM backend is available. Final execution authority for all non-vetoed signals remains with the risk engine and the operator.

Every signal analysis prompt includes the LLM's own most recent hourly reflection (pattern + suggestion), closing the feedback loop so its advice influences subsequent decisions. The hourly reflection itself receives the full indicator state at the time of each trade entry (joined from `trade_ideas`), enabling indicator-level pattern detection rather than summary statistics alone.

### 4. Complete State Persistence
All dashboard-relevant state — positions, equity, price history, news, trade signals, risk rejections, activity log, control toggles, and LLM briefings/reflections — is stored in a local SQLite file. The bot can survive a restart and fully recover from the database with no loss of operational context.

### 5. numpy-Assisted Indicators
Technical indicators (`analysis/indicators.py`) use numpy for scalar reductions such as mean, standard deviation, min/max, and close-to-close absolute movement. EMA series remain explicit loops because each EMA value depends on the previous value.
