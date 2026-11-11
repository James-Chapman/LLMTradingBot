// Package universe resolves the set of tradable markets: the configured fixed
// markets plus a dynamic list of Ethereum-ecosystem coins from CoinGecko that
// are tradable as EUR pairs on Kraken.
package universe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
)

var logger = observability.Logger("universe_resolver")

// DefaultBaseURL is the CoinGecko API origin.
const DefaultBaseURL = "https://api.coingecko.com"

// DefaultMaxCoins caps how many dynamic markets are added.
const DefaultMaxCoins = 10

// coinsMarketsPath is the CoinGecko endpoint listing coins by market cap.
const coinsMarketsPath = "/api/v3/coins/markets"

// defaultKrakenEURMarkets are EUR pairs known to trade on Kraken.
var defaultKrakenEURMarkets = map[string]struct{}{
	"ADA/EUR": {}, "AAVE/EUR": {}, "AVAX/EUR": {}, "BAT/EUR": {}, "COMP/EUR": {}, "DOT/EUR": {},
	"LINK/EUR": {}, "MKR/EUR": {}, "SNX/EUR": {}, "SOL/EUR": {}, "UNI/EUR": {}, "YFI/EUR": {},
}

// fallbackEthEcosystemCoins is used when the CoinGecko request fails.
var fallbackEthEcosystemCoins = []string{"ADA", "LINK", "UNI", "AAVE", "MKR", "SNX", "COMP", "YFI", "BAT"}

// Options configures a Resolver; zero values select sensible defaults.
type Options struct {
	HTTPClient       *http.Client     // default: 10 s timeout client
	BaseURL          string           // default: DefaultBaseURL
	AvailableMarkets []string         // default: known Kraken EUR pairs plus the fixed markets
	MaxCoins         int              // default: DefaultMaxCoins
	Now              func() time.Time // default: UTC now
}

// Resolver resolves the tradable universe. It holds no mutable state after
// construction, so it is safe for concurrent use.
type Resolver struct {
	fixedMarkets  []string
	dynamicSource string
	maxCoins      int
	client        *http.Client
	baseURL       string
	available     map[string]struct{}
	now           func() time.Time
}

// NewResolver creates a resolver for the fixed markets. dynamicSource is only
// recorded in the snapshot as the resolver source label.
func NewResolver(fixedMarkets []string, dynamicSource string, opts Options) *Resolver {
	r := &Resolver{
		fixedMarkets:  append([]string{}, fixedMarkets...),
		dynamicSource: dynamicSource,
		maxCoins:      opts.MaxCoins,
		client:        opts.HTTPClient,
		baseURL:       strings.TrimRight(opts.BaseURL, "/"),
		now:           opts.Now,
	}
	if r.dynamicSource == "" {
		r.dynamicSource = "coingecko"
	}
	if r.maxCoins <= 0 {
		r.maxCoins = DefaultMaxCoins
	}
	if r.client == nil {
		r.client = &http.Client{Timeout: 10 * time.Second}
	}
	if r.baseURL == "" {
		r.baseURL = DefaultBaseURL
	}
	if r.now == nil {
		r.now = domain.NowUTC
	}
	r.available = map[string]struct{}{}
	if len(opts.AvailableMarkets) > 0 {
		for _, m := range opts.AvailableMarkets {
			r.available[m] = struct{}{}
		}
	} else {
		for m := range defaultKrakenEURMarkets {
			r.available[m] = struct{}{}
		}
		for _, m := range r.fixedMarkets {
			r.available[m] = struct{}{}
		}
	}
	return r
}

// ResolveUniverse resolves fixed plus dynamic markets. Fixed markets take
// priority: any dynamic market already fixed is dropped from the dynamic list.
func (r *Resolver) ResolveUniverse(ctx context.Context) domain.UniverseSnapshot {
	logger.Info("Resolving tradable universe")
	dynamic := r.ResolveDynamicMarkets(ctx)

	// Identity mapping for every market seen.
	mapping := map[string]string{}
	seen := map[string]bool{}
	for _, m := range r.fixedMarkets {
		mapping[m] = m
		seen[m] = true
	}
	deduped := []string{}
	for _, m := range dynamic {
		mapping[m] = m
		if !seen[m] {
			deduped = append(deduped, m)
			seen[m] = true
		}
	}

	snapshot := domain.UniverseSnapshot{
		FixedMarkets:   append([]string{}, r.fixedMarkets...),
		DynamicMarkets: deduped,
		ResolverSource: r.dynamicSource,
		ResolvedAt:     r.now().UTC(),
		Mapping:        mapping,
	}
	logger.Info("Universe resolved",
		"fixed_count", len(r.fixedMarkets),
		"dynamic_count", len(dynamic),
		"total_markets", len(snapshot.FixedMarkets)+len(snapshot.DynamicMarkets))
	return snapshot
}

// ResolveDynamicMarkets returns available EUR pairs for the top Ethereum
// ecosystem coins by market cap, falling back to a static list on API failure.
func (r *Resolver) ResolveDynamicMarkets(ctx context.Context) []string {
	coins, err := r.fetchEthEcosystemCoins(ctx)
	if err != nil {
		logger.Warn("Dynamic universe API failed; using fallback", "error", err.Error())
		coins = fallbackEthEcosystemCoins
	}

	markets := []string{}
	added := map[string]bool{}
	for _, coin := range coins {
		market := coin + "/EUR"
		if _, ok := r.available[market]; ok && !added[market] {
			markets = append(markets, market)
			added[market] = true
		}
		if len(markets) >= r.maxCoins {
			break
		}
	}
	return markets
}

// fetchEthEcosystemCoins queries CoinGecko for upper-cased coin symbols in
// market-cap order.
func (r *Resolver) fetchEthEcosystemCoins(ctx context.Context) ([]string, error) {
	params := url.Values{}
	params.Set("vs_currency", "eur")
	params.Set("category", "ethereum-ecosystem")
	params.Set("order", "market_cap_desc")
	params.Set("per_page", strconv.Itoa(r.maxCoins*3))
	params.Set("page", "1")
	params.Set("sparkline", "false")

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+coinsMarketsPath+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("coingecko returned HTTP %d", resp.StatusCode)
	}

	var payload []struct {
		Symbol string `json:"symbol"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	coins := make([]string, 0, len(payload))
	for _, item := range payload {
		coins = append(coins, strings.ToUpper(item.Symbol))
	}
	return coins, nil
}
