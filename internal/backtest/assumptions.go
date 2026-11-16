package backtest

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/control"
	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
	"github.com/James-Chapman/LLMTradingBot/internal/marketdata"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// Assumption biases.
const (
	BiasPessimistic = "pessimistic" // makes the result look worse than live would be
	BiasOptimistic  = "optimistic"  // makes the result look better than live would be
	BiasNeutral     = "neutral"
	BiasUnknown     = "unknown"
)

// fmtNum formats a number for an assumption value.
func fmtNum(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

// impactExample works the size-impact formula through one order for the notes.
func impactExample(x ExchangeConfig) string {
	p := x.ImpactThreshold + 0.10
	return fmt.Sprintf("e.g. a 100 EUR buy in a bar where takers bought %s EUR has participation %s and pays %s bp × (%s − %s) = %s bp above its quote",
		fmtNum(100/p), fmtNum(p), fmtNum(x.ImpactBpsPerUnit), fmtNum(p), fmtNum(x.ImpactThreshold), fmtNum(x.ImpactBpsPerUnit*(p-x.ImpactThreshold)))
}

// assumptions lists every modelling choice of a run that affects its result.
func assumptions(cfg Config, x ExchangeConfig, learnerOn bool, res *Result) []Assumption {
	rulesValue := "built-in fallback for every pair"
	if x.PairRules.Source != "" {
		rulesValue = fmt.Sprintf("%s (%d pairs; fallback for the rest)", x.PairRules.Source, len(x.PairRules.Rules))
	}
	fill := Assumption{
		Name: "fill_price", Value: x.FillModel, Bias: BiasPessimistic,
		Note: fmt.Sprintf("Buys fill at the next bar's taker-buy average and sells at its taker-sell average, never better than the other side's average. "+
			"When the bar only had takers on the other side, every print sat at the far quote, so the fill crosses the full measured spread from that side's average "+
			"(buy = sell average × (1 + 2·hs), sell = buy average × (1 − 2·hs)); only when the bar's sides are unknown is it the close ± hs. "+
			"hs is the rolling median half-spread measured over %d two-sided bars, floored at %s bp (%s bp before any measurement). "+
			"Spread cost is measured from the bar's estimated mid. No slippage constant is added on top.",
			x.SpreadWindowBars, fmtNum(x.MinHalfSpreadBps), fmtNum(x.NoDataHalfSpreadBps)),
	}
	if x.FillModel == FillFixed {
		fill.Note = fmt.Sprintf("Every fill is the next tick's close ± a fixed %s bp half-spread; the bars' taker sides are ignored.", fmtNum(x.FixedHalfSpreadBps))
		fill.Bias = BiasUnknown
	}
	learnerValue, learnerNote := "on", "Starts empty and learns only from trades this run has closed (never seeded from a database), as live does after each close."
	if !learnerOn {
		learnerValue, learnerNote = "off", "Confidence is never adjusted (sensitivity run); live adjusts it."
	}
	universe := Assumption{
		Name: "universe", Value: fmt.Sprintf("explicit: %v", cfg.Markets), Bias: BiasOptimistic,
		Note: "A market list chosen today knows which coins survived; delisted pairs missing from the data cache bias the result upwards.",
	}
	if cfg.Universe != nil {
		universe.Value = fmt.Sprintf("top %d by 24 h traded value, re-selected every %s", cfg.Universe.TopN, everyOrMonth(cfg.Universe.Every))
		universe.Note = "Point-in-time: each selection uses only the 24 hours of data before it and skips pegged pairs, as the live start-up rule ranks pairs. " +
			"It is a different policy from live, which trades its fixed markets plus dynamic coins chosen once at start-up and never re-selects. " +
			"Pairs absent from the data cache (e.g. delisted ones missing from the archive) cannot be selected, which still biases upwards."
	}
	liveDefault := control.DefaultSelectedStrategy
	scope := Assumption{Name: "strategy_scope", Value: res.StrategyID, Bias: BiasUnknown,
		Note: fmt.Sprintf("The live bot's default strategy is %s, whose LLM can scale confidence and veto entries. A %s backtest measures the indicator core only and does not answer whether the bot as run live is profitable.", liveDefault, res.StrategyID)}
	if res.StrategyID == liveDefault {
		scope.Note = "The live default strategy."
	}
	candleValue := "no candle for an interval without trades"
	if cfg.FillEmptyCandles {
		candleValue = "flat zero-volume candles for intervals without trades (--fill-empty-candles)"
	}
	riskFee := Assumption{Name: "risk_fee", Value: fmt.Sprintf("FEE_AND_SLIPPAGE %s", fmtNum(res.RiskFee.FeeAndSlippage)), Bias: BiasNeutral,
		Note: fmt.Sprintf("Not set by the settings, so set for this run to the taker fee (%s) plus the fill model's expected half-spread (%s), so the risk engine leaves room for the costs the simulated exchange charges. Live uses its own setting (default 0.009).",
			fmtNum(res.RiskFee.ExchangeFeeRate), fmtNum(res.RiskFee.ExpectedHalfSpread))}
	if res.RiskFee.Explicit {
		riskFee.Bias = BiasUnknown
		riskFee.Note = "Set by the settings and used unchanged. " + res.RiskFee.Warning
	}
	return []Assumption{
		scope,
		{Name: "fee", Value: fmt.Sprintf("%s bp taker on every fill (%s)", fmtNum(x.FeeBps), x.FeePreset), Bias: BiasNeutral,
			Note: "Source: " + x.FeeSource + ". The bot only sends market orders, which always pay taker. Kraken charges a buy's fee in the quote currency and a sell's in the base asset by default; the economic cost is modelled in the quote currency on the fill value. Check against the account's actual tier and the pair's discount."},
		fill,
		{Name: "latency", Value: fmt.Sprintf("%d tick(s) of 30 s", x.LatencyTicks), Bias: BiasPessimistic,
			Note: "A decision at tick T only fills on trades after T; live sends the order within seconds of the tick. Drift over the latency is reported per order."},
		{Name: "size_impact", Value: fmt.Sprintf("%s bp per unit of participation above %s; rejected above %s", fmtNum(x.ImpactBpsPerUnit), fmtNum(x.ImpactThreshold), fmtNum(x.LiquidityCap)), Bias: BiasUnknown,
			Note: "Participation is the order's value over the fill bar's same-side taker value (a buy against the value takers bought at the ask, a sell against the value they sold at the bid); " +
				"half the bar's traded value stands in when the bar had no taker on that side or its sides are unknown. Impact = rate × (participation − threshold), linear; " +
				impactExample(x) + ". 30-second trade prints cannot reveal order-book depth, so whether this is too harsh or too lenient is unknown. A fill bar with no trades rejects the order (a real market order could still fill against the book)."},
		{Name: "stops", Value: "checked on each 30 s tick price, filled on the next bar", Bias: BiasNeutral,
			Note: "As live: the stop is checked on the sampled last price (never intra-bar highs or lows) and the exit fills after the breach, so a gap fills beyond the stop."},
		{Name: "exchange_rules", Value: rulesValue, Bias: BiasUnknown,
			Note: fmt.Sprintf("ordermin, costmin, lot and price decimals are today's values (AssetPairs) applied to past dates. Fallback: ordermin %s, costmin %s, %d lot decimals, no price rounding. Quantities round down, prices round against the bot.",
				fmtNum(FallbackPairRule.OrderMin), fmtNum(FallbackPairRule.CostMin), FallbackPairRule.LotDecimals)},
		riskFee,
		{Name: "spot_only", Value: "no shorts", Bias: BiasNeutral,
			Note: "A sell signal exits a held long or is ignored, as on the live spot account (paper trading would open a paper short)."},
		{Name: "candles", Value: "5 min, 15 min, 1 h and 4 h, clock-aligned, forming candle included, no refresh lag", Bias: BiasUnknown,
			Note: "Built by histdata.CandleAggregator. Kraken's live OHLC always ends with the forming row, so when the current interval has had no trade yet the replay appends a flat candle at the last close with zero volume. Live refreshes candles every 120 s, so its forming candle can be up to 2 min stale; the replay's is current."},
		intervalAssumption(cfg),
		{Name: "trend_candles", Value: fmt.Sprintf("4 h candles from the %s pre-roll on, at most %d", PreRoll, marketdata.TrendCandleLimit), Bias: BiasPessimistic,
			Note: fmt.Sprintf("Live fetches %d 4-hour candles (120 days) from Kraken at once; the replay only has the candles built since its pre-roll, so each 4-hour strategy waits for its warm-up after the start "+
				"(trend %d days, squeeze %d, MACD %d, Donchian %d, rotation %d, RSI dip %d, basic 4h %d; basic 1h %d hours of 1-hour candles), and its indicators and breakout or dip state are computed on a shorter history during the first months.",
				marketdata.TrendCandleLimit, candleDays(strategy.TrendMinClosedCandles), candleDays(strategy.SqueezeMinClosedCandles),
				candleDays(strategy.MACDMinClosedCandles), candleDays(strategy.DonchianEntryCandles+1),
				candleDays(strategy.RotationMinClosedCandles), candleDays(strategy.RSIDipMinClosedCandles),
				candleDays(strategy.BasicMinClosedCandles+1), strategy.BasicMinClosedCandles+1)},
		{Name: "empty_candles", Value: candleValue, Bias: BiasUnknown,
			Note: "Kraken's downloadable OHLCVT files have no row for an interval without trades; whether Kraken's REST OHLC, which the live bot uses, returns rows for empty intervals is NOT verified. " +
				"For illiquid pairs this changes how much real time 100 candles span, and so ATR, the stochastic and the higher-timeframe filter. Run with --fill-empty-candles (or the sensitivity command) to measure it."},
		{Name: "duplicate_trades", Value: "kept in the bars", Bias: BiasOptimistic,
			Note: "Trades flagged at import as duplicate ids are kept, so they inflate volume and the taker-side notionals: that flatters participation (less impact, fewer liquidity rejections), the universe ranking and the minimum-volume filter. The count for the range is in the data check."},
		{Name: "pre_roll", Value: PreRoll.String(), Bias: BiasNeutral,
			Note: "Data from before the start feeds candles, 24 h volume and the last price without trading."},
		{Name: "price_history_start", Value: "empty at the start", Bias: BiasPessimistic,
			Note: "The price history starts empty, as after a live start-up without saved ticks; live restores saved ticks after a restart, so it leaves warm-up sooner. A small effect."},
		{Name: "learner", Value: learnerValue, Bias: BiasUnknown, Note: learnerNote},
		{Name: "trading_mode", Value: "fully automated, paper route", Bias: BiasUnknown,
			Note: fmt.Sprintf("The live default trading mode is %s (every idea waits for approval); the backtest simulates the fully automated path: the MinSignalConfidence gate and the one-position-per-pair skip apply; no LLM, no news, no emergency stop or disabled markets.", config.Defaults().TradingMode)},
		{Name: "settings_source", Value: res.SettingsSource, Bias: BiasUnknown,
			Note: "Settings come from the built-in defaults unless a settings file is given; the bot's database is never read. If any setting was chosen by looking at this period, the result is in-sample."},
		universe,
		{Name: "end_of_run", Value: "open positions sold on their market's last bar with trades, less the spread and the fee", Bias: BiasUnknown,
			Note: fmt.Sprintf("The ending equity includes this valuation; no liquidity cap applies to it. A position whose market had not traded for more than %s is a stale_liquidation: valued at its last trade in the headline, at zero in the worst case. Reality could be anywhere in between (a delisted coin may be worth nothing).", res.StaleAfter)},
		{Name: "money", Value: "float64", Bias: BiasNeutral,
			Note: "The paper and risk engines' own float64 accounting is used unchanged, as in live paper trading."},
	}
}

// everyOrMonth names the universe interval.
func everyOrMonth(every string) string {
	if every == "" {
		return "month"
	}
	return every
}

// intervalAssumption states the strategy interval the replay used and how it
// compares with the live default.
func intervalAssumption(cfg Config) Assumption {
	d, err := strategyInterval(cfg)
	if err != nil {
		d = histdata.BarInterval // unreachable: the run validated it
	}
	value := fmt.Sprintf("%d s", int(d/time.Second))
	if !slices.Contains(cfg.ExplicitSettings, "STRATEGY_INTERVAL_SECONDS") {
		return Assumption{Name: "strategy_interval", Value: value + " (backtest default)", Bias: BiasUnknown,
			Note: fmt.Sprintf("The strategy, its price history and the stops run every 30-second bar. Live runs them every STRATEGY_INTERVAL_SECONDS (default %d s), and indicators count ticks, so their windows cover a different time; set STRATEGY_INTERVAL_SECONDS in the settings file to replay the live interval.", config.Defaults().StrategyIntervalSeconds)}
	}
	return Assumption{Name: "strategy_interval", Value: value + " (STRATEGY_INTERVAL_SECONDS)", Bias: BiasNeutral,
		Note: "The strategy, its price history, the stops, equity and risk run every interval from the start, as the live loop does; orders still fill on the 30-second bar after the decision, and candles and volume take in every bar."}
}

// candleDays is how many days n finished 4-hour candles take, rounded up.
func candleDays(n int) int { return (n*4 + 23) / 24 }
