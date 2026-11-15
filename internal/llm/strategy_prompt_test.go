package llm

import (
	"context"
	"strings"
	"testing"
)

// TestSignalPromptStrategyBlock covers the strategy-specific part of the
// signal prompt that each strategy's LLM version supplies.
func TestSignalPromptStrategyBlock(t *testing.T) {
	ctx := context.Background()
	reply := map[string]any{"sentiment": 0.2, "confidence_scale": 1.1, "reasoning": "ok"}

	t.Run("GIVEN a request with a strategy prompt WHEN a signal is analysed THEN the prompt carries the strategy's name, instructions and data", func(t *testing.T) {
		client := newFakeLLMClient(reply)
		req := signalRequest()
		req.Strategy = &StrategyPrompt{
			Name:         "Donchian breakout (4-hour)",
			Instructions: "Judge whether the breakout is likely to follow through.",
			Data:         map[string]string{"channel_high": "2,500.00", "atr_pct": "3.10%"},
		}

		got := NewAnalyser(client, "€").AnalyseSignal(ctx, req)

		prompt := client.userPrompt(t)
		for _, want := range []string{"Strategy: Donchian breakout (4-hour)", "Judge whether the breakout is likely to follow through.", "atr_pct: 3.10%", "channel_high: 2,500.00"} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("prompt lacks %q:\n%s", want, prompt)
			}
		}
		if strings.Index(prompt, "atr_pct") > strings.Index(prompt, "channel_high") {
			t.Fatalf("strategy data should be listed in key order:\n%s", prompt)
		}
		if !got.LLMUsed || got.ConfidenceScale != 1.1 {
			t.Fatalf("analysis %+v, want the reply's scale", got)
		}
	})

	t.Run("GIVEN a request without a strategy prompt WHEN a signal is analysed THEN the prompt has no strategy block", func(t *testing.T) {
		client := newFakeLLMClient(reply)

		NewAnalyser(client, "€").AnalyseSignal(ctx, signalRequest())

		if prompt := client.userPrompt(t); strings.Contains(prompt, "Strategy:") {
			t.Fatalf("generic prompt should not have a strategy block:\n%s", prompt)
		}
	})

	t.Run("GIVEN a strategy data value with a line break WHEN a signal is analysed THEN the value stays on its own line", func(t *testing.T) {
		client := newFakeLLMClient(reply)
		req := signalRequest()
		req.Strategy = &StrategyPrompt{Name: "X", Instructions: "line one\nline two", Data: map[string]string{"k": "a\nb"}}

		NewAnalyser(client, "€").AnalyseSignal(ctx, req)

		if prompt := client.userPrompt(t); !strings.Contains(prompt, "k: a b") {
			t.Fatalf("data values should be flattened to one line:\n%s", prompt)
		}
	})
}
