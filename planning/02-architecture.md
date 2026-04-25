# Architecture

## 1. Overview

The system should be designed as a local desktop application with a small number of well-separated subsystems:

1. Desktop shell and operator interface
2. Market and news ingestion
3. Normalization and feature extraction
4. Strategy engine
5. Risk engine
6. Execution engine
7. Simulation engine
8. Storage and audit log

## 2. Tech Stack

### 2.1 Decided

| Layer | Technology | Notes |
|---|---|---|
| Language | Python 3.14 | Minimum required version |
| Backend framework | FastAPI | Async-native, WebSocket support, automatic OpenAPI docs |
| Domain models and validation | Pydantic v2 | Typed domain objects, config validation, strategy output schemas |
| Storage | SQLite via SQLAlchemy | Local file, no external service required |
| UI delivery | FastAPI serving static HTML | Browser-based; no Electron or Node.js |
| Browser target | Firefox | Primary supported browser |
| UI reactivity | Alpine.js (CDN) | Reactive state and event handling in HTML attributes; no build step |
| UI data visualisation | Chart.js (CDN) | Price charts, equity curves, PnL graphs |
| UI server communication | htmx (CDN) | HTTP fragment updates and WebSocket bindings without custom fetch() code |
| UI styling | Tailwind CSS (CDN play build) | Utility classes; no build step required |
| CLI entry point | Python (stdlib + `argparse`) | CLI approval flow shares the same approval service as the UI |

### 2.2 Constraints

- No Node.js or any other runtime outside Python.
- All frontend dependencies loaded from CDN. No local npm or build pipeline.
- The browser UI is served by FastAPI. There is no separate web server process.
- WebSockets (via FastAPI) handle live price feeds and approval queue updates to the browser.

### 2.3 Project Layout

```
kraken-bot/
├── backend/
│   ├── main.py              # FastAPI app entry point
│   ├── config/              # Config loading, secrets, env
│   ├── domain/              # Pydantic domain models
│   ├── ingestion/           # Kraken adapter, news adapters
│   ├── universe/            # Universe resolver
│   ├── features/            # Feature store, indicators
│   ├── strategy/            # Strategy engine + strategies
│   ├── risk/                # Risk engine
│   ├── execution/           # Paper sim + live executor
│   ├── approval/            # Approval service (UI + CLI shared)
│   ├── storage/             # SQLAlchemy models + DB init
│   └── observability/       # Structured logging
├── frontend/
│   ├── index.html           # Main dashboard
│   ├── approvals.html       # Approval queue
│   └── static/              # CSS overrides, any local JS
└── cli.py                   # CLI entry point
```

### 2.4 Open Stack Decisions

- No decision yet on which NLP or summarisation approach to use for news signals. This must be resolved before Phase 3 news work begins.

### 2.5 Decided: News Ingestion Method

RSS is the confirmed ingestion approach for public crypto news sources.

| Source | RSS URL | Status |
|---|---|---|
| CoinDesk | `https://www.coindesk.com/arc/outboundfeeds/rss/` | Implemented |
| CoinTelegraph | `https://cointelegraph.com/rss` | Implemented (supplements planning list) |
| CoinNews | TBD | Stub — no confirmed RSS found |
| CoinWeek | TBD | Stub — no confirmed RSS found |

RSS is publicly provided by these outlets for syndication and requires no authentication or scraping. Python stdlib (`urllib`, `xml.etree.ElementTree`) is used; no third-party RSS library is needed.

## 3. Proposed High-Level Components

### 3.1 Desktop App Layer

Responsibilities:

- Render dashboards, settings, and approvals
- Show strategy rationale and source attribution
- Expose live/paper toggles per strategy and market
- Expose emergency stop controls
- Surface logs, alerts, and error states

Notes:

- The UI is a browser tab pointed at the local FastAPI server. No desktop framework is required.
- CLI approval flow uses the same underlying approval service as the browser UI to avoid diverging logic.

### 3.2 Config and Secrets Layer

Responsibilities:

- Store non-secret configuration
- Load secrets for Kraken credentials
- Separate paper mode settings from live mode settings
- Track enabled strategies, markets, and execution permissions

Rules:

- Live credentials must never be required for paper-only operation.
- Missing or malformed config must fail closed in live mode.

### 3.3 Universe Resolver

Responsibilities:

- Resolve fixed markets: `BTC/EUR`, `ETH/EUR`
- Resolve dynamic ETH ecosystem list
- Map resolved coins to Kraken-supported tradable markets
- Track resolver source, timestamp, and mapping results

Key design note:

This component should produce a canonical "tradable universe" snapshot. Strategies and execution should only read from that snapshot, not from ad hoc symbol lists.

### 3.4 Market Data Ingestion

Responsibilities:

- Connect to Kraken APIs or feeds
- Collect ticker, candles, and optional order book data
- Normalize timestamps and symbol naming
- Detect data gaps and stale feeds

Outputs:

- normalized market events
- candles
- spread and fee context

### 3.5 News Ingestion and NLP Layer

Responsibilities:

- Fetch or parse configured news content
- Normalize articles into a shared schema
- Generate headline sentiment
- Generate article summaries
- Detect market-relevant events
- Link events and sentiment to assets

Outputs:

- `NewsItem`
- `NewsSignal`
- `EventSignal`

Important caution:

No specific ingestion mechanism should be assumed until source access terms are confirmed.

### 3.6 Feature Store / Derived Signals Layer

Responsibilities:

- Combine market features and news features
- Produce strategy-ready views
- Cache recent computed indicators
- Support replay for paper-trading evaluation

Possible outputs:

- momentum indicators
- volatility metrics
- spread/fee awareness
- sentiment aggregates
- event severity flags

### 3.7 Strategy Engine

Responsibilities:

- Run multiple strategies independently
- Produce candidate trades with rationale
- Support multiple time horizons
- Allow strategy-level enable/disable and live toggle flags

Interface shape:

- Input: normalized features + config + tradable universe
- Output: `TradeIdea` objects

### 3.8 Risk Engine

Responsibilities:

- Validate every candidate trade before approval or execution
- Enforce per-trade and daily equity loss constraints
- Enforce market and strategy permissions
- Reject actions when data quality is insufficient

This engine should be mandatory for both paper and live paths.

### 3.9 Approval Service

Responsibilities:

- Receive risk-approved trade ideas
- Route approval requests to browser UI and CLI
- Track approval status, expiry, and operator identity

Modes:

- manual mode: store and display, no order submission
- semi-automated mode: require approval before submit
- fully automated mode: bypass manual approval only when live or paper execution is allowed for the relevant strategy and market

### 3.10 Execution Engine

Responsibilities:

- Translate approved trade intents into exchange-specific orders
- Submit and track Kraken orders in live mode
- Handle retries conservatively
- Confirm final order state

Rules:

- No direct strategy-to-exchange path
- All submissions must pass through risk and permission checks

### 3.11 Simulation Engine

Responsibilities:

- Model fills using live market data
- Apply fees, slippage, and latency
- Simulate order lifecycle transitions
- Produce deterministic replay when feasible

### 3.12 Storage Layer

Suggested responsibilities:

- configuration snapshots
- strategy state
- news cache
- market cache
- orders
- fills
- approvals
- audits
- PnL history

### 3.13 Observability Layer

Responsibilities:

- structured logs
- event timeline
- error reporting
- health indicators
- "why was this trade taken?" traces

## 4. Suggested Data Flow

1. Resolve tradable universe.
2. Ingest market data and news.
3. Normalize and enrich data.
4. Generate strategy signals.
5. Apply risk checks.
6. Route for manual display, approval, or direct execution based on mode and permissions.
7. Send to paper simulator or live execution engine.
8. Persist outcomes and update dashboards.

## 5. Core Domain Objects

Suggested domain models:

- `MarketSnapshot`
- `NewsItem`
- `NewsSignal`
- `EventSignal`
- `StrategyContext`
- `TradeIdea`
- `RiskDecision`
- `ApprovalRequest`
- `ExecutionIntent`
- `OrderRecord`
- `FillRecord`
- `PositionRecord`
- `EquitySnapshot`
- `UniverseSnapshot`

## 6. Key Architecture Decisions

- One code path for decision-making, split only at execution target (`paper` vs `live`)
- A single risk engine for all modes
- Explicit permissions for strategy/market live execution
- Canonical universe resolution before trading begins
- Source attribution stored alongside every news-derived signal
- UI served by FastAPI; no separate desktop runtime or Node.js process

## 7. Architecture Risks

- News access may be brittle if scraping is required
- Dynamic asset universe may drift or remap unpredictably
- "Short" support may conflict with spot-only scope and should be modeled as a capability matrix, not assumed universal behavior
- CDN dependency for frontend libraries means the UI will not load without internet access; a local vendor copy should be considered for offline resilience
