# Implementation Roadmap

## Phase 0: Clarify External Dependencies

Goal:

Reduce project ambiguity before coding deep integration work.

Tasks:

- Confirm legally and technically acceptable ingestion paths for CoinDesk, CoinNews, and CoinWeek
- Select the canonical market-cap source for the dynamic ETH ecosystem universe
- Confirm which in-scope assets are tradable on Kraken against EUR or via an approved fallback rule
- Resolve whether short support is actually feasible in a spot-only v1

Exit criteria:

- External source decisions documented
- Dynamic universe resolution rule approved
- Short capability documented as supported, unsupported, or deferred

## Phase 1: Foundation

Goal:

Create the project skeleton and core domain model.

Stack confirmed for this phase: Python 3.14, FastAPI, Pydantic v2, SQLAlchemy + SQLite.

Tasks:

- Set up repository structure (see `02-architecture.md` section 2.3 for layout)
- Implement FastAPI app entry point with static file serving for the frontend
- Define Pydantic settings and configuration format
- Define Pydantic domain objects
- Implement structured logging (JSON lines to file and stdout)
- Implement SQLAlchemy models and DB initialisation
- Add frontend shell (`index.html`, `approvals.html`) with Alpine.js, htmx, Chart.js, Tailwind CSS loaded from CDN
- Add `cli.py` entry point with `argparse`

Exit criteria:

- `python backend/main.py` starts the FastAPI server on Windows and Linux
- Browser at `http://localhost:<port>` renders the dashboard shell
- Config loads successfully and validates with Pydantic
- Logging writes structured records to file
- SQLite schema initialises cleanly

## Phase 2: Ingestion Layer

Goal:

Integrate market and news data safely.

Tasks:

- Implement Kraken market data adapter (REST + WebSocket, async)
- Implement universe resolver
- Implement news adapters (placeholder structure; real adapters gated on Phase 0 decisions)
- Normalize all incoming data
- Persist source attribution and timestamps
- Detect stale or failed feeds
- Push live price updates to browser via FastAPI WebSocket

**Progress notes (2026-04-22):**
- Kraken REST adapter complete (batch ticker, pair validation, thread-safe async)
- RSS ingestion live for CoinDesk and CoinTelegraph; news displayed in dashboard feed
- Universe resolver uses placeholder ETH ecosystem list pending canonical source decision
- WebSocket price push deferred; polling adequate for current strategy horizons

Exit criteria:

- Market data visible in dashboard ✓
- News items visible in dashboard ✓
- Universe snapshot generated and persisted with provenance ✓ (placeholder source noted)

## Phase 3: Paper Trading Core

Goal:

Build the safe execution path first.

Tasks:

- Implement strategy engine interfaces
- Implement first strategy or small strategy set
- Implement risk engine
- Implement paper execution engine
- Model fees, slippage, latency
- Implement approval workflow: browser buttons via htmx + CLI prompts via `cli.py`

**Progress notes (2026-04-22):**
- Strategy, risk engine, paper execution engine, and approval service all implemented
- Approval → paper execution wired: approving via browser or CLI now triggers `PaperExecutionEngine.execute()`
- CLI `approve`, `reject`, and `run` commands implemented (proxy to HTTP API)
- Fees (0.26% taker) and slippage (0.1%) modelled in paper engine

Exit criteria:

- Paper trades can be reviewed, approved, simulated, and audited ✓
- Risk rules block invalid trades ✓
- Approval queue visible and functional in browser ✓
- CLI approve/reject wired to running server ✓

## Phase 4: Operator Experience

Goal:

Make the system understandable and controllable.

Tasks:

- Dashboard for markets, signals, positions, and equity (Chart.js for equity curves)
- Approval queue with rationale display
- Risk rejection views
- Strategy and market toggles
- Emergency stop controls

**Progress notes (2026-04-22):**
- `ControlState` module added (`backend/control/state.py`) — thread-safe emergency stop, per-market and per-strategy toggles
- Strategy loop respects all control flags; pauses cleanly on emergency stop
- Risk rejections now buffered (last 50) and exposed in dashboard API
- Signals now include `risk_reason` so the dashboard can explain blocked trades
- New API endpoints: `POST /api/control/emergency-stop`, `/resume`, `/markets/{m}/toggle`, `/strategies/{s}/toggle`, `GET /api/control`
- Emergency stop blocks the approve endpoint (returns 503) and clears the approval queue
- Dashboard rebuilt: market on/off toggles per row, strategy on/off toggles, risk rejections panel, emergency stop button in header with confirmation dialog, stop banner when active

Exit criteria:

- Operator can understand why the bot wants to trade ✓
- Operator can enable or disable strategies and markets safely ✓
- Emergency stop halts all trading instantly and clears queue ✓
- Risk rejections visible with reason and timestamp ✓

## Phase 5: Replay and Evaluation

Goal:

Improve confidence before live trading.

Tasks:

- Add historical replay support where data is available
- Add performance metrics
- Add paper-trading reports
- Add signal quality diagnostics

Exit criteria:

- Strategy results can be reviewed over time
- Weak strategies can be identified before live rollout

## Phase 6: Live Trading Guardrails

Goal:

Introduce live trading only after safety controls exist.

Tasks:

- Add Kraken live order adapter
- Enforce live mode credentials and permission checks
- Add per-strategy and per-market live toggles
- Add stronger confirmations around live enablement
- Add separate live audit log markers

Exit criteria:

- Live orders cannot be placed accidentally
- Live execution path is auditable and clearly distinct from paper mode

## Phase 7: Hardening

Goal:

Stabilize behavior and reduce operational risk.

Tasks:

- Test feed failure handling
- Test app restart recovery
- Test mode transitions
- Test risk-limit edge cases
- Test low-capital fee scenarios
- Review logs for secret leakage
- Vendor CDN frontend libraries for offline resilience

Exit criteria:

- Critical failure modes covered by tests
- Operational runbook drafted

## Recommended First Build Slice

Build this slice first:

1. FastAPI app skeleton serving frontend HTML
2. Kraken market data ingestion (WebSocket feed)
3. News ingestion placeholder with one source adapter stub
4. Universe resolver stub
5. One strategy
6. Risk engine
7. Paper execution engine
8. Approval workflows (browser + CLI)

This creates a useful vertical slice without prematurely exposing live capital.
