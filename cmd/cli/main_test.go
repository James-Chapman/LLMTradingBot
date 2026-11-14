package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/app"
	"github.com/James-Chapman/LLMTradingBot/internal/secret"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
)

// addrFlags returns the -host/-port/-log flags pointing the CLI at srv.
func addrFlags(t *testing.T, srv *httptest.Server) []string {
	t.Helper()
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	return []string{"-host", host, "-port", port, "-log", filepath.Join(t.TempDir(), "cli.log")}
}

// TestCLI covers the status, approve and reject commands against a running bot.
func TestCLI(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		switch {
		case r.URL.Path == "/api/dashboard":
			_, _ = w.Write([]byte(`{"version":"0.8.0","mode":"semi_automated","environment":"paper","currency_symbol":"€","equity":"512.34","cash":"400.00"}`))
		case strings.Contains(r.URL.Path, "missing"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"Approval not found"}`))
		default:
			_, _ = w.Write([]byte(`{"status":"approved","id":"abc"}`))
		}
	}))
	defer srv.Close()
	flags := addrFlags(t, srv)

	t.Run("GIVEN a running bot WHEN status runs THEN the bot's live mode, environment and equity are shown", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := run(append(slices.Clone(flags), "status"), &out, &errOut)
		for _, want := range []string{"Trading Bot Status", "Version: 0.8.0", "Mode: semi_automated", "Environment: paper", "Equity: €512.34"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("status output missing %q (exit %d): %s %s", want, code, out.String(), errOut.String())
			}
		}
		if code != 0 || gotPath != "/api/dashboard" {
			t.Errorf("expected exit 0 after GET /api/dashboard, got %d %s", code, gotPath)
		}
	})

	t.Run("GIVEN a running bot WHEN approve runs THEN it POSTs the approval and prints the result", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := run(append(slices.Clone(flags), "approve", "abc"), &out, &errOut)
		if code != 0 || gotMethod != http.MethodPost || gotPath != "/api/approvals/abc/approve" || !strings.Contains(out.String(), "Approved:") {
			t.Errorf("unexpected approve (%d) %s %s: %s", code, gotMethod, gotPath, out.String())
		}
	})

	t.Run("GIVEN an unknown approval WHEN reject runs THEN the API error is reported with exit code 1", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := run(append(slices.Clone(flags), "reject", "missing"), &out, &errOut)
		if code != 1 || !strings.Contains(errOut.String(), "API error 404") || gotPath != "/api/approvals/missing/reject" {
			t.Errorf("unexpected reject (%d): %s", code, errOut.String())
		}
	})

	t.Run("GIVEN the removed .env and mode flags WHEN parsed THEN usage exit code 2 is returned", func(t *testing.T) {
		for _, args := range [][]string{{"--config", ".env", "status"}, {"--mode", "manual", "status"}, {"--environment", "paper", "status"}} {
			var out, errOut bytes.Buffer
			if code := run(args, &out, &errOut); code != 2 {
				t.Errorf("%v: expected exit 2, got %d", args, code)
			}
		}
	})

	t.Run("GIVEN approve without an ID WHEN parsed THEN usage exit code 2 is returned", func(t *testing.T) {
		var out, errOut bytes.Buffer
		if code := run(append(slices.Clone(flags), "approve"), &out, &errOut); code != 2 {
			t.Errorf("expected exit 2, got %d", code)
		}
	})

	t.Run("GIVEN no bot listening WHEN status or approve runs THEN a cannot-reach message exits 1", func(t *testing.T) {
		dead := httptest.NewServer(http.NotFoundHandler())
		deadFlags := addrFlags(t, dead)
		dead.Close()
		for _, command := range [][]string{{"status"}, {"approve", "x"}} {
			var out, errOut bytes.Buffer
			if code := run(append(slices.Clone(deadFlags), command...), &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "Cannot reach bot at") {
				t.Errorf("%v: unexpected (%d): %s", command, code, errOut.String())
			}
		}
	})

	t.Run("GIVEN start-up flags WHEN run starts the bot THEN they are passed through to tradingbot", func(t *testing.T) {
		args := botArgs("data/bot.db", "0.0.0.0", 9000, "logs/bot.log")
		want := []string{"-db", "data/bot.db", "-host", "0.0.0.0", "-port", "9000", "-log", "logs/bot.log"}
		if !slices.Equal(args, want) {
			t.Fatalf("got %v, want %v", args, want)
		}
	})
}

// TestCLIArguments covers unknown commands, flags after the command and the access token.
func TestCLIArguments(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"version":"0.9.3","mode":"manual","environment":"paper","currency_symbol":"€","equity":"1.00","cash":"1.00"}`))
	}))
	defer srv.Close()
	flags := addrFlags(t, srv)
	noDB := []string{"-db", filepath.Join(t.TempDir(), "missing.db")}

	t.Run("GIVEN an unknown or missing command WHEN run THEN usage is shown with exit code 2", func(t *testing.T) {
		for _, args := range [][]string{{"stauts"}, {}} {
			var out, errOut bytes.Buffer
			if code := runWith(append(slices.Clone(noDB), args...), &out, &errOut, secret.Reversible()); code != 2 || !strings.Contains(errOut.String(), "Usage") {
				t.Errorf("%v: expected usage and exit 2, got %d %s", args, code, errOut.String())
			}
		}
	})

	t.Run("GIVEN flags after the command WHEN run THEN they are used", func(t *testing.T) {
		var out, errOut bytes.Buffer
		args := append(append([]string{"status"}, flags...), noDB...)

		code := runWith(args, &out, &errOut, secret.Reversible())

		if code != 0 || gotPath != "/api/dashboard" {
			t.Fatalf("expected the status call to reach the test bot, got %d %s %s", code, gotPath, errOut.String())
		}
	})

	t.Run("GIVEN flags between the command and its ID WHEN approve runs THEN both are used", func(t *testing.T) {
		var out, errOut bytes.Buffer
		args := append(append([]string{"approve"}, flags...), append(noDB, "abc")...)

		code := runWith(args, &out, &errOut, secret.Reversible())

		if code != 0 || gotPath != "/api/approvals/abc/approve" {
			t.Fatalf("unexpected approve (%d) %s: %s", code, gotPath, errOut.String())
		}
	})

	t.Run("GIVEN an access token stored in the database WHEN status runs THEN it is sent as a bearer token", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "bot.db")
		repo, err := storage.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		token, _, err := app.LoadAccessToken(context.Background(), repo, secret.Reversible())
		_ = repo.Close()
		if err != nil {
			t.Fatal(err)
		}
		var out, errOut bytes.Buffer

		code := runWith(append(slices.Clone(flags), "-db", dbPath, "status"), &out, &errOut, secret.Reversible())

		if code != 0 || gotAuth != "Bearer "+token.Token() {
			t.Fatalf("expected the stored token as a bearer token, got %d %q", code, gotAuth)
		}
	})

	t.Run("GIVEN no database file WHEN status runs THEN no token is sent and no database is created", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "none.db")
		var out, errOut bytes.Buffer

		code := runWith(append(slices.Clone(flags), "-db", missing, "status"), &out, &errOut, secret.Reversible())

		if code != 0 || gotAuth != "" {
			t.Fatalf("expected no Authorization header, got %d %q", code, gotAuth)
		}
		if _, err := os.Stat(missing); !os.IsNotExist(err) {
			t.Fatal("the CLI must not create a database")
		}
	})
}
