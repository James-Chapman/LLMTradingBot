package strategy

import (
	"context"
	"sync"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
)

// ShadowEntry is one entry of a non-LLM 4-hour strategy that is waiting for
// its shadow review, with the inputs its LLM version would have seen.
type ShadowEntry struct {
	Idea   domain.TradeIdea
	Candle int64 // start (Unix seconds) of the market's last closed 4-hour candle
	base   Prompter
	in     Input
}

// ShadowVerdict is the LLM's answer to a shadow review: what the strategy's
// LLM version would have done with the entry. It is only recorded.
type ShadowVerdict struct {
	StrategyID     string
	Market         string
	Candle         int64
	TradeIdeaID    string
	BaseConfidence float64
	Analysis       llm.SignalAnalysis
	WouldVeto      bool // the LLM answered with a scale below the veto threshold
	VetoThreshold  float64
}

// ShadowReviewer asks the LLM about the entries of a non-LLM 4-hour strategy
// as that strategy's LLM version would, once per market per closed candle,
// without changing the ideas. It is safe for concurrent use.
type ShadowReviewer struct {
	mu      sync.Mutex
	claimed map[string]int64 // strategy|market → candle already claimed
}

// NewShadowReviewer creates a reviewer with nothing claimed.
func NewShadowReviewer() *ShadowReviewer {
	return &ShadowReviewer{claimed: map[string]int64{}}
}

// Claim returns the entries in ideas that s's LLM version would have sent to
// the LLM and that were not claimed before for their market's last closed
// candle, and marks them claimed. Only a non-LLM strategy with its own
// prompt (the 4-hour strategies) is reviewed; exits, entries for a market
// already held and entries without a closed candle are not. in is the input
// the ideas were evaluated on, with the news and analyser filled in.
func (r *ShadowReviewer) Claim(s Strategy, ideas []domain.TradeIdea, in Input) []ShadowEntry {
	base, ok := s.(Prompter)
	if !ok || s.UsesLLM() {
		return nil
	}
	var out []ShadowEntry
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, idea := range ideas {
		if idea.Direction != domain.Long || holdsLong(in.Portfolio, idea.Market) {
			continue
		}
		candle, ok := lastClosedCandle(in.MarketData[idea.Market].TrendCandles)
		if !ok {
			continue
		}
		key := s.ID() + "|" + idea.Market
		if c, seen := r.claimed[key]; seen && c == candle {
			continue
		}
		r.claimed[key] = candle
		out = append(out, ShadowEntry{Idea: idea, Candle: candle, base: base, in: in})
	}
	return out
}

// Review asks the LLM about a claimed entry with exactly the request the LLM
// version sends. When the LLM cannot answer, the claim is released so the
// entry is reviewed again if it comes back on a later tick.
func (r *ShadowReviewer) Review(ctx context.Context, analyser Analyser, e ShadowEntry, s config.Settings) ShadowVerdict {
	analysis := askLLM(ctx, analyser, e.base, e.Idea, e.in.MarketData[e.Idea.Market], e.in, s)
	if !analysis.LLMUsed {
		r.release(e)
	}
	return ShadowVerdict{
		StrategyID:     e.base.ID(),
		Market:         e.Idea.Market,
		Candle:         e.Candle,
		TradeIdeaID:    e.Idea.ID,
		BaseConfidence: e.Idea.Confidence,
		Analysis:       analysis,
		WouldVeto:      analysis.LLMUsed && analysis.ConfidenceScale < s.LLMVetoThreshold,
		VetoThreshold:  s.LLMVetoThreshold,
	}
}

// release forgets the claim on e's candle, unless a newer candle has been
// claimed since.
func (r *ShadowReviewer) release(e ShadowEntry) {
	key := e.base.ID() + "|" + e.Idea.Market
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claimed[key] == e.Candle {
		delete(r.claimed, key)
	}
}
