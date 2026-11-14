// Package analysis computes technical indicators from price and candle history.
package analysis

// Indicators is the flat indicator snapshot produced by ComputeAll.
//
// It mirrors the JSON shape stored in trade_ideas.indicators and shown in the
// dashboard: scalar values are float64 or string (e.g. "rsi_14", "ema_cross"),
// and grouped indicators ("bb", "macd", "stoch") are nested map[string]any.
// Missing indicators are simply absent.
type Indicators map[string]any

// Num returns key as a float64 when present and numeric.
func (i Indicators) Num(key string) (float64, bool) {
	return Num(i, key)
}

// Str returns key as a string when present.
func (i Indicators) Str(key string) (string, bool) {
	v, ok := i[key].(string)
	return v, ok
}

// Group returns a nested indicator group such as "bb" or "macd".
func (i Indicators) Group(key string) (map[string]any, bool) {
	switch g := i[key].(type) {
	case map[string]any:
		return g, true
	case Indicators:
		return g, true
	default:
		return nil, false
	}
}

// Num reads a numeric value from a generic map, accepting any Go number type
// (values decoded from JSON arrive as float64; values built in code may be int).
func Num(m map[string]any, key string) (float64, bool) {
	switch v := m[key].(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}
