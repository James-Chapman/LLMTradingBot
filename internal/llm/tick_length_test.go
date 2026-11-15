package llm

import (
	"context"
	"strings"
	"testing"
)

// TestSignalPromptTickLength covers telling the LLM how long one indicator tick is.
func TestSignalPromptTickLength(t *testing.T) {
	ctx := context.Background()
	reply := map[string]any{"sentiment": 0.1, "confidence_scale": 1.0, "reasoning": "ok"}

	t.Run("GIVEN a 120-second strategy interval WHEN a signal is analysed THEN the prompt names 120s ticks", func(t *testing.T) {
		client := newFakeLLMClient(reply)
		req := signalRequest()
		req.TickSeconds = 120

		NewAnalyser(client, "€").AnalyseSignal(ctx, req)

		prompt := client.userPrompt(t)
		if !strings.Contains(prompt, "Technical indicators (120s ticks)") || strings.Contains(prompt, "30s ticks") {
			t.Fatalf("prompt should state 120s ticks:\n%s", prompt)
		}
	})

	t.Run("GIVEN no tick length WHEN a signal is analysed THEN the prompt falls back to 30s ticks", func(t *testing.T) {
		client := newFakeLLMClient(reply)

		NewAnalyser(client, "€").AnalyseSignal(ctx, signalRequest())

		if prompt := client.userPrompt(t); !strings.Contains(prompt, "Technical indicators (30s ticks)") {
			t.Fatalf("prompt should state 30s ticks:\n%s", prompt)
		}
	})
}
