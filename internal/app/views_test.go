package app

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/paper"
)

// marketBySymbol returns the dashboard market row for symbol, failing when it is missing.
func marketBySymbol(t *testing.T, d DashboardView, symbol string) MarketView {
	t.Helper()
	for _, m := range d.Markets {
		if m.Symbol == symbol {
			return m
		}
	}
	t.Fatalf("market %s missing from dashboard: %+v", symbol, d.Markets)
	return MarketView{}
}

// decimals returns the number of digits after the decimal point in s.
func decimals(s string) int {
	_, frac, ok := strings.Cut(s, ".")
	if !ok {
		return 0
	}
	return len(frac)
}

// TestPriceFormatting covers adaptive price precision in the dashboard views.
func TestPriceFormatting(t *testing.T) {
	t.Run("GIVEN a sub-cent price WHEN it is formatted THEN its significant digits are kept", func(t *testing.T) {
		got := formatPrice(0.0000085)
		v, err := strconv.ParseFloat(got, 64)
		if err != nil || v == 0 || !strings.Contains(got, "85") {
			t.Errorf("formatPrice(0.0000085) = %q, want a non-zero price showing 85", got)
		}
	})

	t.Run("GIVEN a price of one or more WHEN it is formatted THEN it has 2 decimals", func(t *testing.T) {
		for in, want := range map[float64]string{59611.3: "59611.30", 1: "1.00", 3000: "3000.00"} {
			if got := formatPrice(in); got != want {
				t.Errorf("formatPrice(%v) = %q, want %q", in, got, want)
			}
		}
	})

	t.Run("GIVEN a price below one WHEN it is formatted THEN at least 2 decimals and 4 significant digits are shown", func(t *testing.T) {
		got := formatPrice(0.5432)
		if got != "0.5432" {
			t.Errorf("formatPrice(0.5432) = %q, want 0.5432", got)
		}
	})

	t.Run("GIVEN a market priced 0.0000085 WHEN the dashboard is built THEN the price and the raw price field are non-zero", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		env.app.mu.Lock()
		env.app.currentPrices["SHIB/EUR"] = 0.0000085
		env.app.mu.Unlock()

		m := marketBySymbol(t, env.app.Dashboard(), "SHIB/EUR")
		if v, err := strconv.ParseFloat(m.Price, 64); err != nil || v == 0 {
			t.Errorf("price shown as %q, want non-zero", m.Price)
		}
		if m.PriceValue == nil || *m.PriceValue != 0.0000085 {
			t.Errorf("raw price_value = %v, want 0.0000085", m.PriceValue)
		}
	})

	t.Run("GIVEN BTC priced 59611.3 WHEN the dashboard is built THEN its price still shows 2 decimals", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		env.app.mu.Lock()
		env.app.currentPrices["BTC/EUR"] = 59611.3
		env.app.mu.Unlock()

		if m := marketBySymbol(t, env.app.Dashboard(), "BTC/EUR"); m.Price != "59611.30" {
			t.Errorf("BTC price = %q, want 59611.30", m.Price)
		}
	})

	t.Run("GIVEN an unpriced market WHEN the dashboard is serialised THEN price is - and price_value is null", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		env.boot(t) // loads the active markets; no tick has priced them yet
		raw, err := json.Marshal(marketBySymbol(t, env.app.Dashboard(), "ETH/EUR"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"price":"-"`) || !strings.Contains(string(raw), `"price_value":null`) {
			t.Errorf("unexpected unpriced market JSON: %s", raw)
		}
	})

	t.Run("GIVEN fractional equity and cash WHEN the dashboard is built THEN money amounts keep 2 decimals", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		env.app.mu.Lock()
		env.app.currentEquity, env.app.currentCash = 0.0000085, 1234.5678
		env.app.mu.Unlock()

		d := env.app.Dashboard()
		if d.Equity != "0.00" || d.Cash != "1234.57" {
			t.Errorf("equity/cash = %q/%q, want 0.00/1234.57", d.Equity, d.Cash)
		}
	})

	t.Run("GIVEN a sub-cent position WHEN its view is built THEN the average price is precise and P&L stays money", func(t *testing.T) {
		pos := domain.PositionRecord{PositionID: "0123456789abcdef", Market: "PEPE/EUR", Size: 1e6, AvgPrice: 0.0000085, UnrealizedPnL: 0.123456}
		view := positionView(pos, paper.PositionMeta{StrategyID: "basic"})
		if v, err := strconv.ParseFloat(view.AvgPrice, 64); err != nil || v == 0 {
			t.Errorf("avg_price = %q, want non-zero", view.AvgPrice)
		}
		if view.AvgPriceValue != 0.0000085 {
			t.Errorf("avg_price_value = %v, want 0.0000085", view.AvgPriceValue)
		}
		if view.UnrealizedPnL != "0.12" || decimals(view.UnrealizedPnL) != 2 {
			t.Errorf("unrealized_pnl = %q, want 0.12", view.UnrealizedPnL)
		}
		if view.Direction != "long" || view.PositionID != "01234567" || view.Strategy != "basic" {
			t.Errorf("unexpected position view: %+v", view)
		}
	})
}
