package app

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/app/apptest"
	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
	"github.com/James-Chapman/LLMTradingBot/internal/news"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
	"github.com/James-Chapman/LLMTradingBot/internal/secret"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// testEnv bundles an app with its fakes for behaviour tests.
type testEnv struct {
	app      *App
	repo     *storage.Repository
	market   *apptest.Market
	live     *apptest.Live
	strategy *apptest.Strategy
	llm      *apptest.LLM
	logs     *bytes.Buffer
}

// testSettings returns network-free settings for a paper bot on BTC/EUR and ETH/EUR.
func testSettings() config.Settings {
	s := config.Defaults()
	s.FixedMarkets = []string{"BTC/EUR", "ETH/EUR"}
	s.NewsSources = nil
	s.LMStudioBaseURL = ""
	s.OpenAIBaseURL = ""
	s.TradingMode = "manual"
	s.TradingEnvironment = "paper"
	return s
}

// newEnv builds an app over a temp DB with fakes; seed runs before the app is created.
func newEnv(t *testing.T, mutate func(*config.Settings), seed func(*storage.Repository)) *testEnv {
	t.Helper()
	settings := testSettings()
	if mutate != nil {
		mutate(&settings)
	}
	repo := newTestRepo(t)
	if seed != nil {
		seed(repo)
	}
	env := &testEnv{
		repo:     repo,
		market:   apptest.NewMarket(map[string]float64{"BTC/EUR": 50000, "ETH/EUR": 3000}),
		live:     &apptest.Live{},
		strategy: &apptest.Strategy{StrategyID: strategy.Basic1hID},
		llm:      &apptest.LLM{},
		logs:     &bytes.Buffer{},
	}
	env.app = env.newApp(t, settings)
	return env
}

// restart replaces the app with a fresh one over the same database, as if the
// bot were restarted with the given settings.
func (e *testEnv) restart(t *testing.T, mutate func(*config.Settings)) {
	t.Helper()
	settings := testSettings()
	if mutate != nil {
		mutate(&settings)
	}
	e.app = e.newApp(t, settings)
}

// newApp builds an app over the env's database and fakes.
func (e *testEnv) newApp(t *testing.T, settings config.Settings) *App {
	t.Helper()
	a, err := NewWithOptions(context.Background(), settings, e.repo, Options{
		Market:          e.market,
		Live:            e.live,
		LLM:             e.llm,
		Universe:        apptest.Universe{Markets: settings.FixedMarkets},
		NewsAdapters:    []news.Adapter{},
		StrategyFactory: func(config.Settings) []strategy.Strategy { return []strategy.Strategy{e.strategy} },
		Activity:        observability.NewActivityLog(200),
		Logger:          slog.New(slog.NewJSONHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		SecretBox:       secret.Reversible(),
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	a.control.SelectStrategy(e.strategy.StrategyID)
	return a
}

// restartFromDB restarts the app with settings loaded from the database, as
// cmd/tradingbot does, so saved dashboard settings take effect.
func (e *testEnv) restartFromDB(t *testing.T) {
	t.Helper()
	loaded, _, err := LoadSettings(context.Background(), e.repo, secret.Reversible(), config.Bootstrap{})
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	base := testSettings()
	// Keep the test harness's network-free endpoints; everything else comes from the DB.
	loaded.NewsSources, loaded.LMStudioBaseURL, loaded.OpenAIBaseURL = base.NewsSources, base.LMStudioBaseURL, base.OpenAIBaseURL
	e.app = e.newApp(t, loaded)
}

// seedPrices stores n flat price ticks per market so warm-up is already satisfied.
func seedPrices(n int) func(*storage.Repository) {
	return func(repo *storage.Repository) {
		for i := 0; i < n; i++ {
			_ = repo.SavePriceTick(context.Background(), "BTC/EUR", 50000, nil)
			_ = repo.SavePriceTick(context.Background(), "ETH/EUR", 3000, nil)
		}
	}
}

// boot runs the strategy-loop startup (universe, validation, history restore).
func (e *testEnv) boot(t *testing.T) {
	t.Helper()
	e.app.bootStrategyLoop(context.Background())
}

// tick runs one strategy tick and fails the test on error.
func (e *testEnv) tick(t *testing.T) bool {
	t.Helper()
	skipped, err := e.app.strategyTick(context.Background())
	if err != nil {
		t.Fatalf("strategyTick: %v", err)
	}
	return skipped
}

// activityContains reports whether any recent activity message contains text.
func (e *testEnv) activityContains(text string) bool {
	for _, entry := range e.app.activity.Recent(200) {
		if strings.Contains(entry.Message, text) {
			return true
		}
	}
	return false
}

// TestStartupRestore covers the import-time/lifespan restores of main.py.
func TestStartupRestore(t *testing.T) {
	t.Run("GIVEN persisted equity, rejections and news WHEN the app starts THEN the dashboard shows them", func(t *testing.T) {
		env := newEnv(t, nil, func(repo *storage.Repository) {
			ctx := context.Background()
			_ = repo.SaveEquitySnapshot(ctx, 640, 610, 30)
			_ = repo.SaveRiskRejection(ctx, storage.RiskRejectionInput{Market: "BTC/EUR", Direction: "long", Confidence: 0.6, Reason: "Daily loss limit reached"})
			_ = repo.UpsertNewsItem(ctx, domain.NewsItem{ID: "n1", Source: "Fake", Title: "Bitcoin rallies", PublishedAt: time.Now().UTC()})
		})
		d := env.app.Dashboard()
		if d.Equity != "640.00" || d.Cash != "610.00" {
			t.Errorf("equity/cash not restored in paper mode: %s / %s", d.Equity, d.Cash)
		}
		if len(d.RiskRejections) != 1 {
			t.Errorf("risk rejections not preloaded: %v", d.RiskRejections)
		}
		if len(env.app.News()) != 1 {
			t.Errorf("news not seeded from DB: %v", env.app.News())
		}
		if len(d.EquityHistory) == 0 {
			t.Errorf("equity history must not be empty")
		}
	})

	t.Run("GIVEN an empty database WHEN the app starts THEN equity history starts at the starting capital", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		d := env.app.Dashboard()
		if len(d.EquityHistory) == 0 || d.EquityHistory[0].Equity != env.app.settings.StartingCapital {
			t.Errorf("expected a starting-capital point, got %v", d.EquityHistory)
		}
		if d.Version != Version || d.Signals == nil || d.Positions == nil || d.Approvals == nil {
			t.Errorf("dashboard lists must be non-nil and version set: %+v", d)
		}
	})
}

// TestStrategyTickGates covers the early-exit gates of the strategy loop.
func TestStrategyTickGates(t *testing.T) {
	t.Run("GIVEN the emergency stop is active WHEN a tick runs THEN it is skipped and logged", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		env.boot(t)
		env.app.control.ActivateStop()
		if !env.tick(t) {
			t.Fatalf("tick should be skipped")
		}
		if !env.activityContains("Emergency stop active - tick skipped") {
			t.Errorf("missing emergency-stop activity")
		}
		if len(env.app.Dashboard().Markets) != 2 || env.app.Dashboard().Markets[0].Price != "-" {
			t.Errorf("no prices should be recorded during a stop: %+v", env.app.Dashboard().Markets)
		}
	})

	t.Run("GIVEN every market is disabled WHEN a tick runs THEN it is skipped and logged", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		env.boot(t)
		env.app.control.DisableMarket("BTC/EUR")
		env.app.control.DisableMarket("ETH/EUR")
		if !env.tick(t) || !env.activityContains("All markets disabled - tick skipped") {
			t.Errorf("expected an all-disabled skip")
		}
	})

	t.Run("GIVEN no price history WHEN a strategy that reads price ticks ticks THEN it waits for warm-up", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		env.strategy.Ticks = strategy.LookbackTicks + 1
		env.boot(t)
		env.tick(t)
		if env.strategy.Calls() != 0 {
			t.Errorf("strategy must not evaluate while warming up")
		}
		if !env.activityContains("warming up") {
			t.Errorf("expected a warming-up tick message")
		}
		if env.app.Dashboard().Markets[0].Price == "-" {
			t.Errorf("prices should still be recorded during warm-up")
		}
	})

	t.Run("GIVEN a full price history WHEN an indicator strategy ticks THEN it evaluates", func(t *testing.T) {
		env := newEnv(t, nil, seedPrices(LookbackTicksForTest()+1))
		env.boot(t)
		env.tick(t)
		if env.strategy.Calls() != 1 {
			t.Errorf("expected one evaluation, got %d", env.strategy.Calls())
		}
		if !env.activityContains("Strategies evaluated - no signals this tick") {
			t.Errorf("expected the no-signals activity")
		}
	})

	t.Run("GIVEN an LLM strategy with prices but no news WHEN it ticks THEN it waits for news", func(t *testing.T) {
		env := newEnv(t, nil, seedPrices(20))
		env.strategy.LLM = true
		env.strategy.Ticks = 10
		env.boot(t)
		env.app.mu.Lock()
		env.app.latestNews = nil
		env.app.mu.Unlock()
		env.tick(t)
		if env.strategy.Calls() != 0 || !env.activityContains("LLM data warmup waiting for news") {
			t.Errorf("LLM strategy should wait for news")
		}
	})
}

// TestStrategyTickTradingModes covers manual, semi and fully automated handling.
func TestStrategyTickTradingModes(t *testing.T) {
	longIdea := func(in strategy.Input) []domain.TradeIdea {
		return []domain.TradeIdea{apptest.Idea(strategy.Basic1hID, "BTC/EUR", domain.Long, 0.9)}
	}

	t.Run("GIVEN manual mode WHEN a signal fires THEN it is shown but not traded", func(t *testing.T) {
		env := newEnv(t, nil, seedPrices(40))
		env.strategy.Ideas = longIdea
		env.boot(t)
		before := env.app.DashboardVersion()
		env.tick(t)
		d := env.app.Dashboard()
		if len(d.Signals) != 1 || d.Signals[0].Market != "BTC/EUR" {
			t.Fatalf("expected one buffered signal, got %+v", d.Signals)
		}
		if len(d.Positions) != 0 {
			t.Errorf("manual mode must not open positions")
		}
		if env.app.DashboardVersion() <= before {
			t.Errorf("dashboard version should advance")
		}
		if detail, _ := env.repo.GetSignalDetail(context.Background(), d.Signals[0].TradeIdeaID); detail == nil {
			t.Errorf("trade idea should be persisted with its signal context")
		}
	})

	t.Run("GIVEN semi-automated mode WHEN a signal fires THEN an approval is queued once", func(t *testing.T) {
		env := newEnv(t, func(s *config.Settings) { s.TradingMode = "semi_automated" }, seedPrices(40))
		env.strategy.Ideas = longIdea
		env.boot(t)
		env.tick(t)
		env.tick(t)
		if n := len(env.app.PendingApprovals()); n != 1 {
			t.Errorf("expected exactly one pending approval, got %d", n)
		}
		if !env.activityContains("submitted for approval") {
			t.Errorf("expected submitted-for-approval activity")
		}
	})

	t.Run("GIVEN fully automated paper mode WHEN a confident signal fires THEN a paper position opens", func(t *testing.T) {
		env := newEnv(t, func(s *config.Settings) { s.TradingMode = "fully_automated" }, seedPrices(40))
		env.strategy.Ideas = longIdea
		env.boot(t)
		env.tick(t)
		d := env.app.Dashboard()
		if len(d.Positions) != 1 || d.Positions[0].Market != "BTC/EUR" || d.Positions[0].Status != "open" {
			t.Fatalf("expected one open BTC position, got %+v", d.Positions)
		}
		if env.live.ExecutedCount() != 0 {
			t.Errorf("paper market must not route to Kraken")
		}
		if !env.activityContains("auto-executed (filled)") {
			t.Errorf("expected auto-executed activity")
		}
	})

	t.Run("GIVEN a tick already evaluating WHEN the emergency stop is pressed THEN that tick's signals are not traded or queued", func(t *testing.T) {
		// A slow strategy (the LLM over many markets) can still be evaluating
		// when the operator presses the stop.
		for _, mode := range []string{"fully_automated", "semi_automated"} {
			env := newEnv(t, func(s *config.Settings) { s.TradingMode = mode }, seedPrices(40))
			env.strategy.Ideas = func(in strategy.Input) []domain.TradeIdea {
				env.app.ActivateEmergencyStop(context.Background())
				return longIdea(in)
			}
			env.boot(t)
			env.tick(t)
			if n := len(env.app.Dashboard().Positions); n != 0 {
				t.Errorf("%s: a stopped bot must not open positions, got %d", mode, n)
			}
			if n := len(env.app.PendingApprovals()); n != 0 {
				t.Errorf("%s: a stopped bot must not queue approvals, got %d", mode, n)
			}
		}
	})

	t.Run("GIVEN the live environment and a live market WHEN a signal fires THEN the order routes to Kraken", func(t *testing.T) {
		env := newEnv(t, func(s *config.Settings) {
			s.TradingMode = "fully_automated"
			s.TradingEnvironment = "live"
		}, seedPrices(40))
		env.strategy.Ideas = longIdea
		env.boot(t)
		env.app.control.SetMarketLive("BTC/EUR", true)
		env.tick(t)
		if env.live.ExecutedCount() != 1 {
			t.Errorf("expected one live order, got %d", env.live.ExecutedCount())
		}
		for _, p := range env.app.Dashboard().Positions {
			if p.Environment == string(domain.EnvPaper) {
				t.Errorf("live orders must not create paper positions, got %+v", p)
			}
		}
	})

	t.Run("GIVEN the paper environment and a market toggled live WHEN a signal fires THEN no real order is sent and it trades on paper", func(t *testing.T) {
		env := newEnv(t, func(s *config.Settings) { s.TradingMode = "fully_automated" }, seedPrices(40))
		env.strategy.Ideas = longIdea
		env.boot(t)
		env.app.control.SetMarketLive("BTC/EUR", true)
		env.tick(t)
		if env.live.ExecutedCount() != 0 {
			t.Fatalf("paper environment must never send orders to Kraken, got %d", env.live.ExecutedCount())
		}
		if len(env.app.Dashboard().Positions) != 1 {
			t.Errorf("expected the signal to open a paper position instead")
		}
		if !env.activityContains("TRADING_ENVIRONMENT=paper") {
			t.Errorf("expected a warning that live routing was blocked")
		}
	})

	t.Run("GIVEN fully automated mode WHEN confidence is below threshold THEN no trade and no buffered signal", func(t *testing.T) {
		env := newEnv(t, func(s *config.Settings) { s.TradingMode = "fully_automated"; s.MinSignalConfidence = 0.95 }, seedPrices(40))
		env.strategy.Ideas = func(strategy.Input) []domain.TradeIdea {
			return []domain.TradeIdea{apptest.Idea(strategy.Basic1hID, "BTC/EUR", domain.Long, 0.5)}
		}
		env.boot(t)
		env.tick(t)
		d := env.app.Dashboard()
		if len(d.Positions) != 0 || len(d.Signals) != 0 {
			t.Errorf("low-confidence signal must be skipped entirely: %+v / %+v", d.Positions, d.Signals)
		}
		if !env.activityContains("below threshold") {
			t.Errorf("expected below-threshold activity")
		}
	})

	t.Run("GIVEN a signal the risk engine blocks WHEN it fires THEN a rejection is recorded", func(t *testing.T) {
		env := newEnv(t, func(s *config.Settings) { s.TradingMode = "fully_automated" }, seedPrices(40))
		env.strategy.Ideas = longIdea
		env.boot(t)
		env.tick(t) // opens a long
		env.tick(t) // a second long on the same pair is blocked by risk
		d := env.app.Dashboard()
		if len(d.RiskRejections) == 0 {
			t.Fatalf("expected a risk rejection")
		}
		stored, _ := env.repo.GetRecentRiskRejections(context.Background(), 10)
		if len(stored) == 0 {
			t.Errorf("risk rejection should be persisted")
		}
		if len(d.Signals) != 1 {
			t.Errorf("signals for the same market replace older ones, got %d", len(d.Signals))
		}
	})
}

// TestStopLoss covers automatic stop-loss closes in the strategy loop.
func TestStopLoss(t *testing.T) {
	t.Run("GIVEN an open long WHEN price falls past the stop THEN it is closed and recorded", func(t *testing.T) {
		env := newEnv(t, func(s *config.Settings) { s.TradingMode = "fully_automated" }, seedPrices(40))
		fired := false
		env.strategy.Ideas = func(strategy.Input) []domain.TradeIdea {
			if fired {
				return nil
			}
			fired = true
			return []domain.TradeIdea{apptest.Idea(strategy.Basic1hID, "BTC/EUR", domain.Long, 0.9)}
		}
		env.boot(t)
		env.tick(t)
		if len(env.app.Dashboard().Positions) != 1 {
			t.Fatalf("expected an open position before the drop")
		}
		env.market.SetPrice("BTC/EUR", 40000)
		env.tick(t)
		if len(env.app.Dashboard().Positions) != 0 {
			t.Errorf("position should be closed by the stop-loss")
		}
		if !env.activityContains("STOP-LOSS: BTC/EUR") {
			t.Errorf("expected STOP-LOSS activity")
		}
		closed, _ := env.repo.GetClosedTrades(context.Background(), 10)
		if len(closed) != 1 || closed[0].PnL >= 0 {
			t.Errorf("expected one losing closed trade, got %+v", closed)
		}
	})

	t.Run("GIVEN an open long past its stop WHEN the tick's database writes fail THEN the stop still closes it", func(t *testing.T) {
		env := newEnv(t, func(s *config.Settings) { s.TradingMode = "fully_automated" }, seedPrices(40))
		fired := false
		env.strategy.Ideas = func(strategy.Input) []domain.TradeIdea {
			if fired {
				return nil
			}
			fired = true
			return []domain.TradeIdea{apptest.Idea(strategy.Basic1hID, "BTC/EUR", domain.Long, 0.9)}
		}
		env.boot(t)
		env.tick(t)
		env.market.SetPrice("BTC/EUR", 40000)

		// A cancelled context makes every write of this tick fail.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := env.app.strategyTick(ctx); err == nil {
			t.Fatalf("expected the tick to report the failed writes")
		}

		if n := len(env.app.paper.OpenPositions()); n != 0 {
			t.Errorf("the stop-loss must not depend on housekeeping writes, %d position(s) still open", n)
		}
	})
}

// TestSignalBuffer covers the rolling newest-first signal buffer.
func TestSignalBuffer(t *testing.T) {
	t.Run("GIVEN more signals than the buffer holds WHEN merged THEN at most 12 newest are kept", func(t *testing.T) {
		var existing []SignalView
		for i := 0; i < 20; i++ {
			existing = append(existing, SignalView{Market: "M" + string(rune('A'+i))})
		}
		merged := mergeSignals([]SignalView{{Market: "MA"}, {Market: "NEW"}}, existing)
		if len(merged) != signalBufferMax || merged[0].Market != "MA" || merged[1].Market != "NEW" {
			t.Errorf("unexpected merge result: %+v", merged)
		}
		count := 0
		for _, s := range merged {
			if s.Market == "MA" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("stale signal for the same market must be dropped")
		}
	})
}

// TestReadinessHelpers ports the main.py helper tests from test_strategy_bdd.py.
func TestReadinessHelpers(t *testing.T) {
	t.Run("GIVEN the warm-up constants WHEN inspected THEN the lookback meets the MACD minimum and fixed warm-up is disabled", func(t *testing.T) {
		if strategy.LookbackTicks < 34 {
			t.Errorf("LookbackTicks must be >= 34 (slow EMA 26 + signal 9 - 1)")
		}
		if TradeWarmupTicks != 0 {
			t.Errorf("fixed trade warm-up should stay disabled")
		}
	})

	t.Run("GIVEN LLM strategy inputs WHEN readiness is checked THEN both price history and news are required", func(t *testing.T) {
		ready := make([]float64, 10)
		md := func(hist []float64) map[string]strategy.MarketData {
			return map[string]strategy.MarketData{"BTC/EUR": {Price: 100, PriceHistory: hist}}
		}
		someNews := []storage.NewsEntry{{ID: "article-1", Title: "Market update"}}
		if llmStrategyDataReady(map[string]strategy.MarketData{}, someNews, 10) {
			t.Errorf("no markets must not be ready")
		}
		if llmStrategyDataReady(md(ready), nil, 10) {
			t.Errorf("no news must not be ready")
		}
		if llmStrategyDataReady(md(ready[:9]), someNews, 10) {
			t.Errorf("short history must not be ready")
		}
		if !llmStrategyDataReady(md(ready), someNews, 10) {
			t.Errorf("full history plus news should be ready")
		}
	})

	t.Run("GIVEN briefing outlooks WHEN the market score is extracted THEN it is clamped and falls back to neutral", func(t *testing.T) {
		if briefingSentimentForMarket(nil, "BTC/EUR") != 0 {
			t.Errorf("nil briefing must be neutral")
		}
		legacy := legacyBriefing(map[string]any{"BTC/EUR": "bullish"})
		if briefingSentimentForMarket(legacy, "BTC/EUR") != 0 {
			t.Errorf("legacy string outlook must be neutral")
		}
		strong := legacyBriefing(map[string]any{"BTC/EUR": map[string]any{"bias": "bullish", "score": 0.7}})
		if s := briefingSentimentForMarket(strong, "BTC/EUR"); s <= 0 || s > 1 {
			t.Errorf("expected a positive clamped score, got %v", s)
		}
		if briefingSentimentForMarket(strong, "ETH/EUR") != 0 {
			t.Errorf("missing market must be neutral")
		}
	})
}

// TestBackgroundLoopErrorHandling ports test_background_loop_error_handling_bdd.py.
func TestBackgroundLoopErrorHandling(t *testing.T) {
	t.Run("GIVEN a loop body panics WHEN guarded THEN an ERROR record with the stack trace is logged", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		err := env.app.guarded("Strategy loop error", func() error {
			panic("synthetic error for BDD test")
		})
		if err == nil {
			t.Fatalf("a recovered panic should be returned as an error")
		}
		record := lastLogRecord(t, env.logs)
		if record["level"] != "ERROR" || record["msg"] != "Strategy loop error" {
			t.Errorf("expected an ERROR 'Strategy loop error' record, got %v", record)
		}
		exc, _ := record["exception"].(string)
		if !strings.Contains(exc, "synthetic error for BDD test") || !strings.Contains(exc, "goroutine") {
			t.Errorf("exception must carry the message and stack, got %q", exc)
		}
	})

	t.Run("GIVEN a loop body returns an error WHEN guarded THEN it is logged at ERROR with the error text", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		_ = env.app.guarded("News loop error", func() error { return errTest("feed exploded") })
		record := lastLogRecord(t, env.logs)
		if record["level"] != "ERROR" || !strings.Contains(record["error"].(string), "feed exploded") {
			t.Errorf("expected an ERROR record with the error, got %v", record)
		}
	})

	t.Run("GIVEN a guarded body succeeds WHEN run THEN nothing is logged at ERROR", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		env.logs.Reset()
		if err := env.app.guarded("Strategy loop error", func() error { return nil }); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if strings.Contains(env.logs.String(), `"level":"ERROR"`) {
			t.Errorf("no ERROR expected: %s", env.logs.String())
		}
	})
}

// TestNewsCycle covers one iteration of the news loop.
func TestNewsCycle(t *testing.T) {
	t.Run("GIVEN news adapters with articles WHEN the news cycle runs THEN news is cached, stored and logged", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		now := time.Now().UTC()
		env.app.newsAdapters = []news.Adapter{apptest.News{Items: []domain.NewsItem{
			{ID: "a1", Source: "Fake", Title: "Older", PublishedAt: now.Add(-time.Hour), URL: "https://x/1"},
			{ID: "a2", Source: "Fake", Title: "Newer", PublishedAt: now, URL: "https://x/2"},
		}}}
		if err := env.app.newsCycle(context.Background()); err != nil {
			t.Fatalf("newsCycle: %v", err)
		}
		cached := env.app.News()
		if len(cached) != 2 || cached[0].Title != "Newer" {
			t.Errorf("expected newest-first cached news, got %+v", cached)
		}
		stored, _ := env.repo.GetRecentNews(context.Background(), 10)
		if len(stored) != 2 {
			t.Errorf("news should be upserted, got %d", len(stored))
		}
		if !env.activityContains("News fetched - 2 articles") {
			t.Errorf("expected the news-fetched activity")
		}
		env.app.Wait()
	})
}

// TestRunStopsCleanly covers background loop shutdown.
func TestRunStopsCleanly(t *testing.T) {
	t.Run("GIVEN running loops WHEN the context is cancelled THEN Run returns and the stream stops", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			env.app.Run(ctx)
			close(done)
		}()
		time.Sleep(100 * time.Millisecond)
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("Run did not return after cancellation")
		}
		if !env.market.Stopped {
			t.Errorf("market subscription should be stopped")
		}
	})
}

// errTest is a simple error type for tests.
type errTest string

// Error returns the message.
func (e errTest) Error() string { return string(e) }

// lastLogRecord returns the last JSON log record written to buf.
func lastLogRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &record); err != nil {
		t.Fatalf("bad log line %q: %v", lines[len(lines)-1], err)
	}
	return record
}

// LookbackTicksForTest exposes the indicator lookback used by the warm-up gate.
func LookbackTicksForTest() int { return strategy.LookbackTicks }

// legacyBriefing builds a briefing from raw (possibly legacy) outlook JSON.
func legacyBriefing(raw map[string]any) *llm.MarketBriefing {
	return &llm.MarketBriefing{MarketOutlooks: llm.NormaliseMarketOutlooks(raw, nil)}
}

// TestRegisteredStrategies ports the main.py strategy-dispatch checks.
func TestRegisteredStrategies(t *testing.T) {
	t.Run("GIVEN the registered strategies WHEN inspected THEN LLM context and price-history needs match the strategy type", func(t *testing.T) {
		list := strategy.All(testSettings())
		basic := strategy.ByID(list, strategy.Basic1hID)
		combined := strategy.ByID(list, strategy.BasicAndLLM1hID)
		llmOnly := strategy.ByID(list, strategy.LLMOnlyID)
		if basic == nil || combined == nil || llmOnly == nil {
			t.Fatalf("all three strategies must be registered")
		}
		if basic.UsesLLM() || !combined.UsesLLM() || !llmOnly.UsesLLM() {
			t.Errorf("only the LLM-backed strategies should report LLM use")
		}
		if combined.RequiredPriceTicks() != 0 {
			t.Errorf("indicator-plus-LLM reads candles and waits for its own candle warm-up, not price ticks")
		}
		if llmOnly.RequiredPriceTicks() <= 0 || llmOnly.RequiredPriceTicks() > strategy.LookbackTicks+1 {
			t.Errorf("LLM-only needs a short momentum window, got %d", llmOnly.RequiredPriceTicks())
		}
		if _, ok := StrategyLabels[basic.ID()]; !ok {
			t.Errorf("every strategy needs a dashboard label")
		}
	})
}
