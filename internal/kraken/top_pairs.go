package kraken

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/krakenfx/api-go/v2/pkg/spot"
)

// minDailyRange is the smallest 24h high-low move (as a fraction of the low)
// a pair needs to be worth trading. Stablecoins pegged to a currency (USDC,
// USDT, EURC against EUR) move far less and are skipped.
const minDailyRange = 0.005

// TopPairsByVolume returns up to n online Kraken pairs quoted in quote
// (e.g. "EUR"), biggest 24h traded value first, as internal symbols
// ("BTC/EUR"). Pegged pairs are skipped. It makes two public calls:
// AssetPairs and Ticker for every pair.
func (a *MarketAdapter) TopPairsByVolume(ctx context.Context, quote string, n int) ([]string, error) {
	pairs, err := CallWithBackoff(ctx, a.backoffFor("Kraken asset pairs"),
		func(ctx context.Context) (*spot.Response[map[string]spot.AssetPair], error) {
			return a.restFor(ctx).AssetPairs(nil)
		})
	if err != nil {
		return nil, fmt.Errorf("list Kraken pairs: %w", err)
	}
	tickers, err := CallWithBackoff(ctx, a.backoffFor("Kraken all tickers"),
		func(ctx context.Context) (*spot.Response[map[string]spot.AssetTickerInfo], error) {
			return a.restFor(ctx).Ticker(&spot.TickerRequest{})
		})
	if err != nil {
		return nil, fmt.Errorf("list Kraken tickers: %w", err)
	}

	type ranked struct {
		symbol string
		value  float64 // 24h traded value in the quote currency
	}
	var candidates []ranked
	suffix := "/" + strings.ToUpper(quote)
	for official, pair := range pairs.Result {
		if !strings.HasSuffix(pair.WSName, suffix) || (pair.Status != "" && pair.Status != "online") {
			continue
		}
		info, ok := tickers.Result[official]
		if !ok {
			continue
		}
		volume, okV := decimalAt(info.Volume, 1) // index 1 = rolling 24h
		vwap, okP := decimalAt(info.VWAP, 1)
		high, okH := decimalAt(info.High, 1)
		low, okL := decimalAt(info.Low, 1)
		if !okV || !okP || !okH || !okL || low <= 0 || (high-low)/low < minDailyRange {
			continue
		}
		candidates = append(candidates, ranked{symbol: FromKrakenSymbol(pair.WSName), value: volume * vwap})
	}
	if len(candidates) == 0 {
		return nil, errors.New("no tradable Kraken pairs quoted in " + quote)
	}

	slices.SortFunc(candidates, func(x, y ranked) int {
		if c := cmp.Compare(y.value, x.value); c != 0 {
			return c
		}
		return strings.Compare(x.symbol, y.symbol)
	})
	out := make([]string, 0, min(n, len(candidates)))
	for _, c := range candidates[:min(n, len(candidates))] {
		out = append(out, c.symbol)
	}
	return out, nil
}
