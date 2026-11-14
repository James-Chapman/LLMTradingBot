package kraken

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/krakenfx/api-go/v2/pkg/decimal"
	"github.com/krakenfx/api-go/v2/pkg/spot"
)

// DefaultRESTURL is Kraken's public REST endpoint.
const DefaultRESTURL = "https://api.kraken.com"

// defaultHTTPTimeout bounds every Kraken REST call.
const defaultHTTPTimeout = 15 * time.Second

// baseAliases maps internal base assets to Kraken's legacy names (BTC is XBT on Kraken).
var baseAliases = map[string]string{"BTC": "XBT", "DOGE": "XDG"}

// baseAliasesReverse maps Kraken legacy base names back to internal names.
var baseAliasesReverse = map[string]string{"XBT": "BTC", "XDG": "DOGE"}

// ToKrakenPair converts an internal symbol like "BTC/EUR" to Kraken's altname "XBTEUR".
func ToKrakenPair(symbol string) string {
	base, quote, found := strings.Cut(symbol, "/")
	if !found {
		return symbol
	}
	if alias, ok := baseAliases[base]; ok {
		base = alias
	}
	return base + quote
}

// FromKrakenSymbol converts a Kraken slash symbol like "XBT/EUR" to the internal "BTC/EUR".
func FromKrakenSymbol(pair string) string {
	base, quote, found := strings.Cut(pair, "/")
	if !found {
		return pair
	}
	if alias, ok := baseAliasesReverse[base]; ok {
		base = alias
	}
	return base + "/" + quote
}

// newREST builds an SDK REST client for baseURL, optionally authenticated.
func newREST(baseURL, apiKey, apiSecret string) *spot.REST {
	rest := spot.NewREST()
	if baseURL != "" {
		rest.BaseURL = strings.TrimRight(baseURL, "/")
	}
	rest.PublicKey = apiKey
	rest.PrivateKey = apiSecret
	return rest
}

// defaultHTTPClient returns client, or a new client with the default timeout if nil.
func defaultHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: defaultHTTPTimeout}
}

// restWithContext returns a shallow copy of base whose requests use client and ctx.
// The SDK has no context support, so cancellation is attached via its Executor hook;
// the copy shares base's nonce counter so private-call nonces stay increasing.
func restWithContext(ctx context.Context, base *spot.REST, client *http.Client) *spot.REST {
	bound := *base
	bound.Executor = func(req *http.Request) (*http.Response, error) {
		return client.Do(req.WithContext(ctx))
	}
	return &bound
}

// decimalFloat converts an SDK decimal to float64, returning ok=false for nil.
func decimalFloat(d *decimal.Decimal) (float64, bool) {
	if d == nil {
		return 0, false
	}
	return d.Float64(), true
}

// decimalAt returns the float value at index i of an SDK decimal slice.
func decimalAt(values []*decimal.Decimal, i int) (float64, bool) {
	if i < 0 || i >= len(values) {
		return 0, false
	}
	return decimalFloat(values[i])
}

// anyFloat converts a JSON-decoded value (json.Number, string or float64) to float64.
func anyFloat(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case fmt.Stringer: // json.Number
		return strconv.ParseFloat(x.String(), 64)
	case string:
		return strconv.ParseFloat(x, 64)
	default:
		return 0, fmt.Errorf("unexpected numeric value %T", v)
	}
}
