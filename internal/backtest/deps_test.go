package backtest

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// modulePath is the repository's Go module path.
const modulePath = "github.com/James-Chapman/LLMTradingBot"

// forbiddenDeps are packages a backtest must never link: everything that can
// place an order (the Kraken client and execution engine, the Kraken SDK, the
// live app and its position book), read secrets (the secret store and the
// Windows DPAPI calls in x/sys/windows), serve the dashboard, open a
// database (the bot's storage, database/sql, the SQLite driver) or run a
// program (os/exec).
var forbiddenDeps = []string{
	modulePath + "/internal/app",
	modulePath + "/internal/secret",
	modulePath + "/internal/server",
	modulePath + "/internal/kraken",
	modulePath + "/internal/storage",
	modulePath + "/internal/livebook",
	"github.com/krakenfx/",
	"database/sql",
	"modernc.org/sqlite",
	"os/exec",
	"golang.org/x/sys/windows",
}

// backtestDirs are the directories of the three backtest packages, relative
// to this package.
var backtestDirs = []string{".", "../histdata", "../../cmd/backtest"}

// targetOSes returns the operating systems the repository has
// platform-specific files for (dpapi_windows.go, unsupported_other.go), plus
// the host's.
func targetOSes() []string {
	return slices.Compact(slices.Sorted(slices.Values([]string{runtime.GOOS, "windows", "linux", "darwin"})))
}

// buildTags returns every build tag named in a //go:build line of the three
// backtest packages, so a tagged file cannot hide a forbidden import.
func buildTags(t *testing.T) []string {
	t.Helper()
	known := map[string]bool{"ignore": true, "windows": true, "linux": true, "darwin": true, "unix": true,
		"amd64": true, "arm64": true, "386": true, "arm": true, "cgo": true}
	word := regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_.]*`)
	var tags []string
	for _, dir := range backtestDirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(string(raw), "\n") {
				if expr, ok := strings.CutPrefix(strings.TrimSpace(line), "//go:build "); ok {
					for _, w := range word.FindAllString(expr, -1) {
						if !known[w] && !strings.HasPrefix(w, "go1") {
							tags = append(tags, w)
						}
					}
				}
			}
		}
	}
	return slices.Compact(slices.Sorted(slices.Values(tags)))
}

// listDeps returns `go list -deps` of the backtest packages for goos with tags.
func listDeps(t *testing.T, goos string, tags []string) []string {
	t.Helper()
	args := []string{"list", "-deps"}
	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	args = append(args, modulePath+"/internal/backtest/...", modulePath+"/internal/histdata/...", modulePath+"/cmd/backtest/...")
	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), "GOOS="+goos, "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("GOOS=%s go list -deps: %v\n%s", goos, err, out)
	}
	deps := strings.Fields(string(out))
	if len(deps) == 0 {
		t.Fatalf("GOOS=%s: go list returned no packages", goos)
	}
	return deps
}

// directImports returns the imports of the non-test Go files in dir, by file.
func directImports(t *testing.T, dir string) map[string][]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			out[filepath.Base(f)] = append(out[filepath.Base(f)], path)
		}
	}
	return out
}

// TestBacktestCannotReachLiveTrading enforces structurally that the backtest
// binary and its packages cannot place an order, read secrets or open a database.
func TestBacktestCannotReachLiveTrading(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Fatalf("the go tool is needed to list dependencies: %v", err)
	}
	tags := buildTags(t)
	for _, goos := range targetOSes() {
		t.Run("GIVEN the backtest packages and command built for "+goos+" with tags "+strings.Join(tags, ",")+" WHEN their dependencies are listed THEN no live-execution, secret, server, database or process package is linked", func(t *testing.T) {
			for _, dep := range listDeps(t, goos, tags) {
				for _, bad := range forbiddenDeps {
					if dep == bad || strings.HasPrefix(dep, strings.TrimSuffix(bad, "/")+"/") {
						t.Errorf("forbidden dependency linked: %s", dep)
					}
				}
			}
		})
	}

	t.Run("GIVEN the backtest sources WHEN their direct imports are read THEN only histdata's REST fetcher imports net/http", func(t *testing.T) {
		for _, dir := range backtestDirs {
			for file, imports := range directImports(t, dir) {
				if !slices.Contains(imports, "net/http") {
					continue
				}
				if !(dir == "../histdata" && file == "rest.go") {
					t.Errorf("%s/%s imports net/http directly; only internal/histdata/rest.go may", dir, file)
				}
			}
		}
	})
}

// failingTransport fails the test on any HTTP request.
type failingTransport struct {
	t     *testing.T
	calls atomic.Int32
}

// RoundTrip records and refuses the request.
func (f *failingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.calls.Add(1)
	f.t.Errorf("a backtest made a network request: %s %s", req.Method, req.URL)
	return nil, errors.New("network disabled in backtests")
}

// TestBacktestMakesNoNetworkCall covers that internal/strategy's transitive
// net/http (through internal/llm) is never used: LLM strategies are refused
// before any client exists, and the allowed strategy performs no request.
func TestBacktestMakesNoNetworkCall(t *testing.T) {
	t.Run("GIVEN every HTTP request fails the test WHEN basic_strategy is replayed and the LLM strategies are asked for THEN no request is made and the LLM strategies are refused", func(t *testing.T) {
		ft := &failingTransport{t: t}
		saved := http.DefaultTransport
		http.DefaultTransport = ft
		defer func() { http.DefaultTransport = saved }()

		res := mustRun(t, oneMarket(trendBars(2000, 0.00025)), testConfig([]string{"BTC/EUR"}, 1999))
		if res.EvaluatedTicks == 0 {
			t.Fatal("the strategy never ran")
		}
		for _, id := range []string{strategy.BasicAndLLM1hID, strategy.LLMOnlyID} {
			cfg := testConfig([]string{"BTC/EUR"}, 79)
			cfg.StrategyID = id
			if _, err := Run(context.Background(), oneMarket(flatSeries(80, 100)), cfg); !errors.Is(err, ErrLLMStrategy) {
				t.Fatalf("%s: want ErrLLMStrategy, got %v", id, err)
			}
		}
		if n := ft.calls.Load(); n != 0 {
			t.Fatalf("%d network requests were made", n)
		}
	})
}
