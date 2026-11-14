package app

import (
	"context"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/universe"
)

// TestUniverseHonoursMaxEthEcosystemCoins covers wiring MAX_ETH_ECOSYSTEM_COINS into the resolver.
func TestUniverseHonoursMaxEthEcosystemCoins(t *testing.T) {
	// A closed local port makes CoinGecko unreachable, so the resolver uses its
	// static fallback coin list — no real network access.
	offline := universe.Options{BaseURL: "http://127.0.0.1:1"}

	resolve := func(maxCoins int) int {
		s := config.Defaults()
		s.MaxEthEcosystemCoins = maxCoins
		snap := newUniverse(s, offline).ResolveUniverse(context.Background())
		return len(snap.DynamicMarkets)
	}

	t.Run("GIVEN MAX_ETH_ECOSYSTEM_COINS=2 WHEN the universe resolves THEN at most 2 dynamic markets are added", func(t *testing.T) {
		if got := resolve(2); got > 2 {
			t.Fatalf("expected at most 2 dynamic markets, got %d", got)
		}
	})

	t.Run("GIVEN a larger cap WHEN the universe resolves THEN more dynamic markets are allowed", func(t *testing.T) {
		if small, large := resolve(2), resolve(10); large <= small {
			t.Fatalf("expected the cap to matter: cap 2 gave %d, cap 10 gave %d", small, large)
		}
	})
}
