// Package shadow scores the LLM's shadow verdicts: what an LLM version of a
// 4-hour strategy would have said about each entry its base strategy made,
// measured against the trades those entries became. Nothing here trades.
package shadow

import (
	"sort"
	"time"
)

// Timing used to match a closed trade to the verdict it was entered under.
const (
	// CandleLength is the length of the 4-hour candles the verdicts are keyed on.
	CandleLength = 4 * time.Hour
	// MatchWindow is how long after a reviewed candle opened a trade may be
	// entered and still count as that verdict's trade: the candle itself
	// (4 h), the next one, while the entry is made (4 h), and 4 h of slack
	// for late candle data and approvals.
	MatchWindow = 12 * time.Hour
	// DefaultMinScoredVetoes is how many vetoes of closed trades a strategy
	// needs before its verdict says more than "too few".
	DefaultMinScoredVetoes = 10
	// RecentLimit is how many reviews the recent list shows.
	RecentLimit = 20
)

// Verdicts on whether the LLM's vetoes would have helped a strategy.
const (
	VerdictTooFew = "too_few" // fewer scored vetoes than the minimum
	VerdictAhead  = "ahead"   // the vetoed trades lost money in total: vetoing them would have helped
	VerdictBehind = "behind"  // the vetoed trades made money in total: vetoing them would have hurt
)

// Review is the LLM's shadow verdict on one entry: the answer the strategy's
// LLM version would have given for the market's last closed 4-hour candle.
type Review struct {
	StrategyID     string    `json:"strategy_id"`
	Market         string    `json:"market"`
	Candle         time.Time `json:"candle"`      // start of the last closed 4-hour candle
	ReviewedAt     time.Time `json:"reviewed_at"` // when the LLM answered
	TradeIdeaID    string    `json:"trade_idea_id"`
	BaseConfidence float64   `json:"base_confidence"`
	LLMUsed        bool      `json:"llm_used"` // false: the LLM could not answer
	Scale          float64   `json:"confidence_scale"`
	Sentiment      float64   `json:"sentiment"`
	Reasoning      string    `json:"reasoning"`
	WouldVeto      bool      `json:"would_veto"` // scale below the veto threshold
	VetoThreshold  float64   `json:"veto_threshold"`
}

// Outcome is one closed trade, as recorded for the learner.
type Outcome struct {
	StrategyID string
	Market     string
	Direction  string
	EntryAt    time.Time
	PnL        float64 // realised, after fees
}

// Score is how a strategy's shadow verdicts compare with its closed trades.
type Score struct {
	StrategyID   string `json:"strategy_id"`
	Reviewed     int    `json:"reviewed"`        // entries the LLM answered
	Unavailable  int    `json:"llm_unavailable"` // entries the LLM could not answer
	WouldVeto    int    `json:"would_veto"`      // answered entries below the threshold
	Scored       int    `json:"scored_trades"`   // closed trades with an answered verdict
	Losers       int    `json:"losing_trades"`   // scored trades with P&L <= 0
	ScoredVetoes int    `json:"scored_vetoes"`   // scored trades the LLM would have vetoed
	VetoedLosers int    `json:"vetoed_losers"`   // of those, the losers
	// VetoAccuracy is VetoedLosers / ScoredVetoes; nil without a scored veto.
	VetoAccuracy *float64 `json:"veto_accuracy"`
	// LoserRate is Losers / Scored: the accuracy of vetoes picked at random.
	LoserRate *float64 `json:"loser_rate"`
	// BreakEven is average win / (average win + average loss): the veto
	// accuracy at which vetoes neither help nor hurt. Nil without both a
	// winning and a losing trade.
	BreakEven *float64 `json:"break_even"`
	VetoedPnL float64  `json:"vetoed_pnl"` // total P&L of the trades the LLM would have vetoed
	KeptPnL   float64  `json:"kept_pnl"`   // total P&L of the scored trades it would have kept
	Verdict   string   `json:"verdict"`
}

// RecentReview is one review with the closed trades entered under it.
type RecentReview struct {
	Review
	Trades int      `json:"trades"`
	PnL    *float64 `json:"pnl"` // nil while no trade under this verdict has closed
}

// Summary is the per-strategy scores and the newest reviews.
type Summary struct {
	Strategies []Score        `json:"strategies"`
	Recent     []RecentReview `json:"recent"`
}

// Summarise matches each closed long to the newest review of its strategy
// and market whose candle had closed before the entry (within MatchWindow of
// the candle's start), then scores every strategy that has a review. Trades
// without a review, and reviews the LLM could not answer, are not scored.
func Summarise(reviews []Review, outcomes []Outcome, minScoredVetoes int) Summary {
	trades := make([][]Outcome, len(reviews)) // trades matched to reviews[i]
	for _, o := range outcomes {
		if i := match(reviews, o); i >= 0 {
			trades[i] = append(trades[i], o)
		}
	}

	byStrategy := map[string]*tally{}
	var order []string
	for i, r := range reviews {
		t, ok := byStrategy[r.StrategyID]
		if !ok {
			t = &tally{score: Score{StrategyID: r.StrategyID}}
			byStrategy[r.StrategyID] = t
			order = append(order, r.StrategyID)
		}
		t.add(r, trades[i])
	}
	sort.Strings(order)
	out := Summary{Strategies: []Score{}, Recent: recent(reviews, trades)}
	for _, id := range order {
		out.Strategies = append(out.Strategies, byStrategy[id].finish(minScoredVetoes))
	}
	return out
}

// match returns the index of the review o was entered under, or -1.
func match(reviews []Review, o Outcome) int {
	if o.Direction != "long" {
		return -1
	}
	best := -1
	for i, r := range reviews {
		if r.StrategyID != o.StrategyID || r.Market != o.Market {
			continue
		}
		closed := r.Candle.Add(CandleLength)
		if o.EntryAt.Before(closed) || !o.EntryAt.Before(r.Candle.Add(MatchWindow)) {
			continue
		}
		if best < 0 || r.Candle.After(reviews[best].Candle) {
			best = i
		}
	}
	return best
}

// tally accumulates one strategy's score.
type tally struct {
	score           Score
	wins, losses    int
	winSum, lossSum float64 // lossSum is positive
}

// add counts one review and the trades entered under it.
func (t *tally) add(r Review, trades []Outcome) {
	if !r.LLMUsed {
		t.score.Unavailable++
		return
	}
	t.score.Reviewed++
	if r.WouldVeto {
		t.score.WouldVeto++
	}
	for _, o := range trades {
		t.score.Scored++
		loser := o.PnL <= 0
		if loser {
			t.score.Losers++
			t.losses++
			t.lossSum -= o.PnL
		} else {
			t.wins++
			t.winSum += o.PnL
		}
		if r.WouldVeto {
			t.score.ScoredVetoes++
			t.score.VetoedPnL += o.PnL
			if loser {
				t.score.VetoedLosers++
			}
		} else {
			t.score.KeptPnL += o.PnL
		}
	}
}

// finish computes the ratios and the verdict.
func (t *tally) finish(minScoredVetoes int) Score {
	s := t.score
	if s.ScoredVetoes > 0 {
		s.VetoAccuracy = ratio(float64(s.VetoedLosers), float64(s.ScoredVetoes))
	}
	if s.Scored > 0 {
		s.LoserRate = ratio(float64(s.Losers), float64(s.Scored))
	}
	if t.wins > 0 && t.losses > 0 {
		avgWin, avgLoss := t.winSum/float64(t.wins), t.lossSum/float64(t.losses)
		if avgWin+avgLoss > 0 {
			s.BreakEven = ratio(avgWin, avgWin+avgLoss)
		}
	}
	switch {
	case s.ScoredVetoes < minScoredVetoes:
		s.Verdict = VerdictTooFew
	case s.VetoedPnL < 0:
		s.Verdict = VerdictAhead
	default:
		s.Verdict = VerdictBehind
	}
	return s
}

// ratio returns a pointer to a / b.
func ratio(a, b float64) *float64 {
	v := a / b
	return &v
}

// recent returns the newest reviews first (by candle, then answer time) with
// their closed trades, at most RecentLimit.
func recent(reviews []Review, trades [][]Outcome) []RecentReview {
	idx := make([]int, len(reviews))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ra, rb := reviews[idx[a]], reviews[idx[b]]
		if !ra.Candle.Equal(rb.Candle) {
			return ra.Candle.After(rb.Candle)
		}
		return ra.ReviewedAt.After(rb.ReviewedAt)
	})
	out := []RecentReview{}
	for _, i := range idx[:min(len(idx), RecentLimit)] {
		row := RecentReview{Review: reviews[i], Trades: len(trades[i])}
		if len(trades[i]) > 0 {
			sum := 0.0
			for _, o := range trades[i] {
				sum += o.PnL
			}
			row.PnL = &sum
		}
		out = append(out, row)
	}
	return out
}
