package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// envFileFor writes a .env pointing the CLI at the given test server.
func envFileFor(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	path := filepath.Join(t.TempDir(), ".env")
	content := "HOST=" + host + "\nPORT=" + port + "\nLOG_FILE=" + filepath.Join(t.TempDir(), "cli.log") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestCLI covers the status, approve and reject commands.
func TestCLI(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		if strings.Contains(r.URL.Path, "missing") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"Approval not found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"approved","id":"abc"}`))
	}))
	defer srv.Close()
	env := envFileFor(t, srv)

	t.Run("GIVEN mode and environment overrides WHEN status runs THEN the overridden values are shown", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := run([]string{"--config", env, "--mode", "fully_automated", "--environment", "paper", "status"}, &out, &errOut)
		if code != 0 || !strings.Contains(out.String(), "Mode: fully_automated") || !strings.Contains(out.String(), "Trading Bot Status") {
			t.Errorf("unexpected status output (%d): %s %s", code, out.String(), errOut.String())
		}
	})

	t.Run("GIVEN a running bot WHEN approve runs THEN it POSTs the approval and prints the result", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := run([]string{"--config", env, "approve", "abc"}, &out, &errOut)
		if code != 0 || gotMethod != http.MethodPost || gotPath != "/api/approvals/abc/approve" || !strings.Contains(out.String(), "Approved:") {
			t.Errorf("unexpected approve (%d) %s %s: %s", code, gotMethod, gotPath, out.String())
		}
	})

	t.Run("GIVEN an unknown approval WHEN reject runs THEN the API error is reported with exit code 1", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := run([]string{"--config", env, "reject", "missing"}, &out, &errOut)
		if code != 1 || !strings.Contains(errOut.String(), "API error 404") || gotPath != "/api/approvals/missing/reject" {
			t.Errorf("unexpected reject (%d): %s", code, errOut.String())
		}
	})

	t.Run("GIVEN an invalid mode WHEN parsed THEN usage exit code 2 is returned", func(t *testing.T) {
		var out, errOut bytes.Buffer
		if code := run([]string{"--config", env, "--mode", "yolo", "status"}, &out, &errOut); code != 2 {
			t.Errorf("expected exit 2, got %d", code)
		}
	})

	t.Run("GIVEN approve without an ID WHEN parsed THEN usage exit code 2 is returned", func(t *testing.T) {
		var out, errOut bytes.Buffer
		if code := run([]string{"--config", env, "approve"}, &out, &errOut); code != 2 {
			t.Errorf("expected exit 2, got %d", code)
		}
	})

	t.Run("GIVEN no bot listening WHEN approve runs THEN a cannot-reach message exits 1", func(t *testing.T) {
		dead := httptest.NewServer(http.NotFoundHandler())
		deadEnv := envFileFor(t, dead)
		dead.Close()
		var out, errOut bytes.Buffer
		if code := run([]string{"--config", deadEnv, "approve", "x"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "Cannot reach bot at") {
			t.Errorf("unexpected (%d): %s", code, errOut.String())
		}
	})
}
