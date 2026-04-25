## Project Overview
This is a trading bot software project to trade crypto currencies

## Backend

### Language & Stack
- Web framework: FastAPI + Uvicorn (ASGI, async).
- Database: SQLite via SQLAlchemy 2.x (StaticPool, single file).
- Settings: Pydantic v2 / pydantic-settings (.env file).
- Exchange API: krakenex + pykrakenapi (read-only in paper mode).
- LLM: Ollama (local) — phi3:mini default, REST at localhost:11434.
- HTTP client: httpx (async) — used for Ollama only.
- Logging: Python logging → JSON + file (structured, module-level).

### Backend Coding Rules
- Follow PEP 8 style conventions.
- Use type hints wherever appropriate.
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
- Create a new test for the desired outcome prior to making the code change.
- Run the new test after a code change to validate the change.
- Run linter checks on madified files after code change.
- After modifiying a file, run all unit tests for that file.
- Run broader functionality tests to ensure change hasn't broken functionality.