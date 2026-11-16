package paper

import (
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// krakenTier1Taker is Kraken Pro's Tier 1 spot taker fee, read from
// https://www.kraken.com/pro/fees on 2026-11-16.
const krakenTier1Taker = 0.008

// TestPaperFeeMatchesKraken covers the paper engine charging what a live
// market order would cost at Kraken Pro's lowest fee tier.
func TestPaperFeeMatchesKraken(t *testing.T) {
	t.Run("GIVEN the paper engine WHEN a long fills THEN the fee is at least Kraken's Tier 1 taker fee on the fill value", func(t *testing.T) {
		engine := newEngine(500)
		order, _ := engine.Execute(intent("BTC/EUR", domain.Long, 1), 100, ExecuteOptions{})
		if order.Status != "filled" {
			t.Fatalf("order %+v", order)
		}
		// The cash spent beyond the fill value is the fee.
		value := order.Price * order.Size
		if fee := 500 - engine.Cash() - value; fee < value*krakenTier1Taker-1e-9 {
			t.Fatalf("fee %v on a fill of %v is below the Tier 1 taker fee", fee, value)
		}
	})
}
