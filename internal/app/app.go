// Package app owns the trading bot's runtime state and background loops —
// the Go port of the module globals, startup restores and asyncio tasks in
// backend/main.py. The HTTP layer (package server) reads and mutates this
// state only through App methods.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/approval"
	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/control"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/kraken"
	"github.com/James-Chapman/LLMTradingBot/internal/learner"
	"github.com/James-Chapman/LLMTradingBot/internal/livebook"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
	"github.com/James-Chapman/LLMTradingBot/internal/news"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
	"github.com/James-Chapman/LLMTradingBot/internal/paper"
	"github.com/James-Chapman/LLMTradingBot/internal/portfolio"
	"github.com/James-Chapman/LLMTradingBot/internal/risk"
	"github.com/James-Chapman/LLMTradingBot/internal/secret"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
	"github.com/James-Chapman/LLMTradingBot/internal/universe"
)

// Version is the application version reported by /health and the dashboard.
const Version = "0.17.0"

// Name is the application display name.
const Name = "Kraken Trading Bot"

// Runtime limits ported from main.py.
const (
	// TradeWarmupTicks is the fixed post-start trade delay; disabled because
	// readiness is data-driven (indicator lookback and LLM data checks).
	TradeWarmupTicks = 0

	signalBufferMax        = 12   // signals kept in the dashboard panel
	riskRejectionsMax      = 50   // rolling risk rejections kept in memory
	riskRejectionsShown    = 20   // rejections included in the dashboard
	priceHistoryMax        = 200  // price ticks per market (200 min at the default 60 s interval)
	equityHistoryMax       = 1440 // equity points (4 h at 10 s)
	latestNewsMax          = 60   // cached news articles
	restoredPricesPerSym   = 60   // price ticks restored per market at start-up
	signalOutcomesForSeed  = 1000 // outcomes used to seed the learner
	closedTradesForReflect = 50   // closed trades sent to the reflection prompt
	activityShown          = 60   // activity entries in the dashboard
	activityKeep           = 2000 // activity rows kept in the DB
	ohlcCandleLimit        = 100  // candles fetched per OHLC refresh
)

// StrategyLabels are the dashboard labels for each strategy ID.
var StrategyLabels = map[string]string{
	strategy.BasicID:       "Basic Strategy (Just market indicators, no LLM)",
	strategy.BasicAndLLMID: "Basic + LLM Combined (LLM analysis augments indicator-based signals)",
	strategy.LLMOnlyID:     "All LLM (LLM generates signals from price, indicators, and news — no indicator consensus gate)",
	strategy.TrendID:       "Trend Strategy (4-hour EMA trend following, no LLM)",
	strategy.TrendAndLLMID: "Trend + LLM (4-hour EMA trend; the LLM reviews each entry)",

	strategy.DonchianID:       "Donchian Breakout (4-hour 90/30 channel breakout, no LLM)",
	strategy.DonchianAndLLMID: "Donchian Breakout + LLM (the LLM reviews each breakout)",
	strategy.RSIDipID:         "RSI Dip (buys 4-hour capitulation dips, sells the bounce, no LLM)",
	strategy.RSIDipAndLLMID:   "RSI Dip + LLM (the LLM reviews each dip)",
	strategy.MACDID:           "MACD Momentum (slow 4-hour MACD 36/78/27, no LLM)",
	strategy.MACDAndLLMID:     "MACD Momentum + LLM (the LLM reviews each entry)",
	strategy.SqueezeID:        "Bollinger Squeeze (4-hour squeeze breakout, no LLM)",
	strategy.SqueezeAndLLMID:  "Bollinger Squeeze + LLM (the LLM reviews each breakout)",
	strategy.RotationID:       "Relative Strength Rotation (holds the 3 strongest markets by 14-day return, no LLM)",
	strategy.RotationAndLLMID: "Relative Strength Rotation + LLM (the LLM reviews each entry)",
}

// MarketSource is the Kraken public market-data surface the app uses.
type MarketSource interface {
	ValidateSymbols(ctx context.Context, symbols []string) []string
	GetTickersBatch(ctx context.Context, symbols []string) (map[string]domain.MarketSnapshot, error)
	GetOHLC(ctx context.Context, symbol string, intervalMinutes, limit int) ([]domain.Candle, error)
	Start(symbols []string, cb func(domain.MarketSnapshot))
	Stop()
}

// LiveEngine is the Kraken live execution surface the app uses.
type LiveEngine interface {
	Execute(ctx context.Context, intent domain.ExecutionIntent, opts kraken.ExecuteOptions) (domain.OrderRecord, string)
	GetAccountSnapshot(ctx context.Context, quoteCurrency string) (*kraken.AccountSnapshot, error)
	// ReconcilePendingOrders returns the live orders that finished since the
	// last call. tracked are the live book's own pending orders: any of them
	// that the order ledger does not list as pending is looked up by client order id.
	ReconcilePendingOrders(ctx context.Context, tracked []kraken.PendingLiveOrder) []kraken.OrderUpdate
	// MinOrderSize returns Kraken's minimum order volume for market, if known.
	MinOrderSize(ctx context.Context, market string) (float64, bool)
}

// LLMClient is the LLM client surface: a chat client plus backend switching.
type LLMClient interface {
	llm.Client
	ActiveBackend() string
	RecheckPrimary(ctx context.Context)
}

// UniverseSource resolves the tradable markets.
type UniverseSource interface {
	ResolveUniverse(ctx context.Context) domain.UniverseSnapshot
}

// Intervals are the background loop timings; zero values select main.py's.
type Intervals struct {
	Strategy        time.Duration // 0: STRATEGY_INTERVAL_SECONDS from the settings, read each tick
	News            time.Duration // 5 min between news fetches
	ReflectionDelay time.Duration // 2 min before the first reflection
	Reflection      time.Duration // 1 h between reflections
	OHLCRefresh     time.Duration // 120 s between OHLC refresh cycles
	OHLCMarketGap   time.Duration // 2.5 s between markets (Kraken rate limit)
	OHLCIntervalGap time.Duration // 1 s between the 5- and 15-minute fetches
	OHLCWait        time.Duration // 2 s poll while waiting for active markets
	Equity          time.Duration // 10 s between equity snapshots
	BootRetry       time.Duration // 5 s before the first boot retry (doubles up to 5 min)
}

// withDefaults fills zero intervals with the main.py timings.
func (iv Intervals) withDefaults() Intervals {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&iv.News, 300*time.Second)
	def(&iv.ReflectionDelay, 120*time.Second)
	def(&iv.Reflection, time.Hour)
	def(&iv.OHLCRefresh, 120*time.Second)
	def(&iv.OHLCMarketGap, 2500*time.Millisecond)
	def(&iv.OHLCIntervalGap, time.Second)
	def(&iv.OHLCWait, 2*time.Second)
	def(&iv.Equity, 10*time.Second)
	def(&iv.BootRetry, 5*time.Second)
	return iv
}

// Options injects dependencies; nil fields are built from settings.
type Options struct {
	Market          MarketSource
	Live            LiveEngine
	LLM             LLMClient
	Universe        UniverseSource
	NewsAdapters    []news.Adapter // nil: the built-in sources plus the extra feeds from settings
	StrategyFactory func(config.Settings) []strategy.Strategy
	Activity        *observability.ActivityLog // nil: observability.Activity
	Logger          *slog.Logger
	HTTPClient      *http.Client // alert webhook client (default 5 s timeout)
	Now             func() time.Time
	Intervals       Intervals
	SecretBox       secret.Box // seals API keys in the settings table (nil: Windows DPAPI)
}

// App owns every piece of runtime state main.py kept in module globals.
// Mutable fields below mu are guarded by it; the component services
// (paper, control, approvals, risk, learner, analyser, activity) are
// themselves safe for concurrent use. mu is never held across network,
// LLM or exchange calls.
type App struct {
	// settings is what the running components use; saved is what the
	// database holds. They differ only for restart-required settings changed
	// since start-up. Both are guarded by settingsMu; read through cfg().
	settingsMu      sync.RWMutex
	settings        config.Settings
	saved           config.Settings
	saveMu          sync.Mutex // serialises UpdateSettings
	box             secret.Box
	currencySymbol  string
	repo            *storage.Repository
	logger          *slog.Logger
	activity        *observability.ActivityLog
	now             func() time.Time
	httpClient      *http.Client
	intervals       Intervals
	strategyFactory func(config.Settings) []strategy.Strategy

	learner      *learner.PerformanceLearner
	llmClient    LLMClient
	analyser     *llm.LLMAnalyser
	shadow       *strategy.ShadowReviewer // LLM shadow reviews of non-LLM 4-hour entries (shadow.go)
	shadowMu     sync.Mutex               // one shadow batch asks the LLM at a time
	market       MarketSource
	live         LiveEngine
	universe     UniverseSource
	risk         *risk.Engine
	approvals    *approval.Service
	paper        *paper.Engine
	control      *control.State
	newsAdapters []news.Adapter

	// liveBook tracks positions opened by real Kraken orders (live_trading.go).
	liveBook *livebook.Book
	// liveMu serialises every live-routed decision with the order it sends,
	// so two paths (tick, approval, stop, manual close) cannot both enter or exit.
	liveMu sync.Mutex
	// exitBackoff pauses automatic sells of a live position after Kraken
	// rejected one, by position ID. Guarded by liveMu.
	exitBackoff map[string]exitBackoff

	tasks    sync.WaitGroup // market briefing tasks and background alerts
	briefing briefingGate   // one market briefing at a time (news_cycle.go)

	mu               sync.Mutex
	strategies       []strategy.Strategy
	latestSignals    []SignalView // newest first
	currentPrices    map[string]float64
	dashboardVersion int64
	latestSnapshots  map[string]domain.MarketSnapshot
	activeMarkets    []string
	riskRejections   []storage.RiskRejection // newest first
	briefedNewsIDs   map[string]bool
	ohlc5            map[string]OHLCPayload
	ohlc15           map[string]OHLCPayload
	ohlc240          map[string]OHLCPayload // 4-hour trend candles
	sessionTick      int
	priceHistory     map[string][]float64 // oldest first
	equityHistory    []portfolio.EquityPoint
	equityHistoryEnv string // trading environment equityHistory belongs to
	currentEquity    float64
	currentCash      float64
	latestNews       []storage.NewsEntry // newest first
	lossAlertDay     string              // UTC date the daily_loss_limit_hit alert was last sent
	liveStopWarnedAt time.Time           // last "live positions have no automatic stop" warning
	staleWarnedAt    map[string]time.Time
}

// New wires the app from settings with real Kraken, LLM, universe and news clients.
func New(ctx context.Context, settings config.Settings, repo *storage.Repository) (*App, error) {
	return NewWithOptions(ctx, settings, repo, Options{})
}

// NewWithOptions wires the app and performs main.py's start-up restores:
// activity/control/analyser stores, approvals, risk rejections, equity
// history and cash, open positions, risk state, news, learner seed, and the
// initial LLM probe.
func NewWithOptions(ctx context.Context, settings config.Settings, repo *storage.Repository, opts Options) (*App, error) {
	if repo == nil {
		return nil, fmt.Errorf("app: repository is required")
	}
	a := &App{
		settings:        settings,
		saved:           settings,
		box:             opts.SecretBox,
		currencySymbol:  config.CurrencySymbol(settings.BaseCurrency),
		repo:            repo,
		logger:          opts.Logger,
		activity:        opts.Activity,
		now:             opts.Now,
		httpClient:      opts.HTTPClient,
		intervals:       opts.Intervals.withDefaults(),
		strategyFactory: opts.StrategyFactory,
		learner:         learner.New(),
		llmClient:       opts.LLM,
		market:          opts.Market,
		live:            opts.Live,
		universe:        opts.Universe,
		newsAdapters:    opts.NewsAdapters,
		currentPrices:   map[string]float64{},
		exitBackoff:     map[string]exitBackoff{},
		staleWarnedAt:   map[string]time.Time{},
		latestSnapshots: map[string]domain.MarketSnapshot{},
		briefedNewsIDs:  map[string]bool{},
		ohlc5:           map[string]OHLCPayload{},
		ohlc15:          map[string]OHLCPayload{},
		ohlc240:         map[string]OHLCPayload{},
		priceHistory:    map[string][]float64{},
		latestSignals:   []SignalView{},
		riskRejections:  []storage.RiskRejection{},
		currentEquity:   settings.StartingCapital,
		currentCash:     settings.StartingCapital,
	}
	a.applyDefaults()
	a.strategies = a.strategyFactory(settings)

	a.analyser = llm.NewAnalyser(a.llmClient, a.currencySymbol)
	a.shadow = strategy.NewShadowReviewer()
	a.risk = risk.NewEngine(settings, nil)
	a.approvals = approval.NewService(approval.DefaultTTL, approvalStore{repo: repo}, nil)
	a.paper = paper.NewEngine(settings.StartingCapital, paperStore{repo: repo}, nil)
	a.liveBook = livebook.New(liveBookStore{repo: repo})
	a.control = control.NewState(nil)

	// Wire the repository into the components that persist state.
	a.activity.SetStore(repo)
	a.control.SetRepo(controlStore{repo: repo})
	a.analyser.SetStore(briefingStore{repo: repo})
	if _, err := a.approvals.LoadPendingFromRepository(); err != nil {
		a.logger.Warn("Could not reload pending approvals", "error", err.Error())
	}

	if err := a.control.LoadFromDB(); err != nil {
		a.logger.Warn("Could not restore control state", "error", err.Error())
	}
	a.seedTradingSettings()

	if err := a.restoreState(ctx); err != nil {
		return nil, err
	}
	a.analyser.LoadFromDB(ctx)
	a.activity.SeedFromStore()

	a.probeLLM(ctx)
	return a, nil
}

// applyDefaults builds any dependency not injected through Options.
func (a *App) applyDefaults() {
	s := a.settings
	if a.logger == nil {
		a.logger = observability.Logger("main")
	}
	if a.activity == nil {
		a.activity = observability.Activity
	}
	if a.now == nil {
		a.now = func() time.Time { return time.Now().UTC() }
	}
	if a.httpClient == nil {
		a.httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	if a.box == nil {
		a.box = secret.Default()
	}
	if a.strategyFactory == nil {
		a.strategyFactory = strategy.All
	}
	if a.llmClient == nil {
		a.llmClient = llm.NewSwitchingClientFromSettings(s)
	}
	if a.market == nil {
		a.market = kraken.NewMarketAdapter(kraken.MarketAdapterOptions{RESTURL: s.KrakenRESTURL})
	}
	if a.live == nil {
		a.live = kraken.NewExecutionEngine(kraken.ExecutionOptions{
			APIKey:     s.KrakenAPIKey,
			APISecret:  s.KrakenAPISecret,
			RESTURL:    s.KrakenRESTURL,
			Repository: krakenStore{repo: a.repo},
		})
	}
	if a.universe == nil {
		a.universe = defaultUniverse(s)
	}
	if a.newsAdapters == nil {
		// Every source gets an adapter; switched-off ones are skipped per fetch.
		a.newsAdapters = news.BuildAdapters(config.BuiltInNewsFeeds(), s.NewsSources)
	}
}

// fixedUniverse resolves to the configured fixed markets only.
type fixedUniverse struct{ markets []string }

// ResolveUniverse returns the fixed markets with no dynamic additions.
func (f fixedUniverse) ResolveUniverse(context.Context) domain.UniverseSnapshot {
	return domain.UniverseSnapshot{
		FixedMarkets:   append([]string{}, f.markets...),
		DynamicMarkets: []string{},
		ResolverSource: "none",
		ResolvedAt:     time.Now().UTC(),
		Mapping:        map[string]string{},
	}
}

// defaultUniverse returns the CoinGecko-backed resolver, or a fixed-only
// resolver when DYNAMIC_UNIVERSE_SOURCE is "none".
func defaultUniverse(s config.Settings) UniverseSource {
	if strings.EqualFold(strings.TrimSpace(s.DynamicUniverseSource), "none") {
		return fixedUniverse{markets: s.FixedMarkets}
	}
	return newUniverse(s, universe.Options{})
}

// newUniverse builds the dynamic resolver, applying MAX_ETH_ECOSYSTEM_COINS
// as the cap on how many dynamic markets are added (0 = none) and quoting
// them in BASE_CURRENCY. Kraken symbol validation at boot drops any pair
// Kraken does not list.
func newUniverse(s config.Settings, opts universe.Options) *universe.Resolver {
	opts.MaxCoins = s.MaxEthEcosystemCoins
	opts.QuoteCurrency = s.BaseCurrency
	return universe.NewResolver(s.FixedMarkets, "coingecko", opts)
}

// KnownMarket reports whether market belongs to the bot's universe: an
// active (validated) market, a configured fixed market, or one with a saved
// toggle, so a toggle left over from an earlier market list can be undone.
func (a *App) KnownMarket(market string) bool {
	if market == "" {
		return false
	}
	if slices.Contains(a.ActiveMarkets(), market) || slices.Contains(a.cfg().FixedMarkets, market) {
		return true
	}
	snap := a.control.Snapshot()
	return slices.Contains(snap.DisabledMarkets, market) || slices.Contains(snap.LiveMarkets, market)
}

// restoreState reloads the persisted runtime state main.py restored at import time.
func (a *App) restoreState(ctx context.Context) error {
	s := a.settings

	rejections, err := a.repo.GetRecentRiskRejections(ctx, riskRejectionsMax)
	if err != nil {
		return fmt.Errorf("app: load risk rejections: %w", err)
	}
	a.riskRejections = append(a.riskRejections, rejections...)

	// Equity snapshots are kept per environment: the chart and the display
	// values come from the current environment's rows only.
	environment := a.TradingEnvironment()
	history, err := a.repo.GetEquityHistoryFor(ctx, environment, equityHistoryMax)
	if err != nil {
		return fmt.Errorf("app: load equity history: %w", err)
	}
	a.equityHistoryEnv = environment
	if len(history) > 0 {
		for _, p := range history {
			a.equityHistory = append(a.equityHistory, portfolio.EquityPoint{Timestamp: p.Timestamp, Equity: p.Equity})
		}
		a.currentEquity = history[len(history)-1].Equity
	} else {
		a.equityHistory = []portfolio.EquityPoint{{Timestamp: portfolio.FormatISO(a.now()), Equity: s.StartingCapital}}
	}

	// Paper cash is restored only from paper snapshots, whatever the current
	// environment, so a live Kraken balance never becomes paper cash.
	paperCash, err := a.repo.GetLatestCashFor(ctx, string(domain.EnvPaper))
	if err != nil {
		return fmt.Errorf("app: load latest paper cash: %w", err)
	}
	if paperCash != nil {
		a.paper.SetCash(*paperCash)
	}
	if err := a.paper.RestoreFromDB(); err != nil {
		a.logger.Warn("Could not restore open positions", "error", err.Error())
	}
	if environment == string(domain.EnvPaper) {
		// Free cash: open paper shorts reserve their proceeds as margin.
		a.currentCash = a.paper.AvailableCash()
	} else {
		// Last-known Kraken balance until the first live snapshot.
		liveCash, err := a.repo.GetLatestCashFor(ctx, environment)
		if err != nil {
			return fmt.Errorf("app: load latest %s cash: %w", environment, err)
		}
		if liveCash != nil {
			a.currentCash = *liveCash
		}
	}
	// Live positions must survive a restart: a pending or open one blocks a duplicate entry.
	if n, err := a.liveBook.Restore(); err != nil {
		return fmt.Errorf("app: restore live positions: %w", err)
	} else if n > 0 {
		a.logger.Info("Live positions restored", "count", n)
	}

	// Restore the daily loss tracking so the limit survives a restart.
	if state, err := a.repo.LoadRiskState(ctx); err != nil {
		a.logger.Warn("Could not load risk state", "error", err.Error())
	} else if state != nil {
		a.risk.RestoreState(risk.State{
			DailyLoss:        state.DailyLoss,
			DailyStartEquity: state.DailyStartEquity,
			LastResetDate:    state.LastResetDate,
		})
	}

	// Seed the news cache and mark it as already briefed so the first news
	// cycle does not re-brief articles processed before the restart.
	latest, err := a.repo.GetRecentNews(ctx, latestNewsMax)
	if err != nil {
		return fmt.Errorf("app: load recent news: %w", err)
	}
	a.latestNews = latest
	for _, n := range latest {
		a.briefedNewsIDs[n.ID] = true
	}

	outcomes, err := a.repo.GetSignalOutcomes(ctx, "", signalOutcomesForSeed)
	if err != nil {
		return fmt.Errorf("app: load signal outcomes: %w", err)
	}
	a.learner.LoadFromOutcomes(learnerOutcomes(outcomes))

	// Apply live fills recorded just before a crash; runs last so the risk
	// state and learner it updates are already restored.
	a.recoverLiveBook(ctx)
	return nil
}

// probeLLM checks LLM availability once at start-up (main.py's lifespan).
func (a *App) probeLLM(ctx context.Context) {
	if a.llmStatus() == "not_configured" {
		a.logger.Warn("No LLM configured — set the LM Studio or fallback LLM URL under Settings → LLM")
	}
	if !a.llmClient.Probe(ctx) {
		a.logger.Warn("Both LLM backends unavailable — LLM features disabled")
	}
}

// Run starts the background loops and blocks until ctx is cancelled and
// every loop (and any in-flight briefing task) has exited.
func (a *App) Run(ctx context.Context) {
	loops := []func(context.Context){
		a.strategyLoop,
		a.newsLoop,
		a.ohlcLoop,
		a.reflectionLoop,
		a.equityTickerLoop,
	}
	var wg sync.WaitGroup
	for _, loop := range loops {
		wg.Add(1)
		go func(run func(context.Context)) {
			defer wg.Done()
			run(ctx)
		}(loop)
	}
	<-ctx.Done()
	wg.Wait()
	a.market.Stop()
	a.Wait()
}

// Wait blocks until in-flight market briefing tasks have finished.
func (a *App) Wait() {
	a.tasks.Wait()
}

// guarded runs fn, turning a panic into an error, and logs any failure at
// ERROR level. Panics include the stack trace under "exception" (the Go
// equivalent of logging with exc_info=True).
func (a *App) guarded(logMessage string, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
			a.logger.Error(logMessage, "error", err.Error(), "exception", fmt.Sprintf("%v\n%s", r, debug.Stack()))
		}
	}()
	if err = fn(); err != nil {
		a.logger.Error(logMessage, "error", err.Error())
	}
	return err
}

// sleepCtx waits for d; it returns false when ctx was cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// bumpVersion increments the dashboard version; the caller must hold a.mu.
func (a *App) bumpVersionLocked() {
	a.dashboardVersion++
}

// bumpVersion increments the dashboard version.
func (a *App) bumpVersion() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.bumpVersionLocked()
}

// pricesCopy returns a snapshot of the current prices.
func (a *App) pricesCopy() map[string]float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]float64, len(a.currentPrices))
	for k, v := range a.currentPrices {
		out[k] = v
	}
	return out
}

// strategyByID returns the registered strategy for an ID (legacy IDs accepted), or nil.
func (a *App) strategyByID(id string) strategy.Strategy {
	a.mu.Lock()
	list := a.strategies
	a.mu.Unlock()
	return strategy.ByID(list, control.CanonicalStrategyID(id))
}

// llmStatus returns not_configured, available or unavailable for the dashboard.
func (a *App) llmStatus() string {
	if s := a.cfg(); s.LMStudioBaseURL == "" && s.OpenAIBaseURL == "" {
		return "not_configured"
	}
	if a.llmClient.Available() {
		return "available"
	}
	return "unavailable"
}

// recordTradeResult records a realised P&L in the risk engine and persists
// the daily counters so the daily loss limit survives a restart.
func (a *App) recordTradeResult(pnl float64) {
	if err := risk.RecordTradeResultAndPersist(a.risk, riskStore{repo: a.repo}, pnl); err != nil {
		a.logger.Warn("Could not persist risk state", "error", err.Error())
	}
}

// recordClosedOutcome stamps P&L and teaches the learner after a paper or live
// close. notional is the closed trade's entry price × size, so the learner
// scores a percentage return; 0 means unknown (the learner then uses the P&L).
//
// The P&L is stamped on the order first: the stamp is what start-up recovery
// reads to know a close was already recorded, so a crash between the two
// writes can at worst miss this result, never count it twice.
func (a *App) recordClosedOutcome(ctx context.Context, orderID, strategyID, market, direction string, pnl, notional float64) {
	if err := a.repo.UpdateOrderPnL(ctx, orderID, pnl); err != nil {
		a.logger.Warn("Could not stamp order P&L", "order_id", orderID, "error", err.Error())
	}
	a.recordTradeResult(pnl)
	a.learner.RecordOutcomeWithNotional(strategyID, market, direction, pnl, notional)
}

// paperCloseNotional returns the entry price × size of a closed paper
// position, or 0 when its close details are unknown.
func (a *App) paperCloseNotional(positionID string) float64 {
	meta, ok := a.paper.Meta(positionID)
	if !ok || !meta.Closed {
		return 0
	}
	return meta.AvgPriceAtClose * abs(meta.SizeAtClose)
}

// metaStrategyDirection returns a position's strategy and direction with main.py's defaults.
func (a *App) metaStrategyDirection(positionID string) (string, string) {
	strategyID, direction := "unknown", "long"
	if meta, ok := a.paper.Meta(positionID); ok {
		if meta.StrategyID != "" {
			strategyID = meta.StrategyID
		}
		if meta.Direction != "" {
			direction = meta.Direction
		}
	}
	return strategyID, direction
}

// savePaperEquity persists paper cash immediately after a fill so a crash
// between ticks does not lose the trade. It is always a paper snapshot, even
// when a paper fill happens in the live environment.
func (a *App) savePaperEquity(ctx context.Context, prices map[string]float64) {
	eq := a.paper.TotalEquity(prices)
	cash := a.paper.Cash()
	if err := a.repo.SaveEquitySnapshotFor(ctx, string(domain.EnvPaper), eq, cash, eq-cash); err != nil {
		a.logger.Warn("Could not save equity snapshot", "error", err.Error())
	}
}
