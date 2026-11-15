package universe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// coinServer serves a CoinGecko-style payload and records the last request.
func coinServer(t *testing.T, symbols []string, status int) (*httptest.Server, *url.URL) {
	t.Helper()
	last := &url.URL{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*last = *r.URL
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		payload := make([]map[string]string, 0, len(symbols))
		for _, s := range symbols {
			payload = append(payload, map[string]string{"symbol": s})
		}
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(srv.Close)
	return srv, last
}

// newResolver builds a resolver pointed at a test server.
func newResolver(srv *httptest.Server, fixed []string, available []string) *Resolver {
	return NewResolver(fixed, "coingecko", Options{
		HTTPClient:       srv.Client(),
		BaseURL:          srv.URL,
		AvailableMarkets: available,
		MaxCoins:         DefaultMaxCoins,
	})
}

// assertUnique fails when any market appears more than once.
func assertUnique(t *testing.T, markets []string) {
	t.Helper()
	seen := map[string]bool{}
	for _, m := range markets {
		if seen[m] {
			t.Fatalf("duplicate market %q in %v", m, markets)
		}
		seen[m] = true
	}
}

// TestResolveUniverse covers combining fixed and dynamic markets.
func TestResolveUniverse(t *testing.T) {
	t.Run("GIVEN fixed and dynamic markets with no overlap WHEN resolved THEN every market appears once", func(t *testing.T) {
		srv, _ := coinServer(t, []string{"sol", "ada"}, http.StatusOK)
		resolver := newResolver(srv, []string{"BTC/EUR", "ETH/EUR"}, []string{"SOL/EUR", "ADA/EUR"})

		snap := resolver.ResolveUniverse(context.Background())

		all := append(append([]string{}, snap.FixedMarkets...), snap.DynamicMarkets...)
		assertUnique(t, all)
		if len(all) != 4 {
			t.Fatalf("expected 4 markets, got %v", all)
		}
	})

	t.Run("GIVEN a market in both fixed and dynamic lists WHEN resolved THEN it stays fixed only", func(t *testing.T) {
		srv, _ := coinServer(t, []string{"eth", "sol"}, http.StatusOK)
		resolver := newResolver(srv, []string{"BTC/EUR", "ETH/EUR"}, []string{"ETH/EUR", "SOL/EUR"})

		snap := resolver.ResolveUniverse(context.Background())

		assertUnique(t, append(append([]string{}, snap.FixedMarkets...), snap.DynamicMarkets...))
		for _, m := range snap.DynamicMarkets {
			if m == "ETH/EUR" {
				t.Fatal("duplicate must be removed from dynamic markets")
			}
		}
		if snap.Mapping["ETH/EUR"] != "ETH/EUR" || snap.Mapping["SOL/EUR"] != "SOL/EUR" {
			t.Fatalf("mapping incomplete: %v", snap.Mapping)
		}
	})

	t.Run("GIVEN every dynamic market duplicates a fixed one WHEN resolved THEN dynamic is empty and fixed unchanged", func(t *testing.T) {
		srv, _ := coinServer(t, []string{"btc", "eth"}, http.StatusOK)
		resolver := newResolver(srv, []string{"BTC/EUR", "ETH/EUR"}, []string{"BTC/EUR", "ETH/EUR"})

		snap := resolver.ResolveUniverse(context.Background())

		if len(snap.DynamicMarkets) != 0 {
			t.Fatalf("expected no dynamic markets, got %v", snap.DynamicMarkets)
		}
		if len(snap.FixedMarkets) != 2 || snap.FixedMarkets[0] != "BTC/EUR" || snap.FixedMarkets[1] != "ETH/EUR" {
			t.Fatalf("fixed markets changed: %v", snap.FixedMarkets)
		}
		if snap.ResolverSource != "coingecko" || snap.ResolvedAt.IsZero() {
			t.Fatalf("snapshot metadata missing: %+v", snap)
		}
	})
}

// TestResolveDynamicMarkets covers dynamic market discovery and fallbacks.
func TestResolveDynamicMarkets(t *testing.T) {
	t.Run("GIVEN a CoinGecko payload WHEN dynamic markets resolve THEN only available EUR pairs return in market-cap order", func(t *testing.T) {
		srv, last := coinServer(t, []string{"link", "uni", "notkraken", "aave"}, http.StatusOK)
		resolver := newResolver(srv, []string{"BTC/EUR"}, []string{"LINK/EUR", "UNI/EUR", "AAVE/EUR"})

		markets := resolver.ResolveDynamicMarkets(context.Background())

		want := []string{"LINK/EUR", "UNI/EUR", "AAVE/EUR"}
		if len(markets) != len(want) {
			t.Fatalf("got %v, want %v", markets, want)
		}
		for i := range want {
			if markets[i] != want[i] {
				t.Fatalf("got %v, want %v", markets, want)
			}
		}
		if last.Path != "/api/v3/coins/markets" || last.Query().Get("category") != "ethereum-ecosystem" || last.Query().Get("vs_currency") != "eur" {
			t.Fatalf("unexpected request %s", last.String())
		}
	})

	t.Run("GIVEN the API fails WHEN dynamic markets resolve THEN the fallback coin list is used", func(t *testing.T) {
		srv, _ := coinServer(t, nil, http.StatusInternalServerError)
		resolver := NewResolver([]string{"BTC/EUR"}, "coingecko", Options{HTTPClient: srv.Client(), BaseURL: srv.URL, MaxCoins: DefaultMaxCoins})

		markets := resolver.ResolveDynamicMarkets(context.Background())

		if len(markets) == 0 {
			t.Fatal("expected fallback markets")
		}
		for _, m := range markets {
			if !strings.HasSuffix(m, "/EUR") {
				t.Fatalf("fallback market %q is not quoted in the base currency", m)
			}
		}
	})

	t.Run("GIVEN more coins than the cap WHEN dynamic markets resolve THEN the list is capped", func(t *testing.T) {
		srv, last := coinServer(t, []string{"link", "uni", "aave"}, http.StatusOK)
		resolver := NewResolver(nil, "coingecko", Options{
			HTTPClient: srv.Client(), BaseURL: srv.URL,
			AvailableMarkets: []string{"LINK/EUR", "UNI/EUR", "AAVE/EUR"},
			MaxCoins:         2,
		})

		markets := resolver.ResolveDynamicMarkets(context.Background())

		if len(markets) != 2 {
			t.Fatalf("expected 2 markets, got %v", markets)
		}
		if last.Query().Get("per_page") != "6" {
			t.Fatalf("per_page %q, want 3x the cap", last.Query().Get("per_page"))
		}
	})
}

// TestDynamicMarketSettings covers the cap of 0 and quoting in the base currency.
func TestDynamicMarketSettings(t *testing.T) {
	t.Run("GIVEN a cap of 0 WHEN the universe resolves THEN no dynamic market is added and CoinGecko is not asked", func(t *testing.T) {
		asked := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			asked = true
			_, _ = w.Write([]byte(`[{"symbol":"link"}]`))
		}))
		t.Cleanup(srv.Close)
		resolver := NewResolver([]string{"BTC/EUR"}, "coingecko", Options{HTTPClient: srv.Client(), BaseURL: srv.URL, MaxCoins: 0})

		snap := resolver.ResolveUniverse(context.Background())

		if len(snap.DynamicMarkets) != 0 || len(snap.FixedMarkets) != 1 {
			t.Fatalf("expected the fixed market only, got %+v", snap)
		}
		if asked {
			t.Error("CoinGecko must not be asked when no dynamic market is wanted")
		}
	})

	t.Run("GIVEN base currency USD WHEN dynamic markets resolve THEN they are quoted in USD and priced in USD", func(t *testing.T) {
		srv, last := coinServer(t, []string{"link", "uni"}, http.StatusOK)
		resolver := NewResolver(nil, "coingecko", Options{HTTPClient: srv.Client(), BaseURL: srv.URL, MaxCoins: 5, QuoteCurrency: "usd"})

		markets := resolver.ResolveDynamicMarkets(context.Background())

		if len(markets) != 2 || markets[0] != "LINK/USD" || markets[1] != "UNI/USD" {
			t.Fatalf("expected USD pairs, got %v", markets)
		}
		if last.Query().Get("vs_currency") != "usd" {
			t.Errorf("expected CoinGecko asked in usd, got %q", last.Query().Get("vs_currency"))
		}
	})

	t.Run("GIVEN no list of available markets WHEN dynamic markets resolve THEN every coin becomes a pair for Kraken to validate", func(t *testing.T) {
		srv, _ := coinServer(t, []string{"render", "link"}, http.StatusOK)
		resolver := NewResolver(nil, "coingecko", Options{HTTPClient: srv.Client(), BaseURL: srv.URL, MaxCoins: 5})

		markets := resolver.ResolveDynamicMarkets(context.Background())

		if len(markets) != 2 || markets[0] != "RENDER/EUR" {
			t.Fatalf("expected both coins as EUR pairs, got %v", markets)
		}
	})
}
