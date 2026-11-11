package llm

import (
	"testing"
	"time"
)

// fakeClock is a controllable clock for circuit breaker tests.
type fakeClock struct {
	now time.Time
}

// newFakeClock returns a fake clock fixed at a known instant.
func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)}
}

// Now returns the current fake time.
func (c *fakeClock) Now() time.Time {
	return c.now
}

// Advance moves the fake time forward without sleeping.
func (c *fakeClock) Advance(seconds int) {
	c.now = c.now.Add(time.Duration(seconds) * time.Second)
}

// TestLoadsModelJSON covers tolerant parsing of model JSON output.
func TestLoadsModelJSON(t *testing.T) {
	t.Run("GIVEN fenced JSON with a missing comma WHEN parsed THEN the response is repaired", func(t *testing.T) {
		content := "```json\n{\n  \"action\": \"hold\"\n  \"confidence\": 0.0\n}\n```"

		result, err := LoadsModelJSON(content)

		if err != nil {
			t.Fatalf("expected repaired JSON, got error %v", err)
		}
		if result["action"] != "hold" {
			t.Errorf("action = %v, want hold", result["action"])
		}
		if result["confidence"] != 0.0 {
			t.Errorf("confidence = %v, want 0", result["confidence"])
		}
	})

	t.Run("GIVEN prose around JSON WHEN parsed THEN the first object is extracted", func(t *testing.T) {
		result, err := LoadsModelJSON(`Result follows: {"sentiment": 0.4} done.`)

		if err != nil {
			t.Fatalf("expected extracted JSON, got error %v", err)
		}
		if result["sentiment"] != 0.4 {
			t.Errorf("sentiment = %v, want 0.4", result["sentiment"])
		}
	})

	t.Run("GIVEN plain prose WHEN parsed THEN an error is returned", func(t *testing.T) {
		_, err := LoadsModelJSON("I cannot answer that in JSON")

		if err == nil {
			t.Fatal("expected an error for non-JSON content")
		}
	})

	t.Run("GIVEN a JSON array WHEN parsed THEN it is rejected as not an object", func(t *testing.T) {
		_, err := LoadsModelJSON(`[1, 2, 3]`)

		if err == nil {
			t.Fatal("expected an error for a non-object JSON value")
		}
	})
}

// TestMessagesToPrompt covers rendering chat messages as a transcript.
func TestMessagesToPrompt(t *testing.T) {
	t.Run("GIVEN OpenAI-style messages WHEN rendered THEN roles are labelled", func(t *testing.T) {
		prompt := MessagesToPrompt([]Message{
			{Role: "system", Content: "Return JSON."},
			{Role: "user", Content: "Analyse BTC/EUR."},
		}, true)

		want := "System: Return JSON.\nUser: Analyse BTC/EUR.\nRespond with valid JSON only."
		if prompt != want {
			t.Errorf("prompt = %q, want %q", prompt, want)
		}
	})

	t.Run("GIVEN an unknown role WHEN rendered THEN the role is capitalised", func(t *testing.T) {
		prompt := MessagesToPrompt([]Message{{Role: "tool", Content: "x"}}, false)

		if prompt != "Tool: x" {
			t.Errorf("prompt = %q, want %q", prompt, "Tool: x")
		}
	})
}

// TestCircuitBreaker covers the shared exponential-backoff breaker.
func TestCircuitBreaker(t *testing.T) {
	t.Run("GIVEN a failure WHEN the cooldown is active THEN attempts are blocked until half-open", func(t *testing.T) {
		clock := newFakeClock()
		var logged []string
		cb := &CircuitBreaker{}
		cb.initCircuitBreaker(clock.Now, func(reason string, _ time.Duration) { logged = append(logged, reason) })

		cb.markFailed("timeout")

		if cb.CircuitState() != StateOpen {
			t.Fatalf("state = %s, want open", cb.CircuitState())
		}
		if cb.CanAttempt() || cb.shouldAttempt() {
			t.Fatal("expected attempts to be blocked during cooldown")
		}

		clock.Advance(30)

		if !cb.CanAttempt() || !cb.shouldAttempt() {
			t.Fatal("expected an attempt to be allowed after the retry delay")
		}
		if cb.CircuitState() != StateHalfOpen {
			t.Errorf("state = %s, want half_open", cb.CircuitState())
		}
		if len(logged) != 1 || logged[0] != "timeout" {
			t.Errorf("logged failures = %v, want [timeout]", logged)
		}
	})

	t.Run("GIVEN repeated failures WHEN recorded THEN the retry delay grows and caps", func(t *testing.T) {
		cb := &CircuitBreaker{}
		cb.initCircuitBreaker(newFakeClock().Now, nil)

		var delays []int
		for range 6 {
			cb.markFailed("timeout")
			delays = append(delays, cb.RetryDelaySeconds())
		}

		want := []int{30, 60, 120, 240, 300, 300}
		for i := range want {
			if delays[i] != want[i] {
				t.Fatalf("delays = %v, want %v", delays, want)
			}
		}
	})

	t.Run("GIVEN an open circuit WHEN success is marked THEN the circuit closes and is available", func(t *testing.T) {
		cb := &CircuitBreaker{}
		cb.initCircuitBreaker(newFakeClock().Now, nil)

		cb.markFailed("timeout")
		cb.markSuccess()

		if cb.CircuitState() != StateClosed || !cb.Available() || !cb.CanAttempt() {
			t.Errorf("state=%s available=%v canAttempt=%v, want closed/true/true",
				cb.CircuitState(), cb.Available(), cb.CanAttempt())
		}
		if cb.RetryDelaySeconds() != 30 {
			t.Errorf("retry delay = %d, want reset to 30", cb.RetryDelaySeconds())
		}
	})

	t.Run("GIVEN a new breaker WHEN inspected THEN it is closed but not yet available", func(t *testing.T) {
		cb := &CircuitBreaker{}
		cb.initCircuitBreaker(nil, nil)

		if cb.Available() {
			t.Error("a fresh breaker must not report available before a success")
		}
		if !cb.CanAttempt() {
			t.Error("a fresh (closed) breaker must allow attempts")
		}
	})
}
