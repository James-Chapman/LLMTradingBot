package app

import (
	"fmt"
	"math"
	"strings"

	"github.com/James-Chapman/LLMTradingBot/internal/analysis"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// marketHasLLMPriceData reports whether one market has enough price history.
// Every entry in the tick's market data carries a current price, so only the
// history depth needs checking.
func marketHasLLMPriceData(md strategy.MarketData, requiredTicks int) bool {
	return len(md.PriceHistory) >= requiredTicks
}

// llmReadyMarketData returns only markets with enough price context for LLM strategies.
func llmReadyMarketData(marketData map[string]strategy.MarketData, requiredTicks int) map[string]strategy.MarketData {
	out := map[string]strategy.MarketData{}
	for market, md := range marketData {
		if marketHasLLMPriceData(md, requiredTicks) {
			out[market] = md
		}
	}
	return out
}

// llmStrategyDataReady reports whether LLM strategies have both price and news context.
func llmStrategyDataReady(marketData map[string]strategy.MarketData, latestNews []storage.NewsEntry, requiredTicks int) bool {
	return len(llmReadyMarketData(marketData, requiredTicks)) > 0 && len(latestNews) > 0
}

// briefingSentimentForMarket returns the market's briefing score clamped to
// [-1, 1], or neutral (0) when there is no briefing or no outlook for it.
func briefingSentimentForMarket(briefing *llm.MarketBriefing, market string) float64 {
	if briefing == nil {
		return 0
	}
	outlook, ok := briefing.MarketOutlooks[market]
	if !ok || math.IsNaN(outlook.Score) {
		return 0
	}
	return math.Max(-1, math.Min(1, outlook.Score))
}

// newsArticles converts cached news entries into LLM prompt articles.
func newsArticles(entries []storage.NewsEntry) []llm.NewsArticle {
	out := make([]llm.NewsArticle, 0, len(entries))
	for _, n := range entries {
		out = append(out, llm.NewsArticle{ID: n.ID, Source: n.Source, Title: n.Title, Summary: n.Summary})
	}
	return out
}

// newsAliases maps a base asset to the words that identify it in headlines.
var newsAliases = map[string][]string{
	"btc":  {"bitcoin", "btc"},
	"xbt":  {"bitcoin", "btc"},
	"eth":  {"ethereum", "eth"},
	"sol":  {"solana", "sol"},
	"doge": {"dogecoin", "doge"},
	"ada":  {"cardano", "ada"},
}

// newsForSignal returns up to five articles, those mentioning the market's
// asset first, padded with the rest of the latest news.
func newsForSignal(market string, latest []storage.NewsEntry) []storage.NewsContextItem {
	base := strings.ToLower(strings.Split(market, "/")[0])
	terms, ok := newsAliases[base]
	if !ok {
		terms = []string{base}
	}
	var relevant, others []storage.NewsEntry
	for _, n := range latest {
		text := strings.ToLower(n.Title + n.Summary)
		matched := false
		for _, term := range terms {
			if strings.Contains(text, term) {
				matched = true
				break
			}
		}
		if matched {
			relevant = append(relevant, n)
		} else {
			others = append(others, n)
		}
	}
	ordered := append(relevant, others...)
	if len(ordered) > 5 {
		ordered = ordered[:5]
	}
	out := make([]storage.NewsContextItem, 0, len(ordered))
	for _, n := range ordered {
		out = append(out, storage.NewsContextItem{Source: n.Source, Title: n.Title, URL: derefString(n.URL), Summary: n.Summary})
	}
	return out
}

// numSignal reads a numeric supporting-signal value.
func numSignal(signals map[string]any, key string) (float64, bool) {
	return analysis.Num(signals, key)
}

// strSignal reads a supporting-signal value as text ("" when absent or nil).
func strSignal(signals map[string]any, key string) string {
	v, ok := signals[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// truthy mirrors Python truthiness for supporting-signal flags.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case int:
		return x != 0
	default:
		return true
	}
}

// short8 returns the first eight characters of an ID for display.
func short8(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:8]
}

// directionLabel returns LONG or SHORT for activity messages.
func directionLabel(direction string) string {
	if direction == "long" {
		return "LONG"
	}
	return "SHORT"
}

// fmtMoney formats v like Python's "{v:,.2f}".
func fmtMoney(v float64) string {
	s := fmt.Sprintf("%.2f", math.Abs(v))
	intPart, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, ch := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	sign := ""
	if v < 0 && s != "0.00" {
		sign = "-"
	}
	return sign + b.String() + "." + frac
}

// fmtPrice formats a market price for activity text: like fmtMoney (2
// decimals, thousands separators) at or above 1, and like formatPrice below 1
// so coins priced far below a cent never show as 0.00.
func fmtPrice(v float64) string {
	if math.Abs(v) < 1 {
		return formatPrice(v)
	}
	return fmtMoney(v)
}

// fmtSignedMoney formats v like Python's "{v:+,.2f}".
func fmtSignedMoney(v float64) string {
	s := fmtMoney(v)
	if strings.HasPrefix(s, "-") {
		return s
	}
	return "+" + s
}

// pct0 formats a fraction like Python's "{v:.0%}".
func pct0(v float64) string { return fmt.Sprintf("%.0f%%", v*100) }

// pct1 formats a fraction like Python's "{v:.1%}".
func pct1(v float64) string { return fmt.Sprintf("%.1f%%", v*100) }
