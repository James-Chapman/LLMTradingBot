package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

// TestSetLevel covers changing the log level after logging is set up.
func TestSetLevel(t *testing.T) {
	t.Cleanup(func() { SetLevel("INFO") })

	t.Run("GIVEN level DEBUG WHEN set THEN debug records are enabled", func(t *testing.T) {
		SetLevel("DEBUG")
		if !Logger("test").Enabled(context.Background(), slog.LevelDebug) {
			t.Fatal("debug records should be enabled")
		}
	})

	t.Run("GIVEN level WARNING WHEN set THEN info records are dropped and warnings kept", func(t *testing.T) {
		SetLevel("WARNING")
		log := Logger("test")
		if log.Enabled(context.Background(), slog.LevelInfo) || !log.Enabled(context.Background(), slog.LevelWarn) {
			t.Fatal("expected only warnings and above")
		}
	})
}

// TestLoggerGroups covers attributes and groups on component loggers.
func TestLoggerGroups(t *testing.T) {
	// capture routes the main handler into a buffer for one test.
	capture := func(t *testing.T) *bytes.Buffer {
		t.Helper()
		var buf bytes.Buffer
		mu.Lock()
		saved := mainH
		mainH = slog.NewJSONHandler(&buf, nil)
		mu.Unlock()
		t.Cleanup(func() {
			mu.Lock()
			mainH = saved
			mu.Unlock()
		})
		return &buf
	}
	decode := func(t *testing.T, buf *bytes.Buffer) map[string]any {
		t.Helper()
		var out map[string]any
		if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
			t.Fatalf("not JSON: %q", buf.String())
		}
		return out
	}

	t.Run("GIVEN two nested groups WHEN a record is logged THEN its attributes sit inside both", func(t *testing.T) {
		buf := capture(t)

		Logger("test").WithGroup("outer").WithGroup("inner").Info("hello", "k", 1)

		outer, _ := decode(t, buf)["outer"].(map[string]any)
		inner, _ := outer["inner"].(map[string]any)
		if inner["k"] != float64(1) {
			t.Fatalf("expected outer.inner.k, got %s", buf.String())
		}
	})

	t.Run("GIVEN an attribute added before a group WHEN a record is logged THEN it stays outside the group", func(t *testing.T) {
		buf := capture(t)

		Logger("test").With("before", "x").WithGroup("g").With("after", "y").Info("hello")

		rec := decode(t, buf)
		group, _ := rec["g"].(map[string]any)
		if rec["before"] != "x" || rec["logger"] != "trading_bot.test" || group["after"] != "y" {
			t.Fatalf("expected before and logger at the top and after inside g, got %s", buf.String())
		}
	})
}
