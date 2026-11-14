package kraken

import (
	"context"
	"slices"
	"testing"
)

// topPairsAssetPairs lists EUR pairs (one offline, one pegged stablecoin) and a GBP pair.
const topPairsAssetPairs = `{"error":[],"result":{
	"XXBTZEUR":{"altname":"XBTEUR","wsname":"XBT/EUR","quote":"ZEUR","status":"online"},
	"XETHZEUR":{"altname":"ETHEUR","wsname":"ETH/EUR","quote":"ZEUR","status":"online"},
	"SOLEUR":{"altname":"SOLEUR","wsname":"SOL/EUR","quote":"ZEUR","status":"online"},
	"XDGEUR":{"altname":"XDGEUR","wsname":"XDG/EUR","quote":"ZEUR","status":"online"},
	"USDCEUR":{"altname":"USDCEUR","wsname":"USDC/EUR","quote":"ZEUR","status":"online"},
	"OLDEUR":{"altname":"OLDEUR","wsname":"OLD/EUR","quote":"ZEUR","status":"cancel_only"},
	"XXBTZGBP":{"altname":"XBTGBP","wsname":"XBT/GBP","quote":"ZGBP","status":"online"}}}`

// topPairsTicker gives 24h volume (v[1]), VWAP (p[1]) and high/low (h[1], l[1]).
// EUR value: BTC 2×50000=100000, ETH 30×2000=60000, SOL 100×100=10000,
// DOGE 50000×0.1=5000, USDC 900000×1=900000 (pegged), OLD 1e9, BTC/GBP 1e9.
const topPairsTicker = `{"error":[],"result":{
	"XXBTZEUR":{"v":["1","2"],"p":["50000","50000"],"h":["51000","51000"],"l":["49000","49000"]},
	"XETHZEUR":{"v":["10","30"],"p":["2000","2000"],"h":["2100","2100"],"l":["1900","1900"]},
	"SOLEUR":{"v":["50","100"],"p":["100","100"],"h":["105","105"],"l":["95","95"]},
	"XDGEUR":{"v":["1000","50000"],"p":["0.1","0.1"],"h":["0.11","0.11"],"l":["0.09","0.09"]},
	"USDCEUR":{"v":["1","900000"],"p":["1","1"],"h":["1.001","1.001"],"l":["0.999","0.999"]},
	"OLDEUR":{"v":["1","1000000000"],"p":["1","1"],"h":["2","2"],"l":["1","1"]},
	"XXBTZGBP":{"v":["1","20000"],"p":["50000","50000"],"h":["51000","51000"],"l":["49000","49000"]}}}`

// TestTopPairsByVolume covers ranking Kraken pairs for the default market list.
func TestTopPairsByVolume(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN Kraken's pairs and tickers WHEN the biggest EUR pairs are ranked THEN they are ordered by 24h EUR volume", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/public/AssetPairs", topPairsAssetPairs)
		f.on("/0/public/Ticker", topPairsTicker)

		got, err := newTestMarketAdapter(f, nil).TopPairsByVolume(ctx, "EUR", 10)

		if err != nil {
			t.Fatal(err)
		}
		want := []string{"BTC/EUR", "ETH/EUR", "SOL/EUR", "DOGE/EUR"}
		if !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v (other quotes, offline and pegged pairs excluded; XBT/XDG renamed)", got, want)
		}
	})

	t.Run("GIVEN more pairs than requested WHEN ranked THEN only the biggest n are returned", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/public/AssetPairs", topPairsAssetPairs)
		f.on("/0/public/Ticker", topPairsTicker)

		got, err := newTestMarketAdapter(f, nil).TopPairsByVolume(ctx, "EUR", 2)

		if err != nil || !slices.Equal(got, []string{"BTC/EUR", "ETH/EUR"}) {
			t.Fatalf("got %v, %v", got, err)
		}
	})

	t.Run("GIVEN Kraken returns an error WHEN ranked THEN the error is returned and no pairs", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/public/AssetPairs", `{"error":["EGeneral:Invalid arguments"]}`)

		got, err := newTestMarketAdapter(f, nil).TopPairsByVolume(ctx, "EUR", 50)

		if err == nil || len(got) != 0 {
			t.Fatalf("expected an error and no pairs, got %v, %v", got, err)
		}
	})
}
