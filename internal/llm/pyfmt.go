package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/James-Chapman/LLMTradingBot/internal/analysis"
)

// The helpers in this file reproduce Python's str()/format() output so the
// prompts sent to the model stay textually identical to the Python bot.

// pyRepr renders a value the way Python's str() would (72.0, None, True, ...).
func pyRepr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case float64:
		return pyFloatRepr(x)
	case float32:
		return pyFloatRepr(float64(x))
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case json.Number:
		return x.String()
	default:
		return fmt.Sprint(x)
	}
}

// pyFloatRepr renders a float like Python's repr (shortest round-trip form,
// always with a decimal point or exponent).
func pyFloatRepr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	if abs := math.Abs(f); f != 0 && (abs < 1e-4 || abs >= 1e16) {
		return strconv.FormatFloat(f, 'e', -1, 64)
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// pyTruthy reports Python truthiness for decoded JSON-like values.
func pyTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case map[string]any:
		return len(x) > 0
	case analysis.Indicators:
		return len(x) > 0
	case map[string]float64:
		return len(x) > 0
	case []any:
		return len(x) > 0
	default:
		if f, ok := toNumber(v); ok {
			return f != 0
		}
		return true
	}
}

// toNumber converts Go numeric types (not strings) to float64.
func toNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case int32:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// errNotNumber reports a value Python's float() would reject.
var errNotNumber = errors.New("value is not a number")

// pyFloat converts a value like Python's float(): numbers, bools and numeric
// strings are accepted; None and other types are errors. NaN/Inf are rejected.
func pyFloat(v any) (float64, error) {
	var f float64
	switch x := v.(type) {
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return 0, fmt.Errorf("could not convert string to float: %q", x)
		}
		f = parsed
	default:
		n, ok := toNumber(v)
		if !ok {
			return 0, fmt.Errorf("%w: %v", errNotNumber, v)
		}
		f = n
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("%w: %v", errNotNumber, v)
	}
	return f, nil
}

// floatField reads key from an LLM result like float(result.get(key, def)).
func floatField(result map[string]any, key string, def float64) (float64, error) {
	v, ok := result[key]
	if !ok {
		return def, nil
	}
	f, err := pyFloat(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return f, nil
}

// strField reads key from an LLM result like str(result.get(key, def)).
func strField(result map[string]any, key, def string) string {
	v, ok := result[key]
	if !ok {
		return def
	}
	return pyRepr(v)
}

// pyGet returns m[key], or def when the key is absent (like dict.get).
func pyGet(m map[string]any, key string, def any) any {
	if v, ok := m[key]; ok {
		return v
	}
	return def
}

// numOr returns m[key] as a float64, or 0 when absent or non-numeric.
func numOr(m map[string]any, key string) float64 {
	f, _ := toNumber(m[key])
	return f
}

// asGroup returns a nested indicator group as a generic map.
func asGroup(v any) (map[string]any, bool) {
	switch g := v.(type) {
	case map[string]any:
		return g, true
	case analysis.Indicators:
		return g, true
	case map[string]float64:
		out := make(map[string]any, len(g))
		for k, f := range g {
			out[k] = f
		}
		return out, true
	default:
		return nil, false
	}
}

// clamp limits v to [lo, hi].
func clamp(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}

// fmtSigned formats like Python's f"{v:+.Nf}".
func fmtSigned(v float64, prec int) string {
	return fmt.Sprintf("%+.*f", prec, v)
}

// fmtPercent formats like Python's f"{v:.N%}" (value multiplied by 100).
func fmtPercent(v float64, prec int) string {
	return strconv.FormatFloat(v*100, 'f', prec, 64) + "%"
}

// fmtSignedPercent formats like Python's f"{v:+.N%}".
func fmtSignedPercent(v float64, prec int) string {
	return fmtSigned(v*100, prec) + "%"
}

// fmtThousands formats like Python's f"{v:,.Nf}" (comma thousands separators).
func fmtThousands(v float64, prec int) string {
	s := strconv.FormatFloat(v, 'f', prec, 64)
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	intPart, frac := s, ""
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		intPart, frac = s[:dot], s[dot:]
	}
	var b strings.Builder
	for i, digit := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(digit)
	}
	return sign + b.String() + frac
}

// pyRound1 rounds like Python's round(v, 1) (correctly rounded, half-even).
func pyRound1(v float64) float64 {
	f, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'f', 1, 64), 64)
	return f
}

// itoa formats an int in base 10.
func itoa(n int) string {
	return strconv.Itoa(n)
}

// strconvFixed formats like Python's f"{v:.Nf}".
func strconvFixed(v float64, prec int) string {
	return strconv.FormatFloat(v, 'f', prec, 64)
}
