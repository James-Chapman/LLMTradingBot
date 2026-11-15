package shadow

import (
	"testing"
	"time"
)

// candle0 is the start of the closed 4-hour candle the test entries were decided on.
var candle0 = time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)

// review returns an answered review of donchian_strategy on market for the candle starting at c.
func review(market string, c time.Time, scale float64, veto bool) Review {
	return Review{StrategyID: "donchian_strategy", Market: market, Candle: c, ReviewedAt: c.Add(4*time.Hour + time.Minute),
		LLMUsed: true, Scale: scale, WouldVeto: veto, Reasoning: "why"}
}

// trade returns a closed long of donchian_strategy on market entered at entry.
func trade(market string, entry time.Time, pnl float64) Outcome {
	return Outcome{StrategyID: "donchian_strategy", Market: market, Direction: "long", EntryAt: entry, PnL: pnl}
}

// only returns the single score in scores, failing otherwise.
func only(t *testing.T, scores []Score) Score {
	t.Helper()
	if len(scores) != 1 {
		t.Fatalf("got %d strategy scores, want 1: %+v", len(scores), scores)
	}
	return scores[0]
}

// TestSummarise covers scoring shadow verdicts against the trades that the
// reviewed entries became.
func TestSummarise(t *testing.T) {
	entry := candle0.Add(4*time.Hour + 30*time.Second) // first tick after the candle closed

	t.Run("GIVEN verdicts and the trades they reviewed WHEN summarised THEN veto accuracy, loser rate, break-even and the vetoed P&L are reported", func(t *testing.T) {
		reviews := []Review{
			review("BTC/EUR", candle0, 0.5, true),
			review("ETH/EUR", candle0, 0.6, true),
			review("SOL/EUR", candle0, 1.2, false),
			review("XRP/EUR", candle0, 1.0, false),
		}
		outcomes := []Outcome{
			trade("BTC/EUR", entry, -5), // vetoed loser
			trade("ETH/EUR", entry, 20), // vetoed winner
			trade("SOL/EUR", entry, -5), // kept loser
			trade("XRP/EUR", entry, 10), // kept winner
		}
		got := only(t, Summarise(reviews, outcomes, 1).Strategies)
		if got.Reviewed != 4 || got.WouldVeto != 2 || got.Scored != 4 || got.Losers != 2 || got.ScoredVetoes != 2 || got.VetoedLosers != 1 {
			t.Fatalf("counts %+v", got)
		}
		if *got.VetoAccuracy != 0.5 || *got.LoserRate != 0.5 {
			t.Fatalf("veto accuracy %v, loser rate %v; want 0.5 and 0.5", *got.VetoAccuracy, *got.LoserRate)
		}
		if *got.BreakEven != 0.75 { // average win 15, average loss 5
			t.Fatalf("break-even %v, want 0.75", *got.BreakEven)
		}
		if got.VetoedPnL != 15 || got.KeptPnL != 5 || got.Verdict != VerdictBehind {
			t.Fatalf("vetoed %v kept %v verdict %q; want 15, 5, behind", got.VetoedPnL, got.KeptPnL, got.Verdict)
		}
	})

	t.Run("GIVEN vetoes that would have removed losing trades WHEN summarised THEN the LLM is ahead", func(t *testing.T) {
		reviews := []Review{review("BTC/EUR", candle0, 0.5, true), review("ETH/EUR", candle0, 1.1, false)}
		outcomes := []Outcome{trade("BTC/EUR", entry, -8), trade("ETH/EUR", entry, 3)}
		if got := only(t, Summarise(reviews, outcomes, 1).Strategies); got.Verdict != VerdictAhead || got.VetoedPnL != -8 {
			t.Fatalf("score %+v, want ahead with -8 vetoed", got)
		}
	})

	t.Run("GIVEN fewer scored vetoes than the minimum WHEN summarised THEN it is too early to judge", func(t *testing.T) {
		reviews := []Review{review("BTC/EUR", candle0, 0.5, true)}
		got := only(t, Summarise(reviews, []Outcome{trade("BTC/EUR", entry, -8)}, 2).Strategies)
		if got.Verdict != VerdictTooFew {
			t.Fatalf("verdict %q, want %q", got.Verdict, VerdictTooFew)
		}
	})

	t.Run("GIVEN two trades entered within one candle's window WHEN summarised THEN both are scored against its verdict", func(t *testing.T) {
		reviews := []Review{review("BTC/EUR", candle0, 0.5, true)}
		outcomes := []Outcome{trade("BTC/EUR", entry, -3), trade("BTC/EUR", entry.Add(2*time.Hour), -4)} // stopped out, re-entered
		got := only(t, Summarise(reviews, outcomes, 1).Strategies)
		if got.Scored != 2 || got.ScoredVetoes != 2 || got.VetoedPnL != -7 {
			t.Fatalf("score %+v, want both trades under the veto", got)
		}
	})

	t.Run("GIVEN verdicts for successive candles WHEN a trade is entered THEN it is scored against the newest candle closed before the entry", func(t *testing.T) {
		later := candle0.Add(4 * time.Hour)
		reviews := []Review{review("BTC/EUR", candle0, 0.5, true), review("BTC/EUR", later, 1.2, false)}
		got := only(t, Summarise(reviews, []Outcome{trade("BTC/EUR", later.Add(4*time.Hour+time.Minute), 6)}, 1).Strategies)
		if got.Scored != 1 || got.ScoredVetoes != 0 || got.KeptPnL != 6 {
			t.Fatalf("score %+v, want the trade kept under the later verdict", got)
		}
	})

	t.Run("GIVEN trades without a matching verdict WHEN summarised THEN they are not scored", func(t *testing.T) {
		reviews := []Review{review("BTC/EUR", candle0, 0.5, true)}
		short := trade("BTC/EUR", entry, -2)
		short.Direction = "short"
		other := trade("BTC/EUR", entry, -2)
		other.StrategyID = "macd_strategy"
		outcomes := []Outcome{
			trade("ETH/EUR", entry, -2),                              // another market
			trade("BTC/EUR", candle0.Add(time.Hour), -2),             // before the candle closed
			trade("BTC/EUR", candle0.Add(MatchWindow+time.Hour), -2), // too late
			short, other,
		}
		if got := only(t, Summarise(reviews, outcomes, 1).Strategies); got.Scored != 0 {
			t.Fatalf("score %+v, want nothing scored", got)
		}
	})

	t.Run("GIVEN a verdict the LLM could not give WHEN summarised THEN it is counted as unavailable and its trade is not scored", func(t *testing.T) {
		down := review("BTC/EUR", candle0, 1, false)
		down.LLMUsed = false
		got := only(t, Summarise([]Review{down}, []Outcome{trade("BTC/EUR", entry, -2)}, 1).Strategies)
		if got.Reviewed != 0 || got.Unavailable != 1 || got.Scored != 0 || got.VetoAccuracy != nil || got.BreakEven != nil {
			t.Fatalf("score %+v", got)
		}
	})

	t.Run("GIVEN reviews WHEN summarised THEN the recent list is newest first with each review's closed trades", func(t *testing.T) {
		later := candle0.Add(4 * time.Hour)
		reviews := []Review{review("BTC/EUR", candle0, 0.5, true), review("ETH/EUR", later, 1.2, false)}
		s := Summarise(reviews, []Outcome{trade("BTC/EUR", entry, -3)}, 1)
		if len(s.Recent) != 2 || s.Recent[0].Market != "ETH/EUR" || s.Recent[1].Market != "BTC/EUR" {
			t.Fatalf("recent %+v, want ETH then BTC", s.Recent)
		}
		if s.Recent[0].Trades != 0 || s.Recent[0].PnL != nil || s.Recent[1].Trades != 1 || *s.Recent[1].PnL != -3 {
			t.Fatalf("recent outcomes %+v", s.Recent)
		}
	})
}
