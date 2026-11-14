package strategy

import "testing"

// TestStrategyRegistry covers All, ByID and the warm-up requirements the server relies on.
func TestStrategyRegistry(t *testing.T) {
	list := All(testSettings())

	t.Run("GIVEN the registry WHEN all strategies are listed THEN they come in basic, basic_and_llm, llm_only order", func(t *testing.T) {
		want := []string{BasicID, BasicAndLLMID, LLMOnlyID}
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
			"basic_strategy":         BasicID,
			"basic_and_llm_strategy": BasicAndLLMID,
			"llm_only_strategy":      LLMOnlyID,
			"basic_trend":            BasicID,
			"llm_only":               LLMOnlyID,
		}
		for id, want := range cases {
			got := ByID(list, id)
			if got == nil || got.ID() != want {
				t.Fatalf("ByID(%q) = %v, want %s", id, got, want)
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
		if ByID(list, BasicID).UsesLLM() {
			t.Fatalf("basic must not use LLM")
		}
		if !ByID(list, BasicAndLLMID).UsesLLM() || !ByID(list, LLMOnlyID).UsesLLM() {
			t.Fatalf("LLM strategies must report UsesLLM")
		}
	})

	t.Run("GIVEN LLM strategies WHEN price history requirements are read THEN indicator-plus-LLM waits for the full lookback", func(t *testing.T) {
		if got := ByID(list, BasicAndLLMID).RequiredPriceTicks(); got != LookbackTicks+1 {
			t.Fatalf("basic_and_llm ticks = %d", got)
		}
		llmOnly := ByID(list, LLMOnlyID).RequiredPriceTicks()
		if llmOnly <= 0 || llmOnly >= LookbackTicks+1 {
			t.Fatalf("llm_only ticks = %d", llmOnly)
		}
	})

	// Warm-up gate as applied by the strategy loop: non-LLM strategies wait for
	// indicator warm-up, LLM strategies are gated by their own data readiness.
	warmupBlocks := func(s Strategy, tick, warmupTicks int) bool {
		usesLLM := s != nil && s.UsesLLM()
		return !usesLLM && tick <= warmupTicks
	}

	t.Run("GIVEN the LLM-only strategy WHEN the warm-up check runs early THEN it is not blocked", func(t *testing.T) {
		if warmupBlocks(ByID(list, LLMOnlyID), 5, 20) {
			t.Fatalf("LLM-only should bypass indicator warm-up")
		}
	})

	t.Run("GIVEN the basic strategy WHEN the warm-up check runs early THEN it is blocked", func(t *testing.T) {
		if !warmupBlocks(ByID(list, BasicID), 5, 20) {
			t.Fatalf("basic should wait for warm-up")
		}
	})

	t.Run("GIVEN the basic strategy past warm-up WHEN checked THEN it is not blocked", func(t *testing.T) {
		if warmupBlocks(ByID(list, BasicID), 25, 20) {
			t.Fatalf("basic should run after warm-up")
		}
	})

	t.Run("GIVEN no strategy selected WHEN the warm-up check runs THEN it is treated as non-LLM", func(t *testing.T) {
		if !warmupBlocks(ByID(list, "missing"), 5, 20) {
			t.Fatalf("nil strategy should be treated as non-LLM")
		}
	})
}
