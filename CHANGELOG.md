# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [0.11.0] — 2026-11-14

Historical backtesting: replay the bot's own strategy against real Kraken trade history, with documented execution assumptions, to see whether it would have made money after costs. Full guide: `docs/14-backtesting.md`.

### Added
- **Historical data** (`backtest data`). Imports Kraken's downloadable trade archive, or pages trades from Kraken's public REST API, validates them, and stores them locally as 30-second bars with checksums and a record of where each month came from. Commands: `import`, `fetch`, `verify`, `list`, `universe`.
  - The cache is its own folder (`data/history`, git-ignored), never the bot's database.
  - `universe` lists the biggest markets as of a past date, using only data from before it.
- **Replay** (`backtest run`). Runs the real strategy, risk engine, paper engine and learner on a simulated clock, one tick every 30 seconds, in the same order as live.
  - It sees the inputs live sees: 30-second prices, clock-aligned 5- and 15-minute candles including the one still forming, and 24-hour volume. The bot's market-data builder is now shared code that both live and the backtest call.
  - A decision fills on the next tick, never on the tick that produced it. Stops are checked on the 30-second price, as live does.
  - Two runs with the same data and settings give byte-identical files.
- **Simulated exchange.** Fills are priced at what takers on that side paid in the fill bar. When only the other side traded, the order crosses the full measured spread. Other rules: a taker fee on every fill, extra cost for orders large against the bar's traded value, rejection beyond a liquidity cap, minimum order size, lot rounding, cash checks. Spot only: it never opens a short.
- **Reports.** `summary.md`, `summary.json`, `trades.csv`, `orders.csv`, `equity.csv`, `costs.json` and `run.json` (everything needed to reproduce the run).
  - Net and gross P&L with an exact bridge between them, fees, execution costs, win rate, profit factor, drawdown measured on every tick, exposure, turnover, and Sharpe when there is enough data for it to mean something.
  - Benchmarks at the same costs: cash and buy-and-hold.
  - Cash, fees and positions are re-added in exact arithmetic and compared with the engine's totals; a mismatch fails the run.
- **Robustness commands.** `split` (in-sample against out-of-sample), `walkforward`, `regimes` (bull, bear, sideways; high and low volatility), `sensitivity` (fees, spread, impact, latency, capital, any setting, with an estimated break-even fee) and `compare`.
- **Plain verdicts from fixed rules.** Every summary opens with what the run can and cannot show: number of trades, length of period, survivorship, fee tier, whether it is in-sample. No single run is described as showing an edge.

### Changed
- `cmd/backtest` is a new tool with new flags; the old replay of recent Kraken candles is removed. It no longer reads the bot's database or API keys. Settings come from a JSON file (`--settings`) or the built-in defaults.
- The paper engine accepts its fee, slippage and id generator as options. Paper trading uses the same values as before.

### Safety
- The backtester cannot place an order. A test inspects everything the backtest binary links and fails if it includes the Kraken execution code, the secrets package, the bot's database, or the dashboard server.

### Limits to know before trusting a result
- **LLM strategies cannot be backtested** and are refused: there is no archive of past news, the model's answers are not repeatable, and the model has seen the future of any past period. A backtest of `basic_strategy` measures the indicator core, not the bot's default LLM-combined strategy.
- **The default fee is 0.80% taker**, read from Kraken's fee page. Set `--fees` to your own tier; `--fees paper-default` gives the 0.26% that paper trading assumes.
- Trade prints cannot show order-book depth, so the size-impact figures are an estimate, not a measurement.
- A market list you choose today contains only coins that survived. `--universe top:N` reduces that bias but cannot add coins missing from the data.
- Nothing has been run on real Kraken history yet. The tool is covered by tests and a run on synthetic trades.

---

## [0.10.0] — 2026-11-14

The improvements recommended by the 0.9.2 audit. The largest change is that live trading now tracks its positions.

### Added — live trading
- **Live positions are tracked.** Orders sent to Kraken are recorded in a live position book, separate from paper positions and kept across restarts. Before this, a signal that persisted placed a new real order every 30 seconds, live holdings had no stop-loss, and live losses never counted toward the daily loss limit.
  - A position is recorded as soon as the order is written, before it is sent, so a second order cannot go out while the first is unconfirmed, including after a crash or restart.
  - Stop-loss and trailing stops apply to live positions. A triggered stop sends one market sell for the size actually bought.
  - A sell signal sells the tracked position. With no tracked position nothing is sent (a spot account cannot short).
  - Realised live P&L feeds the daily loss limit, the ledger and the learner.
  - Live positions show in the dashboard with a LIVE badge and their state (entry pending, open, exit pending), and can be closed from there.
- **Orders cannot be duplicated by a retry.** Each order carries a client order id. A retry happens only when Kraken says the order was not placed; when the outcome is unknown (timeout, 503) the order is kept pending and looked up later by that id.
- **Partial fills are recorded.** The executed size, average price and fee come from Kraken, including for orders cancelled after a partial fill.
- **Approvals are re-checked when approved.** The risk check runs again with current cash, positions and the day's losses, and the order is sized from that result. An approval with no price available now stays pending instead of being used up.
- **Forget a live position.** `POST /api/positions/{id}/forget` and a dashboard button remove a tracked position without sending an order, for holdings sold or moved outside the bot.
- A per-market **Live** toggle in the markets table, shown in the live environment, with a confirmation that names the market.

### Changed — live trading safeguards
- Switching from live to paper is refused while live positions are open, and so is turning off a market's live toggle while it holds one. Either would leave a real holding untracked.
- During an emergency stop the bot still reconciles pending orders and allows a manual close, but automatic stops on live positions stay paused, as they do for paper. The activity feed warns while that is the case.
- Live positions keep their stops when their market is disabled or drops out of the market list.
- A rejected live sell is retried after 5 minutes, doubling up to an hour, instead of on every tick. A remainder below Kraken's minimum order size is dropped as dust.
- A price older than two strategy intervals is refreshed over REST; if that fails, stops and strategies skip that market rather than act on a stale price.
- Sell sizes are rounded down so a sell can never exceed the holding.

### Added — access control
- **Login when the dashboard is reachable from other computers.** With `-host` set to anything but a loopback address, the dashboard and API need an access token. It is generated on first start, stored encrypted, and printed to the console at each start. Log in at `/login`, or send `Authorization: Bearer <token>` from scripts. Loopback use is unchanged and needs no login.
- Requests addressed to an unexpected host name are refused, which closes DNS rebinding. A new setting, Allowed host names, lists extra names.
- `*` is no longer accepted as an allowed browser origin.

### Changed — settings
- Stop-loss and trailing stop must be above 0 and at most 0.5; a stop-loss of 0 switched off the per-trade loss check. The per-trade and daily loss limits must be above 0.
- Timeouts, lookback and concurrency settings have upper limits.
- A secret entered as blank spaces is refused instead of silently clearing the stored secret.
- "Max dynamic markets" set to 0 now means none (it meant 10).
- Dynamic markets use the base currency instead of always EUR. The source is named `coingecko`, which is what the code has always called.

### Fixed — reliability
- **Start-up waits for Kraken.** If no market could be validated at start (network not up yet), the bot stayed idle until restarted. It now retries.
- Indicator warm-up no longer sticks when the first market is disabled.
- A panic in the start-up, candle or equity loops no longer stops the bot.
- A second Ctrl+C quits at once, and shutdown waits at most 30 seconds.

### Fixed — news and LLM prompts
- **A failed news feed no longer makes old articles "new".** Its articles stay cached while it is down, so its recovery does not trigger a briefing on stale news. If every feed fails, the previous news is kept.
- **Headlines can no longer inject instructions into prompts.** Feed text is stripped of HTML, joined onto one line and length-capped, placed in a marked block, and the system prompts say that block is untrusted data.
- News is matched to coins by whole words: "solution" no longer counts as SOL news, nor "whether" as ETH.
- Feed dates with US time-zone abbreviations (EST, PDT) are read correctly.
- Only one market briefing runs at a time.

### Fixed — paper trading and accounting
- **Paper and live equity are stored separately.** After running live, a restart loaded the Kraken balance as paper cash, and the equity chart mixed the two.
- **Paper shorts reserve their proceeds**, so cash can no longer go negative when a short loses.
- Entry fees and short proceeds survive a restart, so P&L for a restored position is exact.
- A restored position with no stored direction no longer has its P&L sign flipped.
- Resetting positions can no longer delete one opened during the reset without closing it.
- The emergency stop keeps its start time across a restart.
- The risk check refuses new trades when equity is zero instead of producing an invalid number.
- The learner scores strategies by percentage return, so the score no longer depends on account size.

### Changed — backtest
- The replay simulates stop-loss and trailing-stop exits, applies the confidence threshold and the daily loss limit, runs on candle time, and fills entries at the next candle's open instead of the signal candle's close.

### Changed — dashboard
- **Prices of sub-cent coins are shown in full** in the tables, chart headers, candle charts and activity messages, instead of €0.00.
- **A banner shows when the bot stops answering**, the Live indicator hides, and actions ask for confirmation. The service worker no longer serves stored API data as if it were current.
- Each poll is applied once; a late response can no longer overwrite a newer one.
- Charts are created only for visible cards, refreshed in sequence, and removed for markets that drop out.
- The duplicate Strategy column in the ledger is gone, and the unused htmx script is removed.

### Notes
- The bot assumes a 0.26% taker fee. Check it against your Kraken fee tier: a higher real fee means paper results understate costs.
- Known limits of live trading are listed in `docs/12-operations.md`. None of the live-trading code has been run against Kraken; it is covered by tests with a simulated exchange.

---

## [0.9.2] — 2026-11-14

Bug fixes from a full audit of the project. Each backend fix has a new GIVEN/WHEN/THEN test that failed before the change.

### Fixed — trading and risk
- **Closing a long now sells the whole long.** A short signal that closed a long removed the entire position but credited cash for the signal's own size (a currency amount at today's price). Cash and the recorded P&L disagreed: after a 10% rise the ledger showed a profit while paper cash had fallen. The fill, fee, order record and P&L now all use the size of the long being closed.
- **Exits are no longer blocked by the loss limits.** A short that closes an open long was refused when the day was at its loss limit, or for "insufficient cash" after the price had fallen, leaving the losing position open until its stop. Exits now skip the cash, per-trade and daily-loss checks; new exposure still passes through all of them.
- **The emergency stop now covers a tick that is already running.** The stop was only checked when a tick started. A slow evaluation (the LLM over many markets) could finish after the stop was pressed and still place orders or queue approvals. Every signal is now checked against the stop before it is acted on.
- **Stop-losses no longer depend on database housekeeping.** A failed write of a price tick or equity snapshot ended the tick before the stop check ran. Stops now run first, and the cash from a stop close is saved in the same tick.
- **Kraken order sizes are exact.** The SDK's float conversion truncated some decimals, so a size of 0.29 was sent as 0.28 (0.57 → 0.56, 1.15 → 1.14, 4.35 → 4.34), and limit prices were shifted the same way. Sizes and prices now reach Kraken unchanged.
- **Risk settings refuse `NaN` and `Inf`.** `NaN` passed every range check and then made the per-trade and daily loss limits never trigger.

### Fixed — LLM and indicators
- **Reasoning text is no longer read as the answer.** With a reasoning model, JSON drafted inside `<think>…</think>` was taken as the reply, so a rejected "long" could become a trade. Reasoning blocks are removed before parsing.
- **A briefing with one missing comma is no longer replaced by a fragment.** The parser returned the first valid inner object instead of repairing the whole reply.
- **A signal analysis without `confidence_scale` no longer counts as LLM approval.** It was treated as a scale of 1.0; it is now neutral.
- **Shutting down no longer marks the LLM as unavailable.** A cancelled request opened the circuit breaker and was retried on the fallback endpoint.
- **RSI of a flat price series is 50, not 100.** Pegged pairs were reported as overbought.
- **Volatility for sub-cent coins.** `atr_pct` was computed from an ATR already rounded to 6 decimal places, which is 0 for coins priced far below a cent; that switched off the volatility checks for them.
- The stop-loss percentage in a signal's text was truncated (29% shown as 28%); it is now rounded.

### Fixed — backtest
- The replay now uses the same momentum formula and the same order sizing as live trading (it ignored the target trade amount and the cash cap).
- With markets of unequal history length, candles were matched by position, not time, and open positions were closed at a price the replay never reached. Series are now trimmed to their shared most recent candles.

### Security
- **Other websites can no longer control the bot.** A page open in the same browser could send a plain `POST` to the local API and, for example, switch to live trading or change settings; CORS headers do not prevent that. State-changing requests from a foreign origin are now refused with `403`. The dashboard, the CLI and scripts are unaffected. This is not a login: keep `-host` on a loopback address unless the network is trusted.
- **News and LLM text can no longer run script in the dashboard.** Headlines, feed names, links, signal theses and approval details were inserted into the page as HTML. A crafted RSS item (or LLM text prompted by one) could run script next to the Approve buttons. All server text is now escaped, news links are only followed when they are `http(s)`, and buttons in generated rows no longer embed server text in inline handlers.

### Fixed — dashboard
- **Emergency stop reports failure.** The header showed "stopped" before the server answered and stayed that way if the request failed, while the bot kept trading. The stopped state now appears only after the server confirms, and a failure is shown as an alert. Resume asks for confirmation.
- **Times are shown in the right time zone.** The ledger, closed trades, rejections, news and restored equity points were stored in UTC but read as local time, so they were off by the browser's UTC offset.
- **Approve, reject, reset and settings actions report what happened.** An approval that did not execute (no price, insufficient funds, emergency stop), a refused position reset ("Cleared undefined position(s)") and failed settings or mode changes were silent.
- The LLM backend label always read "None"; it now shows LM Studio or Remote.
- The approvals page no longer stays on "Loading…" after an error, and its hint points to the Trading settings dialog instead of the removed `.env` setting.
- Chart.js and Alpine.js are pinned to exact versions instead of floating to whatever the CDN serves.

### Changed
- `run_tests.bat` exits with code 1 when vet or a test fails (it always exited 0).
- After a dashboard error, the fallback payload is no longer cached under the real `ETag`.

---

## [0.9.1] — 2026-11-14

### Fixed
- **Signal detail modal centred.** Clicking a signal opened its detail modal at the left edge of the page. The modal's centring was an inline `display:flex`, which Alpine's `x-show` removes when it shows the element. It now uses the shared `.modal-backdrop` / `.modal-panel` classes, like the Settings dialogs.
- The header's live-updates badge had the same inline-style problem and lost its icon/text spacing when shown; it now uses a `.live-indicator` class.

---

## [0.9.0] — 2026-11-14

### Changed
- **Default markets: the 50 biggest EUR pairs on Kraken.** Until markets are saved in Settings, the bot asks Kraken at every start for the 50 biggest online pairs in the base currency. Previously the default was only BTC/EUR and ETH/EUR.
  - Pairs are ranked by 24h traded value (volume × VWAP) from the public `AssetPairs` and `Ticker` endpoints.
  - Stablecoins and other pegged pairs (24h range under 0.5%, e.g. USDC/EUR, USDT/EUR, EURC/EUR) are left out.
  - If Kraken cannot be reached, a built-in list of today's top 50 EUR pairs is used and a warning is logged.
  - A market list saved in the dashboard always wins.
  - Each candle refresh cycle now takes about three minutes (one market every ~3.5 s for Kraken's rate limit), and the dashboard draws a chart for each market.
- **Redesigned Settings dialog.** Settings are laid out to be neat and readable:
  - A side navigation shows one group at a time, and a dot marks groups with unsaved changes.
  - Each setting is a row with its label and help on the left and a control sized for its kind on the right: short right-aligned boxes for numbers, switches for on/off settings, and removable chips with an add box for lists (markets, news feeds, browser origins) instead of text areas.
  - The save confirmation lists only the markets or feeds added and removed, not the whole list.
  - On phones the navigation becomes a row of tabs.

---

## [0.8.1] — 2026-11-14

### Changed
- **Strategy choice moved into the Trading settings dialog.** It now sits between **Trading mode** and **Money** in the dialog opened by the header badges, instead of at the bottom of the Markets panel. Each strategy is a card showing its name and what it does.
- Changing strategy now asks for confirmation, like the other trading settings, and a refused change shows the reason in the dialog. Previously one click switched strategy immediately.

---

## [0.8.0] — 2026-11-14

### Removed
- **`.env` and environment variables.** The bot no longer reads a `.env` file (root or `backend/`) or any environment variable, and `.env.example` is deleted. Existing `.env` files are ignored, and nothing is imported from them: the first start of 0.8.0 uses built-in defaults. Re-enter your settings, including the Kraken API keys, in the dashboard.
- `tradingbot -env`, and the CLI's `--config`, `--mode` and `--environment` flags (they now exit with a usage error). The trading mode and environment are changed in the dashboard only.
- `setup.bat` and `launch.bat` no longer create `.env`.

### Added
- **Settings dialog.** A **⚙ Settings** button in the dashboard header opens a dialog with every setting, grouped (Risk & sizing, Signals, Markets, Kraken, LLM, News & alerts, Advanced).
  - Save lists every change in a confirmation first, so a stray click changes nothing.
  - Lists are edited one entry per line.
  - Closing with unsaved changes asks before discarding them.
- **Settings stored in the database.** Values live in a new SQLite `settings` table; a key never saved uses the built-in default.
  - A saved value that no longer parses is skipped with a `Saved setting ignored` warning, so the bot still starts.
- **Encrypted, write-only secrets.** Kraken, LM Studio and fallback-LLM API keys are encrypted with Windows DPAPI for the current Windows user and stored as `sealed:…`.
  - The API and dashboard only report whether a secret is set; the value is never returned.
  - Secrets can be replaced or cleared.
- **Apply live where possible.** Risk, sizing, signal-threshold, momentum, LLM-only concurrency and alert-webhook settings take effect on save.
  - The risk engine keeps its equity and today's loss counters.
  - Strategies are rebuilt.
  - Other settings are used after a restart. Until then the dashboard shows a "Restart the bot to apply" banner and marks each pending field.
- `GET /api/settings` and `POST /api/settings` (`{"changes": {...}}`). Every value is validated before anything is written, and a bad value saves nothing (400, naming the key).
- **Start-up flags** `tradingbot -db -host -port -log` (defaults `./trading_bot.db`, `127.0.0.1`, `8000`, `trading_bot.log`) for the settings needed before the database opens.
- The backtest takes `-db` and uses the settings saved there, or the defaults if the file does not exist.
- The `DEBUG` setting now does something: it forces DEBUG-level logging.

### Changed
- `tradingbot-cli status` now asks the running bot (`/api/dashboard`) and prints its version, mode, environment, equity and cash. `run` passes `-db -host -port -log` through to the bot.
- Going live checks the Kraken keys the bot is using. Keys saved but not yet loaded give "Kraken API keys were saved but are not in use yet: restart the bot, then switch to live".
- The mode panel's "set `TRADING_MODE` in `.env`" hint is replaced by a **Change trading mode** button.

---

## [0.7.0] — 2026-11-14

### Added
- **Change trading mode and paper/live from the dashboard.** The PAPER/LIVE and mode badges in the header open a Trading settings dialog. Every change needs a confirmation: mode changes and switching back to paper show a confirm dialog, and switching to live requires typing `LIVE`.
- `POST /api/control/trading-mode` and `POST /api/control/trading-environment`. Going live is refused server-side without `"confirmation": "LIVE"` or without Kraken API keys.
- `/api/control` and the dashboard's `control` object now include `trading_mode` and `trading_environment`.

### Changed
- Trading mode and environment are saved in `control_state` (new `trading_mode` / `trading_environment` columns, added automatically to existing databases). A choice made in the dashboard wins over `.env` on every restart; `TRADING_MODE` / `TRADING_ENVIRONMENT` (and the CLI's `--mode` / `--environment`) are now starting values for a fresh database. The bot logs a warning at start-up when a saved choice overrides a different `.env` value.
- Control state now loads before the startup equity restore, so a saved LIVE environment picks the Kraken balance source at boot.

### Fixed
- Docs no longer describe a dashboard per-market live toggle (there is none yet; it is API-only) or say the live toggle ignores the environment.

---

## [0.6.2] — 2026-11-14

### Fixed
- **Windows scripts use CRLF line endings.** `launch.bat`, `setup.bat` and `run_tests.bat` were written with LF endings in 0.6.0, which `cmd.exe` misparses (`setlocal` ran as `tlocal`, some `REM` lines ran as commands). They are now CRLF, and a new `.gitattributes` (`*.bat text eol=crlf`) keeps them CRLF on checkout on every OS.

### Added
- `TestWindowsScriptsUseCRLF` (`cmd/tradingbot/scripts_test.go`) guards the script line endings and the `.gitattributes` rule.
- Troubleshooting entries in docs/12-operations.md for LF-damaged scripts and for rebuilding while the bot is running.

---

## [0.6.1] — 2026-11-11

### Fixed
- **Live orders are blocked in the paper environment.** `App.executeIntent()` now sends an order to Kraken only when `TRADING_ENVIRONMENT=live` **and** the market is toggled live. Previously a market toggled live in the dashboard sent real orders even with `TRADING_ENVIRONMENT=paper` (inherited from the Python version). In the paper environment such orders now execute on paper, and the bot logs a warning plus an activity entry explaining why.

---

## [0.6.0] — 2026-11-11

### Changed

- **Backend rewritten in Go 1.27.** The FastAPI/SQLAlchemy/Pydantic Python backend is replaced by a single Go module (`github.com/James-Chapman/LLMTradingBot`). Entry points live in `cmd/` (`tradingbot` server, `cli`, `backtest`) and packages in `internal/` (`config`, `domain`, `observability`, `storage`, `analysis`, `strategy`, `strategy/calc`, `learner`, `portfolio`, `risk`, `approval`, `control`, `paper`, `kraken`, `news`, `universe`, `llm`, `app`, `server`). HTTP routes, methods, status codes and JSON shapes are unchanged, so the existing dashboard works as before.
- **Kraken access uses the official Go SDK** (`github.com/krakenfx/api-go/v2`) for asset pairs, tickers, OHLC, balances, order placement, order queries and cancellation. Live ticker streaming now uses Kraken WebSocket v2 (`BTC/EUR`-style symbols), still falling back to 5-second REST polling. Order volume and limit prices are rounded to each pair's lot and tick size via the SDK normaliser.
- **LLM integration targets LM Studio.** LM Studio's OpenAI-compatible server (`LMSTUDIO_BASE_URL`, default `http://localhost:1234/v1`) is the primary backend; when `LMSTUDIO_MODEL` is blank the first loaded model is used. A remote OpenAI-compatible endpoint (`OPENAI_*`) is now an optional fallback used only while LM Studio is unreachable, and LM Studio is reclaimed automatically when it recovers. Dashboard model labels read "(LM Studio)" or "(Remote)".
- **Database layer** uses `database/sql` with the pure-Go `modernc.org/sqlite` driver (no CGO). Table and column names are unchanged and all previous migrations are reproduced, so existing `trading_bot.db` files open without conversion.
- **Configuration** — `.env` now lives in the repository root (see `.env.example`); environment variables override it. New keys: `LMSTUDIO_BASE_URL`, `LMSTUDIO_MODEL`, `LMSTUDIO_API_KEY`, `LMSTUDIO_TIMEOUT`, `KRAKEN_REST_URL`, `DATABASE_PATH` (legacy `DATABASE_URL=sqlite:///…` is still accepted).
- **Tooling** — `setup.bat`, `launch.bat` and `run_tests.bat` build and test with the Go toolchain; CI runs `go vet`, `gofmt` and `go test` (`.github/workflows/go.yml`). Tests are Go BDD subtests (`GIVEN … WHEN … THEN …`) using `httptest` fakes with no real network access.
- **Behaviour tweaks found during the port** — the dashboard ETag is bumped after approve/reject/close/reset so the UI refreshes immediately; `DYNAMIC_UNIVERSE_SOURCE=none` now means fixed markets only; `MAX_ETH_ECOSYSTEM_COINS` is now honoured (Python hard-coded 10); live execution refuses orders outside the live environment or without API credentials; CLI `--mode`/`--environment` overrides are now applied.

### Removed

- HuggingFace Transformers local model support (`TRANSFORMERS_LLM_MODEL`, `TRANSFORMERS_TIMEOUT`), the Python virtual-environment bootstrap, `requirements*.txt`, `pyproject.toml`, the pytest suite and the pylint workflow.

---

## [0.5.28] — 2026-04-30

### Changed

- **Strategy warm-up is data-driven** — removed the redundant fixed 20-tick trade warm-up. Indicator-only strategy evaluation still waits for the 34-tick indicator lookback, while LLM-backed strategies now wait until they have enough price history for averaged momentum and at least one cached news item.

---

## [0.5.27] — 2026-04-30

### Changed

- **Momentum calculation made more robust** — live market snapshots now pass rolling `price_history` into the shared strategy calculation helper. Momentum uses a 5-price recent average versus a 5-price baseline average when enough samples are available, with the old two-price calculation retained only as the sparse-data fallback.

---

## [0.5.26] — 2026-04-30

### Changed

- **Strategy calculations centralised** — added `backend/strategy/calculations.py` as the shared source for strategy-level formulas including momentum, ATR-adjusted momentum gates, higher-timeframe alignment, hard filters, indicator voting, confidence components, and position sizing. `BasicStrategy`, `BasicAndLLMStrategy`, `LLMOnlyStrategy`, and the LLM recommendation prompt path now call the shared definitions.

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
