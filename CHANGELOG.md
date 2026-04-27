# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [0.4.0] — 2026-04-28

### Added

- **LLM status indicator** in the dashboard header — a coloured dot (green when connected, muted when offline) and the active model name sit next to the environment/mode pills, giving an at-a-glance view of LLM availability without opening the Local LLM panel.
- BDD test `test_given_header_llm_indicator_when_markup_inspected_then_dot_is_in_flex_container` verifying the dot is inside a flex container and the model binding has the "Not configured" fallback.

### Fixed

- **`FallbackLLMClient.llm_model`** previously returned `"default model"` (the settings placeholder) whenever a client was constructed but not yet connected. It now returns `""` until the active backend confirms availability, so the header correctly shows "Not configured" rather than a misleading model name.
- **LLM status dot invisible in header** — the `.llm-dot` span defaulted to `display: inline`, which ignores explicit `width`/`height`. Added `display:flex; align-items:center; gap:6px` to the wrapping `metric-block` so the 8×8 circle renders correctly (mirroring how `.llm-row` works in the LLM panel).

---

## [0.3.0] — 2026-04-27

### Added

- **`LMStudioClient`** (`backend/llm/lm_studio_client.py`) — OpenAI-compatible REST client for LM Studio (`POST /v1/chat/completions`). Same circuit-breaker pattern (exponential backoff, 30 s initial delay, 5 min cap) as `OllamaClient`. Probe hits `GET /v1/models`.
- **`FallbackLLMClient`** (`backend/llm/fallback_client.py`) — probe-and-lock-in chain that tries LM Studio → Ollama → Transformers at startup and locks in the first available backend. If the locked-in client's circuit opens during operation, it promotes automatically to the next available client.
- **`LLMClientProtocol`** (`backend/llm/analyser.py`) — `runtime_checkable` Protocol defining the shared interface (`available`, `can_attempt`, `probe`, `chat`) that all three clients and `FallbackLLMClient` satisfy.
- `LM_STUDIO_URL` and `LM_STUDIO_MODEL` settings (defaults: `http://localhost:1234`, `google/gemma-4-e4b`).
- BDD tests for `LMStudioClient`: circuit-breaker backoff, half-open retry, JSON repair, non-JSON fault isolation.
- BDD tests for `FallbackLLMClient`: probe ordering (all four permutations), runtime promotion when active client's circuit opens, no-client case.
- **`launch.bat`** LLM detection logic: checks LM Studio on port 1234, attempts to start via `lms` CLI or GUI exe if not running, then falls through to Ollama, then logs a notice that Transformers will be used.

### Changed

- `main.py` — `_llm_client` is now a `FallbackLLMClient` wrapping all three backends in priority order. `probe()` is called once at lifespan startup.
- `LLMAnalyser.__init__` parameter type changed from `TransformersClient` to `LLMClientProtocol`.
- `docs/00-overview.md` — technology stack and architecture diagram updated to reflect the three-backend LLM chain.
- `docs/07-llm-integration.md` — LM Studio client and `FallbackLLMClient` documented; LM Studio marked as highest priority.

---

## [0.2.0] — 2026-04-27

### Added

- **`BasicAndLLMStrategy`** (`backend/strategy/basic_and_llm_strategy.py`) — new combined indicator-consensus + LLM analysis strategy. Applies the same momentum gate, hard filters, and indicator voting as `BasicStrategy`, then performs an internal LLM signal-analysis/veto pass. Selected by default.
- **`TransformersClient`** (`backend/llm/transformers_client.py`) — local Hugging Face Transformers model client with the same circuit-breaker interface as `OllamaClient` (probe, chat, can_attempt, circuit_state, available). Loaded via `asyncio.to_thread` to avoid blocking the event loop.
- BDD tests for `BasicAndLLMStrategy`: strategy ID, `uses_llm_analysis` attribute, momentum gate, and LLM veto gate.
- BDD tests for `TransformersClient`: circuit-breaker backoff, half-open retry, JSON repair, non-JSON fault isolation.

### Changed

- **LLM client** — active client switched from `OllamaClient` to `TransformersClient` in `main.py`. `LLMAnalyser` accepts either via a shared duck-typed interface.
- **`llm/client.py` renamed** to `llm/ollama_client.py` for clarity alongside the new Transformers client.
- **Strategy dispatch** in `_strategy_loop` is now a three-way branch keyed on marker attributes:
  - `uses_llm_recommendation` → `LLMOnlyStrategy` (passes analyser, equity, cash, positions)
  - `uses_llm_analysis` → `BasicAndLLMStrategy` (passes analyser + portfolio_data dict)
  - Neither → `BasicStrategy` (market data only)
- **Signal loop** no longer calls `analyse_signal` a second time for `basic_and_llm_strategy` ideas; the strategy applies LLM analysis internally and the loop reconstructs `SignalAnalysis` from `supporting_signals`.
- Strategy labels updated: `"combined"` → `"basic_and_llm_strategy"`, `"llm"` → `"llm_only_strategy"` throughout tests and helpers.

### Fixed

- `SyntaxError` in `_cache_market_snapshot` — `nonlocal` was incorrectly used for module-level globals inside a nested async function; removed the declaration (dict mutation requires no binding keyword).
- Duplicate `import asyncio` statements in `main.py` (appeared three times).
- `_strategy_loop` was spawned twice in the FastAPI lifespan, causing two competing strategy loops.
- `_reload_strategy_instances()` referenced an undefined variable `basic_strategy_strategy` and omitted `basic_and_llm_strategy` from the rebuilt strategy list.
- Double-space in `from .transformers_client import  TransformersClient` import in `analyser.py`.
- Stale commented-out `OllamaClient` import removed from `analyser.py`.

### Documentation

- `docs/00-overview.md` — file layout updated: `client.py` → `ollama_client.py`, `transformers_client.py` added, `basic_strategy_strategy.py` corrected to `basic_and_llm_strategy.py`.
- `docs/06-strategy-and-learning.md` — stale `basic_strategy_strategy.py` section replaced with correct `BasicAndLLMStrategy` description.
- `docs/07-llm-integration.md` — added `TransformersClient` section; updated strategy name references from `combined`/`llm` to `basic_and_llm_strategy`/`llm_only_strategy`; updated `LLMAnalyser` type annotation to reflect interchangeable clients.

---

## [0.1.0] — initial release

- FastAPI backend with paper and live trading modes.
- `BasicStrategy` (indicator-only) and `LLMOnlyStrategy` strategies.
- `OllamaClient` for local LLM inference via Ollama REST API.
- SQLite persistence: orders, positions, outcomes, equity, news, signals, activity, control state, LLM briefings and reflections.
- Risk engine: position limits, cash sufficiency, per-trade loss cap, daily loss cap.
- Approval queue for semi-automated mode.
- News ingestion from multiple RSS feeds.
- Alpine.js + Tailwind CSS dashboard with Lightweight Charts candlesticks and Chart.js equity curve.
