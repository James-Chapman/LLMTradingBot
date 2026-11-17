# Testing

The project uses the standard Go `testing` package. Tests live next to the code they cover (`*_test.go` in each package under `internal/` and `cmd/`). They are written in a BDD style: each behaviour is a subtest named with explicit GIVEN WHEN THEN wording.

## Run Tests

From the project root:

```powershell
go test ./...
```

Verbose output shows every GIVEN WHEN THEN subtest name:

```powershell
go test -v ./...
```

Run one package, or one test by name:

```powershell
go test ./internal/approval
go test ./internal/llm -run TestSwitchingClientChat -v
```

On Windows, `run_tests.bat` runs `go vet ./...` followed by `go test -v ./...` and reports whether everything passed. It exits with code 1 when either step fails, so callers can detect a failure.

## Run Linters

The standard Go tooling is used; no extra developer dependencies are required.

```powershell
go vet ./...
gofmt -l .
```

`gofmt -l .` lists files that are not formatted. To format them in place:

```powershell
gofmt -w .
```

CI (`.github/workflows/go.yml`) runs on every push and pull request: `go vet ./...`, a `gofmt -l .` formatting check, and `go test -race ./...`.

## Coverage Focus

The suite intentionally checks stable behaviour rather than internal implementation shape:

- Approval queue lifecycle: submit, duplicate suppression, approve, reject, expiry purge, and persistence/reload of pending approvals.
- Risk decisions: valid approval, same-direction position rejection, closing signal allowance, cash insufficiency, daily loss block, daily-loss state persistence, and zero equity (new trades rejected, no `NaN`/`Inf` sizing) (`TestRiskEngineNonPositiveEquity`).
- Paper execution: fills, cash accounting with fee/slippage, duplicate-position guard, shorts, FIFO close behaviour, stop-loss and trailing stops, mark-to-market equity, restore from the database, and operator reset.
  - Short margin (`internal/paper/margin_test.go`): a short's proceeds are excluded from the cash available for new orders, so cash never goes negative; opening a short changes equity only by the fee.
  - Restored positions: an empty stored direction is derived from the size sign; the entry fee and short proceeds are persisted and used after a restart.
- Equity per environment (`internal/app/equity_test.go`, `internal/storage/equity_env_test.go`): live snapshots never change the paper cash restored at start-up; the chart only shows the current environment; legacy snapshots migrate as paper; trimming keeps each environment's rows; the app's cash excludes short margin; an operator reset never silently deletes a position opened while it runs.
- Kraken execution: live AddOrder payload mapping, Kraken error handling, fail-closed missing-credential behaviour, account snapshots, order reconciliation and cancellation.
- Kraken market data: ranking the biggest pairs by 24h traded value (quote filter, offline and pegged pairs skipped, XBT/XDG renamed, top-n limit, errors), batch tickers, OHLC parsing, WebSocket v2 ticker parsing, subscription fallback to polling, symbol conversion, and retry/backoff.
- Strategy behaviour: public strategy IDs and registry, momentum gate, hard RSI and Bollinger filters, the five-indicator support gate, higher-timeframe alignment, bullish and bearish consensus signal generation, opposing consensus block, and indicator-only ignoring non-indicator sentiment.
- Strategy calculations: momentum, votes, confidence and position sizing formulas in `internal/strategy/calc`.
- Trend strategy (`internal/strategy/trend_test.go`): no idea during the 4-hour candle warm-up or without candles, a long in a steady 4-hour uptrend and an exit (short) in a downtrend, both above the automated confidence gate, the forming candle ignored, and markets evaluated independently. The 4-hour candles reach the strategy unchanged (`internal/marketdata/marketdata_test.go`) and, live, the OHLC loop fetches them with the longer window and the strategy tick passes them on (`internal/app/trend_candles_test.go`). The replay's 4-hour candles are covered by the parity tests below, and the `trend_candles` disclosure by `TestDisclosures`.
- Trend prompt (`TestTrendPrompt`): the prompt names the strategy, explains how to judge it and lists the EMA values; `trend_and_llm_strategy` wraps `trend_strategy`.
- 4-hour candle strategies (`internal/strategy/candle_strategies_test.go`): for Donchian, RSI dip, MACD and Bollinger squeeze, no idea during warm-up, a long on the entry pattern, an exit on the exit pattern (Donchian: below the 30-candle low; RSI dip: RSI over 50 or the 18-candle time limit; MACD: negative histogram; squeeze: below the middle band), no entry on a near miss (shallow decline, breakout without a squeeze), the forming candle ignored, and each prompt's name and data. `TestCandleStrategyReuse`: a decision is reused until a new candle closes, and every idea reports the current price.
- Rotation (`internal/strategy/rotation_test.go`): the top 3 rising markets by 14-day return are bought, rank 4 gets no idea (buffer), the rest are sold; a negative return is never bought; a market in warm-up takes no rank; the prompt lists rank, return and the number of ranked markets.
- Clock-timed candle fetch and warm-up: `internal/app/candle_schedule_test.go` (`TestCandleFetchSchedule`: cached 1h/4h candles holding the current interval are not fetched again; a new hour is fetched 20 s after the boundary and 4-hour candles only after a 4-hour boundary; too soon after the boundary waits; an answer without the current row is fetched again next cycle; nothing cached fetches everything; `ohlcWait` wakes just after the next hour. `TestWarmUpByStrategyNeed`: a candle strategy evaluates on the first tick with no price history), and `internal/backtest/replay_test.go` (`TestReplayWarmUp`: a strategy is first evaluated on the first tick or once its required ticks have filled).
- LLM score weight (0.22.0): `internal/strategy/llm_weight_test.go` (`TestLLMWeight`: the default weight gives the LLM its full effect; at half weight the LLM moves a basic + LLM or LLM-version entry half as far as at full weight; the raw scale, weight and applied scale are recorded; at weight 0 the score is unchanged but a raw scale below `LLM_VETO_THRESHOLD` still vetoes; an unused LLM leaves the score and records no applied scale), `internal/app/llm_score_test.go` (`TestSignalScoreSteps`: a stored signal keeps the strategy's `pre_llm_confidence` and records `pre_learner_confidence`; without the LLM the two are equal) and `config.TestSettingsFields` (`LLM_WEIGHT` is a live Signals setting accepting 0–1).
- Fees (0.21.0): `paper.TestPaperFeeMatchesKraken` (a paper fill pays at least Kraken Pro's Tier 1 taker fee) and `config.TestFeeAndSlippageDefault` (the `FEE_AND_SLIPPAGE` default covers the taker fee with room for slippage).
- Basic exit and entry rules (0.20.0): `internal/strategy/basic_rules_test.go` (`TestBasicExitRules`: a turn needs `BASIC_EXIT_VOTES` net votes or the momentum gate; a turn on one candle holds the position however many ticks run, the confirming candle sells; a break restarts the count; a short is covered after two bullish candles; a full short signal sells at once. `TestBasicTrendFilter`: entries with or without the trend pass, against it are dropped, off lets them through; `trend_ema` is the EMA of the finished higher-timeframe candles and absent when off; the reading's trend is up above, down below, unknown without it. `TestBasicTrailingStop`: a long sold three ATRs below the high since entry, not from a high before the entry; a short covered three ATRs above its low; off holds. `TestBasicCooldown`: no re-entry for N candles after a close, at once with no cooldown or a never-held market; the LLM is not asked during it) and `internal/strategy/basic_entry_rules_test.go` (`TestBasicStricterEntries`: `BASIC_ENTRY_VOTES` for longs and shorts; fresh signals enter on the first candle only, a weak signal growing strong counts as new; off follows a persisting signal. `TestBasicVolatilityFilter`: candle ATR against `BASIC_MIN_ATR_COST` × `FEE_AND_SLIPPAGE`, off, exits unaffected. `TestBasicProfitTarget`: long and short targets in ATRs from the entry, off holds). The older candle tests run with these rules off (`rulesOff`). Settings: `config.TestBasicRuleSettings`, `config.TestBasicEntrySettings` (live Signals fields, defaults accepted, out-of-range values rejected, 0 switches a rule off).
- Basic candle strategies: `internal/strategy/basic_candle_test.go` (`TestBasicCandleStrategies`: ids; the 1h view is the finished 1-hour candles with the 4-hour candles as higher timeframe, the 4h view the finished 4-hour candles with whole UTC days; warm-up; one reading per candle; a long entry at the live price; nothing while held; a rule exit when the consensus turns, repeated until sold; one entry per candle, long or paper short; a covering long for a held short; the real rules sell on falling candles; sorted markets. `TestBasicAndLLMCandleStrategies`: the LLM scales an entry and is told the candle length and live price; asked once per candle; a veto holds for the candle; an unavailable LLM passes the entry unscaled; exits skip the LLM. `TestBasicLongOnlyStrategies`: the long-only versions never short and never ask the LLM about a short, but still enter long and sell on a turn). The tick-era gate tests (`basic_test.go`, `basic_and_llm_test.go`) run the same gates on a view handed in directly (`asView`). Also `calc.TestVoteCandleChanges`, `analysis.TestCandleChanges`, the 1h/4h prompt labels in `llm.TestSignalPromptTickLength`, the 1-hour candles in `marketdata.TestBuildTrendCandles`, `app.TestTrendCandlesReachStrategy` and the replay parity tests, and the legacy selection mapping in `control` (`basic_strategy` loads as `basic_strategy_1h`). The replay tests that run the real strategy give it `warmBars` (42 hours) of 30-second bars first.
- LLM shadow reviews: `internal/strategy/shadow_test.go` (`TestShadowReviewer`: the LLM gets exactly the LLM version's request and the idea is unchanged; a scale at the threshold is not a veto; one claim per market per closed candle; exits, held markets, entries without a closed candle, the basic strategies and LLM strategies are not reviewed; an unanswered entry is claimed again), `internal/shadow/score_test.go` (`TestSummarise`: veto accuracy, loser rate, break-even and vetoed P&L; ahead, behind and too few; several trades under one verdict; the newest candle closed before the entry wins; trades on other markets, strategies, directions or outside the window and unanswered reviews are not scored; the recent list), `internal/storage/shadow_test.go` (`TestShadowReviews`: round trip; an answer replaces an unanswered row but is never replaced; newest candle first), `internal/app/shadow_test.go` (`TestShadowReview`: the entry is traded as without shadow mode and the veto recorded with the strategy's own prompt; the closed trade is scored against it and the exit never reaches the LLM; off or without news, the LLM is not asked), `internal/server/shadow_test.go` (`TestShadowReviewsEndpoint`) and `internal/config/shadow_setting_test.go` (`TestShadowReviewSetting`).
- Rule exits and the confidence gate (`internal/backtest/exit_gate_test.go` and `internal/app/exit_gate_test.go`, `TestExitGate`): a rule exit (`TradeIdea.Exit`) of a held long below `MIN_SIGNAL_CONFIDENCE` is executed at once, in the replay and in fully automated paper trading; an ordinary sell below the gate is still dropped; a rule exit with nothing held is ignored and never opens a short.
- LLM versions (`internal/strategy/llm_version_test.go`, `TestLLMVersion`): the base entry is sent with the strategy's own prompt and scaled by the LLM (capped at 0.95); a scale below `LLM_VETO_THRESHOLD` drops it; exits and entries for a market already held are passed on without an LLM call; one call per market per closed 4-hour candle (a veto is reused too); an unavailable LLM keeps the base entry and is retried; no analyser behaves like an unavailable LLM.
- Signal prompt strategy block (`internal/llm/strategy_prompt_test.go`): a request with a `StrategyPrompt` carries its name, instructions and data (in key order, one line per value); without one the generic prompt is unchanged.
- Registry and labels (`registry_test.go`, `internal/app/strategy_labels_test.go`): 15 strategies in display order, each LLM version wraps its base and uses the LLM, each base does not, and every strategy has a dashboard label (LLM versions say so).
- Control state behaviour: the default selected strategy, single selected-strategy switching, market and live toggles, and persistence, including the emergency-stop timestamp surviving a restart and invalid stored modes/environments falling back to manual/paper (`internal/control/restore_test.go`).
- LLM strategy behaviour: LLM long recommendations emit trade ideas, hold recommendations do not, indicators are passed through to the LLM, and the veto threshold blocks low-scale signals.
- LLM analyser prompt behaviour: briefing, signal analysis, recommendation and reflection prompts include the expected context; responses are parsed and clamped defensively.
- LLM clients: tolerant JSON parsing, circuit breaker backoff, LM Studio model auto-selection, and the switching client's fallback to the remote endpoint and reclaiming of LM Studio.
- News: RSS spec parsing, RSS and Atom fetching and parsing, building adapters from the built-in catalogue plus extra feeds, timestamp normalisation, and the Fear & Greed adapter. The app tests cover switching feeds off and on (`TestNewsFeedSwitches`).
- Indicators: insufficient-history behaviour, RSI edge cases, named price-change windows, Python-compatible rounding, and JSON-safe output.
- Backtest replay (`internal/backtest`, see `docs/14-backtesting.md`): accounting, tick timing, determinism, no look-ahead (`TestReplayNoLookAhead`), input parity with a naive recomputation (`TestReplayInputParity`), universe selection, provenance and the simulated exchange's fills, on synthetic bars built in the tests; LLM strategies are refused. The real strategy and scripted strategies drive the runs.
- Backtest reports (`internal/backtest`):
  - The per-tick tracker: drawdown episodes, recovery, exposure and end-of-day equity (`TestTickTracker`).
  - Every metric on a hand-computed result, with each null and its note (`TestMetrics`); Sharpe/Sortino against closed forms and their gates (`TestRiskAdjustedRatios`); the signal funnel (`TestSignalStats`).
  - The gross-to-net bridge reconciles exactly, with a zero residual on a real replay (`TestBridgeAndCosts`); the exact reconciliation passes on real runs and flags corrupted cash, fees, quantities and trade P&L (`TestReconciliation`).
  - Benchmarks: fill timing, fee, end valuation and equal weight (`TestBenchmarks`).
  - Report files, the honesty block first, byte-identical output (`TestReports`); verdict rules, including that no single run claims an edge (`TestRunVerdict`).
- Backtest robustness (`internal/backtest`):
  - Results do not depend on the number of workers (`TestRunAll`).
  - Split periods are independent (`TestSplit`); walk-forward windows, learner seeding and tuning use train data only (`TestWalkForward`).
  - Sensitivity: dimensions, deduplication, the break-even estimate, sign survival, and no "negative" claim on a flat baseline (`TestSensitivity`).
  - Regime buckets and cells (`TestRegimes`); the evidence conditions behind any "edge" wording (`TestCompare`).
  - Stale positions, worst-case P&L, the empty-candles dimension and data warnings reach every robustness summary and the `compare` conditions (`TestRobustnessFlowThrough`).
- Backtest honesty fixes (`internal/backtest`):
  - Fill model: a one-sided bar crosses the full spread, inverted averages never pay the bot, unknown sides use close ± half-spread, participation is against the same-side taker value with the 0.25 cap; a 20,000-sample property test shows a buy never fills below the taker-sell average and a sell never above the taker-buy average, and a constant-spread guard requires 2·s·notional of spread per round trip on one-sided bars (`TestFillModelBias`, `exchange_test.go`).
  - Universe: the selection is asserted unconditionally (A before the start, B the next day); a market that leaves and re-enters keeps a continuous price history; with several markets, changing one market after T changes nothing at or before T (`TestReplayUniverse`).
  - Stale positions are flagged, liquidated as `stale_liquidation`, valued at zero in the worst case, and make a sign-deciding positive verdict inconclusive (`TestStalePositions`).
  - The data check refuses a missing month or an error month unless allowed, and warns about late or early coverage (`TestDataCoverageCheck`); the risk fee is aligned to the exchange unless explicit, with a warning below the fee (`TestRiskFeeAlignment`); every disclosure appears in the assumptions and the honesty block (`TestDisclosures`).
  - Live-path parity: the replay's inputs equal `marketdata.Build` on Kraken-shaped OHLC rows (5-minute, 15-minute and 4-hour), with and without empty rows (`TestLiveInputParity`); in `TestReplayInputParity` the 4-hour candles start at the pre-roll, the documented difference from live.
  - Dependencies on windows, linux and darwin and every build tag; no database, `os/exec` or Windows system package; `net/http` imported directly only by `histdata/rest.go`; a full replay with a failing HTTP transport makes no call (`deps_test.go`, `TestBacktestMakesNoNetworkCall`).
- Backtest strategy interval (`internal/backtest/interval_test.go`, `TestStrategyInterval`): with `STRATEGY_INTERVAL_SECONDS` 120 set explicitly the strategy runs every 120 s from start + 120 s and the result counts strategy ticks; without it every 30 s bar is a strategy tick; a buy decided at a strategy tick fills on the next 30 s bar; 45 s is refused; the `strategy_interval` assumption states the interval. `TestReplayInputParity` runs at 30 s and 120 s.
- Historical data import lock (`internal/histdata/lock_test.go`, `TestImportLock`): two caches opened on one directory before either imports both end up in the manifest; concurrent imports verify with no `not_in_manifest` finding; a stale lock is taken over and removed; a live lock makes a waiter return its context error without writing bars; a dry run creates no lock or manifest; the heartbeat keeps a held lock from being judged stale.
- Historical data range check: missing months inside coverage, late and early coverage, and error findings from `verify` and from import (`internal/histdata/rangecheck_test.go`, `TestCheckRange`).
- Learner: no adjustment before enough samples, confidence boost after wins, confidence reduction after losses, rolling win rate, P&L percentiles, concurrent use, and a quality score that is the same for the same percentage outcomes on any account size, with recency-weighted magnitudes (`TestQualityScoreScale`).
- Storage: schema creation, migration of legacy (Python-created) databases, retention trimming (activity by insertion order, so a clock step back cannot delete newer rows), the trade ledger, and time handling.
- Frontend (page-source tests in `internal/server/frontend_*_test.go`; they read `index.html`, every script in `static/app/`, `styles.css`, `approvals.html`, `login.html` and `sw.js`, and assert on the code that carries each behaviour):
  - Layout and design system (`frontend_test.go`): one dark token palette, the shared stylesheet on every page and no utility-class shims, the breakpoints, dialogs centred by `.modal-backdrop`, no `x-show` element relying on an inline `display`, trade-table columns, and the service worker (network-first for HTML and the app's own scripts and styles, versioned cache).
  - App shell (`frontend_shell_test.go`): script order, the view module contract (`Dashboard.register`, merged mixins, hook names), hash routing and navigation, per-view polling only while a view is shown, and the selected-market chart lifecycle.
  - View details (`frontend_views_test.go`): candle charts fitted to their data after `setData` and on resize; the briefing's outlooks ordered bearish → bullish → neutral with a nine-card preview and a *Show all* button; activity details clamped to two lines with a *more / less* button; the two-row phone header with the emergency stop on the first row; the phone list → detail flow of Markets (no automatic selection, a back control, the list's scroll position kept); 36 px touch targets; the focus ring on the current nav item; neutral figures not coloured.
  - Safety (`frontend_safety_test.go`): text bindings instead of HTML, `safeUrl`, no server value in an inline handler, `parseUtc`, emergency stop shown only after the server confirms, resume confirmation, surfaced failures (sticky error toasts), pinned CDN versions, and dialog focus (moves in, Tab trapped, Escape closes).
  - Dashboard behaviour (`frontend_dashboard_test.go`): the adaptive price and two-decimal money formatters, the connection-lost banner and the stale-data guard, the service worker never serving API data from cache, single application of each dashboard response, out-of-order table loads, one OHLC request per market with sorted, de-duplicated candles, today's realised P&L, live positions (LIVE badge, pending states, Close disabled, *Forget*), and approvals (503/409 answers, buttons disabled while deciding, data attributes on the approvals page).
  - Settings and trading settings dialogs (`frontend_settings_test.go`, `frontend_trading_test.go`), the login page and 401 redirect (`frontend_login_test.go`), and the Backtests view (`frontend_backtests_test.go`: registered view and section, per-segment URL encoding of run ids, the honesty block before every number, plain-text server text, n/a for null metrics, verdict colours only for the positive rules, one equity chart instance, late responses dropped, and GET-only loads on entering the view or on Refresh).
- App and server: startup restore, strategy-tick gates and trading modes (including the guard that keeps live-toggled markets on paper in the paper environment), background-loop error handling, every HTTP endpoint's status codes and JSON shapes, CORS, and the frontend pages.
- Trading settings from the web interface: persistence across restarts (saved choice beats the defaults), the typed `LIVE` confirmation and API-key check for going live, the HTTP endpoints, the dashboard's confirm/typed-confirm wiring, and the strategy choice sitting between Trading mode and Money with its own confirm (no longer in the Markets panel) (`internal/app/trading_settings_test.go`, `internal/server/trading_settings_test.go`, `internal/server/frontend_trading_test.go`, plus storage and control tests).
- Cross-package wiring (`internal/app/wiring_test.go`, plus adapter tests in `internal/app/adapters_test.go` and `TestRepositoryPositions` in storage): a paper long and short closed after a restart include their stored entry fee and the short settles from its stored proceeds; the emergency-stop time survives a restart through the app; the learner scores paper and live closes, and its start-up seed, as returns on notional; the strategy sees the live book on live-routed markets and paper positions elsewhere; the dashboard lists live positions with `environment` and `entry_pending` / `open` / `exit_pending` status; activity messages keep sub-cent prices. Page-source tests (`internal/server/frontend_dashboard_test.go`) cover the LIVE badge, the disabled Close button while a live order is pending, the live close message, and the 503/409 approval answers.
- Windows scripts: every `.bat` file in the repo root uses CRLF line endings and `.gitattributes` keeps them that way (`cmd/tradingbot/scripts_test.go`).
- Dashboard-managed settings (0.8.0):
  - Registry: every key listed once, secret and live flags, text parse/read-back per kind, refused values leave the setting unchanged, `EffectiveLogLevel` (`internal/config/settings_test.go`).
  - Secrets: seal/open round trip and the `sealed:` prefix, plus DPAPI on Windows (`internal/secret`).
  - Storage: settings save/load and upsert (`internal/storage/settings_test.go`).
  - Risk: `SetSettings` keeps equity and today's loss (`internal/risk/settings_test.go`).
  - App (`internal/app/settings_test.go`):
    - `.env` and environment variables are ignored; a bad stored value falls back with a warning.
    - Updates are all-or-nothing; secrets are sealed, never shown in the view, and can be cleared.
    - A live change affects the next tick; `restart_required` is reported.
    - Saved Kraken keys allow live only after a restart.
  - Server: `GET`/`POST /api/settings`, the 400 cases, and secrets never appearing in `GET` (`internal/server/settings_test.go`).
  - Frontend: the dialog markup contract: side navigation with one group at a time, label/help beside a sized control, switches, chip lists instead of text areas, the draft filled before the fields render, and themed secondary buttons (`internal/server/frontend_settings_test.go`).
  - Default markets: 50 built-in EUR pairs without stablecoins (`internal/config/settings_test.go`); Kraken's live list used only when no markets are saved, built-in list kept on errors or an empty answer (`internal/app/default_markets_test.go`).
  - Logging: `SetLevel` (`internal/observability/logging_test.go`).
- Start-up flags and scripts (`cmd/tradingbot/flags_test.go`):
  - Flag defaults and parsing.
  - `-env` and out-of-range ports are refused.
  - No `.bat` mentions `.env`, and `.env.example` is gone.
- CLI: argument parsing, `status` from the running bot's `/api/dashboard`, removed `--config`/`--mode`/`--environment` flags exit 2, start-up flags passed through by `run`, and approve/reject calls.
- Backtest command (`cmd/backtest`):
  - Settings come from a JSON file or the built-in defaults, and the command never opens a database (`settings_test.go`).
  - Run flags and exit codes (`main_test.go`); the data subcommands (`data_test.go`).
  - A cache missing a month refuses `run` until `--allow-data-gaps`; the new flags and the settings source are recorded (`TestRunDataChecks`, `datacheck_test.go`).
  - `run` writes every report file and prints the honesty block and the reconciliation; a reconciliation warning fails unless allowed (`TestRunReports`).
  - `split`, `walkforward`, `regimes`, `sensitivity` and `compare` on an imported synthetic cache: output layout, progress, byte-identical files with 1 and 3 workers, and usage errors (`TestRobustnessCommands`).

## BDD Style

Use this naming pattern for new tests: one top-level `Test…` function per behaviour area, and one `t.Run` subtest per scenario.

```go
// From internal/approval/service_test.go.
func TestApprovalService(t *testing.T) {
	t.Run("GIVEN a market already awaiting approval WHEN another signal arrives THEN it is not queued twice", func(t *testing.T) {
		// GIVEN a pending approval for ETH/EUR
		svc := NewService(DefaultTTL, nil, newClock().Now)
		first := makeIdea("ETH/EUR")
		second := makeIdea("ETH/EUR")
		firstReq, _ := svc.Submit(first, makeRisk(first, "ok"))

		// WHEN a second ETH/EUR signal is submitted
		_, ok := svc.Submit(second, makeRisk(second, "ok"))

		// THEN it is refused and the queue is unchanged
		if ok {
			t.Fatal("duplicate market must not be queued")
		}
		if pending := svc.GetPending(); len(pending) != 1 || pending[0].ID != firstReq.ID {
			t.Fatalf("queue changed: %+v", pending)
		}
	})
}
```

Keep assertions specific where the contract matters, such as order status, trade direction, or risk approval. Keep assertions general where exact values would make the suite brittle, such as confidence ranges, partial reason text, or collection membership.

## Test Doubles and Fixtures

- **No real network calls.** HTTP dependencies (Kraken REST, LM Studio / OpenAI-compatible endpoints, RSS feeds, Fear & Greed, the CoinMarketCap universe, the bot API for the CLI) are served by `net/http/httptest` servers. Kraken uses a shared fake in `internal/kraken/fake_kraken_test.go`, and the WebSocket stream is replaced through the injectable `TickerStreamFunc`.
- **App fakes.** `internal/app/apptest` provides network-free fakes for the app's external dependencies (Kraken market data and execution, LLM, universe, news and strategies), used by the `internal/app` and `internal/server` tests.
- **Injectable clocks and sleeps.** Components that depend on time (approval expiry, control state, circuit breaker, Kraken backoff) accept a clock or sleep function, so tests never wait on real timers.
- **Databases.** Tests that need persistence open a fresh SQLite file under `t.TempDir()`; nothing touches `trading_bot.db`.
- **Fixtures.** Static test data lives in the package's `testdata/` directory. The backtest tests build their market data in code (synthetic bars, or synthetic trade CSVs imported into a temporary cache) instead of using fixture files, and never download real archives.

## Test Design Rules

- Prefer exported functions, methods and domain types over unexported implementation details.
- Use exact numeric assertions only for accounting or indicator formulas where precision is the point of the behaviour.
- Use `strings.Contains` or range checks when wording or scoring can evolve without breaking the user-facing contract.
- Keep tests independent and avoid a shared SQLite database unless the behaviour under test is persistence; use `t.TempDir()` for any file.
- Never call real external services; use `httptest` or the fakes above.
- Add or update the relevant GIVEN WHEN THEN test before changing behaviour, and run it after the change.
