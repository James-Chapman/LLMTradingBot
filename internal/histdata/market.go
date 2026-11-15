package histdata

import (
	"cmp"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// DefaultQuotes are the quote currencies an archive file name may end with.
// Longer codes are tried first, so "ETHUSDT" is ETH/USDT, not ETHU/SDT.
var DefaultQuotes = []string{"EUR", "USD", "GBP", "CAD", "JPY", "CHF", "AUD", "USDT", "USDC", "DAI", "PYUSD", "XBT", "BTC", "ETH"}

// DefaultAliases maps Kraken's legacy asset codes to the names the bot uses.
var DefaultAliases = map[string]string{"XBT": "BTC", "XDG": "DOGE"}

// legacyQuotes are the four-letter quote codes of Kraken's oldest pair names
// ("XXBTZEUR" = X + XBT + Z + EUR).
var legacyQuotes = map[string]string{
	"ZEUR": "EUR", "ZUSD": "USD", "ZGBP": "GBP", "ZCAD": "CAD", "ZJPY": "JPY", "ZCHF": "CHF", "ZAUD": "AUD",
	"XXBT": "XBT", "XETH": "ETH",
}

// restAltNames maps the bot's asset names back to Kraken's REST altname codes.
var restAltNames = map[string]string{"BTC": "XBT", "DOGE": "XDG"}

// MarketMapper turns archive file names into markets ("BASE/QUOTE").
//
// The rule, in order:
//  1. an override for the file's stem (name without directory and ".csv",
//     compared case-insensitively) wins;
//  2. otherwise the stem is upper-cased and the separators "-", "_", ".",
//     "/" and space are removed;
//  3. an eight-letter legacy name X<base3>{Z|X}<quote3> ("XXBTZEUR") is split
//     at the middle;
//  4. otherwise the longest known quote suffix (Quotes) is split off; the
//     rest is the base;
//  5. aliases (XBT→BTC, XDG→DOGE) are applied to both halves.
//
// Anything else — no known quote, an empty base, non-alphanumeric characters,
// a file that is not .csv — is unmapped and must be reported, never guessed.
type MarketMapper struct {
	Overrides map[string]string // file stem → market, e.g. "RNDREUR" → "RENDER/EUR"
	Quotes    []string          // nil means DefaultQuotes
	Aliases   map[string]string // nil means DefaultAliases
}

// fileStem returns the upper-cased name of a .csv file without directory or
// extension; ok is false for any other file.
func fileStem(name string) (string, bool) {
	base := path.Base(filepath.ToSlash(name))
	if len(base) < 5 || !strings.EqualFold(base[len(base)-4:], ".csv") {
		return "", false
	}
	return strings.ToUpper(base[:len(base)-4]), true
}

// MarketForFile maps an archive file name to a market; ok is false when the
// name cannot be mapped by the documented rule.
func (m MarketMapper) MarketForFile(name string) (string, bool) {
	stem, ok := fileStem(name)
	if !ok {
		return "", false
	}
	for k, v := range m.Overrides {
		if strings.ToUpper(k) == stem && validMarket(v) {
			return v, true
		}
	}
	compact := strings.Map(func(r rune) rune {
		if strings.ContainsRune("-_./ ", r) {
			return -1
		}
		return r
	}, stem)
	base, quote, ok := m.split(compact)
	if !ok {
		return "", false
	}
	market := m.alias(base) + "/" + m.alias(quote)
	if !validMarket(market) {
		return "", false
	}
	return market, true
}

// split separates a compact pair name into base and quote.
func (m MarketMapper) split(compact string) (string, string, bool) {
	if len(compact) == 8 && compact[0] == 'X' {
		if q, ok := legacyQuotes[compact[4:]]; ok {
			return compact[1:4], q, true
		}
	}
	quotes := m.Quotes
	if quotes == nil {
		quotes = DefaultQuotes
	}
	ordered := slices.Clone(quotes)
	slices.SortStableFunc(ordered, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	for _, q := range ordered {
		q = strings.ToUpper(q)
		if len(compact) > len(q) && strings.HasSuffix(compact, q) {
			return compact[:len(compact)-len(q)], q, true
		}
	}
	return "", "", false
}

// alias applies the asset aliases to one asset code.
func (m MarketMapper) alias(asset string) string {
	aliases := m.Aliases
	if aliases == nil {
		aliases = DefaultAliases
	}
	if a, ok := aliases[asset]; ok {
		return a
	}
	return asset
}

// validMarket reports whether s is BASE/QUOTE with upper-case letters and digits.
func validMarket(s string) bool {
	base, quote, ok := strings.Cut(s, "/")
	return ok && validAsset(base) && validAsset(quote)
}

// validAsset reports whether s is a non-empty upper-case alphanumeric code.
func validAsset(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// RESTPairName returns the pair name Kraken's REST API accepts for market:
// the altname, i.e. BASEQUOTE with BTC sent as XBT and DOGE as XDG.
func RESTPairName(market string) (string, error) {
	if !validMarket(market) {
		return "", fmt.Errorf("market %q must look like BASE/QUOTE (e.g. BTC/EUR)", market)
	}
	base, quote, _ := strings.Cut(market, "/")
	alt := func(a string) string {
		if v, ok := restAltNames[a]; ok {
			return v
		}
		return a
	}
	return alt(base) + alt(quote), nil
}

// MarketDir is the cache directory name of market ("BTC/EUR" → "BTC-EUR").
func MarketDir(market string) string {
	return strings.Replace(market, "/", "-", 1)
}

// marketFromDir reverses MarketDir; ok is false for a directory that is not a market.
func marketFromDir(dir string) (string, bool) {
	m := strings.Replace(dir, "-", "/", 1)
	return m, validMarket(m)
}
