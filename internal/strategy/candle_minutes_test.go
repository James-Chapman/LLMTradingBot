package strategy

import (
	"slices"
	"testing"
)

// TestCandleMinutes covers the candle length each strategy reads its signals
// from, which the signal dialog's chart opens on.
func TestCandleMinutes(t *testing.T) {
	t.Run("GIVEN every strategy WHEN its candle length is asked THEN it is one the bot keeps candles for", func(t *testing.T) {
		for _, s := range All(testSettings()) {
			if m := CandleMinutes(s.ID()); !slices.Contains([]int{15, 60, 240}, m) {
				t.Errorf("%s: %d minutes, want 15, 60 or 240", s.ID(), m)
			}
		}
	})

	t.Run("GIVEN a 1-hour basic strategy, with or without the LLM WHEN its candle length is asked THEN it is one hour", func(t *testing.T) {
		for _, id := range []string{Basic1hID, BasicLong1hID, BasicAndLLM1hID, BasicAndLLMLong1hID} {
			if m := CandleMinutes(id); m != 60 {
				t.Errorf("%s: %d minutes, want 60", id, m)
			}
		}
	})

	t.Run("GIVEN a 4-hour candle strategy, with or without the LLM WHEN its candle length is asked THEN it is four hours", func(t *testing.T) {
		for _, id := range []string{Basic4hID, BasicAndLLMLong4hID, TrendID, TrendAndLLMID, DonchianAndLLMID, RSIDipID, MACDID, SqueezeAndLLMID, RotationID, RotationAndLLMID} {
			if m := CandleMinutes(id); m != 240 {
				t.Errorf("%s: %d minutes, want 240", id, m)
			}
		}
	})

	t.Run("GIVEN the All LLM strategy WHEN its candle length is asked THEN it is one hour", func(t *testing.T) {
		if m := CandleMinutes(LLMOnlyID); m != 60 {
			t.Errorf("%s: %d minutes, want 60", LLMOnlyID, m)
		}
	})

	t.Run("GIVEN an unknown id WHEN its candle length is asked THEN the 15-minute candles are used", func(t *testing.T) {
		for _, id := range []string{"retired_strategy"} {
			if m := CandleMinutes(id); m != 15 {
				t.Errorf("%s: %d minutes, want 15", id, m)
			}
		}
	})
}
