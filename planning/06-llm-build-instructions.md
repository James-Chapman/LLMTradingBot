# LLM Build Instructions

## Purpose

These instructions are for any LLM or coding agent implementing this project from the planning pack.

## Operating Rules

- Do not assume unspecified requirements are approved.
- If a decision is missing and materially changes safety, architecture, legality, or trading behavior, stop and ask.
- Prefer explicit `TBD` markers over hidden assumptions.
- Treat paper trading as the default and safest path.
- Treat live trading as opt-in and fail closed.

## Source Documents

Read these files before implementation:

1. `planning/00-project-brief.md`
2. `planning/01-requirements.md`
3. `planning/02-architecture.md`
4. `planning/03-risk-register.md`
5. `planning/04-strategy-spec.md`
6. `planning/05-implementation-roadmap.md`

## Decided Tech Stack

Use exactly these technologies. Do not introduce alternatives without operator approval.

| Layer | Technology |
|---|---|
| Language | Python 3.14 (minimum) |
| Backend framework | FastAPI |
| Domain models and validation | Pydantic v2 |
| Storage | SQLite via SQLAlchemy |
| UI delivery | FastAPI serving static HTML files |
| Browser target | Firefox |
| UI reactivity | Alpine.js (CDN) |
| UI data visualisation | Chart.js (CDN) |
| UI server communication | htmx (CDN) |
| UI styling | Tailwind CSS (CDN play build) |
| CLI | Python stdlib (`argparse`) |

**No Node.js. No Electron. No build pipeline.** All frontend libraries load from CDN. The browser UI is a tab pointed at `http://localhost:<port>`.

## Build Priorities

Implement in this order unless the operator changes scope:

1. Foundation and configuration
2. Kraken market ingestion
3. Paper trading engine
4. Risk engine
5. Strategy framework
6. Operator controls
7. News ingestion and analysis
8. Live trading guardrails

## Safety Constraints

- Never create a direct path from strategy output to live exchange order submission.
- Every trade idea must pass through a centralized risk engine.
- Live trading must remain disabled by default.
- Live execution must be enabled separately per strategy and per market.
- If mode, config, credentials, or market resolution is ambiguous, fail closed.

## Required Behaviors

- Preserve clear separation between paper and live execution targets.
- Log rationale, source inputs, approvals, and risk decisions.
- Ensure UI and CLI approval paths share the same underlying approval logic.
- Keep the dynamic market universe auditable by storing source, timestamp, and resolved mapping.

## When To Ask The Operator

Ask before proceeding if any of the following are still unresolved:

- canonical source for Ethereum ecosystem market-cap ranking
- permitted ingestion method for the named news sources
- exact interpretation of short support under spot-only scope
- storage engine choice if it materially affects deployment or auditability
- NLP approach for news sentiment and summarisation

## Implementation Style

- Prefer modular, testable components with clear boundaries.
- Keep data models explicit and typed using Pydantic.
- Avoid coupling UI concerns to strategy or execution logic.
- Build one useful vertical slice before broadening strategy count.
- Use `async`/`await` throughout the FastAPI layer; Kraken WebSocket feeds require async.

## Minimum Deliverable For First Coding Pass

The first implementation should aim for:

- FastAPI app skeleton serving the frontend
- Config loading with Pydantic settings
- Kraken market data ingestion
- One basic strategy
- Centralized risk engine
- Paper trading execution
- Approval workflow in UI and CLI
- Structured audit logging to SQLite

## Prohibited Assumptions

- Do not assume all top 10 Ethereum ecosystem coins are tradable on Kraken.
- Do not assume shorting is available just because the operator wants long and short.
- Do not assume scraping is allowed for any news source.
- Do not assume paper performance predicts live performance.
- Do not introduce any runtime dependency outside the Python 3.14 standard library and approved packages above.

## Definition Of Success For The First Build

Success for the first build is not profitability.

Success means:

- the system runs locally on Windows and Linux,
- a browser tab at localhost shows the dashboard,
- paper trades can be generated and reviewed,
- risk rules are enforced,
- rationale is inspectable,
- the route to live trading is intentionally gated.
