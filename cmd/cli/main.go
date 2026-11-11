// Command cli is the operator command line for the trading bot (the Go port
// of cli.py): show the configuration, approve or reject pending trades on the
// running bot, or start the bot.
//
// Usage:
//
//	cli [--config .env] [--mode MODE] [--environment ENV] status|approve ID|reject ID|run
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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
)

// Allowed values for the override flags (argparse choices in cli.py).
var (
	modeChoices        = []string{"manual", "semi_automated", "fully_automated"}
	environmentChoices = []string{"paper", "live"}
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
	fs := flag.NewFlagSet("cli", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", ".env", "path to the .env settings file")
	mode := fs.String("mode", "", "override trading mode: "+strings.Join(modeChoices, ", "))
	environment := fs.String("environment", "", "override trading environment: "+strings.Join(environmentChoices, ", "))
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Kraken Trading Bot CLI")
		fmt.Fprintln(stderr, "\nUsage: cli [flags] <command> [args]")
		fmt.Fprintln(stderr, "\nCommands:")
		fmt.Fprintln(stderr, "  status          Show bot status")
		fmt.Fprintln(stderr, "  approve ID      Approve pending trade")
		fmt.Fprintln(stderr, "  reject ID       Reject pending trade")
		fmt.Fprintln(stderr, "  run             Run the trading bot")
		fmt.Fprintln(stderr, "\nFlags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *mode != "" && !slices.Contains(modeChoices, *mode) {
		fmt.Fprintf(stderr, "invalid --mode %q (choose from %s)\n", *mode, strings.Join(modeChoices, ", "))
		return 2
	}
	if *environment != "" && !slices.Contains(environmentChoices, *environment) {
		fmt.Fprintf(stderr, "invalid --environment %q (choose from %s)\n", *environment, strings.Join(environmentChoices, ", "))
		return 2
	}

	settings, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, "cannot load settings:", err)
		return 1
	}
	if *mode != "" {
		settings.TradingMode = *mode
	}
	if *environment != "" {
		settings.TradingEnvironment = *environment
	}

	closeLogs, err := observability.SetupLogging(settings.LogLevel, settings.LogFile)
	if err != nil {
		fmt.Fprintln(stderr, "cannot set up logging:", err)
		return 1
	}
	defer closeLogs()
	logger := observability.Logger("cli")

	command := fs.Arg(0)
	logger.Info("CLI started", "command", command, "cli_args", args)

	switch command {
	case "status":
		showStatus(stdout, settings)
		return 0
	case "approve", "reject":
		if fs.NArg() < 2 {
			fmt.Fprintf(stderr, "usage: cli %s TRADE_ID\n", command)
			return 2
		}
		return decide(stdout, stderr, settings, command, fs.Arg(1))
	case "run":
		return runBot(stdout, stderr, settings, *mode, *environment)
	default:
		fs.Usage()
		return 0
	}
}

// showStatus prints the effective configuration.
func showStatus(w io.Writer, s config.Settings) {
	symbol := config.CurrencySymbol(s.BaseCurrency)
	fmt.Fprintln(w, "Trading Bot Status")
	fmt.Fprintf(w, "Mode: %s\n", s.TradingMode)
	fmt.Fprintf(w, "Environment: %s\n", s.TradingEnvironment)
	fmt.Fprintf(w, "Starting Capital: %s%.2f\n", symbol, s.StartingCapital)
	fmt.Fprintf(w, "Max Loss Per Trade: %.1f%%\n", s.MaxLossPerTradePercent)
	fmt.Fprintf(w, "Max Daily Loss: %.1f%%\n", s.MaxDailyLossPercent)
	fmt.Fprintf(w, "Database: %s\n", s.DatabasePath)
	fmt.Fprintf(w, "Log Level: %s\n", s.LogLevel)
}

// decide approves or rejects a pending trade through the running bot's API.
func decide(stdout, stderr io.Writer, s config.Settings, action, tradeID string) int {
	result, code := apiPost(stderr, s, fmt.Sprintf("/api/approvals/%s/%s", tradeID, action))
	if code != 0 {
		return code
	}
	label := map[string]string{"approve": "Approved", "reject": "Rejected"}[action]
	fmt.Fprintf(stdout, "%s: %s\n", label, result)
	observability.Logger("cli").Info("Trade "+label+" via CLI", "trade_id", tradeID, "result", result)
	return 0
}

// apiPost POSTs to the running bot and returns the compact JSON body, or an
// exit code of 1 after printing the failure.
func apiPost(stderr io.Writer, s config.Settings, path string) (string, int) {
	url := "http://" + net.JoinHostPort(s.Host, strconv.Itoa(s.Port)) + path
	client := &http.Client{Timeout: apiTimeout}
	resp, err := client.Post(url, "application/json", strings.NewReader(""))
	if err != nil {
		fmt.Fprintf(stderr, "Cannot reach bot at %s: %v\n", url, err)
		fmt.Fprintln(stderr, "Is the bot running? Start it with:  go run ./cmd/tradingbot")
		return "", 1
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		fmt.Fprintf(stderr, "API error %d: %s\n", resp.StatusCode, body)
		return "", 1
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		fmt.Fprintf(stderr, "API returned invalid JSON: %s\n", body)
		return "", 1
	}
	compact, _ := json.Marshal(parsed)
	return string(compact), 0
}

// runBot starts the tradingbot binary in the foreground, passing any
// mode/environment overrides through the environment.
func runBot(stdout, stderr io.Writer, s config.Settings, mode, environment string) int {
	binary, err := findBotBinary()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Starting bot on http://%s ...\n", net.JoinHostPort(s.Host, strconv.Itoa(s.Port)))
	observability.Logger("cli").Info("Bot started via CLI")

	cmd := exec.Command(binary)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stdout, stderr
	cmd.Env = os.Environ()
	if mode != "" {
		cmd.Env = append(cmd.Env, "TRADING_MODE="+mode)
	}
	if environment != "" {
		cmd.Env = append(cmd.Env, "TRADING_ENVIRONMENT="+environment)
	}
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
