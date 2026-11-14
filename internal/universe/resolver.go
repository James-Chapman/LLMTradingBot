// Package universe resolves the set of tradable markets: the configured fixed
// markets plus a dynamic list of Ethereum-ecosystem coins from CoinGecko,
// quoted in the base currency. Which of them Kraken really lists is decided
// later, by the market adapter's symbol validation at start-up.
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

// DefaultMaxCoins is the default cap on dynamic markets (MAX_ETH_ECOSYSTEM_COINS).
const DefaultMaxCoins = 10

// DefaultQuoteCurrency is the quote currency when none is given.
const DefaultQuoteCurrency = "EUR"

// coinsMarketsPath is the CoinGecko endpoint listing coins by market cap.
const coinsMarketsPath = "/api/v3/coins/markets"

// fallbackEthEcosystemCoins is used when the CoinGecko request fails.
var fallbackEthEcosystemCoins = []string{"ADA", "LINK", "UNI", "AAVE", "MKR", "SNX", "COMP", "YFI", "BAT"}

// Options configures a Resolver; zero values select sensible defaults.
type Options struct {
	HTTPClient       *http.Client     // default: 10 s timeout client
	BaseURL          string           // default: DefaultBaseURL
	AvailableMarkets []string         // optional allow-list; default: every coin, left to Kraken validation
	MaxCoins         int              // cap on dynamic markets; 0 adds none, negative selects DefaultMaxCoins
	QuoteCurrency    string           // quote currency of the dynamic pairs; default DefaultQuoteCurrency
	Now              func() time.Time // default: UTC now
}

// Resolver resolves the tradable universe. It holds no mutable state after
// construction, so it is safe for concurrent use.
type Resolver struct {
	fixedMarkets  []string
	dynamicSource string
	maxCoins      int
	quote         string
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
		quote:         strings.ToUpper(strings.TrimSpace(opts.QuoteCurrency)),
		client:        opts.HTTPClient,
		baseURL:       strings.TrimRight(opts.BaseURL, "/"),
		now:           opts.Now,
	}
	if r.dynamicSource == "" {
		r.dynamicSource = "coingecko"
	}
	if r.maxCoins < 0 {
		r.maxCoins = DefaultMaxCoins
	}
	if r.quote == "" {
		r.quote = DefaultQuoteCurrency
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
	if len(opts.AvailableMarkets) > 0 {
		r.available = map[string]struct{}{}
		for _, m := range opts.AvailableMarkets {
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

// ResolveDynamicMarkets returns up to maxCoins pairs (coin/quote) for the top
// Ethereum-ecosystem coins by market cap, falling back to a static coin list
// on API failure. A cap of 0 adds none without asking CoinGecko.
func (r *Resolver) ResolveDynamicMarkets(ctx context.Context) []string {
	markets := []string{}
	if r.maxCoins == 0 {
		return markets
	}
	coins, err := r.fetchEthEcosystemCoins(ctx)
	if err != nil {
		logger.Warn("Dynamic universe API failed; using fallback", "error", err.Error())
		coins = fallbackEthEcosystemCoins
	}

	added := map[string]bool{}
	for _, coin := range coins {
		market := coin + "/" + r.quote
		if r.isAvailable(market) && !added[market] {
			markets = append(markets, market)
			added[market] = true
		}
		if len(markets) >= r.maxCoins {
			break
		}
	}
	return markets
}

// isAvailable reports whether market passes the optional allow-list.
func (r *Resolver) isAvailable(market string) bool {
	if r.available == nil {
		return true
	}
	_, ok := r.available[market]
	return ok
}

// fetchEthEcosystemCoins queries CoinGecko for upper-cased coin symbols in
// market-cap order.
func (r *Resolver) fetchEthEcosystemCoins(ctx context.Context) ([]string, error) {
	params := url.Values{}
	params.Set("vs_currency", strings.ToLower(r.quote))
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
