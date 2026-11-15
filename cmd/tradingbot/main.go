// Command tradingbot runs the trading bot: background trading loops plus the
// dashboard and REST API (the Go port of backend/main.py).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/app"
	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/kraken"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
	"github.com/James-Chapman/LLMTradingBot/internal/secret"
	"github.com/James-Chapman/LLMTradingBot/internal/server"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
)

// shutdownTimeout bounds how long in-flight HTTP requests may take on exit.
const shutdownTimeout = 10 * time.Second

// loopsStopTimeout bounds how long shutdown waits for the background loops.
const loopsStopTimeout = 30 * time.Second

// HTTP server timeouts. Every response is small (the CSV export is capped at
// 10,000 rows) and nothing streams; the write timeout leaves room for an
// approval that places a live Kraken order.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 2 * time.Minute
	idleTimeout       = 2 * time.Minute
)

// main runs the bot and exits non-zero on a startup or serve failure.
func main() {
	boot, frontendDir, err := parseFlags(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		os.Exit(2)
	}
	if err := run(boot, frontendDir); err != nil {
		fmt.Fprintln(os.Stderr, "tradingbot:", err)
		os.Exit(1)
	}
}

// parseFlags reads the start-up flags: the only settings needed before the
// database opens. Everything else is managed in the dashboard. Usage and
// errors are written to out.
func parseFlags(args []string, out io.Writer) (config.Bootstrap, string, error) {
	defaults := config.Defaults()
	fs := flag.NewFlagSet("tradingbot", flag.ContinueOnError)
	fs.SetOutput(out)
	db := fs.String("db", defaults.DatabasePath, "SQLite database file (holds trades and every dashboard setting)")
	host := fs.String("host", defaults.Host, "address the dashboard listens on")
	port := fs.Int("port", defaults.Port, "port the dashboard listens on")
	logFile := fs.String("log", defaults.LogFile, "log file (llm.log is written beside it)")
	frontendDir := fs.String("frontend", "frontend", "directory holding index.html, approvals.html, sw.js and static/")
	if err := fs.Parse(args); err != nil {
		return config.Bootstrap{}, "", err
	}
	if *port < 1 || *port > 65535 {
		err := fmt.Errorf("-port must be between 1 and 65535, got %d", *port)
		fmt.Fprintln(out, err)
		return config.Bootstrap{}, "", err
	}
	return config.Bootstrap{DatabasePath: *db, Host: *host, Port: *port, LogFile: *logFile}, *frontendDir, nil
}

// marketLookupTimeout bounds the start-up request for Kraken's biggest pairs.
const marketLookupTimeout = 20 * time.Second

// useLiveDefaultMarkets picks the default markets from Kraken's biggest pairs
// when none were saved from the dashboard, keeping the built-in list if
// Kraken cannot be reached.
func useLiveDefaultMarkets(settings *config.Settings, repo *storage.Repository, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), marketLookupTimeout)
	defer cancel()
	ranker := kraken.NewMarketAdapter(kraken.MarketAdapterOptions{RESTURL: settings.KrakenRESTURL})
	used, err := app.UseLiveDefaultMarkets(ctx, repo, settings, ranker)
	switch {
	case err != nil:
		logger.Warn("Could not get the biggest pairs from Kraken; using the built-in default markets",
			"error", err.Error(), "markets", len(settings.FixedMarkets))
	case used:
		logger.Info("Default markets picked from Kraken by 24h volume",
			"markets", len(settings.FixedMarkets), "quote", settings.BaseCurrency)
	}
}

// run wires logging, storage, the saved settings, the app and the HTTP
// server, and blocks until Ctrl+C/SIGTERM triggers a graceful shutdown.
func run(boot config.Bootstrap, frontendDir string) error {
	// Log at INFO until the saved log level is known.
	closeLogs, err := observability.SetupLogging("INFO", boot.LogFile)
	if err != nil {
		return fmt.Errorf("set up logging: %w", err)
	}
	defer closeLogs()
	logger := observability.Logger("main")

	repo, err := storage.Open(boot.DatabasePath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer repo.Close()

	box := secret.Default()
	settings, warnings, err := app.LoadSettings(context.Background(), repo, box, boot)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	for _, w := range warnings {
		logger.Warn("Saved setting ignored", "detail", w)
	}
	if err := settings.Validate(); err != nil {
		return fmt.Errorf("invalid settings: %w", err)
	}
	observability.SetLevel(settings.EffectiveLogLevel())
	useLiveDefaultMarkets(&settings, repo, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bot, err := app.New(ctx, settings, repo)
	if err != nil {
		return fmt.Errorf("start app: %w", err)
	}

	access, err := setupAccess(ctx, settings, repo, box, os.Stdout, logger)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              net.JoinHostPort(settings.Host, strconv.Itoa(settings.Port)),
		Handler:           server.New(bot, server.Options{FrontendDir: frontendDir, Access: access}),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	loopsDone := make(chan struct{})
	go func() {
		bot.Run(ctx)
		close(loopsDone)
	}()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("Starting Kraken Trading Bot",
			"host", settings.Host, "port", settings.Port,
			"mode", bot.TradingMode(), "environment", bot.TradingEnvironment(), "version", app.Version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	var failure error
	select {
	case <-ctx.Done():
		logger.Info("Shutdown requested; press Ctrl+C again to quit at once")
	case err := <-serveErr:
		failure = fmt.Errorf("serve http: %w", err)
	}
	// Stop the loops (after a serve failure) and restore the default signal
	// handling, so a second Ctrl+C ends the process immediately.
	stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("HTTP shutdown did not complete cleanly", "error", err.Error())
	}
	if !waitFor(loopsDone, loopsStopTimeout) {
		logger.Warn("Background loops did not stop in time; exiting anyway", "timeout", loopsStopTimeout.String())
		return failure
	}
	logger.Info("Trading bot stopped")
	return failure
}

// waitFor waits until done is closed or timeout passes; it reports whether
// done closed in time.
func waitFor(done <-chan struct{}, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// setupAccess returns the access token when the dashboard listens on a
// non-loopback address (nil on loopback, where no login is needed). The
// token is generated and stored sealed on the first such start and printed
// to out at every start; it is never written to the log file.
func setupAccess(ctx context.Context, settings config.Settings, repo *storage.Repository, box secret.Box, out io.Writer, logger *slog.Logger) (server.TokenSource, error) {
	if config.IsLoopbackHost(settings.Host) {
		return nil, nil
	}
	logger.Warn("The dashboard is reachable from other computers: every request needs the access token",
		"host", settings.Host, "port", settings.Port)
	token, created, err := app.LoadAccessToken(ctx, repo, box)
	if err != nil {
		return nil, fmt.Errorf("load access token: %w", err)
	}
	if created {
		logger.Info("New dashboard access token generated")
	}
	printAccessToken(out, token.Token(), settings.Port)
	return token, nil
}

// printAccessToken writes the access token in a clearly marked block.
func printAccessToken(out io.Writer, token string, port int) {
	line := strings.Repeat("=", 72)
	fmt.Fprintln(out, line)
	fmt.Fprintln(out, "DASHBOARD ACCESS TOKEN (keep it secret):")
	fmt.Fprintln(out, "  "+token)
	fmt.Fprintf(out, "Open http://<this computer>:%d/login and paste it, or send\n", port)
	fmt.Fprintln(out, "  Authorization: Bearer <token>   from scripts.")
	fmt.Fprintln(out, line)
}
