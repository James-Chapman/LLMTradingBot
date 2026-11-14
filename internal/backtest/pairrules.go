package backtest

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
)

// PairRule is the exchange's order constraints for one pair (Kraken
// AssetPairs: ordermin, costmin, lot_decimals, pair_decimals).
type PairRule struct {
	OrderMin     float64 `json:"ordermin"`      // minimum quantity in the base asset
	CostMin      float64 `json:"costmin"`       // minimum order value in the quote currency
	LotDecimals  int     `json:"lot_decimals"`  // quantity decimals; quantities round down
	PairDecimals int     `json:"pair_decimals"` // price decimals; -1 = no price rounding
}

// FallbackPairRule applies to pairs missing from the rules file (or every
// pair without one): no minimum quantity, a 5-unit minimum order value (ten
// times Kraken's usual 0.5 EUR costmin), 8 lot decimals and no price rounding.
var FallbackPairRule = PairRule{OrderMin: 0, CostMin: 5, LotDecimals: 8, PairDecimals: -1}

// PairRules holds the per-pair rules a run uses.
type PairRules struct {
	Source string              `json:"source"` // file the rules came from; "" = built-in fallback only
	Rules  map[string]PairRule `json:"rules"`  // by internal symbol, e.g. BTC/EUR
}

// krakenAssetPair is the subset of a Kraken AssetPairs entry the rules use.
type krakenAssetPair struct {
	WSName       string `json:"wsname"`
	LotDecimals  *int   `json:"lot_decimals"`
	PairDecimals *int   `json:"pair_decimals"`
	OrderMin     string `json:"ordermin"`
	CostMin      string `json:"costmin"`
}

// krakenLegacyBases maps Kraken's legacy base names to the bot's symbols.
var krakenLegacyBases = map[string]string{"XBT": "BTC", "XDG": "DOGE"}

// LoadPairRules reads a saved Kraken AssetPairs response (the JSON of
// https://api.kraken.com/0/public/AssetPairs, saved by the owner; nothing is
// fetched here). An empty path returns rules that use FallbackPairRule.
func LoadPairRules(path string) (PairRules, error) {
	rules := PairRules{Rules: map[string]PairRule{}}
	if path == "" {
		return rules, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return PairRules{}, fmt.Errorf("pair rules: %w", err)
	}
	var doc struct {
		Error  []string                   `json:"error"`
		Result map[string]krakenAssetPair `json:"result"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return PairRules{}, fmt.Errorf("pair rules %s: %w", path, err)
	}
	if len(doc.Error) > 0 {
		return PairRules{}, fmt.Errorf("pair rules %s: saved response holds errors: %s", path, strings.Join(doc.Error, "; "))
	}
	keys := make([]string, 0, len(doc.Result))
	for k := range doc.Result {
		keys = append(keys, k)
	}
	sort.Strings(keys) // a symbol listed twice keeps the first entry in key order
	for _, k := range keys {
		p := doc.Result[k]
		symbol, ok := internalSymbol(p.WSName)
		if !ok {
			continue
		}
		if _, dup := rules.Rules[symbol]; dup {
			continue
		}
		rule, err := p.rule()
		if err != nil {
			return PairRules{}, fmt.Errorf("pair rules %s, %s: %w", path, k, err)
		}
		rules.Rules[symbol] = rule
	}
	rules.Source = path
	return rules, nil
}

// rule converts an AssetPairs entry, keeping the fallback for missing fields.
func (p krakenAssetPair) rule() (PairRule, error) {
	r := FallbackPairRule
	if p.LotDecimals != nil {
		r.LotDecimals = *p.LotDecimals
	}
	if p.PairDecimals != nil {
		r.PairDecimals = *p.PairDecimals
	}
	for _, f := range []struct {
		raw string
		dst *float64
	}{{p.OrderMin, &r.OrderMin}, {p.CostMin, &r.CostMin}} {
		if f.raw == "" {
			continue
		}
		v, err := strconv.ParseFloat(f.raw, 64)
		if err != nil || v < 0 || math.IsNaN(v) {
			return PairRule{}, fmt.Errorf("bad number %q", f.raw)
		}
		*f.dst = v
	}
	if r.LotDecimals < 0 || r.LotDecimals > 12 || r.PairDecimals > 12 {
		return PairRule{}, fmt.Errorf("decimals out of range: lot %d pair %d", r.LotDecimals, r.PairDecimals)
	}
	return r, nil
}

// internalSymbol converts a Kraken wsname ("XBT/EUR") to the bot's symbol ("BTC/EUR").
func internalSymbol(wsname string) (string, bool) {
	base, quote, ok := strings.Cut(wsname, "/")
	if !ok || base == "" || quote == "" {
		return "", false
	}
	if alias, ok := krakenLegacyBases[base]; ok {
		base = alias
	}
	return base + "/" + quote, true
}

// For returns market's rule and whether it came from the file.
func (p PairRules) For(market string) (PairRule, bool) {
	if r, ok := p.Rules[market]; ok {
		return r, true
	}
	return FallbackPairRule, false
}

// roundQtyDown rounds a quantity down to decimals places. A tiny tolerance
// keeps values already on the grid (2.33 stored as 2.3299999...) in place.
func roundQtyDown(q float64, decimals int) float64 {
	scale := math.Pow(10, float64(decimals))
	return math.Floor(q*scale+1e-9) / scale
}

// roundPriceAgainst rounds a buy price up and a sell price down to decimals
// places, so rounding never favours the bot; decimals < 0 leaves it as is.
func roundPriceAgainst(price float64, decimals int, buy bool) float64 {
	if decimals < 0 {
		return price
	}
	scale := math.Pow(10, float64(decimals))
	if buy {
		return math.Ceil(price*scale-1e-9) / scale
	}
	return math.Floor(price*scale+1e-9) / scale
}
