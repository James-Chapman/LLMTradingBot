package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/secret"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
)

// TestSetupAccess covers the access token printed at start-up.
func TestSetupAccess(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	open := func(t *testing.T) *storage.Repository {
		repo, err := storage.Open(filepath.Join(t.TempDir(), "bot.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = repo.Close() })
		return repo
	}

	t.Run("GIVEN a loopback host WHEN the bot starts THEN no token is used or printed", func(t *testing.T) {
		var out bytes.Buffer
		settings := config.Defaults()

		access, err := setupAccess(ctx, settings, open(t), secret.Reversible(), &out, logger)

		if err != nil || access != nil || out.Len() != 0 {
			t.Fatalf("expected no token, got %v %v %q", access, err, out.String())
		}
	})

	t.Run("GIVEN host 0.0.0.0 WHEN the bot starts twice THEN the same token is printed in a marked line each time", func(t *testing.T) {
		repo := open(t)
		settings := config.Defaults()
		settings.Host = "0.0.0.0"
		var first, second bytes.Buffer

		a1, err1 := setupAccess(ctx, settings, repo, secret.Reversible(), &first, logger)
		a2, err2 := setupAccess(ctx, settings, repo, secret.Reversible(), &second, logger)

		if err1 != nil || err2 != nil || a1 == nil || a2 == nil {
			t.Fatalf("expected a token, got %v %v", err1, err2)
		}
		if a1.Token() != a2.Token() {
			t.Fatal("the token must survive a restart")
		}
		for _, out := range []string{first.String(), second.String()} {
			if !strings.Contains(out, "DASHBOARD ACCESS TOKEN") || !strings.Contains(out, a1.Token()) {
				t.Errorf("expected the marked token line, got %q", out)
			}
		}
	})
}

// TestWaitFor covers the bounded wait for the background loops at shutdown.
func TestWaitFor(t *testing.T) {
	t.Run("GIVEN loops that never stop WHEN shutdown waits THEN it gives up after the timeout", func(t *testing.T) {
		if waitFor(make(chan struct{}), 10*time.Millisecond) {
			t.Fatal("expected a timeout")
		}
	})

	t.Run("GIVEN loops that have stopped WHEN shutdown waits THEN it returns at once", func(t *testing.T) {
		done := make(chan struct{})
		close(done)
		if !waitFor(done, time.Hour) {
			t.Fatal("expected done")
		}
	})
}

// TestLaunchScriptQuoting covers starting the bot from a folder whose path has spaces.
func TestLaunchScriptQuoting(t *testing.T) {
	t.Run("GIVEN launch.bat WHEN it starts the server window THEN the folder and binary are quoted with start /D, not nested cmd quotes", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(repoRoot, "launch.bat"))
		if err != nil {
			t.Fatal(err)
		}
		script := string(data)
		if strings.Contains(script, `""%ROOT%""`) || strings.Contains(script, `cd /d ""`) {
			t.Error("launch.bat still nests doubled quotes inside cmd /k, which breaks on paths with spaces")
		}
		if !strings.Contains(script, `/D "%ROOT_DIR%"`) || !strings.Contains(script, `cmd /k ""%BIN%""`) {
			t.Error(`expected start "..." /D "%ROOT_DIR%" cmd /k ""%BIN%""`)
		}
	})
}
