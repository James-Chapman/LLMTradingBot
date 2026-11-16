# Backtesting

## Overview

`backtest run` replays cached Kraken trades (see *Historical data*) through the bot's real code: the strategy registry, `marketdata.Build` (the same function the live loop calls to build `strategy.MarketData`), the risk engine, the learner and the paper engine. Orders go to a simulated exchange that fills them later, at worse prices, with fees and Kraken's order rules. The aim is an honest answer about profitability, so wherever a choice exists the backtest takes the assumption that cannot flatter the result, and no decision can see data from after it. Every assumption and its bias direction is written into the result (`assumptions` in `result.json`).

Code: `internal/backtest` (replay, exchange, result), `internal/marketdata` (shared with live), `cmd/backtest` (command line). Only spot long trading of the non-LLM strategies is modelled; LLM strategies are refused (below). The live default strategy is `basic_and_llm_strategy`, so **a backtest never tests the strategy the bot runs by default** (see *Interpreting results*).

## How a replay works

Time advances in 30-second ticks taken from `histdata.MergeTicks`. By default the strategy runs on every tick (30 s). When the settings file (or a `--vary`) sets `STRATEGY_INTERVAL_SECONDS` explicitly, the strategy runs only on ticks at `--start` + a whole number of intervals; the value must be a positive multiple of 30 or the run is refused. Between strategy ticks only step 1 below runs, so an order still fills on the next 30-second bar after its decision, as live sends it within seconds. The price history, stops, equity points and the per-tick metrics (drawdown, utilisation, `ticks`) then count strategy ticks, as the live loop and stop checker run once per interval. The interval used is recorded as the `strategy_interval` assumption. Tick `τ` sees only the bars with `Start < τ`, each fed to the candles and the volume only at its own tick, so the last price is the close of the bar `[τ-30s, τ)` — the last trade before the sample. Ticks run from `--start + 30s` to the end of the `--end` day. The 25 hours before the start are a **pre-roll**: they feed candles, the rolling 24 h volume and the last price, but the strategy is not called and nothing trades; the price history starts empty, as after a live start-up.

Each tick mirrors the live strategy loop and stop checker (`internal/app/loops.go`), in this order:

1. Orders whose fill tick has come are filled or rejected (see *Execution assumptions*).
2. The last price of every *candidate* market (the `--markets` list, or with `--universe` every cached market in the quote currency) is pushed into its price history (at most `PriceHistoryTicks`), whether or not it is in the universe, and the paper engine's marks and trailing watermarks are updated. So a market that leaves and re-enters the universe has a history of consecutive tick prices, like its candles and volume, never an old price spliced next to a new one. Memory stays bounded for 50 or more markets: per market at most 2 × 200 floats of history, 100 candles per interval and the bars of a 24 h volume window.
3. Stops: each open long is checked against its stop-loss and trailing stop on the tick's last price (never on intra-bar highs or lows), as the live checker does. A breach schedules a sell.
4. Equity is recorded and passed to `risk.Engine.UpdateEquity`; the daily loss limit resets at UTC midnight of the replay clock.
5. Warm-up: while no market has more than 34 prices, the strategy is not called (live's gate).
6. `marketdata.Build` assembles each market's inputs from the price history, the 5 and 15 minute candles, the 24 h volume and a sentiment of 0 (no news, no LLM). Candles come from `histdata.CandleAggregator` (clock-aligned, at most 100, intervals without trades have none); because Kraken's live OHLC response always ends with the forming row, the replay appends a flat candle at the last close with zero volume when the current interval has had no trade yet. That last rule is an assumption about the live endpoint for illiquid pairs, not verified. Whether Kraken also returns flat zero-volume rows for *past* intervals without trades is not documented either: by default the replay leaves them out (as the aggregator does); `--fill-empty-candles` inserts a flat row at the previous close for every empty interval inside the last 100, and the `empty-candles` sensitivity dimension runs both. The 24 h volume is `histdata.RollingVolume` at the tick. The **1-hour candles** (`MarketData.HourCandles`, read by the 1h basic strategies) and the **4-hour candles** (`MarketData.TrendCandles`, read by the 4h basic strategies, `trend_strategy` and the other 4-hour strategies) come from two more aggregators, keeping `marketdata.HourCandleLimit` = 720 and `marketdata.TrendCandleLimit` = 720, with the same forming-row and empty-interval rules. Because nothing before the pre-roll is read, the replay's 4-hour window starts 25 h before `--start`, whereas live fetches 120 days of them from Kraken at once: each 4-hour strategy therefore waits for its warm-up after the start (`trend_strategy` about 27 days for its 160 closed candles, `squeeze_strategy` the same, `rsi_dip_strategy` about 7, `basic_strategy_4h` 7, `basic_strategy_1h` about 16 hours after its 25-hour pre-roll), and computes its EMAs on a shorter history during the first months (assumption `trend_candles`, pessimistic).
7. The strategy evaluates; the learner adjusts each idea's confidence from trades this run has closed; `risk.Engine.EvaluateTrade` decides with the paper engine's available cash.
8. The fully automated gates follow, as live: a sell with no long held is ignored (spot), an idea below `MIN_SIGNAL_CONFIDENCE` is dropped unless it is a rule exit (`TradeIdea.Exit`) of a held long, a market with an open position in the same direction or a pending order is skipped. Every outcome is logged in `decisions`.
9. An approved idea is sized with the live `sizeInBase` rule and sent to the simulated exchange.

Fills are accounted by the paper engine, with the simulated exchange's fill price and fee: a signal exit goes through `paper.Engine.Execute` (as `autoExecute` does), a stop exit through `ClosePosition` (as the stop checker does). Each close is passed to `risk.Engine.RecordTradeResult` and the learner. Live records a signal exit under the closing idea's direction (`short`) and a stop exit under the position's direction (`long`); the replay keeps that quirk so the learner behaves as it would live.

**Universe.** `--markets` is a fixed list. `--universe top:N` selects the top N EUR markets by traded value over the 24 hours before each selection (pegged pairs skipped, `histdata.Universe`), re-selected each month, week or given duration. This is a **different policy from live**, which ranks Kraken's ticker (no point-in-time history) and refreshes on its own schedule; it is a reasonable point-in-time stand-in, not a reproduction. A market that drops out is still evaluated while it holds a position, so it can exit, but opens nothing new.

**Stale positions.** A position whose market has had no bar for longer than `--stale-after` (default 24 h) is *stale*: its last price no longer says what it could be sold for. The replay keeps marking it at the last trade (the curve would otherwise jump), but every equity point records `stale_positions` and `stale_value`, and the result lists each one in `stale_positions` (position, market, first flagged, last bar start, quantity, marked value, and whether it was still stale at the end).

**End of run.** Pending orders are cancelled (`run_ended`). Each open position is sold at the end at the last bar's sell quote (the fill model's price for a sell on that bar) less the fee, with no liquidity cap; the exit is logged as `end_of_run`, or `stale_liquidation` when the position is stale at the end. This is included in `ending_equity` but not in the equity curve. Because a stale position may not be sellable at all, `ending_equity_worst_case` values every stale position at zero; when the stale positions decide the sign of the net P&L the verdict is inconclusive (`inconclusive_stale_positions`).

**Data coverage check.** Before a run reads anything, `histdata.Cache.CheckRange` checks the cache for every market over `[start − pre-roll, end]`: a month missing between the market's first and last bar in that range, or a month with an error-level finding (from `verify` on the month file, or recorded at import by a source still in the manifest), **refuses the run** unless `--allow-data-gaps` is given, in which case each becomes a recorded warning. Coverage that starts late or ends early (a pair listed or delisted inside the period) is always only a warning. The report is kept in `data_check`, the warnings in `data_warnings`; both are printed in the honesty block, and any warning adds the `data_coverage_warning` verdict line.

**Determinism.** Ids are sequential (`idea-000001`, `ord-000001`, `pos-000001`), map iteration is sorted, and the result's JSON keys are sorted: the same inputs give a byte-identical `result.json`. `--seed` is only recorded; the replay uses no randomness.

## Execution assumptions

The live bot only sends **market orders**: `buildAddOrder` (`internal/kraken/execution.go:353`) sets `OrderType: "market"` and switches to a limit order only when `intent.Price` is set, which no caller does (`internal/app/loops.go:670`, `operations.go:377`, `live_trading.go:221`). So every fill pays the taker fee and crosses the spread, and there is no limit-order simulator.

| Item | Default | Bias |
|---|---|---|
| Fee | `--fees kraken-base` = 80 bp taker on every fill, Kraken Pro's lowest-volume tier (https://www.kraken.com/pro/fees, read 2026-11-14). `paper-default` = the paper engine's constant, 80 bp since 0.21.0 (26 bp before); or a number of bp. Charged on the fill value in the quote currency. | neutral for a small account; check your tier |
| Latency | A decision at tick `k` fills at tick `k + 1` on that tick's bar (`--latency`, rounded up to whole 30 s ticks, at least one). Drift over the latency is reported per order. | pessimistic |
| Fill price, `--fill-model spread` | `hs` is the rolling median half-spread measured on the last 240 two-sided bars, floored at 5 bp (`--min-half-spread-bps`); 50 bp before any measurement. On the fill bar: **both sides traded** — a buy pays the higher of the taker-buy and taker-sell averages, a sell gets the lower (so inverted averages never pay the bot), mid = their mean; **only the order's side traded** — that side's average, mid = average ∓ `hs`; **only the other side traded** — the full spread is crossed: a buy pays the sell average × (1 + 2`hs`), a sell gets the buy average × (1 − 2`hs`) (`price_source` `other_side_plus_full_spread`), mid = average ± `hs`; **sides unknown** — close ± `hs`, mid = close. No extra slippage constant. | pessimistic |
| Fill price, `--fill-model fixed` | The fill bar's close ± `--half-spread-bps` (default 10). | unknown (a sensitivity tool) |
| Size impact | Participation = order value / the quote value **taken on the order's side** in the fill bar (taker buys for a buy, taker sells for a sell; if that side did not trade, half the bar's two-sided notional; if sides are unknown, close × volume / 2). Above 0.10 (`--impact-threshold`) the price worsens by `--impact-bps` (default 100) × (participation − 0.10); above 0.25 (`--liquidity-cap`) the order is rejected. Example: a 2,000 buy into a bar with 10,000 of taker buys has participation 0.20 and pays 100 × 0.10 = 10 bp impact; 3,000 (0.30) is rejected. A fill bar with no trades rejects the order (`insufficient_liquidity`). The linear form and both constants are not calibrated against Kraken's order book. | unknown |
| Stops | Checked on the 30 s last price, filled on the next bar, so a gap fills beyond the stop. | neutral (as live) |
| Order rules | From `--pair-rules`, a saved copy of Kraken's `GET https://api.kraken.com/0/public/AssetPairs` response (the whole JSON, `{"error":[],"result":{...}}`; `ordermin`, `costmin`, `lot_decimals`, `pair_decimals` per pair, `XBT`/`XDG` read as BTC/DOGE). Save it yourself with a browser or `curl`; the backtest never fetches it. Without it, or for a pair it lacks: ordermin 0, costmin 5, 8 lot decimals, no price rounding. Quantities round down, prices round against the bot. Below ordermin/costmin, or cash short of value plus fee, the order is rejected. Today's rules are applied to past dates. | unknown |
| Shorts | None: a sell signal only exits a held long. | neutral (spot account) |
| Money | The paper and risk engines' float64 accounting, unchanged. | neutral |

Each filled order in `orders` breaks its cost into latency drift, spread, impact and rounding against the reference price (the last price at the decision), with the participation, fee and fee rate. Latency drift is mid − reference and spread is price − mid, both against the bot, using the fill bar's estimated mid (`fill_bar_mid`). Rejected and cancelled orders are kept with their reason.

**Risk sizing fee.** The risk engine caps a buy at cash / (1 + `FEE_AND_SLIPPAGE`). The live default (0.9 %; 0.36 % before 0.21.0) is below the run's 80 bp fee plus half-spread, so near all-in buys would pass risk and then be rejected for funds. Unless the settings file sets `FEE_AND_SLIPPAGE`, the run sets it to the taker fee plus the expected half-spread (the fixed model's, or max(50 bp, floor) for the spread model). An explicit value is kept, and a warning is recorded when it is below the fee. `risk_fee` in the result records which happened.

## Safety

- **No live trading is reachable.** `internal/backtest/deps_test.go` runs `go list -deps` on `internal/backtest`, `internal/histdata` and `cmd/backtest` and fails if any depends on `internal/app`, `internal/secret`, `internal/server`, `internal/kraken`, `internal/storage`, `internal/livebook` or the Kraken SDK. The backtest therefore cannot place an order, open the bot's database (`trading_bot.db`) or read secrets.
- **No network.** `backtest run` reads only the data cache and the files you pass; it fails if the cache directory does not exist rather than creating it. `deps_test.go` checks the dependencies for windows, linux and darwin with `CGO_ENABLED=0` and every build tag found in the packages, forbids `database/sql`, `modernc.org/sqlite`, `os/exec` and `golang.org/x/sys/windows`, and allows a direct `net/http` import only in `internal/histdata/rest.go` (the `fetch` command). `net/http` is still linked transitively because `internal/strategy` imports `internal/llm`; the LLM strategies are refused before any client is built, and `TestBacktestMakesNoNetworkCall` replaces `http.DefaultTransport` with one that fails the test and runs a full replay.
- **Settings from a file.** `--settings` takes a JSON object of dashboard keys (`{"MIN_SIGNAL_CONFIDENCE": 0.9}`), validated by the same field registry as the dashboard. Only the Risk and Signals groups and `BASE_CURRENCY` are accepted; a secret key is refused. Without the flag the built-in defaults are used, never the dashboard's saved settings. The result records every setting used (`settings`), where they came from (`settings_source`) and which keys the file set.
- **LLM strategies are refused.** The `basic_and_llm_strategy_*` versions, the 4-hour strategies' LLM versions and `llm_only_strategy` depend on news and an LLM that cannot be replayed: the news is not reproducible and a model trained later has seen the future. The run fails before reading data.
- **No look-ahead.** Tests run the real strategy on two series that differ only after a time T and require identical decisions and fills up to T (`TestReplayNoLookAhead`), and compare the replay's candles, volume and last price against a naive recomputation from bars before each tick, including ticks in intervals without trades (`TestReplayInputParity`). `marketdata.Build` is pinned to the pre-move live behaviour by `internal/app/marketdata_golden_test.go`.

## Running a backtest

```powershell
go run ./cmd/backtest run --markets BTC/EUR,ETH/EUR --start 2025-01-01 --end 2025-12-31 `
    --capital 10000 --strategy basic_strategy_1h --fees kraken-base --pair-rules data/assetpairs.json
go run ./cmd/backtest run --universe top:20 --universe-every month --start 2025-01-01 --end 2025-12-31 `
    --settings my-settings.json --output results/top20-2025
go run ./cmd/backtest run --markets BTC/EUR,ETH/EUR --start 2026-01-01 --end 2026-11-14 --strategy trend_strategy `
    --settings docs/backtest-settings/trend_strategy.json --output results/trend-2026
```

`docs/backtest-settings/trend_strategy.json` holds the wider stops `trend_strategy` was tested with (`STOP_LOSS_PCT` 0.10, `TRAILING_STOP_PCT` 0.15); with the defaults (5 % and 3 %) the trailing stop cuts its trends short. Results: [06-strategy-and-learning.md](06-strategy-and-learning.md#trend-strategy-internalstrategytrendgo).

| Flag | Default | Meaning |
|---|---|---|
| `--markets` / `--universe top:N` | (one required) | fixed list, or point-in-time top N |
| `--universe-every` | `month` | `month`, `week` or a duration (`72h`) |
| `--start`, `--end` | (required) | UTC days, end inclusive |
| `--capital` | `STARTING_CAPITAL` | starting cash |
| `--strategy` | `basic_strategy_1h` | strategy id |
| `--fees` | `kraken-base` | `kraken-base`, `paper-default` or bp |
| `--fill-model` | `spread` | `spread` or `fixed` |
| `--latency` | `30s` | rounded up to ticks |
| `--half-spread-bps`, `--min-half-spread-bps`, `--impact-threshold`, `--impact-bps`, `--liquidity-cap` | 10, 5, 0.10, 100, 0.25 | exchange model |
| `--stale-after` | `24h` | a position whose market has not traded for longer is stale |
| `--fill-empty-candles` | off | insert flat zero-volume candles for past intervals without trades |
| `--allow-data-gaps` | off | run despite missing months or error-level findings, recording them as warnings |
| `--pair-rules` | built-in fallback | saved AssetPairs JSON |
| `--settings` | built-in defaults | settings JSON |
| `--data` | `data/history` | data cache |
| `--no-learner` | off | never adjust confidence |
| `--equity-every` | `1h` | equity curve sampling; each point also keeps the lowest equity since the previous one |
| `--seed` | 1 | recorded only |
| `--output` | `results/<strategy>_<start>_<end>` (`results/<command>_<strategy>_<start>_<end>` for the robustness commands) | report directory |
| `--allow-reconcile-warning` | off | exit 0 even when the exact reconciliation fails (see *Reports and metrics*) |

The command prints the honesty block, the verdict, a summary (ending equity, net, fees, trades) and the reconciliation, and writes the report set described in *Reports and metrics* into `<output>`. `result.json` (schema version 3) holds the run's inputs, `settings`, `exchange`, `assumptions`, `data_provenance` (the manifest entry of every cached month read, pre-roll included, for every market read: file and SHA-256, and each source with its SHA-256 and import time, from `histdata.ProvenanceFor`), `universe` selections, `decisions`, `orders`, `trades`, the `equity` curve (each point with its running `peak`), the totals, and since schema 2 `initial_markets`, `tick_stats` (per-tick drawdown, exposure, utilisation and end-of-UTC-day equity), `learned_outcomes` (each closed trade as the learner saw it) and `learner_seed_outcomes`, and since schema 3 `settings_source`, `risk_fee`, `fill_empty_candles`, `stale_after`, `stale_positions`, `ending_equity_worst_case`, `data_check`, `data_warnings`, each order's `fill_bar_mid` and each equity point's `stale_positions` and `stale_value`. Exit codes: 0 success, 1 run failure (missing cache, data gaps without `--allow-data-gaps`, LLM strategy, bad settings, a reconciliation warning without `--allow-reconcile-warning`), 2 usage error. `backtest` with no arguments prints usage.

Throughput: about 6,500 ticks per second for three markets on a desktop CPU (`BenchmarkReplay`), so a year of three markets (about 1.05 million ticks) takes about three minutes; time grows roughly with the number of markets.

## Historical data

Backtests replay real Kraken trades stored locally as 30-second bars. The code is `internal/histdata`; the commands are `backtest data <subcommand>`. Nothing here talks to the bot's database (`trading_bot.db`), reads secrets or links the live order code: `internal/histdata/deps_test.go` fails the build if the package (or its tests) depends on `internal/app`, `internal/secret`, `internal/server`, `internal/kraken` or `internal/storage`.

### Sources

| Source | Command | Use |
|---|---|---|
| Kraken time-and-sales archive (zip of one CSV per pair, quarterly updates) | `backtest data import --archive <dir\|zip\|csv>...` | Bulk history. The owner downloads it; the tool never does. |
| Kraken REST `GET /0/public/Trades` | `backtest data fetch --markets BTC/EUR --from DATE --to DATE` | Top-up after the newest archive, or small ranges. |

**Archive CSVs.** The importer reads a directory (recursively), a `.zip` (entries are streamed, never extracted; `archive/zip` checks each entry's CRC-32) or one `.csv`. The layout is detected, not assumed:

- a first row whose first field is not a number is a header; columns are found by name (`timestamp`/`time`, `price`, `volume`/`qty`/`amount`, `type`/`side`, `trade_id`/`id`);
- without a header, 7 columns are `timestamp,price,volume,type,order_type,misc,trade_id` and 3 columns are the older `timestamp,price,volume`;
- timestamps are UNIX seconds, integer or decimal (`1688669597`, `1688669597.8277369`; digits past nanoseconds are truncated), always UTC;
- `type` is `b`/`buy` or `s`/`sell` (the taker side). Empty or missing means unknown: the trade counts in the bar's `Volume` but in neither `BuyVolume` nor `SellVolume`, and the import summary says how many trades and how much volume that is. Any other value rejects the file (it is probably not a trade file).

**File name → market.** Kraken does not document the per-pair file names, so the rule is explicit and every file it cannot map is listed as `UNMAPPED` and skipped, never guessed (`internal/histdata/market.go`):

1. a `--map FILE=BASE/QUOTE` override for the file's stem wins;
2. the stem is upper-cased and `-`, `_`, `.`, `/` and spaces are removed;
3. an eight-letter legacy name `X<base>{Z|X}<quote>` (`XXBTZEUR`, `XETHXXBT`) splits in the middle;
4. otherwise the longest known quote suffix is split off: `EUR USD GBP CAD JPY CHF AUD USDT USDC DAI PYUSD XBT BTC ETH`;
5. `XBT` becomes `BTC` and `XDG` becomes `DOGE` on both sides.

So `XBTEUR.csv` → BTC/EUR, `XDGEUR.csv` → DOGE/EUR, `ETHXBT.csv` → ETH/BTC. Names with an interval suffix such as `XBTEUR_1.csv` (OHLCVT files) are unmapped on purpose. Renamed tokens (MATIC→POL, RNDR→RENDER) are not merged; use `--map` if you want that.

**REST paging.** Pairs are sent in Kraken's altname form (`BTC/EUR` → `XBTEUR`; BTC is sent as XBT and DOGE as XDG, otherwise `BASEQUOTE`). The response is keyed by the official name (`XXBTZEUR`), so the single key that is not `last` is taken. Each page asks for 1000 trades from the `since` cursor (nanoseconds; the first call uses `--from`), and the next page uses the response's `last`. Calls are at least 1.1 s apart. `EAPI:Rate limit exceeded`, HTTP 429/5xx and `EService:Unavailable/Busy` are retried with backoff from 5 s doubling to 2 min (8 tries); `EService: Throttled: <unix>` waits until that time. Paging stops at the first trade at or after `--to`, or when a page brings nothing new (caught up with the present). Trades go to a spool, `rest/<BASE-QUOTE>/trades.csv` in the archive's CSV layout, with the cursor in `state.json` saved after every synced page. Running the same command again resumes from the saved cursor (the spool is first cut back to its last committed length), and trades whose id is not above the last stored id are skipped, so overlapping pages never duplicate a trade. A new `--from` starts a new spool. After paging, the whole spool is imported as source `rest`.

### Bars

Trades become 30-second bars aligned to UTC (`Start` = trade time truncated to 30 s; a trade at exactly :30.000 opens the next bar). A bar holds open/high/low/close, volume, trade count and the taker-side volume and notional (price × volume) totals. Intervals with no trades have no bar. The builder keeps one bar in memory; a month of bars (at most 89,280) is buffered before it is written.

Trades must be in time order (Kraken's archive is ordered by timestamp; equal timestamps are fine). An earlier trade than the previous one **rejects the file** with its line number. Rows are not re-sorted: a disordered file is a data problem to look at, not something to fix silently. Months completed before the bad line have already been written; re-importing a corrected file replaces them.

### Cache layout

Default directory `data/history` (`--cache` to change; ignored by git):

```
data/history/
  manifest.json                 provenance, see below
  bars/BTC-EUR/2026-07.bars     one file per market and UTC month
  rest/BTC-EUR/trades.csv       REST spool (raw trades) and state.json
```

**Bar file format, version 1** (little-endian), chosen because it is exact (raw IEEE-754 bits, so every float64 round-trips), fixed-width (a month is one read, the bar count is the file size), and readable from any language without this code:

| Offset | Type | Header field (64 bytes) |
|---|---|---|
| 0 | 8 bytes | magic `HISTBARS` |
| 8 | uint16 | format version, 1 |
| 10 | uint16 | record size, 88 |
| 12 | uint32 | bar interval in seconds, 30 |
| 16 | int64 | month start, UNIX seconds |
| 24 | uint64 | record count |
| 32 | 32 bytes | SHA-256 of all record bytes |

Each 88-byte record: int64 start (UNIX seconds), float64 open, high, low, close, volume, int64 trades, float64 buy volume, buy notional, sell volume, sell notional. Records are sorted, unique and inside the month. Python: `struct.unpack('<q5dq4d', record)`.

Every write goes to a temporary file in the same directory, is synced, then renamed over the old file, so a crash leaves the old month or the new one, never half of one.

**Incremental, idempotent imports.** A source *covers* a time range: from its first kept trade's bar (or `--from`, or the REST start) to the bar after its last trade (or `--to`), clipped to each month. Rebuilding a month keeps the existing bars outside that range and replaces those inside it. Importing the same trades again therefore produces byte-identical files; the Kraken Full archive and an overlapping quarterly file replace each other's bars rather than adding up; a REST top-up replaces only its own range. A month in which a source has no trades is not touched.

### Reading bars

`histdata.NewCacheReader(dir)` implements `histdata.Reader` (`internal/histdata/types.go`): `Markets()`, `Coverage(market)` and `Bars(market, from, to)`, which streams bars with `from <= Start < to` in order, opening only the months that overlap the range. Each month file is checked whole (header, size, checksum, order) before any of its bars is yielded, so a consumer never sees a corrupt bar; a damaged file ends the stream with an error.

### Helpers for the replay

These reproduce the inputs the live bot sees (see `internal/histdata/series.go`):

- `MergeTicks(r Reader, markets []string, from, to time.Time) iter.Seq2[Tick, error]` — one `Tick` per 30-second boundary `T` in `(from, to]`. `Tick.Time` is the instant the strategy would sample; each `MarketPrice` is the last trade before `T` (the close of the bar `[T-30s, T)` when `HasBar`, otherwise carried forward). A market appears once it has traded inside the range; ticks are produced even when no market has a price yet.
- `NewCandleAggregator(interval time.Duration, keep int) (*CandleAggregator, error)`, `Add(Bar) error`, `Candles(at time.Time) ([]Candle, bool)` — clock-aligned candles (5 m at :00/:05…, 15 m at :00/:15…), oldest first, the last one possibly still forming (the bool), like the last row of Kraken's OHLC response. Feed a bar only at its tick (`Start+30s`). Intervals without trades produce no candle. `keep` is how many rows to return (live keeps 100).
- `NewRollingVolume(window time.Duration) *RollingVolume`, `Add(Bar)`, `At(t time.Time) float64` — base volume of bars that ended in the window before `t` (24 h for the ticker's rolling volume). Queries must not go back in time.
- `Universe(r Reader, at time.Time, opts UniverseOptions) (UniverseResult, error)` — the `Top` markets quoted in `Quote` by traded value in the 24 h (or, with `RankBy7d`, 7 days) before `at`, from cached bars that ended by `at`. Pegged pairs are excluded by the live bot's rule (24 h high-low range under 0.5 % of the low, restated from `internal/kraken/top_pairs.go`). Traded value is the side notionals plus, for trades without a side, volume × mean(O,H,L,C). Unlike live there is no listing status: a market that traded counts.
- `ProvenanceFor(dir, market string, from, to time.Time) ([]Provenance, error)` — the manifest entries of the months a run read; reports cite them.

### Commands

```powershell
backtest data import --archive D:\kraken\Kraken_Trades_2026Q3.zip --markets BTC/EUR,ETH/EUR
backtest data import --archive D:\kraken\trades --from 2025-01-01 --map RNDREUR=RENDER/EUR
backtest data fetch --markets BTC/EUR --from 2026-10-01 --to 2026-11-01
backtest data verify --markets BTC/EUR --gap 2h
backtest data verify --ohlcvt D:\kraken\ohlcvt\XBTEUR_1.csv --ohlcvt-market BTC/EUR --ohlcvt-minutes 1
backtest data list
backtest data universe --date 2026-07-01 --quote EUR --top 50 [--rank 7d] [--json]
```

Dates are UTC, `YYYY-MM-DD` (midnight) or RFC 3339. `import --dry-run` validates and summarises without writing. Exit codes: 0 success, 1 failure (or `verify` found an error-level finding), 2 usage error. Progress goes to stderr every million rows (import) or 60 pages (fetch).

## Data validation and provenance

Findings are recorded, shown and never silently fixed. Each has a kind, a severity (`info` = a market fact, `warning` = likely data problem, `error` = data cannot be trusted as stored), the market, the time (and end, for gaps) and a detail. The first 20 of each kind are listed; all are counted.

### At import (per trade)

| Kind | Rule | Effect |
|---|---|---|
| `invalid_value` | price or volume unparseable, non-finite, or not > 0; timestamp unparseable | row skipped |
| `timestamp_out_of_range` (error) | before 2013-01-01 (before Kraken) or more than 24 h after the import clock | row skipped |
| `timestamp_out_of_range` (warning) | outside the quarter named in the source (`…2026Q3…`) | kept |
| `duplicate_trade_id` | id not above the previous id (Kraken ids increase per pair) | kept |
| `price_jump` | consecutive trade prices differ by more than `--jump` (default ×1.25 either way) | kept |
| `no_trades` | a run of empty 30 s intervals of at least `--gap` (default 1 h) | info |
| `side_unknown` | trades without a taker side | info, with count and volume |
| (none) | a trade earlier than the previous one | file rejected |

Duplicates are flagged, not dropped: dropping them would be a silent fix. If a duplicate count is non-zero, look at the source before trusting its volumes.

### On demand: `backtest data verify`

- **Integrity:** every month file's header, size, checksum and bar order (`corrupt_file`, error); when `manifest.json` exists, a month file it does not list or whose SHA-256 differs from the recorded one (`not_in_manifest`, warning).
- **Bar sanity:** finite positive prices with low ≤ open/close ≤ high, positive volume, side volumes not above the total (`invalid_value`, error); bar open against the previous close beyond the jump factor (`price_jump`). Trade-level checks happen at import because the cache keeps bars, not trades.
- **Gaps:** runs of empty intervals of at least `--gap`. Inside one month and one source they are `no_trades` (info — genuine for illiquid pairs). A gap that reaches a month boundary (where a missing month file would show) or the edge of an import source's coverage is `boundary_gap` (warning — likely missing data). A period when every market whose coverage spans it was silent at once is `all_markets_gap` (warning — an outage or missing data; needs at least two markets).
- **Coverage per market:** first and last bar, months, bars, the share of 30 s intervals with a trade, trades, volume, gap count, longest gap, bytes, and the finding counts recorded at import.
- **OHLCVT cross-check** (optional): candles built from the cached bars are compared with a Kraken OHLCVT file (`timestamp,open,high,low,close,volume,trades`, no header). Kraken does not say whether the timestamp is the candle's open or close, so both are tried and the better match is reported as the convention. Tolerances: prices within 1e-9 relative (they are trade prices, so they should be identical), volume within max(1e-8, 1e-6 × volume) (summation order and the file's printed decimals), trade count exact. Candles on one side only are `ohlcvt_missing`; different ones `ohlcvt_mismatch`. Only the cache's covered span is compared.

### Manifest

Imports are serialised by a lock file, `.lock` in the cache directory, created exclusively (`O_EXCL`) and holding the pid and start time. Under the lock an import reloads `manifest.json` from disk before merging its months, so several `data fetch` or `data import` processes on one cache (for example one per market, in parallel) each add to the manifest instead of overwriting what another wrote. A waiting import retries every 250 ms until its context ends. The holder refreshes the lock's modification time every 30 s; a lock not refreshed for 2 minutes is treated as left by a crashed import and taken over. A dry run takes no lock. If a process is killed, the stale `.lock` is cleared by the next import after 2 minutes; deleting it by hand is safe when no import is running.

`manifest.json` (written atomically after each source file; keys sorted) holds the tool version (`histdata/1.0.0`), the build's VCS revision when known, the bar file version, and for each market and month:

- the month file's path, SHA-256 and bar count;
- one record per source merged into it: source name (`Kraken_Trades_2026Q3.zip!XBTEUR.csv`, `Kraken_Trades_2026Q3/XBTEUR.csv` for an extracted directory, or `rest`), SHA-256 of the source bytes (for a zip entry, of the decompressed entry; for REST, of the committed spool), tool version, import time, covered range, first and last trade, trade count, bars written, trades without a side, and the month's findings.

A record wholly inside a newer import's coverage is dropped as superseded; partially overlapped records stay. Import time is the only wall-clock value; reports built from the same cache cite the same entries.

### Known limits

- The archive's file naming, XBT/BTC spelling and timestamp precision are not documented by Kraken; the rules above handle the likely forms and report the rest.
- Delisted pairs are included only if Kraken's archive includes them (not verified). A universe built from the cache cannot contain a market the cache lacks, so check `backtest data list` against the period you test.
- The REST spool for one market covers one start date; fetching from a new `--from` replaces it (the cached bars it produced stay).

## Reports and metrics

Code: `internal/backtest/{report,render,metrics,track,costs,reconcile,benchmark,verdict}.go`. Every run directory holds:

| File | Content |
|---|---|
| `result.json` | the raw replay (above) |
| `summary.json` | `kind: "run"`, `summary_schema_version` 1, `tool_version`: honesty block, verdict, metrics, gross-to-net bridge, costs, benchmarks, reconciliation, signal funnel |
| `summary.md` | the same for a reader; it always opens with **Read this first** (the honesty block) and the verdict, before any number |
| `trades.csv` | one row per round trip: entry/exit time, holding seconds, quantity, reference and fill prices, gross P&L, cost components, fees, net P&L, return %, exit reason |
| `orders.csv` | every order, filled or not, with its status, reason, prices, participation and costs |
| `equity.csv` | the sampled equity curve with its peak and drawdown |
| `costs.json` | costs in total and per market |
| `run.json` | everything needed to reproduce the run: command line, tool version, build revision (from the Go build info), seed, inputs, settings, exchange model, assumptions, universe selections, data provenance |

Reports contain no wall-clock time, no random ids and no map-order dependence: the same inputs give byte-identical files.

**Viewing results in the dashboard.** The running bot serves the result directories under `results/` (relative to its working directory) read-only through `GET /api/backtests` and `GET /api/backtests/{id}` (verdict, honesty block, metrics, downsampled equity curve, paged trades and orders, costs, `summary.md`); see [08-api-endpoints.md](08-api-endpoints.md#backtests). Write runs with `--output results/<name>` to see them there. The dashboard's **Backtests** view (`#/backtests`) lists them and shows each run read-only: the "Read this first" lines before any number, then the verdict as written, the key figures, equity and drawdown, benchmarks, costs, reconciliation, paged trades and orders, and the assumptions and command line; multi-run kinds get their own tables (see [11-frontend.md](11-frontend.md#backtests-view)). The server only reads these files (never `result.json`), never runs a backtest, and refuses any id that would leave the results folder.

**Metric definitions.** Money is the quote currency; *capital* is the starting capital.

| Metric | Definition | Null (`n/a`) when |
|---|---|---|
| Net P&L | ending equity − capital; open positions at the end are valued as in *End of run* | never |
| Annualised return | `(ending/capital)^(365/days) − 1`, 365 because crypto trades every day | under 30 days, or the account was wiped out |
| Gross P&L | Σ sell qty × reference price − Σ buy qty × reference price: every fill at the last price at its decision, no fee and no execution cost | never |
| Execution cost | Σ qty × (fill − reference) against the bot: latency drift + spread + size impact + price rounding (+ an unexplained residual, which is 0 on a real replay) | never |
| Round trip | a closed position; positions still open at the end are counted, and flagged, as end-of-run closes | |
| Win / loss / breakeven | net P&L > 0.005, < −0.005, otherwise breakeven (half a cent) | |
| Win rate | wins / round trips | no round trips |
| Average and median P&L per trip, average win and loss | over the trips of that kind | none of that kind |
| Payoff ratio | average win / −average loss | no win or no loss |
| Profit factor | Σ wins / −Σ losses | no losing trip (then a note says so: an infinite value is not reported) |
| Max drawdown (by % and by amount) | the largest fall from a running peak of equity, measured **every 30 s tick** and at the end-of-run valuation, not on the sampled curve; each with its peak, trough and recovery times | no fall |
| Longest drawdown | the longest time below a previous peak; an unrecovered one counts to the end | no fall |
| Sharpe | mean / sample standard deviation of daily returns × √365, risk-free rate 0. Daily returns use end-of-UTC-day equity, the first against the capital | fewer than 60 daily returns or 30 round trips |
| Sortino | mean / √(Σ min(r, 0)² / n) × √365 | as Sharpe, or no negative day |
| Exposure | share of ticks with any open position | |
| Capital utilisation | positions value / equity per tick: average and maximum | |
| Turnover | traded value / average tick equity | no ticks |
| Signal funnel | ideas, ideas sent as orders, filled, not traded by reason (risk reasons grouped by the text before `:`), order rejections by reason | |

**Gross to net.** `net = gross − latency drift − spread − impact − rounding − residual − fees`. The bridge is computed exactly with `math/big.Rat` from the order log and compared with the engine's net P&L; the components are exact (Sterbenz) differences of logged float64 prices, so the residual is exactly 0 unless the log is inconsistent.

**Exact reconciliation.** `Reconcile` recomputes the account from the order log in exact rational arithmetic on the exact values of the logged float64 numbers: `capital − Σ buys − Σ fees + Σ sells = final cash`, each fee recomputed as qty × fill price × fee rate, the open quantity per position, open positions marked at their last fill tick's close, final equity, total fees and Σ round-trip P&L. A difference above the tolerance `max(1e-9, 1e-10 × (capital + traded value + fees))` in the quote currency is an **accounting warning**: it is printed first in the honesty block and the verdict, and `backtest run` (and every robustness command) exits 1 unless `--allow-reconcile-warning` is given. The tolerance only absorbs the paper engine's float64 rounding.

**Benchmarks.** Each is computed over the same period, from the same cache, with the same fill model, fee and order rules:

- `cash`: the capital held, 0 %.
- `buy_and_hold:<market>` for each traded market (or the single initial market): buy with all the capital at the fill model's quote one latency after the first tick with a price, sell at the end like the strategy's open positions (the last bar's sell quote less the fee). No size impact or liquidity cap is applied, which slightly flatters the benchmark.
- `equal_weight_buy_and_hold` over the initial markets when there are several: the capital split equally, each leg as above.

The primary benchmark (equal weight, else the single market's buy-and-hold) gives "strategy minus benchmark" in percentage points.

**Honesty block.** Every human-readable summary starts with "NOT THE LIVE STRATEGY" (the tested strategy against the live default); then an accounting warning if any; whether the data was checked and every data warning; the number of duplicate trade ids in the data read; stale positions and the worst-case net P&L; the risk sizing fee; that live's default mode is manual and where the settings came from; the candle rule (empty intervals omitted or filled); that the price history starts empty while live restores saved ticks; the period and markets; the number of round trips and end-of-run closes; "not statistically meaningful" under 30 round trips; "too short" under 90 days; that this is one historical path; survivorship (`explicit_list_warning` for `--markets`, `point_in_time_partial` for `--universe`, both biased upwards), and for `--universe` that it is a different policy from live; the fee and that it must match your tier; and that a setting chosen by looking at this period makes the result in-sample. `honesty.statistically_meaningful` is true only with at least 30 round trips over at least 90 days.

## Robustness testing

Code: `internal/backtest/{robustness,sensitivity,regimes,compare,render_multi}.go`, `cmd/backtest/robust.go`. Each command takes every `backtest run` flag as its baseline, writes `summary.json` (with its own `kind`), `summary.md` and `run.json` (`kind`, base inputs and command line) at the top of `--output`, and each run's full report set under `runs/NN_<label>/`. Runs are independent and deterministic; `--parallel N` runs N at once, results are collected in a fixed order, and output files are byte-identical for any N (`--parallel` is left out of the recorded command line). Progress is printed to stderr as `[k/n] label`.

```powershell
go run ./cmd/backtest split       --markets BTC/EUR --start 2025-01-01 --end 2025-12-31 --oos-fraction 0.3
go run ./cmd/backtest walkforward --markets BTC/EUR --start 2024-01-01 --end 2025-12-31 --train 90d --test 30d --tune STOP_LOSS_PCT=0.01,0.02,0.03 --parallel 4
go run ./cmd/backtest regimes     --markets BTC/EUR,ETH/EUR --start 2024-01-01 --end 2025-12-31 --bucket month
go run ./cmd/backtest sensitivity --markets BTC/EUR --start 2025-01-01 --end 2025-12-31 --vary MIN_SIGNAL_CONFIDENCE=0.6,0.7 --grid fees,latency --parallel 4
go run ./cmd/backtest compare results/split_x results/walkforward_x results/sensitivity_x
```

**`split`.** `--split YYYY-MM-DD` (first out-of-sample day) or `--oos-fraction F` (the last F of the period; the split is rounded down to a UTC day). Both periods run separately: each with its own pre-roll, the full capital and an empty learner, so nothing from the in-sample run reaches the out-of-sample one. The summary states that parameters may only be chosen using the in-sample period.

**`walkforward`.** `--train` and `--test` lengths (`90d` or a Go duration), `--step` (default the test length; shorter is refused, so test windows never overlap). Each window trains on `[s, s+train)` and tests on `[s+train, s+train+test)`. The learner's state is its list of closed outcomes; the test run starts seeded with the outcomes of its train run (`Config.LearnerSeed`), as if the bot had run through the train window. `--tune KEY=v1,v2,...` runs each train window once per value and keeps the best train net return (the first on ties) for the test; the summary reports the chosen values, how often the choice changed and the mean train vs test return of the chosen values. The aggregate covers the test windows only: compounded return, min, median, max, share of windows net positive and round trips.

**`regimes`.** One continuous run, cut into buckets (`--bucket day|week|month|quarter`, weeks from Monday, UTC). Trend: the mean market return over the initial markets in the bucket against `--bull`/`--bear` (defaults ±1 % a day, ±2 % a week, ±5 % a month, ±10 % a quarter). Volatility: high when the bucket's median daily realised volatility (5-minute log returns) is above the whole period's median, else low; buckets without enough data are `unknown`. Per bucket: P&L and return from the end-of-UTC-day equity, primary benchmark return, round trips assigned by exit time, drawdown on the sampled equity curve. Cells (trend × volatility, and each alone) with fewer than 3 buckets are marked thin.

**`sensitivity`.** Varies one input at a time against the baseline: `--dims` (default `fees,latency,half-spread,impact,empty-candles` with 26/40/80/120 bp, 1/2/4/10 ticks, 2/5/10/20 bp, 50/100/200/400 bp and `false`/`true` for `--fill-empty-candles`), each overridable with `--fees-list`, `--latency-list`, `--half-spread-list`, `--impact-list`; `capital` and `fill-model` need `--capital-list` and `--fill-models`. `--vary KEY=v1,v2` (repeatable) varies a dashboard setting, validated by the settings registry (secrets and settings that do not affect a backtest are refused). `--grid A,B` also runs every combination of two varied dimensions. A variant equal to another (fee preset labels ignored) reuses its run. Each row says whether the sign of the net P&L survives. The **break-even fee** is the linear interpolation between the bracketing tested fee levels — an estimate, not a measurement — or "above the highest" / "below the lowest" tested fee, or "not monotonic".

**`compare DIR DIR ... [--json]`.** Reads the `summary.json` and `run.json` of two or more result directories of any kind, prints their key numbers side by side and the inputs that differ (`exchange.fee_bps`, `settings.<KEY>`, ...).

## Interpreting results

A single run cannot show a lasting advantage, and the verdict never says it does.

**What a backtest is not.** Each of these is in `assumptions` and in the honesty block:

| Disclosure | Why it matters | Bias |
|---|---|---|
| Strategy differs from live (`strategy_scope`) | Live defaults to `basic_and_llm_strategy_1h`; the LLM strategies cannot be replayed, so the result says nothing about it. | unknown |
| Universe policy differs from live (`universe`) | `--universe` ranks cached bars point in time; live ranks Kraken's current ticker. | optimistic (survivorship) |
| Mode and settings (`trading_mode`, `settings_source`) | Live defaults to manual approval; the backtest executes every approved idea at once. Settings are the built-in defaults or `--settings`, never the dashboard's. | unknown |
| Empty candles (`empty_candles`) | Whether live OHLC includes flat rows for intervals without trades is not documented; compare `--fill-empty-candles` in the sensitivity run. | unknown |
| Duplicate trades (`duplicate_trades`) | Trades flagged `duplicate_trade_id` at import stay in the bars, so volume and liquidity can be overstated. | optimistic |
| Price history starts empty (`price_history_start`) | Live restores saved ticks at start-up; the replay starts empty, so the first ~35 trading ticks make no decision. | pessimistic (small) |
| Strategy interval (`strategy_interval`) | 30 s unless `STRATEGY_INTERVAL_SECONDS` is set explicitly; the live default is the dashboard setting. Fills between strategy ticks happen on every 30-second bar. | unknown at the default, neutral when set |
| 4-hour window starts at the pre-roll (`trend_candles`) | Live has 120 days of 4-hour candles at once; the replay builds them from the pre-roll on, so `trend_strategy` makes no decision for about 27 days after `--start`. | pessimistic |

Verdict rules, in order:

| Rule | When |
|---|---|
| `data_coverage_warning` | the data check recorded a warning (allowed gaps, late start, early end) |
| `accounting_warning` | the reconciliation failed: nothing else is trustworthy |
| `no_round_trips` | no round trips: says nothing about profitability |
| `net_negative_after_costs` | net P&L ≤ 0 ("not net positive"; `costs_consume_gross` too when gross was positive) |
| `inconclusive_stale_positions` | net positive, but not with the stale positions at zero: the stale positions decide the sign |
| `positive_but_few_trades` | net positive with fewer than 30 round trips: not statistically meaningful |
| `positive_but_short_period` | net positive over fewer than 90 days |
| `positive_not_validated` | net positive with enough trips and days: still one path; run split, walkforward and sensitivity |
| `below_buy_and_hold` / `above_buy_and_hold` | the difference to the primary benchmark |

The robustness summaries have their own verdicts (`in_sample_negative`, `positive_in_sample_negative_out_of_sample`, `out_of_sample_few_trades`; `most_test_windows_lose`, `test_windows_few_trades`, `tuned_value_unstable`; `baseline_no_round_trips`, `baseline_not_positive`, `positive_only_below_fee`, `not_positive_at_any_tested_fee`, `sign_does_not_survive`; `below_buy_and_hold_in_buckets`, `thin_cells`).

The words "edge" or "profitable strategy" appear only in `compare` given a split, a walk-forward and a sensitivity directory of the same strategy, and only when **all** of these hold:

| Condition | Requirement |
|---|---|
| `out_of_sample` | in-sample and out-of-sample both net positive, with at least 30 out-of-sample round trips |
| `walk_forward` | compounded test return positive, at least 60 % of at least 3 test windows net positive, at least 30 test round trips |
| `fee_stress` | net positive at every tested fee up to 1.5 × the baseline fee, and such a fee was tested |
| `same_strategy` | all three are of the same strategy |
| `no_stale_sign` | in no in-sample, out-of-sample, sensitivity-baseline or walk-forward test run do the stale positions decide the sign |
| `data_coverage` | none of those runs recorded a data warning |

Even then the text says this is one market history and does not guarantee future results; otherwise it lists the failed conditions and says not to treat the results as evidence of a lasting advantage.

Assumptions that can bias these reports: buy-and-hold ignores size impact and the liquidity cap (favours the benchmark); a ±0.005 breakeven band; Sharpe/Sortino annualise with √365 and a zero risk-free rate; regime volatility is relative to the tested period, not to history; bucket drawdowns use the sampled curve (each point keeps the lowest equity since the previous one), whereas the run's drawdown is per tick; the reconciliation tolerance absorbs float64 rounding only.
