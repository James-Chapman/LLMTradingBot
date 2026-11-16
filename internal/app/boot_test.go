package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/app/apptest"
	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/news"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
	"github.com/James-Chapman/LLMTradingBot/internal/secret"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
	"github.com/James-Chapman/LLMTradingBot/internal/universe"
)

// flakyMarket is a fake Kraken market whose pair map is unavailable for the
// first failures calls to ValidateSymbols, and whose OHLC call can panic.
type flakyMarket struct {
	*apptest.Market
	mu          sync.Mutex
	failures    int
	validations int
	ohlcPanics  bool
	ohlcCalls   atomic.Int32
}

// ValidateSymbols returns nothing while failures remain, then the fake's markets.
func (m *flakyMarket) ValidateSymbols(ctx context.Context, symbols []string) []string {
	m.mu.Lock()
	m.validations++
	failing := m.validations <= m.failures
	m.mu.Unlock()
	if failing {
		return []string{}
	}
	return m.Market.ValidateSymbols(ctx, symbols)
}

// GetOHLC panics when ohlcPanics is set, otherwise serves the fake's candles.
func (m *flakyMarket) GetOHLC(ctx context.Context, symbol string, interval, limit int) ([]domain.Candle, error) {
	m.ohlcCalls.Add(1)
	if m.ohlcPanics {
		panic("ohlc exploded")
	}
	return m.Market.GetOHLC(ctx, symbol, interval, limit)
}

// Validations returns how often the pair map was asked.
func (m *flakyMarket) Validations() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.validations
}

// newBootApp builds an app over a fresh database with the flaky market and
// short loop intervals; seed runs before the app is created.
func newBootApp(t *testing.T, market *flakyMarket, now func() time.Time, seed func(*storage.Repository)) *App {
	t.Helper()
	settings := testSettings()
	repo := newTestRepo(t)
	if seed != nil {
		seed(repo)
	}
	strat := &apptest.Strategy{StrategyID: strategy.Basic1hID}
	a, err := NewWithOptions(context.Background(), settings, repo, Options{
		Market:          market,
		Live:            &apptest.Live{},
		LLM:             &apptest.LLM{},
		Universe:        apptest.Universe{Markets: settings.FixedMarkets},
		NewsAdapters:    []news.Adapter{},
		StrategyFactory: func(config.Settings) []strategy.Strategy { return []strategy.Strategy{strat} },
		Activity:        observability.NewActivityLog(200),
		Logger:          slog.New(slog.NewJSONHandler(io.Discard, nil)),
		SecretBox:       secret.Reversible(),
		Now:             now,
		Intervals: Intervals{
			BootRetry:       5 * time.Millisecond,
			OHLCRefresh:     time.Millisecond,
			OHLCMarketGap:   time.Millisecond,
			OHLCIntervalGap: time.Millisecond,
			OHLCWait:        time.Millisecond,
			Equity:          time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	a.control.SelectStrategy(strat.StrategyID)
	return a
}

// activityHas reports whether any recent activity message contains text.
func activityHas(a *App, text string) bool {
	for _, entry := range a.activity.Recent(200) {
		if strings.Contains(entry.Message, text) {
			return true
		}
	}
	return false
}

// eventually polls cond for up to five seconds.
func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestBootRetry covers starting while Kraken's pair map cannot be loaded.
func TestBootRetry(t *testing.T) {
	prices := map[string]float64{"BTC/EUR": 50000, "ETH/EUR": 3000}

	t.Run("GIVEN Kraken's pair map fails on the first attempt WHEN the strategy loop boots THEN it retries and the markets become active", func(t *testing.T) {
		market := &flakyMarket{Market: apptest.NewMarket(prices), failures: 1}
		a := newBootApp(t, market, nil, nil)

		a.bootStrategyLoop(context.Background())

		if got := a.ActiveMarkets(); len(got) != 2 {
			t.Fatalf("expected both markets active after the retry, got %v", got)
		}
		if market.Validations() < 2 {
			t.Errorf("expected a second validation attempt, got %d", market.Validations())
		}
		if !activityHas(a, "No markets could be validated") {
			t.Error("expected an activity entry explaining the retry")
		}
		if activityHas(a, "All markets disabled") {
			t.Error("the misleading all-disabled message must not appear")
		}
	})

	t.Run("GIVEN Kraken stays unreachable WHEN the bot shuts down THEN the boot stops retrying and returns", func(t *testing.T) {
		market := &flakyMarket{Market: apptest.NewMarket(prices), failures: 1 << 30}
		a := newBootApp(t, market, nil, nil)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})

		go func() { a.bootStrategyLoop(ctx); close(done) }()
		eventually(t, func() bool { return market.Validations() >= 2 })
		cancel()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("boot kept retrying after cancellation")
		}
		if len(a.ActiveMarkets()) != 0 {
			t.Errorf("no market should be active, got %v", a.ActiveMarkets())
		}
	})
}

// TestWarmUpCount covers the warm-up progress shown by the indicator strategy.
func TestWarmUpCount(t *testing.T) {
	t.Run("GIVEN the first market is disabled and another has a full history WHEN a tick runs THEN warm-up is complete and the strategy evaluates", func(t *testing.T) {
		// Only ETH has history; BTC (the first active market) has none.
		seedETH := func(repo *storage.Repository) {
			for i := 0; i < strategy.LookbackTicks+1; i++ {
				_ = repo.SavePriceTick(context.Background(), "ETH/EUR", 3000, nil)
			}
		}
		market := &flakyMarket{Market: apptest.NewMarket(map[string]float64{"BTC/EUR": 50000, "ETH/EUR": 3000})}
		a := newBootApp(t, market, nil, seedETH)
		a.bootStrategyLoop(context.Background())
		a.control.DisableMarket("BTC/EUR")

		if _, err := a.strategyTick(context.Background()); err != nil {
			t.Fatal(err)
		}

		if activityHas(a, "warming up") {
			t.Error("the tick must not report warming up when an enabled market has a full history")
		}
		if !activityHas(a, "Strategies evaluated") {
			t.Error("expected the strategy to evaluate")
		}
	})
}

// TestLoopPanicGuard covers loops that must survive a panic in one iteration.
func TestLoopPanicGuard(t *testing.T) {
	prices := map[string]float64{"BTC/EUR": 50000, "ETH/EUR": 3000}

	t.Run("GIVEN an OHLC fetch that panics WHEN the OHLC loop runs THEN it logs and keeps cycling", func(t *testing.T) {
		market := &flakyMarket{Market: apptest.NewMarket(prices), ohlcPanics: true}
		a := newBootApp(t, market, nil, nil)
		a.bootStrategyLoop(context.Background())
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})

		go func() { a.ohlcLoop(ctx); close(done) }()
		eventually(t, func() bool { return market.ohlcCalls.Load() >= 3 })
		cancel()
		<-done
	})

	t.Run("GIVEN an equity snapshot that panics WHEN the equity ticker runs THEN it logs and keeps ticking", func(t *testing.T) {
		var exploding atomic.Bool
		var calls atomic.Int32
		now := func() time.Time {
			if exploding.Load() {
				calls.Add(1)
				panic("clock exploded")
			}
			return time.Now().UTC()
		}
		a := newBootApp(t, &flakyMarket{Market: apptest.NewMarket(prices)}, now, nil)
		a.mu.Lock()
		a.currentPrices = map[string]float64{"BTC/EUR": 50000}
		a.mu.Unlock()
		exploding.Store(true)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})

		go func() { a.equityTickerLoop(ctx); close(done) }()
		eventually(t, func() bool { return calls.Load() >= 3 })
		cancel()
		<-done
	})
}

// TestStoredSettingsOutOfBounds covers databases saved before the bounds existed.
func TestStoredSettingsOutOfBounds(t *testing.T) {
	t.Run("GIVEN a stored stop-loss of 0 and a huge LLM timeout WHEN settings load THEN the defaults are used with a warning each", func(t *testing.T) {
		ctx := context.Background()
		repo := newTestRepo(t)
		_ = repo.SaveSettings(ctx, map[string]string{"STOP_LOSS_PCT": "0", "LMSTUDIO_TIMEOUT": "99999999999"})

		s, warnings, err := LoadSettings(ctx, repo, secret.Reversible(), config.Bootstrap{})

		if err != nil {
			t.Fatal(err)
		}
		d := config.Defaults()
		if s.StopLossPct != d.StopLossPct || s.LMStudioTimeout != d.LMStudioTimeout {
			t.Fatalf("expected defaults, got stop %v timeout %v", s.StopLossPct, s.LMStudioTimeout)
		}
		if len(warnings) != 2 {
			t.Fatalf("expected two warnings, got %v", warnings)
		}
	})
}

// TestBlankSecret covers typing only spaces into a secret field.
func TestBlankSecret(t *testing.T) {
	t.Run("GIVEN a saved API key WHEN only whitespace is saved over it THEN the save is refused and the key unchanged", func(t *testing.T) {
		ctx := context.Background()
		env := newEnv(t, nil, nil)
		if _, err := env.app.UpdateSettings(ctx, map[string]string{"OPENAI_API_KEY": "sk-1"}); err != nil {
			t.Fatal(err)
		}

		_, err := env.app.UpdateSettings(ctx, map[string]string{"OPENAI_API_KEY": "   "})

		if !isClientError(err) {
			t.Fatalf("expected a client error, got %v", err)
		}
		if env.app.savedSettings().OpenAIAPIKey != "sk-1" || !fieldView(t, env.app.SettingsView(), "OPENAI_API_KEY").IsSet {
			t.Fatal("the stored key must be unchanged")
		}
	})
}

// TestAccessToken covers the token that guards a dashboard reachable from other machines.
func TestAccessToken(t *testing.T) {
	ctx := context.Background()
	box := secret.Reversible()

	t.Run("GIVEN a new database WHEN the token loads THEN one is generated and stored sealed", func(t *testing.T) {
		repo := newTestRepo(t)

		tok, created, err := LoadAccessToken(ctx, repo, box)

		if err != nil || !created {
			t.Fatalf("expected a new token, got created=%v err=%v", created, err)
		}
		if len(tok.Token()) < 22 || strings.ContainsAny(tok.Token(), "+/= ") {
			t.Fatalf("expected a long URL-safe token, got %q", tok.Token())
		}
		stored, _ := repo.LoadSettings(ctx)
		raw := stored[AccessTokenKey]
		if !strings.HasPrefix(raw, secret.SealedPrefix) || strings.Contains(raw, tok.Token()) {
			t.Fatalf("the token must be stored sealed, got %q", raw)
		}
	})

	t.Run("GIVEN a stored token WHEN the bot restarts THEN the same token is used", func(t *testing.T) {
		repo := newTestRepo(t)
		first, _, _ := LoadAccessToken(ctx, repo, box)

		again, created, err := LoadAccessToken(ctx, repo, box)

		if err != nil || created || again.Token() != first.Token() {
			t.Fatalf("expected the stored token reused, got created=%v err=%v", created, err)
		}
	})

	t.Run("GIVEN a token WHEN rotated THEN a new one is in use and survives a restart", func(t *testing.T) {
		repo := newTestRepo(t)
		tok, _, _ := LoadAccessToken(ctx, repo, box)
		old := tok.Token()

		fresh, err := tok.Rotate(ctx)

		if err != nil || fresh == old || tok.Token() != fresh {
			t.Fatalf("rotation failed: %v", err)
		}
		reloaded, _, _ := LoadAccessToken(ctx, repo, box)
		if reloaded.Token() != fresh {
			t.Fatal("the rotated token must be stored")
		}
	})

	t.Run("GIVEN a stored token WHEN the settings are listed THEN the token is not among them", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		tok, _, _ := LoadAccessToken(ctx, env.repo, box)

		raw, _ := json.Marshal(env.app.SettingsView())

		if strings.Contains(string(raw), tok.Token()) || strings.Contains(string(raw), AccessTokenKey) {
			t.Fatal("the access token must not be listed in the settings")
		}
		if _, err := env.app.UpdateSettings(ctx, map[string]string{AccessTokenKey: "mine"}); !isClientError(err) {
			t.Fatalf("the token must not be settable as a dashboard setting, got %v", err)
		}
	})
}

// TestUniverseSettings covers MAX_ETH_ECOSYSTEM_COINS=0 and the base currency.
func TestUniverseSettings(t *testing.T) {
	// A closed local port makes CoinGecko unreachable, so the resolver uses its
	// static fallback coin list - no real network access.
	offline := universe.Options{BaseURL: "http://127.0.0.1:1"}

	t.Run("GIVEN max dynamic markets 0 WHEN the universe resolves THEN only the fixed markets are traded", func(t *testing.T) {
		s := testSettings()
		s.MaxEthEcosystemCoins = 0

		snap := newUniverse(s, offline).ResolveUniverse(context.Background())

		if len(snap.DynamicMarkets) != 0 || len(snap.FixedMarkets) != 2 {
			t.Fatalf("expected fixed markets only, got %+v", snap)
		}
	})

	t.Run("GIVEN base currency USD WHEN the universe resolves THEN dynamic markets are quoted in USD", func(t *testing.T) {
		s := testSettings()
		s.BaseCurrency = "USD"

		snap := newUniverse(s, offline).ResolveUniverse(context.Background())

		if len(snap.DynamicMarkets) == 0 {
			t.Fatal("expected fallback dynamic markets")
		}
		for _, m := range snap.DynamicMarkets {
			if !strings.HasSuffix(m, "/USD") {
				t.Errorf("dynamic market %s is not quoted in USD", m)
			}
		}
	})

	t.Run("GIVEN the active and configured markets WHEN a market is looked up THEN only known markets are found", func(t *testing.T) {
		env := newEnv(t, nil, nil)

		if !env.app.KnownMarket("BTC/EUR") {
			t.Error("a configured market must be known before boot")
		}
		env.boot(t)
		if !env.app.KnownMarket("ETH/EUR") || env.app.KnownMarket("NOPE/EUR") || env.app.KnownMarket("") {
			t.Error("only active or configured markets are known")
		}
	})
}

// TestMomentumLookbackFitsHistory covers the bound on MOMENTUM_LOOKBACK_TICKS.
func TestMomentumLookbackFitsHistory(t *testing.T) {
	t.Run("GIVEN the kept price history WHEN the config bound is compared THEN the longest lookback can fill", func(t *testing.T) {
		if config.PriceHistoryTicks != priceHistoryMax {
			t.Fatalf("config.PriceHistoryTicks (%d) must match priceHistoryMax (%d)", config.PriceHistoryTicks, priceHistoryMax)
		}
	})
}
