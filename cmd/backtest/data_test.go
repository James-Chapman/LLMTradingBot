package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runDataT runs the data subcommand and returns its exit code and output.
func runDataT(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := runData(context.Background(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// archiveDir writes a small trade archive directory and returns its path.
func archiveDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "Kraken_Trades_2026Q3")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	var b strings.Builder
	b.WriteString("timestamp,price,volume,type,order_type,misc,trade_id\n")
	for i := range 20 {
		ts := start.Add(time.Duration(i) * 7 * time.Second)
		fmt.Fprintf(&b, "%d.5,%d,0.5,%s,m,,%d\n", ts.Unix(), 50000+i*100, map[bool]string{true: "b", false: "s"}[i%2 == 0], i+1)
	}
	if err := os.WriteFile(filepath.Join(dir, "XBTEUR.csv"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "WHAT.csv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestDataCommand covers the `backtest data` subcommands.
func TestDataCommand(t *testing.T) {
	t.Run("GIVEN no subcommand WHEN run THEN usage is shown and the exit code is 2", func(t *testing.T) {
		code, _, errOut := runDataT(t)
		if code != 2 || !strings.Contains(errOut, "import") {
			t.Fatalf("code %d, stderr %q", code, errOut)
		}
	})

	t.Run("GIVEN an unknown subcommand WHEN run THEN the exit code is 2", func(t *testing.T) {
		if code, _, _ := runDataT(t, "frobnicate"); code != 2 {
			t.Fatalf("code %d", code)
		}
	})

	t.Run("GIVEN an archive WHEN imported, listed, verified and ranked THEN each step succeeds and reports the market", func(t *testing.T) {
		cache := t.TempDir()
		code, out, errOut := runDataT(t, "import", "--cache", cache, "--archive", archiveDir(t))
		if code != 0 || !strings.Contains(out, "BTC/EUR") || !strings.Contains(out, "WHAT.csv") {
			t.Fatalf("import: code %d\nstdout %s\nstderr %s", code, out, errOut)
		}
		code, out, _ = runDataT(t, "list", "--cache", cache)
		if code != 0 || !strings.Contains(out, "BTC/EUR") || !strings.Contains(out, "2026-07-09T12:00:00Z") {
			t.Fatalf("list: code %d\n%s", code, out)
		}
		code, out, _ = runDataT(t, "verify", "--cache", cache, "--json")
		var rep struct {
			Markets []struct{ Market string } `json:"markets"`
		}
		if code != 0 || json.Unmarshal([]byte(out), &rep) != nil || len(rep.Markets) != 1 {
			t.Fatalf("verify: code %d\n%s", code, out)
		}
		code, out, _ = runDataT(t, "universe", "--cache", cache, "--date", "2026-07-10", "--quote", "EUR", "--top", "5")
		if code != 0 || !strings.Contains(out, "BTC/EUR") {
			t.Fatalf("universe: code %d\n%s", code, out)
		}
	})

	t.Run("GIVEN a corrupted cache file WHEN verified THEN the exit code is 1", func(t *testing.T) {
		cache := t.TempDir()
		if code, _, errOut := runDataT(t, "import", "--cache", cache, "--archive", archiveDir(t)); code != 0 {
			t.Fatalf("import failed: %s", errOut)
		}
		p := filepath.Join(cache, "bars", "BTC-EUR", "2026-07.bars")
		data, _ := os.ReadFile(p)
		data[len(data)-1] ^= 0xff
		_ = os.WriteFile(p, data, 0o644)
		if code, out, _ := runDataT(t, "verify", "--cache", cache); code != 1 || !strings.Contains(out, "corrupt_file") {
			t.Fatalf("code %d\n%s", code, out)
		}
	})

	t.Run("GIVEN a malformed date WHEN a command runs THEN it fails with a usage error", func(t *testing.T) {
		code, _, errOut := runDataT(t, "universe", "--cache", t.TempDir(), "--date", "10/07/2026")
		if code != 2 || !strings.Contains(errOut, "YYYY-MM-DD") {
			t.Fatalf("code %d, stderr %q", code, errOut)
		}
	})

	t.Run("GIVEN a missing archive WHEN imported THEN the exit code is 1", func(t *testing.T) {
		if code, _, _ := runDataT(t, "import", "--cache", t.TempDir(), "--archive", filepath.Join(t.TempDir(), "nope")); code != 1 {
			t.Fatalf("code %d", code)
		}
	})

	t.Run("GIVEN a Trades server WHEN fetched THEN the trades are cached as bars", func(t *testing.T) {
		start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("pair") != "XBTEUR" {
				http.Error(w, "bad pair", http.StatusBadRequest)
				return
			}
			_, _ = fmt.Fprintf(w, `{"error":[],"result":{"XXBTZEUR":[["100.5","1.0",%d.25,"b","m","",1],["101","2",%d.75,"s","l","",2]],"last":"%d"}}`,
				start.Unix(), start.Unix()+40, start.Add(41*time.Second).UnixNano())
		}))
		defer srv.Close()
		dataRESTSleep = func(context.Context, time.Duration) error { return nil }
		defer func() { dataRESTSleep = nil }()
		cache := t.TempDir()
		code, out, errOut := runDataT(t, "fetch", "--cache", cache, "--markets", "BTC/EUR", "--from", "2026-10-01",
			"--to", "2026-10-01T00:01:00Z", "--base-url", srv.URL)
		if code != 0 {
			t.Fatalf("fetch: code %d\n%s\n%s", code, out, errOut)
		}
		code, out, _ = runDataT(t, "list", "--cache", cache)
		if code != 0 || !strings.Contains(out, "BTC/EUR") {
			t.Fatalf("list: %s", out)
		}
	})
}

// TestParseDataDate covers the accepted date forms.
func TestParseDataDate(t *testing.T) {
	for in, want := range map[string]time.Time{
		"2026-07-10":                time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC),
		"2026-07-10T12:30:00Z":      time.Date(2026, 7, 10, 12, 30, 0, 0, time.UTC),
		"2026-07-10T14:30:00+02:00": time.Date(2026, 7, 10, 12, 30, 0, 0, time.UTC),
	} {
		t.Run("GIVEN "+in+" WHEN parsed THEN it is the UTC instant", func(t *testing.T) {
			got, err := parseDataDate(in)
			if err != nil || !got.Equal(want) || got.Location() != time.UTC {
				t.Fatalf("parseDataDate(%q) = %v, %v", in, got, err)
			}
		})
	}
}
