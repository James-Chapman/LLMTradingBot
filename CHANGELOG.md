# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [0.5.25] — 2026-04-30

### Removed

- **Trial dashboard themes** — removed `frontend/index2.html` through `frontend/index9.html`, their `/index2` through `/index9` FastAPI routes, and the unused scoped CSS for the discarded theme experiments. The promoted ledger-glass dashboard remains the single maintained `/` entry point.

---

## [0.5.24] — 2026-04-30

### Changed

- **Primary dashboard layout** — promoted the `/index3` ledger-glass dashboard to the default `/` `frontend/index.html` shell, kept `/index3` available as the same layout route, and bumped the service worker cache so browsers fetch the new HTML.

---

## [0.5.23] — 2026-04-29

### Fixed

- **Live mode Available Cash showed 0** — `get_account_snapshot` looked up the EUR fiat balance using only the Z-prefixed Kraken key (`ZEUR`). Accounts that return the bare key (`EUR`) would get `cash=0`, making the dashboard header show the wrong cash figure. The lookup now falls back to the bare currency code when the Z-prefixed key is absent.

---

## [0.5.22] — 2026-04-29

### Changed

- **News and Price Charts height** — tripled the shared Crypto News and Price Charts panel height while keeping Price Charts internally scrollable and preserving side-by-side 5-minute / 15-minute chart panes on normal widths.

---

## [0.5.21] — 2026-04-29

### Changed

- **Price Charts panel** — Price Charts now starts expanded by default, matches the Crypto News panel height, scrolls internally to fit all market graph cards, and keeps each market's 5-minute and 15-minute charts side by side on normal widths.

---

## [0.5.20] — 2026-04-29

### Changed

- **Index3 dark theme** — converted `/index3` (`layout-ledger-glass`) from a light ledger palette to a cool dark theme with midnight surfaces, cyan/teal accents, darker table contrast, and scoped panel/header treatments. The primary `/` dashboard and other alternate layouts are unchanged.

---

## [0.5.19] — 2026-04-29

### Changed

- **Alternate dashboard concepts refreshed** — overwrote `/index2` through `/index9` with new radar deck, ledger glass, pulse wall, incident rail, holdings map, candle lab, dispatch flow, and signal matrix variants. Each page keeps the shared dashboard behaviour hooks while using a new body class, layout geometry, and scoped theme so the primary `/` dashboard remains unchanged.

---

## [0.5.18] — 2026-04-29

### Changed

- **Migrated Kraken execution to `python-kraken-sdk`** — `KrakenExecutionEngine` now uses `kraken.spot.Trade` and `kraken.spot.User` from `python-kraken-sdk` instead of `krakenex`. The SDK returns clean dicts and raises exceptions on API errors, removing the manual `{"error": [], "result": {}}` response parsing. `get_account_snapshot` no longer accepts a `prices` argument; cash comes from `get_account_balance()` and total equity from `get_trade_balance(asset=...)` (`eb` field). `requirements.txt` updated to include `python-kraken-sdk>=3.0.0`.

---

## [0.5.17] — 2026-04-29

### Fixed

- **Live balance display** — Dashboard "Available Cash" and "Total Equity" were showing paper-trading figures in live mode because the dashboard endpoint made a fresh Kraken `Balance` API call on every poll; if that call failed or was rate-limited the fallback to paper values overwrote the correctly-fetched live data. The dashboard now reads from `_current_equity` and `_current_cash`, the cached globals that the equity ticker keeps fresh from Kraken every 10 seconds.

---

## [0.5.16] — 2026-04-29

### Added

- **Five new dashboard concepts** — added `/index5` through `/index9` with command ribbon, portfolio cockpit, market lab, ops flow, and signal board layouts for broader visual comparison.

---

## [0.5.15] — 2026-04-29

### Added

- **Alternate dashboard layouts** — added `/index2`, `/index3`, and `/index4` as complete dashboard variants for comparing desktop workbench, tables-first, and monitor wall arrangements without changing the main `/` dashboard.

### Fixed

- **Live account totals** — `/api/dashboard` now sources Available Cash and Total Equity from Kraken account balances in live mode instead of continuing to show paper-engine cash and equity.

---

## [0.5.14] — 2026-04-29

### Changed

- **Trade tables** — aligned Open Positions, Closed Positions, Trade Ledger, and Rejected Trades columns to the requested operational order.
- **Rejected Trades panel** — now uses the same subtle zebra-striping as the other trade tables.
- **Dashboard API** — open positions now expose strategy, source, and open status metadata for the expanded table.

---

## [0.5.13] — 2026-04-29

### Changed

- **Trade tables** — Open Positions, Closed Positions, and Trade Ledger now use subtle alternate-row contrast for easier scanning.

---

## [0.5.12] — 2026-04-29

### Fixed

- **Open Positions panel** — Time Opened now falls back to the matching ledger open trade timestamp when the dashboard position payload does not include `opened_at`.

---

## [0.5.11] — 2026-04-29

### Changed

- **Open Positions panel** — added a leading Time Opened column and market/direction filter chips matching the Closed Positions panel.
- **Dashboard API** — open-position rows now include `opened_at` for UI display and filtering context.

---

## [0.5.10] — 2026-04-29

### Changed

- **Dashboard status row** — Markets, P&L Summary, and Signals now remain equal 1/3-width panels until the small-mobile breakpoint instead of collapsing at tablet widths.

---

## [0.5.9] — 2026-04-29

### Changed

- **Dashboard layout** — Markets, P&L Summary, and Signals now share the top status row, with Open Positions, Closed Positions, and Trade Ledger rendered as full-width panels below.
- **Open Positions table** — open positions now render as single-line table rows matching the closed-position panel style, including market, direction, size, entry price, value, unrealised P&L, position ID, signal, and close action.
- **BDD coverage** — updated frontend layout tests for the new panel order, full-width placement, and single-line open-position rows.

---

## [0.5.8] — 2026-04-29

### Changed

- **LLM client shared helpers** — added `backend/llm/common.py` for shared JSON response parsing, chat-message prompt rendering, UTC clock handling, and exponential-backoff circuit-breaker state.
- **`OpenAiClient` and `TransformersClient`** now reuse the shared helpers while keeping endpoint-specific request/probe/model-loading behavior local to each client.
- **BDD coverage** — added `tests/test_llm_common_bdd.py` for shared JSON recovery, prompt rendering, and circuit retry timing.

---

## [0.5.7] — 2026-04-29

### Fixed

- **OpenAI-compatible chat requests** — `OpenAiClient.chat()` now posts a proper chat-completions object (`model` plus `messages`) instead of sending the flattened prompt as a JSON string. This prevents local OpenAI-compatible servers such as LM Studio from returning `400 Bad Request` while `probe()` still succeeds.
- **BDD coverage** — added a regression test that verifies the HTTP request body sent by `OpenAiClient.chat()`.

---

## [0.5.6] — 2026-04-29

### Changed

- **`NEWS_SOURCES` is now functional** — each entry must be a `"Name::URL"` string. `build_rss_adapters()` parses the list at startup and constructs `RSSAdapter` instances dynamically, replacing the hardcoded adapter list in `main.py`. Invalid entries are skipped with a warning.
- **`FearGreedAdapter` remains hardcoded** — always appended to the adapter list; no `NEWS_SOURCES` entry needed.

### Removed

- All concrete `RSSAdapter` subclasses (`CoinDeskAdapter`, `CoinTelegraphAdapter`, `TheBlockAdapter`, `DecryptAdapter`, `BitcoinMagazineAdapter`, `CryptoSlateAdapter`, `TheDefiantAdapter`, `CryptoPotaroAdapter`, `NewsBTCAdapter`) — now expressed as default `NEWS_SOURCES` entries.
- `CoinNewsAdapter` and `CoinWeekAdapter` legacy stubs that returned empty lists.

### Added

- `rss_adapter_from_spec(spec)` — parses a `"Name::URL"` string into an `RSSAdapter`, or returns `None` with a warning if malformed.
- `build_rss_adapters(specs)` — builds the full RSS adapter list from `settings.news_sources`.
- BDD tests in `tests/test_news_adapter_bdd.py` covering spec parsing, invalid inputs, and fetch delegation.

---

## [0.5.5] — 2026-04-29

### Added

- **`OpenAiClient`** (`backend/llm/openai_client.py`) — connects to any OpenAI-compatible chat-completions endpoint (OpenAI, LM Studio, llama.cpp, Ollama OpenAI compat, etc.). Includes a circuit breaker with exponential backoff, a `probe()` method, and JSON response parsing with markdown-fence stripping.
- **`SwitchingLLMClient`** (`backend/llm/switching_client.py`) — routes LLM calls to `OpenAiClient` when available, falls back to `TransformersClient` automatically. On startup, if OpenAI is reachable the local pipeline is unloaded to free memory; `recheck_primary()` detects availability changes each news cycle.
- **Settings** — added `OPENAI_BASE_URL`, `OPENAI_API_KEY`, `OPENAI_MODEL`, `OPENAI_TIMEOUT` to `BotSettings`.
- **`main.py`** — wired `OpenAiClient` + `SwitchingLLMClient` as the active LLM client.
- **BDD tests** — `tests/test_openai_client_bdd.py` and `tests/test_switching_client_bdd.py` covering circuit breaker, probe, chat delegation, fallback, and backend switching.

---

## [0.5.4] — 2026-04-29

### Fixed

- **LLM briefing never generated / "Waiting for first news fetch…" stuck** — `TransformersClient.chat()` was passing `max_length=512` to the pipeline, which caps *total* tokens including the input prompt. Long briefing prompts left no room for the model to generate JSON, so `chat()` returned `None` and `latest_briefing` was never set.
- **Signal LLM assessment always "LLM unavailable"** — same root cause: when the pipeline returned empty or truncated output, JSON parsing failed silently (JSON errors don't trip the circuit breaker), so `analyse_signal()` always fell back to `_neutral()` with `llm_used=False`.

### Changed

- **`TransformersClient.chat()`** now passes `max_new_tokens=512` (limits only *generated* tokens, not total) and `return_full_text=False` (returns only the model's new output, not prompt + output). This ensures sufficient generation budget for any prompt length and removes false `{` matches from the prompt text during JSON extraction.

---

## [0.5.3] — 2026-04-28

### Changed

- **LLM backend consolidated to Transformers only** — removed `OllamaClient`, `LMStudioClient`, `LlamacppClient`, and `FallbackLLMClient`. `TransformersClient` is now wired directly into `main.py` with no fallback chain.
- **Settings cleaned up** — removed `OLLAMA_URL`, `OLLAMA_MODEL`, `OLLAMA_TIMEOUT`, `LM_STUDIO_URL`, `LM_STUDIO_MODEL`, `LLAMA_CPP_URL`, `LLAMA_CPP_MODEL`, `LLAMA_CPP_TIMEOUT`. Added `TRANSFORMERS_LLM_MODEL`, `TRANSFORMERS_TIMEOUT`, and `LLM_ONLY_MAX_CONCURRENCY` (previously missing from `BotSettings`).
- **`.env.example` synced** — now matches `BotSettings` field aliases exactly so the settings BDD test passes cleanly.

### Removed

- `backend/llm/ollama_client.py`, `lm_studio_client.py`, `llamacpp_client.py`, `fallback_client.py`
- `tests/test_ollama_client_bdd.py`, `test_lm_studio_client_bdd.py`, `test_fallback_client_bdd.py`

---

## [0.5.2] — 2026-04-28

### Fixed

- **UI: strategy labels in Trade Ledger and Rejected Trades** — `_strategyLabel()` mapped only the old `combined`/`llm` IDs, so current strategy IDs (`basic_and_llm_strategy`, `llm_only_strategy`) rendered as raw strings. Added the current IDs; kept legacy aliases for any historic records.
- **UI: signal modal strategy badge** — badge text and colour map used the same stale IDs, leaving `basic_and_llm_strategy` and `llm_only_strategy` with an unstyled grey badge. Both the text and `:style` maps now include all current and legacy IDs.
- **UI: version label** — header showed `v0.4.0`; corrected to `v0.5.1` (current release).

---

## [0.5.1] — 2026-04-28

### Fixed

- **LLM Assessment — Scale always × 1.00**: `BasicAndLLMStrategy` applies the LLM `confidence_scale` correctly inside the strategy, but the outer signal loop in `main.py` was hardcoding `confidence_scale=1.0` when reconstructing the `SignalAnalysis` for DB storage. The dashboard therefore always showed `Scale × 1.00` regardless of what the LLM actually returned. The reconstruction now reads the real scale from `supporting_signals["llm_confidence_scale"]` (where the strategy stores it) and removes the now-dead outer multiplication block that would have double-applied the scale.
- **LLM Assessment — empty reasoning**: Same reconstruction bug; `reasoning` was hardcoded to `""` with a comment "already in thesis; empty prevents duplication". The fix passes the actual reasoning text from `supporting_signals["llm_reasoning"]` through to storage so the signal detail modal displays it. The frontend reasoning box is now wrapped in `x-if="signalModal.llm_reasoning"` so it is hidden when the model genuinely returns an empty string rather than rendering `""`.

---

## [0.5.0] — 2026-04-28

### Fixed (T2 Codebase Audit — Bugs)

- **`save_approval_request`** no longer overwrites an existing `TradeIdeaModel` row that was already saved by `save_trade_idea` with full signal context (INSERT-if-not-exists semantics).
- **Trailing stop** is now evaluated after the fixed stop-loss check; positions closing via trailing-stop are recorded with `close_reason="trailing_stop"` rather than being silently missed.
- **Momentum lookback** uses `settings.momentum_lookback_ticks` instead of the former hardcoded `10`, respecting the `.env` value.
- **`FallbackLLMClient`** "no active client" message downgraded from `WARNING` to `DEBUG` to avoid noisy log spam during normal warm-up.

### Added (T2 Codebase Audit — Quality & Enhancements)

- **`strategy/constants.py`** — 25 shared indicator threshold constants extracted from `BasicStrategy` and `BasicAndLLMStrategy` so a single change propagates to both.
- **`asyncio_mode = "auto"`** in `pyproject.toml` — all async test methods now work without `run_until_complete` wrappers.
- **`tests/conftest.py`** — shared in-memory SQLite fixtures (`in_memory_engine`, `db_session`) for unit tests.
- **ETag caching** on the `/api/dashboard` endpoint — increments `_dashboard_version` on price and signal updates; returns HTTP 304 on `If-None-Match` match.
- **`trim_old_equity_snapshots()`** in `storage/repository.py` — caps the `equity_snapshots` table at 17 280 rows (24 h at 5 s ticks); called every 10 ticks.
- **Composite index** `ix_market_snapshots_symbol_timestamp` on `(symbol, timestamp)` in `MarketSnapshotModel`.
- **`PerformanceLearner.adjust_confidence`** wired into the strategy loop — every generated `TradeIdea` has its confidence scaled by historical win rate before risk evaluation.
- **Direction fix** — two locations in `main.py` that hardcoded `"long"` now use `idea.direction.value`.
- **Startup warning** logged when LLM model names are set to the placeholder `"default model"`.
- **CORS origins** read from `settings.cors_origins` (via `.env` `CORS_ORIGINS`) instead of a hardcoded list.
- **BDD tests** — 63 unit tests across `analysis/indicators.py`, `risk/engine.py`, `strategy/basic_strategy.py`, and `strategy/learner.py`.

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
