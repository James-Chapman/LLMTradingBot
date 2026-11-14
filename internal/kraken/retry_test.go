package kraken

import (
	"context"
	"errors"
	"testing"
	"time"
)

// recordingSleep returns a SleepFunc that records requested delays without waiting.
func recordingSleep(sleeps *[]time.Duration) SleepFunc {
	return func(ctx context.Context, d time.Duration) error {
		*sleeps = append(*sleeps, d)
		return ctx.Err()
	}
}

// zeroJitter disables jitter so backoff delays are deterministic in tests.
func zeroJitter(time.Duration) time.Duration { return 0 }

// TestCallWithBackoff covers retry behaviour for Kraken rate-limit errors.
func TestCallWithBackoff(t *testing.T) {
	t.Run("GIVEN a rate-limited first call WHEN retried THEN the second result is returned after one backoff", func(t *testing.T) {
		var sleeps []time.Duration
		calls := 0
		got, err := CallWithBackoff(context.Background(), BackoffOptions{Sleep: recordingSleep(&sleeps), Jitter: zeroJitter},
			func(ctx context.Context) (string, error) {
				calls++
				if calls == 1 {
					return "", errors.New("EAPI:Rate limit exceeded")
				}
				return "ok", nil
			})

		if err != nil || got != "ok" {
			t.Fatalf("expected ok result, got %q err=%v", got, err)
		}
		if calls != 2 {
			t.Fatalf("expected 2 calls, got %d", calls)
		}
		if len(sleeps) != 1 || sleeps[0] != 500*time.Millisecond {
			t.Fatalf("expected one 500ms backoff, got %v", sleeps)
		}
	})

	t.Run("GIVEN a non-retryable error WHEN called THEN it is returned immediately without sleeping", func(t *testing.T) {
		var sleeps []time.Duration
		calls := 0
		_, err := CallWithBackoff(context.Background(), BackoffOptions{Sleep: recordingSleep(&sleeps), Jitter: zeroJitter},
			func(ctx context.Context) (int, error) {
				calls++
				return 0, errors.New("EOrder:Insufficient funds")
			})

		if err == nil {
			t.Fatal("expected error")
		}
		if calls != 1 || len(sleeps) != 0 {
			t.Fatalf("expected a single attempt and no sleep, got calls=%d sleeps=%v", calls, sleeps)
		}
	})

	t.Run("GIVEN persistent rate limiting WHEN attempts are exhausted THEN the last error is returned and delays double", func(t *testing.T) {
		var sleeps []time.Duration
		calls := 0
		_, err := CallWithBackoff(context.Background(), BackoffOptions{Sleep: recordingSleep(&sleeps), Jitter: zeroJitter},
			func(ctx context.Context) (int, error) {
				calls++
				return 0, errors.New("EGeneral:Too many requests")
			})

		if err == nil || !IsRetryableError(err) {
			t.Fatalf("expected retryable error to surface, got %v", err)
		}
		if calls != 3 {
			t.Fatalf("expected default of 3 attempts, got %d", calls)
		}
		if len(sleeps) != 2 || sleeps[1] != 2*sleeps[0] {
			t.Fatalf("expected two doubling backoffs, got %v", sleeps)
		}
	})

	t.Run("GIVEN positive jitter WHEN backing off THEN the jitter is added to the delay", func(t *testing.T) {
		var sleeps []time.Duration
		calls := 0
		_, _ = CallWithBackoff(context.Background(), BackoffOptions{
			MaxAttempts: 2,
			Sleep:       recordingSleep(&sleeps),
			Jitter:      func(d time.Duration) time.Duration { return d / 10 },
		}, func(ctx context.Context) (int, error) {
			calls++
			return 0, errors.New("rate limit")
		})

		if len(sleeps) != 1 || sleeps[0] <= 500*time.Millisecond {
			t.Fatalf("expected jittered delay above base, got %v", sleeps)
		}
	})

	t.Run("GIVEN a cancelled context WHEN backing off THEN the call stops with the context error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		_, err := CallWithBackoff(ctx, BackoffOptions{Jitter: zeroJitter}, func(ctx context.Context) (int, error) {
			calls++
			cancel()
			return 0, errors.New("rate limit exceeded")
		})

		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
		if calls != 1 {
			t.Fatalf("expected no retry after cancellation, got %d calls", calls)
		}
	})
}

// TestDefaultJitter checks that default jitter stays within ten percent of the delay.
func TestDefaultJitter(t *testing.T) {
	t.Run("GIVEN a delay WHEN default jitter is drawn THEN it is between zero and ten percent", func(t *testing.T) {
		for range 100 {
			j := DefaultJitter(time.Second)
			if j < 0 || j > 100*time.Millisecond {
				t.Fatalf("jitter out of range: %v", j)
			}
		}
	})
}

// TestIsRetryableMessage checks rate-limit marker detection.
func TestIsRetryableMessage(t *testing.T) {
	t.Run("GIVEN Kraken error strings WHEN classified THEN only rate-limit style errors are retryable", func(t *testing.T) {
		retryable := []string{"EAPI:Rate limit exceeded", "EService:Unavailable: Service Unavailable", "Temporary lockout", "429 Too Many Requests"}
		for _, msg := range retryable {
			if !IsRetryableMessage(msg) {
				t.Errorf("expected %q to be retryable", msg)
			}
		}
		if IsRetryableMessage("EOrder:Insufficient funds") {
			t.Error("insufficient funds must not be retryable")
		}
		if IsRetryableError(nil) {
			t.Error("nil error must not be retryable")
		}
	})
}
