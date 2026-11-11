package config

import "strings"

// currencySymbols maps ISO codes to their display symbol.
var currencySymbols = map[string]string{
	"EUR": "€",
	"GBP": "£",
	"USD": "$",
}

// CurrencySymbol returns a display symbol for an ISO currency code,
// falling back to the upper-cased code itself.
func CurrencySymbol(code string) string {
	upper := strings.ToUpper(code)
	if sym, ok := currencySymbols[upper]; ok {
		return sym
	}
	return upper
}
