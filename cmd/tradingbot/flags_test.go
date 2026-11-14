package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStartupFlags covers the four settings needed before the database opens.
func TestStartupFlags(t *testing.T) {
	t.Run("GIVEN no flags WHEN parsed THEN today's defaults apply so launch.bat needs no arguments", func(t *testing.T) {
		boot, frontend, err := parseFlags(nil, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if boot.DatabasePath != "./trading_bot.db" || boot.Host != "127.0.0.1" || boot.Port != 8000 || boot.LogFile != "trading_bot.log" || frontend != "frontend" {
			t.Fatalf("unexpected defaults %+v %q", boot, frontend)
		}
	})

	t.Run("GIVEN flags WHEN parsed THEN they set the database, address and log file", func(t *testing.T) {
		boot, _, err := parseFlags([]string{"-db", "data/bot.db", "-host", "0.0.0.0", "-port", "9000", "-log", "logs/bot.log"}, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if boot.DatabasePath != "data/bot.db" || boot.Host != "0.0.0.0" || boot.Port != 9000 || boot.LogFile != "logs/bot.log" {
			t.Fatalf("flags not applied: %+v", boot)
		}
	})

	t.Run("GIVEN the removed -env flag or a bad port WHEN parsed THEN an error is returned", func(t *testing.T) {
		for _, args := range [][]string{{"-env", ".env"}, {"-port", "0"}, {"-port", "70000"}} {
			if _, _, err := parseFlags(args, io.Discard); err == nil {
				t.Errorf("%v: expected an error", args)
			}
		}
	})
}

// TestScriptsDoNotUseEnvFile covers removing .env from the setup and launch scripts.
func TestScriptsDoNotUseEnvFile(t *testing.T) {
	t.Run("GIVEN the Windows scripts WHEN read THEN none creates or reads a .env file", func(t *testing.T) {
		scripts, _ := filepath.Glob(filepath.Join(repoRoot, "*.bat"))
		for _, path := range scripts {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), ".env") {
				t.Errorf("%s still mentions .env", filepath.Base(path))
			}
		}
	})

	t.Run("GIVEN the repository WHEN listed THEN the .env.example template is gone", func(t *testing.T) {
		if _, err := os.Stat(filepath.Join(repoRoot, ".env.example")); !os.IsNotExist(err) {
			t.Fatalf("expected .env.example to be removed, stat error: %v", err)
		}
	})
}
