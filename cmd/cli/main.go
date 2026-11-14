// Command cli is the operator command line for the trading bot (the Go port
// of cli.py): show the running bot's status, approve or reject pending
// trades, or start the bot. Settings are managed in the dashboard; the CLI
// only needs to know where the bot listens.
//
// Usage:
//
//	cli [-host H] [-port P] [-log FILE] [-db FILE] status|approve ID|reject ID|run
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
)

// apiTimeout bounds each call to the running bot.
const apiTimeout = 5 * time.Second

// main runs the CLI and exits with its status code.
func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run parses args, executes the command and returns the process exit code
// (0 ok, 1 failure, 2 usage error).
func run(args []string, stdout, stderr io.Writer) int {
	defaults := config.Defaults()
	fs := flag.NewFlagSet("cli", flag.ContinueOnError)
	fs.SetOutput(stderr)
	host := fs.String("host", defaults.Host, "address the bot listens on")
	port := fs.Int("port", defaults.Port, "port the bot listens on")
	logFile := fs.String("log", defaults.LogFile, "log file")
	db := fs.String("db", defaults.DatabasePath, "database file (only used by run)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Kraken Trading Bot CLI")
		fmt.Fprintln(stderr, "\nUsage: cli [flags] <command> [args]")
		fmt.Fprintln(stderr, "\nCommands:")
		fmt.Fprintln(stderr, "  status          Show the running bot's status")
		fmt.Fprintln(stderr, "  approve ID      Approve pending trade")
		fmt.Fprintln(stderr, "  reject ID       Reject pending trade")
		fmt.Fprintln(stderr, "  run             Run the trading bot (flags are passed through)")
		fmt.Fprintln(stderr, "\nFlags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	closeLogs, err := observability.SetupLogging("INFO", *logFile)
	if err != nil {
		fmt.Fprintln(stderr, "cannot set up logging:", err)
		return 1
	}
	defer closeLogs()
	logger := observability.Logger("cli")

	command := fs.Arg(0)
	logger.Info("CLI started", "command", command, "cli_args", args)
	addr := net.JoinHostPort(*host, strconv.Itoa(*port))

	switch command {
	case "status":
		return showStatus(stdout, stderr, addr)
	case "approve", "reject":
		if fs.NArg() < 2 {
			fmt.Fprintf(stderr, "usage: cli %s TRADE_ID\n", command)
			return 2
		}
		return decide(stdout, stderr, addr, command, fs.Arg(1))
	case "run":
		return runBot(stdout, stderr, addr, botArgs(*db, *host, *port, *logFile))
	default:
		fs.Usage()
		return 0
	}
}

// botArgs builds the tradingbot start-up flags that `cli run` passes through.
func botArgs(db, host string, port int, logFile string) []string {
	return []string{"-db", db, "-host", host, "-port", strconv.Itoa(port), "-log", logFile}
}

// dashboardStatus is the part of /api/dashboard that status prints.
type dashboardStatus struct {
	Version        string `json:"version"`
	Mode           string `json:"mode"`
	Environment    string `json:"environment"`
	CurrencySymbol string `json:"currency_symbol"`
	Equity         string `json:"equity"`
	Cash           string `json:"cash"`
}

// showStatus prints the running bot's version, mode, environment and equity.
func showStatus(stdout, stderr io.Writer, addr string) int {
	body, code := apiCall(stderr, http.MethodGet, addr, "/api/dashboard")
	if code != 0 {
		return code
	}
	var d dashboardStatus
	if err := json.Unmarshal(body, &d); err != nil {
		fmt.Fprintf(stderr, "API returned invalid JSON: %s\n", body)
		return 1
	}
	fmt.Fprintln(stdout, "Trading Bot Status")
	fmt.Fprintf(stdout, "Version: %s\n", d.Version)
	fmt.Fprintf(stdout, "Mode: %s\n", d.Mode)
	fmt.Fprintf(stdout, "Environment: %s\n", d.Environment)
	fmt.Fprintf(stdout, "Equity: %s%s\n", d.CurrencySymbol, d.Equity)
	fmt.Fprintf(stdout, "Cash: %s%s\n", d.CurrencySymbol, d.Cash)
	return 0
}

// decide approves or rejects a pending trade through the running bot's API.
func decide(stdout, stderr io.Writer, addr, action, tradeID string) int {
	body, code := apiCall(stderr, http.MethodPost, addr, fmt.Sprintf("/api/approvals/%s/%s", tradeID, action))
	if code != 0 {
		return code
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		fmt.Fprintf(stderr, "API returned invalid JSON: %s\n", body)
		return 1
	}
	compact, _ := json.Marshal(parsed)
	label := map[string]string{"approve": "Approved", "reject": "Rejected"}[action]
	fmt.Fprintf(stdout, "%s: %s\n", label, compact)
	observability.Logger("cli").Info("Trade "+label+" via CLI", "trade_id", tradeID, "result", string(compact))
	return 0
}

// apiCall sends a request to the running bot and returns the response body,
// or an exit code of 1 after printing the failure.
func apiCall(stderr io.Writer, method, addr, path string) ([]byte, int) {
	url := "http://" + addr + path
	req, err := http.NewRequest(method, url, strings.NewReader(""))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, 1
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: apiTimeout}).Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "Cannot reach bot at %s: %v\n", url, err)
		fmt.Fprintln(stderr, "Is the bot running? Start it with:  go run ./cmd/tradingbot")
		return nil, 1
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		fmt.Fprintf(stderr, "API error %d: %s\n", resp.StatusCode, body)
		return nil, 1
	}
	return body, 0
}

// runBot starts the tradingbot binary in the foreground with the given
// start-up flags.
func runBot(stdout, stderr io.Writer, addr string, args []string) int {
	binary, err := findBotBinary()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Starting bot on http://%s ...\n", addr)
	observability.Logger("cli").Info("Bot started via CLI")

	cmd := exec.Command(binary, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintln(stderr, "cannot start bot:", err)
		return 1
	}
	return 0
}

// findBotBinary locates the tradingbot executable next to this CLI or on PATH.
func findBotBinary() (string, error) {
	name := "tradingbot"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if self, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(self), name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("cannot find the %s binary next to the CLI or on PATH; build it with `go build ./cmd/tradingbot` or run `go run ./cmd/tradingbot`", name)
}
