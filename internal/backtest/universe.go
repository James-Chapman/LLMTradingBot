package backtest

import (
	"fmt"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
)

// UniverseRule re-selects the markets at points in time, the way the live
// bot picks Kraken's biggest pairs at start-up, using only data before each
// selection time (histdata.Universe: 24 h traded value, pegged pairs skipped).
type UniverseRule struct {
	TopN  int    `json:"top_n"`
	Quote string `json:"quote"` // quote currency; "" uses the settings' base currency
	Every string `json:"every"` // "month" (default, at each UTC month start), "week", or a Go duration
}

// nextAfter returns the first re-selection time after at.
func (u UniverseRule) nextAfter(at time.Time) (time.Time, error) {
	switch u.Every {
	case "", "month":
		y, m, _ := at.UTC().Date()
		return time.Date(y, m+1, 1, 0, 0, 0, 0, time.UTC), nil
	case "week":
		return at.Add(7 * 24 * time.Hour), nil
	}
	d, err := time.ParseDuration(u.Every)
	if err != nil || d < time.Hour {
		return time.Time{}, fmt.Errorf("universe interval must be month, week or a duration of at least 1h, got %q", u.Every)
	}
	return at.Add(d), nil
}

// selectTop returns up to n markets quoted in quote by traded value in the
// 24 hours before at, biggest first.
func (r *runner) selectTop(n int, quote string, at time.Time) ([]string, error) {
	sel, err := histdata.Universe(r.reader, at, histdata.UniverseOptions{Quote: quote, Top: n})
	if err != nil {
		return nil, fmt.Errorf("universe at %s: %w", at.Format(time.RFC3339), err)
	}
	out := make([]string, 0, len(sel.Selected))
	for _, e := range sel.Selected {
		out = append(out, e.Market)
	}
	return out, nil
}
