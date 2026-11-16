package config

import "testing"

// TestFeeAndSlippageDefault covers the default per-side cost the risk engine
// and the basic strategies' volatility filter assume.
func TestFeeAndSlippageDefault(t *testing.T) {
	t.Run("GIVEN default settings WHEN read THEN FEE_AND_SLIPPAGE covers Kraken Pro's Tier 1 taker fee with room for slippage", func(t *testing.T) {
		// Kraken Pro Tier 1 taker fee, https://www.kraken.com/pro/fees (2026-11-16).
		const tier1Taker = 0.008
		if fee := Defaults().FeeAndSlippage; fee <= tier1Taker || fee > 2*tier1Taker {
			t.Fatalf("FEE_AND_SLIPPAGE default %v, want above the 0.8%% taker fee and no more than twice it", fee)
		}
	})
}
