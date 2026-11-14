// Command backtest runs an opt-in historical replay against recent Kraken
// public OHLC data and writes a JSON report (port of backend/backtest/replay.py).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/app"
	"github.com/James-Chapman/LLMTradingBot/internal/backtest"
	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/kraken"
	"github.com/James-Chapman/LLMTradingBot/internal/secret"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// cliArgs holds the parsed command-line flags.
type cliArgs struct {
	market          string
	hours           int
	interval        int
	strategy        string
	startingCapital float64
	source          string
	requireProfit   bool
	report          string
	db              string
}

// parseArgs parses the command-line flags (same names and defaults as the Python CLI).
func parseArgs() cliArgs {
	var a cliArgs
	flag.StringVar(&a.market, "market", "BTC/EUR", "market to replay")
	flag.IntVar(&a.hours, "hours", 48, "hours of history to replay")
	flag.IntVar(&a.interval, "interval", 5, "candle interval in minutes")
	flag.StringVar(&a.strategy, "strategy", strategy.BasicAndLLMID, "strategy id")
	flag.Float64Var(&a.startingCapital, "starting-capital", backtest.DefaultStartingCapital, "starting paper capital")
	flag.StringVar(&a.source, "source", "kraken", "candle source (only kraken is supported)")
	flag.BoolVar(&a.requireProfit, "require-profit", false, "fail unless the replay is profitable")
	flag.StringVar(&a.report, "report", "", "report path (default docs/backtests/<date>_<market>_<strategy>_<hours>h.json)")
	flag.StringVar(&a.db, "db", config.Defaults().DatabasePath, "bot database to read saved settings from (defaults are used if it does not exist)")
	flag.Parse()
	return a
}

// loadSettings returns the settings saved in the bot's database at dbPath,
// or the built-in defaults when that file does not exist (it is never
// created here).
func loadSettings(ctx context.Context, dbPath string, box secret.Box) (config.Settings, []string, error) {
	if _, err := os.Stat(dbPath); errors.Is(err, fs.ErrNotExist) {
		return config.Defaults(), nil, nil
	}
	repo, err := storage.Open(dbPath)
	if err != nil {
		return config.Settings{}, nil, err
	}
	defer repo.Close()
	return app.LoadSettings(ctx, repo, box, config.Bootstrap{})
}

// run executes the replay, writes the report and prints the summary.
func run(ctx context.Context, a cliArgs) error {
	if a.source != "kraken" {
		return fmt.Errorf("unsupported --source %q (choose kraken)", a.source)
	}
	settings, warnings, err := loadSettings(ctx, a.db, secret.Default())
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "warning: saved setting ignored:", w)
	}
	adapter := kraken.NewMarketAdapter(kraken.MarketAdapterOptions{RESTURL: settings.KrakenRESTURL})

	result, err := backtest.RunLiveReplay(ctx, adapter, backtest.LiveOptions{
		Market:          a.market,
		Hours:           a.hours,
		Interval:        a.interval,
		StrategyID:      a.strategy,
		StartingCapital: a.startingCapital,
		RequireProfit:   a.requireProfit,
		Settings:        &settings,
	})
	if err != nil {
		return err
	}

	path := a.report
	if path == "" {
		path = backtest.DefaultReportPath(time.Now(), a.market, a.strategy, a.hours)
	}
	if err := backtest.WriteReport(result, path); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	summary, err := json.MarshalIndent(result.ToReport().Summary, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(summary))
	return nil
}

// main parses flags and runs the replay, exiting non-zero on failure.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, parseArgs()); err != nil {
		fmt.Fprintln(os.Stderr, "backtest:", err)
		os.Exit(1)
	}
}
