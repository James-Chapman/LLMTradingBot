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

On Windows, `run_tests.bat` runs `go vet ./...` followed by `go test -v ./...` and reports whether everything passed.

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
- Risk decisions: valid approval, same-direction position rejection, closing signal allowance, cash insufficiency, daily loss block, and daily-loss state persistence.
- Paper execution: fills, cash accounting with fee/slippage, duplicate-position guard, shorts, FIFO close behaviour, stop-loss and trailing stops, mark-to-market equity, restore from the database, and operator reset.
- Kraken execution: live AddOrder payload mapping, Kraken error handling, fail-closed missing-credential behaviour, account snapshots, order reconciliation and cancellation.
- Kraken market data: ranking the biggest pairs by 24h traded value (quote filter, offline and pegged pairs skipped, XBT/XDG renamed, top-n limit, errors), batch tickers, OHLC parsing, WebSocket v2 ticker parsing, subscription fallback to polling, symbol conversion, and retry/backoff.
- Strategy behaviour: public strategy IDs and registry, momentum gate, hard RSI and Bollinger filters, the five-indicator support gate, higher-timeframe alignment, bullish and bearish consensus signal generation, opposing consensus block, and indicator-only ignoring non-indicator sentiment.
- Strategy calculations: momentum, votes, confidence and position sizing formulas in `internal/strategy/calc`.
- Control state behaviour: the default selected strategy, single selected-strategy switching, market and live toggles, and persistence.
- LLM strategy behaviour: LLM long recommendations emit trade ideas, hold recommendations do not, indicators are passed through to the LLM, and the veto threshold blocks low-scale signals.
- LLM analyser prompt behaviour: briefing, signal analysis, recommendation and reflection prompts include the expected context; responses are parsed and clamped defensively.
- LLM clients: tolerant JSON parsing, circuit breaker backoff, LM Studio model auto-selection, and the switching client's fallback to the remote endpoint and reclaiming of LM Studio.
- News: RSS spec parsing, feed fetching and parsing, timestamp normalisation, and the Fear & Greed adapter.
- Indicators: insufficient-history behaviour, RSI edge cases, named price-change windows, Python-compatible rounding, and JSON-safe output.
- Backtest replay: deterministic 48-hour replay behaviour plus drawdown, trade stats, and candle resampling metrics.
- Learner: no adjustment before enough samples, confidence boost after wins, confidence reduction after losses, rolling win rate, P&L percentiles, and concurrent use.
- Storage: schema creation, migration of legacy (Python-created) databases, retention trimming, the trade ledger, and time handling.
- App and server: startup restore, strategy-tick gates and trading modes (including the guard that keeps live-toggled markets on paper in the paper environment), background-loop error handling, every HTTP endpoint's status codes and JSON shapes, CORS, and the frontend pages.
- Trading settings from the web interface: persistence across restarts (saved choice beats the defaults), the typed `LIVE` confirmation and API-key check for going live, the HTTP endpoints, the dashboard's confirm/typed-confirm wiring, and the strategy choice sitting between Trading mode and Money with its own confirm (no longer in the Markets panel) (`internal/app/trading_settings_test.go`, `internal/server/trading_settings_test.go`, `internal/server/frontend_trading_test.go`, plus storage and control tests).
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
- Backtest: defaults without a database, saved settings with one (`cmd/backtest/settings_test.go`).

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
- **Fixtures.** Static test data lives in the package's `testdata/` directory, e.g. `internal/backtest/testdata/btc_eur_5m_profitable_48h.json` for the deterministic replay test.

## Test Design Rules

- Prefer exported functions, methods and domain types over unexported implementation details.
- Use exact numeric assertions only for accounting or indicator formulas where precision is the point of the behaviour.
- Use `strings.Contains` or range checks when wording or scoring can evolve without breaking the user-facing contract.
- Keep tests independent and avoid a shared SQLite database unless the behaviour under test is persistence; use `t.TempDir()` for any file.
- Never call real external services; use `httptest` or the fakes above.
- Add or update the relevant GIVEN WHEN THEN test before changing behaviour, and run it after the change.
