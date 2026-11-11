// Package kraken wraps the official Kraken Go SDK (github.com/krakenfx/api-go/v2)
// for market data, live order execution and retry/backoff handling.
package kraken

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"
)

// rateLimitMarkers are lower-case substrings that identify transient Kraken errors.
var rateLimitMarkers = []string{
	"rate limit",
	"temporary lockout",
	"service unavailable",
	"too many requests",
}

const (
	defaultMaxAttempts  = 3
	defaultInitialDelay = 500 * time.Millisecond
)

// SleepFunc waits for d or until ctx is done, returning ctx.Err() if cancelled.
type SleepFunc func(ctx context.Context, d time.Duration) error

// JitterFunc returns extra delay to add to a backoff delay.
type JitterFunc func(delay time.Duration) time.Duration

// BackoffOptions configures CallWithBackoff; zero values select the defaults.
type BackoffOptions struct {
	MaxAttempts  int           // total attempts including the first (default 3)
	InitialDelay time.Duration // delay before the second attempt (default 500ms)
	Sleep        SleepFunc     // default: context-aware timer sleep
	Jitter       JitterFunc    // default: DefaultJitter
	Logger       *slog.Logger  // optional warning logger
	Operation    string        // name used in log records
}

// DefaultJitter returns random jitter of up to ten percent of delay.
func DefaultJitter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(delay)/10 + 1))
}

// ContextSleep waits for d unless ctx is cancelled first.
func ContextSleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// IsRetryableMessage reports whether an error string looks like a Kraken rate limit.
func IsRetryableMessage(msg string) bool {
	text := strings.ToLower(msg)
	for _, marker := range rateLimitMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// IsRetryableError reports whether err looks like a transient Kraken rate limit.
func IsRetryableError(err error) bool {
	return err != nil && IsRetryableMessage(err.Error())
}

// CallWithBackoff runs op, retrying with exponential backoff and jitter while it
// fails with a rate-limit style error. Other errors are returned immediately.
// The SDK reports Kraken's in-body "error" array as a Go error, so both raised
// and in-response rate limits arrive here as err.
func CallWithBackoff[T any](ctx context.Context, opts BackoffOptions, op func(context.Context) (T, error)) (T, error) {
	maxAttempts := opts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}
	delay := opts.InitialDelay
	if delay <= 0 {
		delay = defaultInitialDelay
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = ContextSleep
	}
	jitter := opts.Jitter
	if jitter == nil {
		jitter = DefaultJitter
	}
	operation := opts.Operation
	if operation == "" {
		operation = "Kraken API call"
	}

	for attempt := 1; ; attempt++ {
		result, err := op(ctx)
		if err == nil || attempt >= maxAttempts || !IsRetryableError(err) {
			return result, err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			var zero T
			return zero, ctxErr
		}

		sleepFor := delay + max(0, jitter(delay))
		if opts.Logger != nil {
			opts.Logger.Warn("Kraken call rate-limited, backing off",
				"operation", operation,
				"attempt", attempt,
				"delay_seconds", sleepFor.Seconds(),
				"error", err.Error(),
			)
		}
		if sleepErr := sleep(ctx, sleepFor); sleepErr != nil {
			var zero T
			return zero, sleepErr
		}
		delay *= 2
	}
}
