package backtest

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
	"github.com/James-Chapman/LLMTradingBot/internal/paper"
)

// Fill models (one per run; they are never stacked).
const (
	// FillSpread fills a buy at the next bar's taker-buy average and a sell at
	// its taker-sell average (never better than the other side's); with
	// trades on the other side only, across the full measured spread from
	// that side; with unknown sides, at the close ± the measured half-spread.
	FillSpread = "spread"
	// FillFixed fills at the next bar close ± a fixed half-spread, for
	// sensitivity runs and data without taker sides.
	FillFixed = "fixed"
)

// Fee presets.
const (
	// FeeKrakenBase is Kraken Pro's Tier 1 spot taker fee ($0+ 30-day
	// volume): 0.80 % on every market order. Check it against the owner's
	// actual tier and the pair's discount eligibility.
	FeeKrakenBase = "kraken-base"
	// FeePaperDefault is the fee the bot's paper engine charges (paper.TakerFeeRate;
	// 0.80 % since 0.21.0, the same as kraken-base).
	FeePaperDefault = "paper-default"
	// FeeCustom marks an explicit basis-point rate.
	FeeCustom = "custom"

	// KrakenFeesURL is where the Kraken taker rate was read (2026-11-14).
	KrakenFeesURL = "https://www.kraken.com/pro/fees"
	// krakenBaseTakerBps is Tier 1's taker rate in basis points.
	krakenBaseTakerBps = 80
)

// ExchangeConfig is the simulated exchange's execution assumptions. Every
// field is recorded in the result.
type ExchangeConfig struct {
	FeePreset string  `json:"fee_preset"` // kraken-base | paper-default | custom
	FeeBps    float64 `json:"fee_bps"`    // taker fee on every fill, basis points of the fill value
	FeeSource string  `json:"fee_source"`

	FillModel    string `json:"fill_model"`    // spread | fixed
	LatencyTicks int    `json:"latency_ticks"` // whole 30 s ticks between decision and fill, at least 1

	FixedHalfSpreadBps  float64 `json:"fixed_half_spread_bps"`   // fixed model
	MinHalfSpreadBps    float64 `json:"min_half_spread_bps"`     // spread model floor on the measured half-spread
	NoDataHalfSpreadBps float64 `json:"no_data_half_spread_bps"` // spread model, before any two-sided bar was seen
	SpreadWindowBars    int     `json:"spread_window_bars"`      // two-sided bars in the rolling median

	ImpactThreshold  float64 `json:"impact_threshold"`    // participation (order value / fill bar same-side taker value) above which impact applies
	ImpactBpsPerUnit float64 `json:"impact_bps_per_unit"` // impact in bp per unit of participation above the threshold
	LiquidityCap     float64 `json:"liquidity_cap"`       // participation above which the order is rejected

	PairRules PairRules `json:"pair_rules"`
}

// DefaultExchange returns the documented defaults: Kraken's Tier 1 taker
// fee, the spread fill model, one tick of latency, a 5 bp half-spread floor
// (50 bp before any measurement), impact above 10 % of the fill bar's
// same-side taker value at 100 bp per unit of excess participation, and
// rejection above 25 % of it.
func DefaultExchange() ExchangeConfig {
	return ExchangeConfig{
		FeePreset:           FeeKrakenBase,
		FeeBps:              krakenBaseTakerBps,
		FeeSource:           KrakenFeesURL + " (Tier 1 taker, read 2026-11-14)",
		FillModel:           FillSpread,
		LatencyTicks:        1,
		FixedHalfSpreadBps:  10,
		MinHalfSpreadBps:    5,
		NoDataHalfSpreadBps: 50,
		SpreadWindowBars:    240,
		ImpactThreshold:     0.10,
		ImpactBpsPerUnit:    100,
		LiquidityCap:        0.25,
		PairRules:           PairRules{Rules: map[string]PairRule{}},
	}
}

// SetFees applies a fee preset name or an explicit number of basis points.
func (x *ExchangeConfig) SetFees(spec string) error {
	switch spec {
	case FeeKrakenBase:
		x.FeePreset, x.FeeBps = FeeKrakenBase, krakenBaseTakerBps
		x.FeeSource = KrakenFeesURL + " (Tier 1 taker, read 2026-11-14)"
	case FeePaperDefault:
		x.FeePreset, x.FeeBps = FeePaperDefault, paper.TakerFeeRate*1e4
		x.FeeSource = "internal/paper TakerFeeRate: what paper trading charges"
	default:
		bps, err := strconv.ParseFloat(strings.TrimSpace(spec), 64)
		if err != nil || math.IsNaN(bps) || bps < 0 || bps >= 1e4 {
			return fmt.Errorf("fees must be %s, %s or basis points in [0, 10000), got %q", FeeKrakenBase, FeePaperDefault, spec)
		}
		x.FeePreset, x.FeeBps, x.FeeSource = FeeCustom, bps, "explicit run input"
	}
	return nil
}

// SetLatency converts a latency to whole strategy ticks, rounding up. A
// fill always uses data after the decision, so the minimum is one tick.
func (x *ExchangeConfig) SetLatency(d time.Duration) error {
	if d <= 0 {
		return fmt.Errorf("latency must be positive (a fill can only use data after the decision), got %s", d)
	}
	x.LatencyTicks = int((d + histdata.BarInterval - 1) / histdata.BarInterval)
	return nil
}

// Validate checks the configuration is usable.
func (x ExchangeConfig) Validate() error {
	switch {
	case x.FillModel != FillSpread && x.FillModel != FillFixed:
		return fmt.Errorf("fill model must be %s or %s, got %q", FillSpread, FillFixed, x.FillModel)
	case x.LatencyTicks < 1:
		return fmt.Errorf("latency must be at least one tick, got %d", x.LatencyTicks)
	case x.FeeBps < 0 || x.FeeBps >= 1e4:
		return fmt.Errorf("fee %v bp out of range", x.FeeBps)
	case x.FixedHalfSpreadBps < 0 || x.MinHalfSpreadBps < 0 || x.NoDataHalfSpreadBps < 0:
		return fmt.Errorf("half-spreads must not be negative")
	case x.SpreadWindowBars < 1:
		return fmt.Errorf("spread window must be at least one bar")
	case x.ImpactThreshold < 0 || x.ImpactBpsPerUnit < 0 || x.LiquidityCap <= 0:
		return fmt.Errorf("impact threshold and rate must not be negative and the liquidity cap must be positive")
	}
	return nil
}

// feeRate returns the fee as a fraction of the fill value.
func (x ExchangeConfig) feeRate() float64 { return x.FeeBps / 1e4 }

// fillQuote is the unimpacted price an order would fill at, and the bar's
// estimated mid that the spread cost is measured from.
type fillQuote struct {
	price      float64
	mid        float64
	source     string
	halfSpread float64 // fraction, when a measured or fixed half-spread was applied
}

// quote prices a market order on the fill bar of f before size impact.
func (x ExchangeConfig) quote(buy bool, f *marketFeed) fillQuote {
	if f.hasBar {
		return x.quoteBar(buy, f, f.bar)
	}
	return x.quoteBar(buy, f, histdata.Bar{Close: f.last})
}

// quoteBar prices a market order on bar b with the spread model (or the
// fixed one). With taker trades on the order's side, a buy pays the taker-buy
// average and a sell receives the taker-sell average, but never better than
// the other side's average (in a trending bar the averages can cross). With
// trades on the other side only, every print sat at the far quote, so the
// fill crosses the full measured spread from that side's average:
// buy = sellAvg × (1 + 2·hs), sell = buyAvg × (1 − 2·hs). Only when the bar's
// sides are unknown (data without taker sides) is it close ± hs.
func (x ExchangeConfig) quoteBar(buy bool, f *marketFeed, b histdata.Bar) fillQuote {
	hs, source := x.halfSpread(f)
	if x.FillModel == FillSpread {
		buyAvg, sellAvg := 0.0, 0.0
		if b.BuyVolume > 0 {
			buyAvg = b.BuyNotional / b.BuyVolume
		}
		if b.SellVolume > 0 {
			sellAvg = b.SellNotional / b.SellVolume
		}
		switch {
		case buyAvg > 0 && sellAvg > 0:
			mid := (buyAvg + sellAvg) / 2
			if buy {
				return fillQuote{price: math.Max(buyAvg, sellAvg), mid: mid, source: SourceTakerSide}
			}
			return fillQuote{price: math.Min(buyAvg, sellAvg), mid: mid, source: SourceTakerSide}
		case buy && buyAvg > 0: // the mid is estimated one half-spread inside the prints
			return fillQuote{price: buyAvg, mid: buyAvg * (1 - hs), source: SourceTakerSide}
		case !buy && sellAvg > 0:
			return fillQuote{price: sellAvg, mid: sellAvg * (1 + hs), source: SourceTakerSide}
		case buy && sellAvg > 0:
			return fillQuote{price: sellAvg * (1 + 2*hs), mid: sellAvg * (1 + hs), source: SourceOtherSideSpread, halfSpread: hs}
		case !buy && buyAvg > 0:
			return fillQuote{price: buyAvg * (1 - 2*hs), mid: buyAvg * (1 - hs), source: SourceOtherSideSpread, halfSpread: hs}
		}
	}
	if buy {
		return fillQuote{price: b.Close * (1 + hs), mid: b.Close, source: source, halfSpread: hs}
	}
	return fillQuote{price: b.Close * (1 - hs), mid: b.Close, source: source, halfSpread: hs}
}

// halfSpread returns the half-spread fraction applied to a close: the fixed
// one, or the market's rolling median measured half-spread (the no-data
// value before any measurement) floored at MinHalfSpreadBps.
func (x ExchangeConfig) halfSpread(f *marketFeed) (float64, string) {
	if x.FillModel == FillFixed {
		return x.FixedHalfSpreadBps / 1e4, SourceFixedSpread
	}
	hs, ok := f.spread.median()
	if !ok {
		hs = x.NoDataHalfSpreadBps / 1e4
	}
	return math.Max(hs, x.MinHalfSpreadBps/1e4), SourceMeasuredSpread
}

// impact returns the size impact fraction for an order of the given
// participation: ImpactBpsPerUnit per unit of participation above the threshold.
func (x ExchangeConfig) impact(participation float64) float64 {
	if participation <= x.ImpactThreshold {
		return 0
	}
	return x.ImpactBpsPerUnit / 1e4 * (participation - x.ImpactThreshold)
}

// sideTradedValue is the quote value an order's participation is measured
// against: a buy only consumes asks, so it is the bar's taker-buy value (the
// liquidity that actually traded at the ask), and a sell's is the taker-sell
// value. When the bar had no taker on the order's side, or its sides are
// unknown, half the bar's traded value stands in for it.
func sideTradedValue(buy bool, b histdata.Bar) float64 {
	same := b.SellNotional
	if buy {
		same = b.BuyNotional
	}
	switch total := b.BuyNotional + b.SellNotional; {
	case same > 0:
		return same
	case total > 0:
		return total / 2
	default:
		return b.Close * b.Volume / 2
	}
}
