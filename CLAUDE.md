## Project Overview
This is a trading bot software project to trade crypto currencies

## Backend

### Language & Stack
- Language: Go 1.27 (module github.com/James-Chapman/LLMTradingBot); entry points in cmd/, packages in internal/.
- Web framework: net/http ServeMux (Go 1.22+ method/wildcard patterns), background loops as goroutines with context cancellation.
- Database: SQLite via database/sql + modernc.org/sqlite (pure Go, no CGO; single connection, WAL).
- Settings: internal/config — .env file in the repo root, overridden by environment variables.
- Exchange API: official Kraken Go SDK github.com/krakenfx/api-go/v2 (REST + WebSocket v2; order placement only in live mode with API keys).
- LLM: LM Studio (local, OpenAI-compatible REST at localhost:1234/v1) as primary; optional remote OpenAI-compatible endpoint as fallback.
- HTTP client: net/http — LLM endpoints, news feeds, alerts.
- Logging: log/slog → JSON to stdout + file (LLM components also write llm.log).

### Backend Coding Rules
- gofmt/go vet clean; follow Effective Go conventions.
- Use explicit types; prefer small consumer-defined interfaces between packages.
- Add a short comment that describes each function above the function.

## Frontend libraries and purpose
- Alpine.js 3.x (CDN): Reactive UI state and DOM binding.
- Tailwind CSS 3.x (CDN): Utility-class styling.
- Chart.js 4.x (CDN): Equity curve line chart.
- Lightweight Charts 4.x (CDN): Candlestick price charts per market.

### Frontend Coding Rules
- Keep it clean and easy to read by a human.

## Code Presentation
- Always wrap code in code blocks with the relevant language tag.
- Include clear, concise inline comments explaining non-obvious logic.
- Show complete, runnable code — avoid partial snippets unless explicitly asked.
- After each code block, briefly explain what changed and why (2–5 sentences max).

## Tone & Style
- Be direct and concise. No filler phrases or lengthy preambles.
- Get to the answer first, then add context if needed.
- Avoid over-explaining things that are straightforward.

## General Rules
- If something is ambiguous, ask clarifying questions, never assume.
- Prefer simple, readable solutions over clever ones. 
- Flag potential bugs or edge cases after the code explanation if relevant.
- With every change, update the documents in the docs folder.
- Use behaviour driven development in a GIVEN WHEN THEN style.
- Tests should test behaviour and state rather than specific outcomes.
- Create a new test(s) for the desired outcome prior to making the code change.
- After code change, run the new test(s).
- Increment the version number on each set of changes.
- Record changes for each version in CHANGELOG.md
