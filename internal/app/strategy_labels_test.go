package app

import (
	"strings"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// TestStrategyLabels covers the dashboard label of every registered strategy.
func TestStrategyLabels(t *testing.T) {
	t.Run("GIVEN every registered strategy WHEN the dashboard lists it THEN it has a label that says whether it uses the LLM", func(t *testing.T) {
		for _, s := range strategy.All(config.Defaults()) {
			label, ok := StrategyLabels[s.ID()]
			if !ok || label == "" {
				t.Fatalf("%s has no dashboard label", s.ID())
			}
			if _, wrapped := s.(*strategy.LLMVersion); wrapped && !strings.Contains(label, "LLM") {
				t.Fatalf("%s label %q should say it uses the LLM", s.ID(), label)
			}
		}
	})
}
