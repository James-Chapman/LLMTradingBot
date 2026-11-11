// Command tradingbot runs the trading bot: background trading loops plus the
// dashboard and REST API (the Go port of backend/main.py).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/app"
	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
	"github.com/James-Chapman/LLMTradingBot/internal/server"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
)

// shutdownTimeout bounds how long in-flight HTTP requests may take on exit.
const shutdownTimeout = 10 * time.Second

// main runs the bot and exits non-zero on a startup or serve failure.
func main() {
	frontendDir := flag.String("frontend", "frontend", "directory holding index.html, approvals.html, sw.js and static/")
	envFile := flag.String("env", ".env", "path to the .env settings file")
	flag.Parse()

	if err := run(*envFile, *frontendDir); err != nil {
		fmt.Fprintln(os.Stderr, "tradingbot:", err)
		os.Exit(1)
	}
}

// run wires settings, logging, storage, the app and the HTTP server, and
// blocks until Ctrl+C/SIGTERM triggers a graceful shutdown.
func run(envFile, frontendDir string) error {
	settings, err := config.Load(envFile)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	if err := settings.Validate(); err != nil {
		return fmt.Errorf("invalid settings: %w", err)
	}

	closeLogs, err := observability.SetupLogging(settings.LogLevel, settings.LogFile)
	if err != nil {
		return fmt.Errorf("set up logging: %w", err)
	}
	defer closeLogs()
	logger := observability.Logger("main")

	repo, err := storage.Open(settings.DatabasePath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer repo.Close()

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
			"mode", settings.TradingMode, "environment", settings.TradingEnvironment, "version", app.Version)
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
