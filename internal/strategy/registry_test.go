package strategy

import "testing"

// TestStrategyRegistry covers All, ByID and the warm-up requirements the server relies on.
func TestStrategyRegistry(t *testing.T) {
	list := All(testSettings())

	t.Run("GIVEN the registry WHEN all strategies are listed THEN the eight basic strategies come first, then llm_only and trend, then each 4-hour strategy followed by its LLM version", func(t *testing.T) {
		want := []string{Basic1hID, BasicLong1hID, Basic4hID, BasicLong4hID,
			BasicAndLLM1hID, BasicAndLLMLong1hID, BasicAndLLM4hID, BasicAndLLMLong4hID,
			LLMOnlyID, TrendID, TrendAndLLMID,
			DonchianID, DonchianAndLLMID, RSIDipID, RSIDipAndLLMID, MACDID, MACDAndLLMID,
			SqueezeID, SqueezeAndLLMID, RotationID, RotationAndLLMID}
		if len(list) != len(want) {
			t.Fatalf("got %d strategies", len(list))
		}
		for i, id := range want {
			if list[i].ID() != id {
				t.Fatalf("position %d: got %s want %s", i, list[i].ID(), id)
			}
		}
	})

	t.Run("GIVEN a canonical or legacy id WHEN looked up THEN the matching strategy is returned", func(t *testing.T) {
		cases := map[string]string{
			"basic_strategy_1h":              Basic1hID,
			"basic_strategy_long_4h":         BasicLong4hID,
			"basic_and_llm_strategy_long_1h": BasicAndLLMLong1hID,
			"llm_only_strategy":              LLMOnlyID,
			"trend_strategy":                 TrendID,
			"basic_strategy":                 Basic1hID, // legacy: the tick-based basic strategy
			"basic_and_llm_strategy":         BasicAndLLM1hID,
			"basic_trend":                    Basic1hID,
			"llm_only":                       LLMOnlyID,
		}
		for id, want := range cases {
			got := ByID(list, id)
			if got == nil || got.ID() != want {
				t.Fatalf("ByID(%q) = %v, want %s", id, got, want)
			}
		}
	})

	t.Run("GIVEN every 4-hour strategy WHEN its LLM version is looked up THEN it wraps that strategy and uses the LLM, while the base does not", func(t *testing.T) {
		pairs := map[string]string{TrendAndLLMID: TrendID, DonchianAndLLMID: DonchianID, RSIDipAndLLMID: RSIDipID,
			MACDAndLLMID: MACDID, SqueezeAndLLMID: SqueezeID, RotationAndLLMID: RotationID}
		for llmID, baseID := range pairs {
			v, ok := ByID(list, llmID).(*LLMVersion)
			if !ok || v.Base().ID() != baseID || !v.UsesLLM() {
				t.Fatalf("%s: %v", llmID, ByID(list, llmID))
			}
			if base := ByID(list, baseID); base == nil || base.UsesLLM() {
				t.Fatalf("%s must exist and not use the LLM", baseID)
			}
		}
	})

	t.Run("GIVEN an unknown id WHEN looked up THEN nil is returned", func(t *testing.T) {
		if ByID(list, "nope") != nil {
			t.Fatalf("expected nil")
		}
	})

	t.Run("GIVEN the lookback constant WHEN inspected THEN it covers the MACD minimum bar count", func(t *testing.T) {
		if LookbackTicks < 34 {
			t.Fatalf("LookbackTicks = %d", LookbackTicks)
		}
	})

	t.Run("GIVEN each strategy WHEN its LLM context requirement is checked THEN only LLM strategies need it", func(t *testing.T) {
		if ByID(list, Basic1hID).UsesLLM() || ByID(list, Basic4hID).UsesLLM() || ByID(list, TrendID).UsesLLM() {
			t.Fatalf("basic and trend must not use LLM")
		}
		if !ByID(list, BasicAndLLM1hID).UsesLLM() || !ByID(list, BasicAndLLM4hID).UsesLLM() || !ByID(list, LLMOnlyID).UsesLLM() {
			t.Fatalf("LLM strategies must report UsesLLM")
		}
	})

	t.Run("GIVEN LLM strategies WHEN price history requirements are read THEN the candle-based combined strategies need no ticks and llm_only needs a short history", func(t *testing.T) {
		if got := ByID(list, BasicAndLLM1hID).RequiredPriceTicks(); got != 0 {
			t.Fatalf("basic_and_llm_strategy_1h ticks = %d", got)
		}
		llmOnly := ByID(list, LLMOnlyID).RequiredPriceTicks()
		if llmOnly <= 0 || llmOnly >= LookbackTicks+1 {
			t.Fatalf("llm_only ticks = %d", llmOnly)
		}
	})

	// Warm-up gate as applied by the strategy loop and the replay: a strategy
	// without LLM waits for the price ticks it needs; LLM strategies are
	// gated by their own data readiness.
	warmupBlocks := func(s Strategy, tick int) bool {
		if s == nil || s.UsesLLM() {
			return false
		}
		return tick < s.RequiredPriceTicks()
	}

	t.Run("GIVEN the LLM-only strategy WHEN the warm-up check runs early THEN it is not blocked", func(t *testing.T) {
		if warmupBlocks(ByID(list, LLMOnlyID), 5) {
			t.Fatalf("LLM-only should bypass the tick warm-up")
		}
	})

	t.Run("GIVEN every strategy without LLM WHEN the warm-up check runs on the first tick THEN none is blocked: they all read candles", func(t *testing.T) {
		for _, s := range list {
			if !s.UsesLLM() && warmupBlocks(s, 0) {
				t.Fatalf("%s waits for price ticks", s.ID())
			}
		}
	})

	t.Run("GIVEN no strategy selected WHEN the warm-up check runs THEN it does not block", func(t *testing.T) {
		if warmupBlocks(ByID(list, "missing"), 0) {
			t.Fatalf("a missing strategy should not hold the loop in warm-up")
		}
	})
}
