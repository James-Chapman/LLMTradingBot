package config

import (
	"os"
	"path/filepath"
	"testing"
)

// mapLookup adapts a map to the lookup signature used by FromLookup.
func mapLookup(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

// TestSettings covers defaults, parsing, validation and .env precedence.
func TestSettings(t *testing.T) {
	t.Run("GIVEN no overrides WHEN settings load THEN safe paper/manual defaults apply", func(t *testing.T) {
		s, err := FromLookup(mapLookup(nil))
		if err != nil {
			t.Fatal(err)
		}
		if s.TradingEnvironment != "paper" || s.TradingMode != "manual" {
			t.Fatalf("expected paper/manual defaults, got %s/%s", s.TradingEnvironment, s.TradingMode)
		}
		if s.LMStudioBaseURL == "" {
			t.Fatal("expected a default LM Studio URL")
		}
	})

	t.Run("GIVEN JSON list values WHEN settings load THEN lists are parsed", func(t *testing.T) {
		s, err := FromLookup(mapLookup(map[string]string{"FIXED_MARKETS": `["SOL/EUR","ADA/EUR","DOT/EUR"]`}))
		if err != nil {
			t.Fatal(err)
		}
		if len(s.FixedMarkets) != 3 || s.FixedMarkets[0] != "SOL/EUR" {
			t.Fatalf("unexpected markets %v", s.FixedMarkets)
		}
	})

	t.Run("GIVEN an invalid trading mode WHEN settings load THEN an error is returned", func(t *testing.T) {
		if _, err := FromLookup(mapLookup(map[string]string{"TRADING_MODE": "yolo"})); err == nil {
			t.Fatal("expected validation error")
		}
	})

	t.Run("GIVEN a non-numeric float WHEN settings load THEN an error is returned", func(t *testing.T) {
		if _, err := FromLookup(mapLookup(map[string]string{"STOP_LOSS_PCT": "lots"})); err == nil {
			t.Fatal("expected parse error")
		}
	})

	t.Run("GIVEN a legacy SQLAlchemy DATABASE_URL WHEN settings load THEN it becomes a file path", func(t *testing.T) {
		s, _ := FromLookup(mapLookup(map[string]string{"DATABASE_URL": "sqlite:///./legacy.db"}))
		if s.DatabasePath != "./legacy.db" {
			t.Fatalf("got %q", s.DatabasePath)
		}
	})

	t.Run("GIVEN a .env file and an env var for the same key WHEN loading THEN the env var wins", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".env")
		content := "# comment\nPORT=9001\nBASE_CURRENCY=\"GBP\"\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PORT", "9002")
		s, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if s.Port != 9002 {
			t.Fatalf("env var should override file, got %d", s.Port)
		}
		if s.BaseCurrency != "GBP" {
			t.Fatalf("file value should apply with quotes stripped, got %q", s.BaseCurrency)
		}
	})

	t.Run("GIVEN a missing .env file WHEN loading THEN defaults are used without error", func(t *testing.T) {
		if _, err := Load(filepath.Join(t.TempDir(), "absent.env")); err != nil {
			t.Fatal(err)
		}
	})
}

// TestCurrencySymbol covers mapping ISO codes to display symbols.
func TestCurrencySymbol(t *testing.T) {
	t.Run("GIVEN a known code WHEN formatting THEN its symbol is returned", func(t *testing.T) {
		if CurrencySymbol("eur") != "€" {
			t.Fatal("expected euro sign")
		}
	})
	t.Run("GIVEN an unknown code WHEN formatting THEN the upper-cased code is returned", func(t *testing.T) {
		if CurrencySymbol("chf") != "CHF" {
			t.Fatal("expected CHF")
		}
	})
}
