# Requirements

## 1. Product Scope

The product is a local desktop application that ingests Kraken market data and selected crypto news, runs one or more trading strategies, and either:

- emits signals only,
- requests approval before execution, or
- executes live trades automatically for explicitly enabled markets and strategies.

## 2. Functional Requirements

### 2.1 Modes of Operation

The system must support these modes in a single codebase:

- `manual`
- `semi_automated`
- `fully_automated`

The system must allow mode behavior to differ by strategy and by market where appropriate.

### 2.2 Trading Environments

The system must support:

- `paper_trading`
- `live_trading`

Live trading must be disabled by default.

The system must support per-strategy and per-market toggles for live execution. If a strategy or market is not explicitly live-enabled, orders must remain in paper mode even if other parts of the app are live-enabled.

### 2.3 Market Coverage

The system must support:

- `BTC/EUR`
- `ETH/EUR`
- ETH and the top 10 Ethereum ecosystem coins by market cap, resolved dynamically

The system must record which resolver source was used to determine the dynamic coin universe and when that list was last refreshed.

### 2.4 Market Data

The system must ingest live Kraken market data sufficient for:

- price monitoring
- candle generation or retrieval
- order book or spread awareness if needed by strategies
- trade simulation
- fee and slippage modeling

### 2.5 News Ingestion

The system must ingest content from:

- CoinDesk
- CoinNews
- CoinWeek

The system must extract or derive:

- headlines
- article summaries
- sentiment signals
- event tags or event classifications

The system must persist source attribution for all news-derived signals.

### 2.6 Strategy Engine

The system must support multiple strategies in one bot.

The system must allow strategies to consume:

- market indicators
- market structure signals
- news sentiment
- summary signals
- event detections

Each strategy must produce:

- rationale
- side
- confidence or conviction score
- entry conditions
- exit conditions
- invalidation conditions
- risk sizing proposal

### 2.7 Operator Controls

The system must provide operator control through:

- desktop UI controls
- CLI prompts

Operator controls must include:

- approve or reject trade
- pause strategy
- disable live execution
- flatten paper positions
- flatten live positions if live mode is enabled
- review rationale and source data

### 2.8 Risk Management

The system must enforce:

- max loss per trade of `5%` of current equity
- max daily loss of `5%` of current equity
- no execution when risk checks fail

The system must provide configurable risk limits, but the above values are the approved starting defaults.

### 2.9 Simulation

Paper trading must simulate:

- market prices
- fees
- slippage
- latency
- order state transitions

The simulation engine must produce an auditable event trail showing why a fill occurred at a given simulated price.

### 2.10 Logging and Auditability

The system must log:

- market inputs used for each decision
- news items used for each decision
- strategy outputs
- approvals and rejections
- orders submitted
- fills or simulated fills
- risk checks
- mode changes
- live/paper toggle changes
- errors and fallbacks

### 2.11 Desktop Support

The product must run on:

- Windows
- Linux

## 3. Non-Functional Requirements

### 3.1 Safety

- Fail closed on configuration ambiguity.
- Fail closed on missing credentials in live mode.
- Fail closed on unresolved market universe.
- No live order placement without explicit enablement.

### 3.2 Transparency

- Every trade decision must be inspectable.
- Every news-derived conclusion must reference source content.
- Every risk rejection must explain which rule blocked execution.

### 3.3 Maintainability

- Modular components for ingestion, analysis, strategy, risk, execution, and UI
- Clear interfaces between paper and live execution paths
- Strategy plugins or strategy modules designed for isolated testing

### 3.4 Reproducibility

- Configurable seeds where stochastic simulation is used
- Versioned configuration snapshots
- Strategy run metadata persisted with timestamps

### 3.5 Performance

The system does not need HFT latency, but it must react quickly enough for:

- intraday decisions
- swing strategies
- operator approvals without stale context

## 4. Constraints

- No paid APIs in v1
- Local desktop deployment only
- Kraken only in v1
- Spot only in v1

## 5. Open Questions

- Which exact external source defines the "top 10 Ethereum ecosystem coins by market cap"?
- What licensed or technically allowed ingestion method is acceptable for each news source?
- How should short exposure be supported under a spot-only v1 scope?
- Should certain strategies be prohibited from live mode until minimum paper performance thresholds are met?
