package backtest

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
)

// checkCoverage turns a data check into warnings. A month missing inside a
// market's coverage, or a market-month with an error-level finding, is an
// error unless allowGaps, in which case it becomes a warning too. Coverage
// that starts after or ends before the range is always only a warning: a
// pair listed later or delisted earlier is legitimate, but must be visible.
func checkCoverage(rep histdata.RangeReport, allowGaps bool) ([]string, error) {
	var warnings, problems []string
	day := func(t time.Time) string { return t.UTC().Format(time.DateOnly) }
	for _, m := range rep.Markets {
		for _, month := range m.MissingMonths {
			problems = append(problems, fmt.Sprintf("%s has no data for %s inside its coverage %s..%s (missing data, not a month without trades)", m.Market, month, day(m.First), day(m.Last)))
		}
		for _, month := range m.ErrorMonths {
			problems = append(problems, fmt.Sprintf("%s %s has an error-level data finding (see `backtest data verify`; re-import the month to resolve it)", m.Market, month))
		}
		switch {
		case !m.HasData:
			warnings = append(warnings, fmt.Sprintf("%s has no data in the cache at all", m.Market))
		case m.StartsLate && m.EndsEarly:
			warnings = append(warnings, fmt.Sprintf("%s data only covers %s..%s of the range read (it starts late and ends early)", m.Market, day(m.First), day(m.Last)))
		case m.StartsLate:
			warnings = append(warnings, fmt.Sprintf("%s data starts on %s, after the range read begins (%s)", m.Market, day(m.First), day(rep.From)))
		case m.EndsEarly:
			warnings = append(warnings, fmt.Sprintf("%s data ends on %s, before the range read ends (%s)", m.Market, day(m.Last), day(rep.To)))
		}
	}
	if len(problems) > 0 && !allowGaps {
		return nil, errors.New("the data cache does not cover the run: " + strings.Join(problems, "; ") +
			"; import the missing data, or rerun with --allow-data-gaps to accept it as a recorded warning")
	}
	return append(problems, warnings...), nil
}

// expectedHalfSpread is the half-spread fraction the fill model expects to
// pay before it has measured anything: the fixed one, or the spread model's
// no-data value (never below its floor).
func expectedHalfSpread(x ExchangeConfig) float64 {
	if x.FillModel == FillFixed {
		return x.FixedHalfSpreadBps / 1e4
	}
	return math.Max(x.NoDataHalfSpreadBps, x.MinHalfSpreadBps) / 1e4
}

// alignRiskFee makes the risk engine size with the simulated exchange's
// costs. The risk engine caps a buy to cash / (1 + FEE_AND_SLIPPAGE); with the
// live default (0.9 %) below the run's taker fee plus half-spread, near all-in buys would pass
// risk and then be rejected for funds. Unless the settings set it explicitly,
// FEE_AND_SLIPPAGE becomes the taker fee plus the expected half-spread for the
// run; an explicit value is kept and a warning recorded when it is below the fee.
func alignRiskFee(s *config.Settings, x ExchangeConfig, explicit bool) RiskFeeAlignment {
	a := RiskFeeAlignment{Explicit: explicit, ExchangeFeeRate: x.feeRate(), ExpectedHalfSpread: expectedHalfSpread(x)}
	if !explicit {
		s.FeeAndSlippage = a.ExchangeFeeRate + a.ExpectedHalfSpread
	} else if s.FeeAndSlippage < a.ExchangeFeeRate {
		a.Warning = fmt.Sprintf("FEE_AND_SLIPPAGE %s (set by the settings) is below the simulated exchange's %s taker fee: the risk engine sizes buys the exchange may reject for funds",
			fmtNum(s.FeeAndSlippage), fmtNum(a.ExchangeFeeRate))
	}
	a.FeeAndSlippage = s.FeeAndSlippage
	return a
}
