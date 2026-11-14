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

	settings, warnings, err := app.LoadSettings(context.Background(), repo, secret.Default(), boot)
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

	srv := &http.Server{
		Addr:              net.JoinHostPort(settings.Host, strconv.Itoa(settings.Port)),
		Handler:           server.New(bot, server.Options{FrontendDir: frontendDir}),
		ReadHeaderTimeout: 10 * time.Second,
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
		logger.Info("Shutdown requested")
	case err := <-serveErr:
		failure = fmt.Errorf("serve http: %w", err)
		stop() // stop the background loops too
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("HTTP shutdown did not complete cleanly", "error", err.Error())
	}
	<-loopsDone
	logger.Info("Trading bot stopped")
	return failure
}
