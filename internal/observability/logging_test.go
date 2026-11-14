package observability

import (
	"context"
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
