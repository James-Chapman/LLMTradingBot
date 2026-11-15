package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
)

// botHome creates a folder laid out like the repository: frontend/index.html
// and a bin folder for the executable.
func botHome(t *testing.T) (home, bin string) {
	t.Helper()
	home = t.TempDir()
	bin = filepath.Join(home, "bin")
	for _, dir := range []string{filepath.Join(home, "frontend"), bin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "frontend", "index.html"), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, bin
}

// defaultBoot returns the start-up settings parseFlags gives with no flags.
func defaultBoot() config.Bootstrap {
	d := config.Defaults()
	return config.Bootstrap{DatabasePath: d.DatabasePath, Host: d.Host, Port: d.Port, LogFile: d.LogFile}
}

// TestResolvePaths covers finding the dashboard files and the database
// whatever folder the bot is started from.
func TestResolvePaths(t *testing.T) {
	t.Run("GIVEN the bot is started from its own folder WHEN paths are resolved THEN that folder's dashboard and database are used", func(t *testing.T) {
		home, bin := botHome(t)

		boot, frontend, err := resolvePaths(defaultBoot(), defaultFrontendDir, []string{home, bin})

		if err != nil {
			t.Fatal(err)
		}
		if frontend != filepath.Join(home, "frontend") || boot.DatabasePath != filepath.Join(home, "trading_bot.db") || boot.LogFile != filepath.Join(home, "trading_bot.log") {
			t.Fatalf("unexpected paths: %q %+v", frontend, boot)
		}
	})

	t.Run("GIVEN the executable is double-clicked in bin WHEN paths are resolved THEN the dashboard and the database of the folder above are used", func(t *testing.T) {
		// Explorer starts a program in its own folder, where there is no
		// frontend folder and no database.
		home, bin := botHome(t)

		boot, frontend, err := resolvePaths(defaultBoot(), defaultFrontendDir, []string{bin, bin, home})

		if err != nil {
			t.Fatal(err)
		}
		if frontend != filepath.Join(home, "frontend") {
			t.Errorf("frontend %q, want the folder above bin", frontend)
		}
		if boot.DatabasePath != filepath.Join(home, "trading_bot.db") {
			t.Errorf("database %q: a new database must not be created beside the executable", boot.DatabasePath)
		}
		if boot.LogFile != filepath.Join(home, "trading_bot.log") {
			t.Errorf("log file %q, want it beside the database", boot.LogFile)
		}
	})

	t.Run("GIVEN no dashboard files anywhere WHEN paths are resolved THEN start-up is refused with the places searched", func(t *testing.T) {
		empty := t.TempDir()

		_, _, err := resolvePaths(defaultBoot(), defaultFrontendDir, []string{empty})

		if err == nil {
			t.Fatal("expected an error instead of a bot that serves no page")
		}
		if !strings.Contains(err.Error(), empty) || !strings.Contains(err.Error(), "-frontend") {
			t.Errorf("the error should name where it looked and the flag to use, got: %v", err)
		}
	})

	t.Run("GIVEN explicit -db, -log and -frontend flags WHEN paths are resolved THEN they are used as given", func(t *testing.T) {
		home, bin := botHome(t)
		boot := defaultBoot()
		boot.DatabasePath, boot.LogFile = "data/bot.db", "logs/bot.log"
		explicitFrontend := filepath.Join(home, "frontend")

		got, frontend, err := resolvePaths(boot, explicitFrontend, []string{bin})

		if err != nil {
			t.Fatal(err)
		}
		if frontend != explicitFrontend || got.DatabasePath != "data/bot.db" || got.LogFile != "logs/bot.log" {
			t.Fatalf("explicit flags must not be moved: %q %+v", frontend, got)
		}
	})

	t.Run("GIVEN a -frontend folder without index.html WHEN paths are resolved THEN start-up is refused", func(t *testing.T) {
		_, bin := botHome(t)

		if _, _, err := resolvePaths(defaultBoot(), bin, []string{bin}); err == nil {
			t.Fatal("expected an error for a frontend folder that holds no dashboard")
		}
	})
}
