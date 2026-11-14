// Package backtest replays stored historical market data through the bot's
// real strategy, risk engine, learner and paper engine, deterministically,
// with a simulated exchange in front of the paper engine that decides fill
// prices and rejections conservatively.
//
// Every 30 seconds of simulated time is one strategy tick, and each tick
// mirrors the live strategy loop (internal/app strategyTick): price update,
// mark and trailing update, stop checks, equity snapshot, warm-up gate,
// strategy evaluation on the shared market-data builder, learner
// adjustment, risk evaluation, the fully automated mode's gates, then the
// order. A decision made at a tick fills on the following bar (no
// look-ahead). The backtest is spot only: it never opens a short.
//
// The package reads public market data from a histdata.Reader only. It must
// never import anything that can place an order, read secrets or open the
// bot's database; deps_test.go enforces that.
package backtest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/histdata"
	"github.com/James-Chapman/LLMTradingBot/internal/learner"
	"github.com/James-Chapman/LLMTradingBot/internal/marketdata"
	"github.com/James-Chapman/LLMTradingBot/internal/paper"
	"github.com/James-Chapman/LLMTradingBot/internal/risk"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// ctxCheckTicks is how often (in ticks) the replay checks for cancellation.
const ctxCheckTicks = 2880

// PreRoll is how much data before the start feeds the candles, the 24-hour
// volume and the last price without trading, so the first tick sees what
// the live bot sees at start-up (100 fifteen-minute candles are 25 hours).
const PreRoll = 25 * time.Hour

// DefaultStaleAfter is the default stale-position threshold.
const DefaultStaleAfter = 24 * time.Hour

// DefaultSettingsSource is Config.SettingsSource when no file was given.
const DefaultSettingsSource = "built-in defaults"

// ErrLLMStrategy is returned for a strategy that needs the LLM.
var ErrLLMStrategy = errors.New("LLM strategies cannot be backtested: there is no historical news archive, " +
	"the model's output is not reproducible, and the model has seen the future of any historical test window; " +
	"run basic_strategy instead")

// Config is one replay run's inputs.
type Config struct {
	// Markets is an explicit market list, in the order the live bot would
	// list them. Exactly one of Markets and Universe is set.
	Markets  []string
	Universe *UniverseRule

	// Start and End bound the run (UTC, multiples of 30 s): ticks run at
	// Start+30s ... End, each seeing the bars that started before it.
	Start, End time.Time

	StartingCapital float64 // 0 uses Settings.StartingCapital
	StrategyID      string
	Settings        config.Settings
	Exchange        ExchangeConfig
	DisableLearner  bool          // sensitivity runs: never adjust confidence
	Seed            int64         // recorded in the result; nothing in the replay is random
	EquityEvery     time.Duration // equity curve sampling; 0 records every tick
	// LearnerSeed, when the learner is on, is recorded into it in order
	// before the first tick: a walk-forward test window starts with what its
	// train window learned. Empty for every other run.
	LearnerSeed []LearnedOutcome

	// StaleAfter is how long a held market may go without a trade before its
	// position is reported stale; 0 means DefaultStaleAfter.
	StaleAfter time.Duration
	// FillEmptyCandles inserts a flat zero-volume candle for every 5- and
	// 15-minute interval without trades (sensitivity of an unverified assumption).
	FillEmptyCandles bool
	// SettingsSource names where Settings came from ("built-in defaults" or
	// the settings file); ExplicitSettings are the keys it (or a variation) set.
	SettingsSource   string
	ExplicitSettings []string
	// CheckData, when set, checks the data cache over the range read before
	// the run; a missing month or an error-level finding refuses the run
	// unless AllowDataGaps downgrades it to a recorded warning.
	CheckData     func(markets []string, from, to time.Time) (histdata.RangeReport, error)
	AllowDataGaps bool

	strategy strategy.Strategy // tests: replaces the registry strategy
	// Provenance, when set, returns the data cache's provenance for a market
	// over [from, to); the result cites it for every market read.
	Provenance func(market string, from, to time.Time) ([]histdata.Provenance, error)

	observe func(TickObservation) // tests: called every tick before evaluation
}

// TickObservation is what one tick saw, for tests.
type TickObservation struct {
	Time            time.Time
	Prices          map[string]float64
	MarketData      map[string]strategy.MarketData // nil while warming up
	Equity          float64
	Cash            float64
	Positions       []domain.PositionRecord
	LearnedOutcomes int
}

// pendingOrder is an order waiting for its fill tick.
type pendingOrder struct {
	log        int // index in Result.Orders
	fillTick   int
	market     string
	buy        bool
	positionID string // exits
	reason     string
	idea       domain.TradeIdea // signal orders
}

// runner holds one replay's state.
type runner struct {
	cfg      Config
	settings config.Settings
	ex       ExchangeConfig
	reader   histdata.Reader
	st       strategy.Strategy

	clock   time.Time
	paper   *paper.Engine
	risk    *risk.Engine
	learner *learner.PerformanceLearner
	learned int

	candidates []string // markets with a feed, sorted
	feeds      map[string]*marketFeed
	active     []string // current universe, in selection order
	activeSet  map[string]bool
	evaluated  map[string]bool
	nextSelect time.Time
	quote      string // universe quote currency

	pending     []*pendingOrder
	pendingMkt  map[string]int
	exitPending map[string]bool
	entryOrder  map[string]int // position id -> index of its entry in Result.Orders
	stale       map[string]int // position id -> index in Result.StalePositions

	orderSeq, ideaSeq, paperSeq int
	lastPoint                   time.Time
	minEquity                   float64
	track                       *tracker // per-tick drawdown, exposure and daily equity
	res                         *Result
}

// Run replays cfg over the bars in reader and returns the raw result. The
// engines log through observability; callers silence INFO logs for long runs.
func Run(ctx context.Context, reader histdata.Reader, cfg Config) (*Result, error) {
	r, err := newRunner(reader, cfg)
	if err != nil {
		return nil, err
	}
	if err := r.run(ctx); err != nil {
		return nil, err
	}
	return r.res, nil
}

// resolveStrategy returns the registry strategy for id, refusing LLM ones.
func resolveStrategy(id string, s config.Settings) (strategy.Strategy, error) {
	st := strategy.ByID(strategy.All(s), id)
	if st == nil {
		return nil, fmt.Errorf("unknown strategy %q", id)
	}
	if st.UsesLLM() {
		return nil, fmt.Errorf("%s: %w", st.ID(), ErrLLMStrategy)
	}
	return st, nil
}

// newRunner validates cfg and builds the engines on simulated time.
func newRunner(reader histdata.Reader, cfg Config) (*runner, error) {
	settings := cfg.Settings
	if cfg.StartingCapital > 0 {
		settings.StartingCapital = cfg.StartingCapital
	}
	switch {
	case reader == nil:
		return nil, errors.New("no data reader")
	case !(settings.StartingCapital > 0):
		return nil, fmt.Errorf("starting capital must be positive, got %v", settings.StartingCapital)
	case !cfg.End.After(cfg.Start):
		return nil, fmt.Errorf("end %s must be after start %s", cfg.End, cfg.Start)
	case cfg.Start.UnixNano()%int64(histdata.BarInterval) != 0 || cfg.End.UnixNano()%int64(histdata.BarInterval) != 0:
		return nil, errors.New("start and end must be multiples of 30 seconds")
	case (len(cfg.Markets) == 0) == (cfg.Universe == nil):
		return nil, errors.New("give either an explicit market list or a universe rule")
	case cfg.Universe != nil && cfg.Universe.TopN < 1:
		return nil, errors.New("the universe must select at least one market")
	}
	if err := cfg.Exchange.Validate(); err != nil {
		return nil, err
	}
	if cfg.StaleAfter < 0 {
		return nil, fmt.Errorf("the stale threshold must not be negative, got %s", cfg.StaleAfter)
	}
	if cfg.StaleAfter == 0 {
		cfg.StaleAfter = DefaultStaleAfter
	}
	if cfg.SettingsSource == "" {
		cfg.SettingsSource = DefaultSettingsSource
	}
	riskFee := alignRiskFee(&settings, cfg.Exchange, slices.Contains(cfg.ExplicitSettings, "FEE_AND_SLIPPAGE"))
	st := cfg.strategy
	if st == nil {
		var err error
		if st, err = resolveStrategy(cfg.StrategyID, settings); err != nil {
			return nil, err
		}
	}

	r := &runner{
		cfg: cfg, settings: settings, ex: cfg.Exchange, reader: reader, st: st,
		clock: cfg.Start, feeds: map[string]*marketFeed{}, activeSet: map[string]bool{},
		evaluated: map[string]bool{}, pendingMkt: map[string]int{}, exitPending: map[string]bool{},
		entryOrder: map[string]int{}, stale: map[string]int{},
	}
	now := func() time.Time { return r.clock }
	r.risk = risk.NewEngine(settings, now)
	r.risk.UpdateEquity(settings.StartingCapital)
	r.paper = paper.NewEngineWithOptions(settings.StartingCapital, nil, now, paper.Options{
		Costs: &paper.Costs{FeeRate: r.ex.feeRate(), SlippageRate: 0}, // the simulated exchange sets the price
		NewID: func() string { r.paperSeq++; return fmt.Sprintf("pos-%06d", r.paperSeq) },
	})
	seeded := 0
	if !cfg.DisableLearner {
		r.learner = learner.New() // starts empty; learns only from this run's closes (and an explicit seed)
		for _, o := range cfg.LearnerSeed {
			r.learner.RecordOutcomeWithNotional(o.StrategyID, o.Market, o.Direction, o.PnL, o.Notional)
		}
		seeded = len(cfg.LearnerSeed)
	}
	r.track = newTracker(cfg.Start, settings.StartingCapital)

	stored := reader.Markets()
	if cfg.Universe != nil {
		r.quote = strings.ToUpper(cfg.Universe.Quote)
		if r.quote == "" {
			r.quote = strings.ToUpper(settings.BaseCurrency)
		}
		quote, suffix := r.quote, "/"+r.quote
		for _, m := range stored {
			if strings.HasSuffix(m, suffix) {
				r.candidates = append(r.candidates, m)
			}
		}
		if len(r.candidates) == 0 {
			return nil, fmt.Errorf("the data cache holds no market quoted in %s", quote)
		}
	} else {
		for _, m := range cfg.Markets {
			if !slices.Contains(stored, m) {
				return nil, fmt.Errorf("the data cache holds no data for %s", m)
			}
			if slices.Contains(r.candidates, m) {
				return nil, fmt.Errorf("market %s listed twice", m)
			}
			r.candidates = append(r.candidates, m)
		}
		sort.Strings(r.candidates)
		r.setActive(cfg.Markets)
	}
	for _, m := range r.candidates {
		r.feeds[m] = newMarketFeed(m, r.ex.SpreadWindowBars)
	}
	var dataCheck *histdata.RangeReport
	var dataWarnings []string
	if cfg.CheckData != nil {
		rep, err := cfg.CheckData(r.candidates, cfg.Start.Add(-PreRoll), cfg.End)
		if err != nil {
			return nil, fmt.Errorf("data check: %w", err)
		}
		if dataWarnings, err = checkCoverage(rep, cfg.AllowDataGaps); err != nil {
			return nil, err
		}
		dataCheck = &rep
	}

	r.res = &Result{
		SchemaVersion: ResultSchemaVersion, StrategyID: st.ID(), Start: cfg.Start, End: cfg.End,
		TickInterval: histdata.BarInterval.String(), Seed: cfg.Seed, LearnerEnabled: r.learner != nil,
		StartingCapital: settings.StartingCapital, Settings: SettingsSnapshot(settings), Exchange: r.ex,
		Orders: []OrderLogEntry{}, Trades: []ClosedTrade{}, Decisions: []Decision{},
		DecisionCounts: map[string]int{}, RejectionCounts: map[string]int{},
		EquityCurve: []EquityPoint{}, UniverseSelections: []UniverseSelection{},
		DataProvenance: []histdata.Provenance{}, InitialMarkets: append([]string{}, cfg.Markets...),
		LearnerSeedOutcomes: seeded, LearnedOutcomes: []LearnedOutcome{},
		SettingsSource: cfg.SettingsSource, RiskFee: riskFee, FillEmptyCandles: cfg.FillEmptyCandles,
		StaleAfter: cfg.StaleAfter.String(), StalePositions: []StalePosition{},
		DataCheck: dataCheck, DataWarnings: append([]string{}, dataWarnings...),
	}
	if cfg.Provenance != nil {
		for _, m := range r.candidates { // sorted; every candidate's bars are read
			entries, err := cfg.Provenance(m, cfg.Start.Add(-PreRoll), cfg.End)
			if err != nil {
				return nil, fmt.Errorf("data provenance for %s: %w", m, err)
			}
			r.res.DataProvenance = append(r.res.DataProvenance, entries...)
		}
	}
	return r, nil
}

// setActive makes markets the current universe.
func (r *runner) setActive(markets []string) {
	r.active = append([]string(nil), markets...)
	r.activeSet = map[string]bool{}
	for _, m := range markets {
		r.activeSet[m] = true
	}
}

// run steps the clock from the pre-roll to the end, then settles the account.
func (r *runner) run(ctx context.Context) error {
	if r.cfg.Universe != nil {
		r.nextSelect = r.cfg.Start
	}
	// One tick per 30 s boundary in (start-PreRoll, end]; each carries only
	// trades from before its time.
	k := 0
	for tick, err := range histdata.MergeTicks(r.reader, r.candidates, r.cfg.Start.Add(-PreRoll), r.cfg.End) {
		if err != nil {
			return err
		}
		if k%ctxCheckTicks == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		r.clock = tick.Time
		j := 0 // tick.Prices and r.candidates are both sorted by market
		for _, m := range r.candidates {
			var mp *histdata.MarketPrice
			if j < len(tick.Prices) && tick.Prices[j].Market == m {
				mp = &tick.Prices[j]
				j++
			}
			if err := r.feeds[m].observe(tick.Time, mp); err != nil {
				return err
			}
		}
		k++
		if !tick.Time.After(r.cfg.Start) {
			continue // pre-roll: data only, no trading
		}
		// Every candidate's price history is market data, kept on every tick
		// whether or not the market is in the universe, so a market that
		// re-enters has a continuous history (as its candles and volume are).
		for _, m := range r.candidates {
			if f := r.feeds[m]; f.last > 0 {
				f.hist.push(f.last)
			}
		}
		if err := r.step(ctx, k-1, tick.Time); err != nil {
			return err
		}
	}
	r.finish()
	return nil
}

// step runs one strategy tick in the live loop's order.
func (r *runner) step(ctx context.Context, k int, tick time.Time) error {
	if r.cfg.Universe != nil && !tick.Before(r.nextSelect) {
		if err := r.reselect(); err != nil {
			return err
		}
	}
	r.fillDue(k, tick)

	// Prices: the last trade per evaluated market (its history was updated in run).
	prices := map[string]float64{}
	var ordered []string
	for _, m := range r.evaluatedMarkets() {
		f := r.feeds[m]
		if f.last <= 0 {
			continue // no trade yet: the market contributes ticks from its first trade
		}
		prices[m] = f.last
		ordered = append(ordered, m)
		r.evaluated[m] = true
	}
	r.res.Ticks++

	r.paper.UpdateMarkPrices(prices)
	r.paper.UpdateTrailingPrices(prices)
	r.checkStops(k, tick, prices)

	equity := r.paper.TotalEquity(prices)
	cash := r.paper.AvailableCash()
	r.risk.UpdateEquity(equity)
	r.recordEquity(tick, equity, cash)

	positions := r.paper.OpenPositions()
	obs := TickObservation{Time: tick, Prices: prices, Equity: equity, Cash: cash, Positions: positions, LearnedOutcomes: r.learned}

	// Indicator strategies wait until the longest history fills the lookback.
	collected := 0
	for _, m := range ordered {
		collected = max(collected, r.feeds[m].hist.len())
	}
	if len(ordered) == 0 || min(collected, strategy.LookbackTicks+1) <= strategy.LookbackTicks {
		r.notify(obs)
		return nil
	}

	md := make(map[string]strategy.MarketData, len(ordered))
	for _, m := range ordered {
		f := r.feeds[m]
		md[m] = marketdata.Build(marketdata.Input{
			Price:   f.last,
			Volume:  f.volume24h(),
			History: f.hist.window(),
			OHLC5:   candles(f.c5, tick, 5*time.Minute, r.cfg.FillEmptyCandles),
			OHLC15:  candles(f.c15, tick, 15*time.Minute, r.cfg.FillEmptyCandles),
		}, r.settings.MomentumLookbackTicks)
	}
	obs.MarketData = md
	r.notify(obs)

	ideas := r.st.Evaluate(ctx, strategy.Input{
		MarketData: md,
		Portfolio:  strategy.Portfolio{Equity: equity, Cash: cash, OpenPositions: positions},
	})
	r.res.EvaluatedTicks++
	for _, idea := range ideas {
		r.handleIdea(k, tick, idea, md, prices, equity, cash)
	}
	return nil
}

// notify passes an observation to the test hook, if any.
func (r *runner) notify(obs TickObservation) {
	if r.cfg.observe != nil {
		r.cfg.observe(obs)
	}
}

// evaluatedMarkets returns the universe plus any market still holding a
// position or a pending order after it left the universe.
func (r *runner) evaluatedMarkets() []string {
	out := append([]string(nil), r.active...)
	var extra []string
	for _, p := range r.paper.OpenPositions() {
		if !r.activeSet[p.Market] && !slices.Contains(extra, p.Market) {
			extra = append(extra, p.Market)
		}
	}
	for m, n := range r.pendingMkt {
		if n > 0 && !r.activeSet[m] && !slices.Contains(extra, m) {
			extra = append(extra, m)
		}
	}
	sort.Strings(extra)
	return append(out, extra...)
}

// reselect picks the point-in-time universe and schedules the next pick.
func (r *runner) reselect() error {
	at := r.nextSelect
	markets, err := r.selectTop(r.cfg.Universe.TopN, r.quote, at)
	if err != nil {
		return err
	}
	r.setActive(markets)
	if len(r.res.UniverseSelections) == 0 {
		r.res.InitialMarkets = append([]string{}, markets...)
	}
	r.res.UniverseSelections = append(r.res.UniverseSelections, UniverseSelection{Time: at, Markets: markets})
	next, err := r.cfg.Universe.nextAfter(at)
	if err != nil {
		return err
	}
	r.nextSelect = next
	return nil
}

// checkStops schedules a sell for every position whose stop-loss or
// trailing stop is breached at this tick's price, as live checkStops does;
// the sell fills on the following bar, not at the stop level.
func (r *runner) checkStops(k int, tick time.Time, prices map[string]float64) {
	for _, pos := range r.paper.OpenPositions() {
		price, ok := prices[pos.Market]
		if !ok || r.exitPending[pos.PositionID] {
			continue
		}
		stopHit := r.paper.StopLossTriggered(pos.PositionID, price, r.risk.StopLossAssumption())
		trailHit := !stopHit && r.paper.TrailingStopTriggered(pos.PositionID, price, r.settings.TrailingStopPct)
		if !stopHit && !trailHit {
			continue
		}
		reason := ReasonTrailingStop
		if stopHit {
			reason = ReasonStopLoss
		}
		meta, _ := r.paper.Meta(pos.PositionID)
		r.submit(k, tick, &pendingOrder{market: pos.Market, buy: false, positionID: pos.PositionID, reason: reason},
			pos.Size, price, meta.StrategyID, "")
	}
}

// handleIdea mirrors the live handleIdea in fully automated mode: learner
// adjustment, risk evaluation, the spot-exit rule, the MinSignalConfidence
// gate and the one-position-per-pair skip, then the order.
func (r *runner) handleIdea(k int, tick time.Time, idea domain.TradeIdea, md map[string]strategy.MarketData, prices map[string]float64, equity, cash float64) {
	r.ideaSeq++
	idea.ID = fmt.Sprintf("idea-%06d", r.ideaSeq) // replaces the random UUID
	raw := idea.Confidence
	if r.learner != nil {
		idea.Confidence = r.learner.AdjustConfidence(idea.StrategyID, idea.Market, string(idea.Direction), idea.Confidence)
	}

	price, hasPrice := prices[idea.Market]
	availableCash := cash
	opts := risk.EvaluateOptions{OpenPositions: r.paper.OpenPositions(), AvailableCash: &availableCash}
	if hasPrice {
		p := price
		opts.MarketPrice = &p
	}
	if data, ok := md[idea.Market]; ok {
		opts.MarketVolume24h = data.Volume
	}
	decision := r.risk.EvaluateTrade(idea, opts)

	d := Decision{
		Time: tick, IdeaID: idea.ID, Market: idea.Market, StrategyID: idea.StrategyID, Direction: string(idea.Direction),
		RawConfidence: raw, Confidence: idea.Confidence, RiskApproved: decision.Approved, RiskReason: decision.Reason,
	}
	long, holding := r.oldestLong(idea.Market)
	switch {
	case !decision.Approved:
		d.Outcome = OutcomeRiskRejected
	case idea.Direction == domain.Short && !holding:
		d.Outcome = OutcomeNoHolding // spot: a sell signal only exits a held long
	case idea.Confidence < r.settings.MinSignalConfidence:
		d.Outcome = OutcomeLowConfidence
	case idea.Direction == domain.Long && holding:
		d.Outcome = OutcomePositionOpen
	case !hasPrice || price == 0:
		d.Outcome = OutcomeNoPrice
	case r.pendingMkt[idea.Market] > 0:
		d.Outcome = OutcomeOrderPending
	case idea.Direction == domain.Long && !r.activeSet[idea.Market]:
		d.Outcome = OutcomeNotInUniverse
	case idea.Direction == domain.Long:
		qty := roundQtyDown(sizeInBase(idea, decision, equity, price), r.rule(idea.Market).LotDecimals)
		d.OrderID = r.submit(k, tick, &pendingOrder{market: idea.Market, buy: true, reason: ReasonSignal, idea: idea},
			qty, price, idea.StrategyID, idea.ID)
		d.Outcome = OutcomeSubmitted
	default:
		d.OrderID = r.submit(k, tick, &pendingOrder{market: idea.Market, buy: false, positionID: long.PositionID, reason: ReasonSignal, idea: idea},
			long.Size, price, idea.StrategyID, idea.ID)
		d.Outcome = OutcomeSubmitted
	}
	r.res.Decisions = append(r.res.Decisions, d)
	r.res.DecisionCounts[d.Outcome]++
}

// oldestLong returns the long the paper engine would sell on market.
func (r *runner) oldestLong(market string) (domain.PositionRecord, bool) {
	for _, p := range r.paper.OpenPositions() {
		if p.Market == market && p.Size > 0 {
			return p, true
		}
	}
	return domain.PositionRecord{}, false
}

// sizeInBase converts a risk decision into a base-currency order size exactly
// as the live loop does (internal/app sizeInBase): the risk engine's proposed
// value first, then its adjusted sizing, then the idea's own sizing.
func sizeInBase(idea domain.TradeIdea, decision domain.RiskDecision, equity, marketPrice float64) float64 {
	if decision.ProposedSize != nil {
		return *decision.ProposedSize / marketPrice
	}
	sizing := idea.PositionSizingProposal
	if decision.AdjustedSizing != nil && *decision.AdjustedSizing != 0 {
		sizing = *decision.AdjustedSizing
	}
	return sizing * equity / marketPrice
}

// rule returns the exchange rules of market.
func (r *runner) rule(market string) PairRule {
	rule, _ := r.ex.PairRules.For(market)
	return rule
}

// submit logs an order request and schedules its fill LatencyTicks later.
func (r *runner) submit(k int, tick time.Time, o *pendingOrder, qty, reference float64, strategyID, ideaID string) string {
	r.orderSeq++
	side := SideSell
	if o.buy {
		side = SideBuy
	}
	entry := OrderLogEntry{
		ID: fmt.Sprintf("ord-%06d", r.orderSeq), Market: o.market, Side: side, Reason: o.reason,
		StrategyID: strategyID, IdeaID: ideaID, PositionID: o.positionID,
		DecisionTime: tick, FillTime: tick.Add(time.Duration(r.ex.LatencyTicks) * histdata.BarInterval),
		RequestedQty: qty, ReferencePrice: reference, FeeRate: r.ex.feeRate(),
	}
	r.res.Orders = append(r.res.Orders, entry)
	o.log = len(r.res.Orders) - 1
	o.fillTick = k + r.ex.LatencyTicks
	r.pending = append(r.pending, o)
	r.pendingMkt[o.market]++
	if !o.buy {
		r.exitPending[o.positionID] = true
	}
	return entry.ID
}

// fillDue executes, in submission order, the orders due at tick k.
func (r *runner) fillDue(k int, tick time.Time) {
	kept := r.pending[:0]
	for _, o := range r.pending {
		if o.fillTick != k {
			kept = append(kept, o)
			continue
		}
		r.execute(o, tick)
		r.pendingMkt[o.market]--
		if !o.buy {
			delete(r.exitPending, o.positionID)
		}
	}
	r.pending = kept
}

// reject marks an order rejected and counts the reason.
func (r *runner) reject(e *OrderLogEntry, reason string) {
	e.Status, e.RejectReason = StatusRejected, reason
	e.FilledQty, e.FillPrice = 0, 0
	r.res.RejectionCounts[reason]++
}

// execute prices an order on this tick's bar, applies the exchange rules and
// the cash check, then fills it through the paper engine.
func (r *runner) execute(o *pendingOrder, tick time.Time) {
	e := &r.res.Orders[o.log]
	f := r.feeds[o.market]
	e.FillTime = tick
	e.FillBarClose = f.last
	if !o.buy {
		pos, open := r.paper.Position(o.positionID)
		if !open {
			r.reject(e, RejectGone)
			return
		}
		e.RequestedQty = pos.Size
	}
	qty := e.RequestedQty
	if f.last <= 0 {
		r.reject(e, RejectNoPrice)
		return
	}
	if !f.hasBar || f.bar.Volume <= 0 {
		r.reject(e, RejectLiquidity) // nothing traded in the interval after the decision
		return
	}
	q := r.ex.quote(o.buy, f)
	e.FillBarMid = q.mid
	e.Participation = qty * q.price / sideTradedValue(o.buy, f.bar)
	if e.Participation > r.ex.LiquidityCap {
		r.reject(e, RejectLiquidity)
		return
	}
	impact := r.ex.impact(e.Participation)
	rawPrice := q.price * (1 - impact)
	if o.buy {
		rawPrice = q.price * (1 + impact)
	}
	rule := r.rule(o.market)
	price := roundPriceAgainst(rawPrice, rule.PairDecimals, o.buy)
	switch {
	case qty <= 0 || qty < rule.OrderMin:
		r.reject(e, RejectMinOrder)
		return
	case qty*price < rule.CostMin:
		r.reject(e, RejectMinCost)
		return
	case o.buy && qty*price*(1+r.ex.feeRate()) > r.paper.AvailableCash():
		r.reject(e, RejectFunds)
		return
	}

	positionID, ok := r.applyFill(o, qty, price)
	if !ok {
		r.reject(e, RejectPaper)
		return
	}
	sign := 1.0
	if !o.buy {
		sign = -1
	}
	e.Status, e.PositionID = StatusFilled, positionID
	e.FilledQty, e.FillPrice, e.PriceSource, e.HalfSpread = qty, price, q.source, q.halfSpread
	// Drift runs to the fill bar's estimated mid and the spread from there,
	// so a fill bar's one-sided prints are not mistaken for price movement.
	e.LatencyDrift = sign * (q.mid - e.ReferencePrice)
	e.SpreadCost = sign * (q.price - q.mid)
	e.ImpactCost = sign * (rawPrice - q.price)
	e.RoundingCost = sign * (price - rawPrice)
	e.Notional = qty * price
	e.Fee = e.Notional * r.ex.feeRate() // the paper engine's own formula
	if o.buy {
		r.entryOrder[positionID] = o.log
		return
	}
	pnl := r.closed(positionID, o)
	e.RealisedPnL = &pnl
}

// applyFill sends a priced fill to the paper engine: a buy opens a long, a
// signal sell closes the market's oldest long (as live autoExecute does), a
// stop sell closes its position (as live checkStops does).
func (r *runner) applyFill(o *pendingOrder, qty, price float64) (string, bool) {
	switch {
	case o.buy:
		confidence := o.idea.Confidence
		order, positionID := r.paper.Execute(domain.ExecutionIntent{
			ApprovalRequestID: "backtest", Market: o.market, Direction: domain.Long, Size: qty,
		}, price, paper.ExecuteOptions{StrategyID: o.idea.StrategyID, SignalConfidence: &confidence, Environment: "paper", TradeIdeaID: o.idea.ID})
		return positionID, order.Status == "filled" && positionID != ""
	case o.reason == ReasonSignal:
		order, positionID := r.paper.Execute(domain.ExecutionIntent{
			ApprovalRequestID: "backtest", Market: o.market, Direction: domain.Short, Size: qty,
		}, price, paper.ExecuteOptions{StrategyID: o.idea.StrategyID, Environment: "paper", TradeIdeaID: o.idea.ID})
		return positionID, order.Status == "filled" && positionID == o.positionID
	default:
		_, ok := r.paper.ClosePosition(o.positionID, price, paper.CloseOptions{ApprovalRequestID: "backtest_" + o.reason})
		return o.positionID, ok
	}
}

// closed records a closed position: the risk engine's daily loss, the
// learner (with the same strategy and direction keys live uses: the closing
// idea's for a signal exit, the position's own for a stop), and the trade.
func (r *runner) closed(positionID string, o *pendingOrder) float64 {
	pnl, _ := r.paper.RealisedPnL(positionID)
	meta, _ := r.paper.Meta(positionID)
	r.risk.RecordTradeResult(pnl)
	if r.learner != nil {
		strategyID, direction := meta.StrategyID, meta.Direction
		if strategyID == "" {
			strategyID = "unknown"
		}
		if direction == "" {
			direction = "long"
		}
		if o.reason == ReasonSignal {
			strategyID, direction = o.idea.StrategyID, string(o.idea.Direction)
		}
		notional := meta.AvgPriceAtClose * meta.SizeAtClose
		r.learner.RecordOutcomeWithNotional(strategyID, o.market, direction, pnl, notional)
		r.learned++
		r.res.LearnedOutcomes = append(r.res.LearnedOutcomes, LearnedOutcome{
			Time: r.clock, StrategyID: strategyID, Market: o.market, Direction: direction, PnL: pnl, Notional: notional,
		})
	}
	r.addTrade(positionID, o.log, pnl)
	return pnl
}

// addTrade appends the closed trade of positionID, exited by order exitLog.
func (r *runner) addTrade(positionID string, exitLog int, pnl float64) {
	meta, _ := r.paper.Meta(positionID)
	entry := r.res.Orders[r.entryOrder[positionID]]
	exit := r.res.Orders[exitLog]
	r.res.Trades = append(r.res.Trades, ClosedTrade{
		PositionID: positionID, Market: exit.Market, StrategyID: entry.StrategyID,
		EntryOrder: entry.ID, ExitOrder: exit.ID, EntryTime: entry.FillTime, ExitTime: exit.FillTime,
		Qty: meta.SizeAtClose, EntryPrice: meta.AvgPriceAtClose, ExitPrice: meta.ExitPriceAtClose,
		EntryFee: meta.EntryFee, ExitFee: meta.ExitFeeAtClose, PnL: pnl, ExitReason: exit.Reason, EntryIdeaID: entry.IdeaID,
	})
	delete(r.entryOrder, positionID)
}

// recordEquity samples the equity curve, keeping the lowest equity seen
// between samples.
func (r *runner) recordEquity(tick time.Time, equity, cash float64) {
	open := len(r.paper.OpenPositions())
	staleN, staleValue := r.markStale(tick)
	r.track.observe(tick, equity, cash, open)
	if len(r.res.EquityCurve) == 0 || equity < r.minEquity {
		r.minEquity = equity
	}
	if len(r.res.EquityCurve) > 0 && r.cfg.EquityEvery > 0 && tick.Sub(r.lastPoint) < r.cfg.EquityEvery {
		return
	}
	r.res.EquityCurve = append(r.res.EquityCurve, EquityPoint{
		Time: tick, Equity: equity, Cash: cash, PositionsValue: equity - cash,
		MinEquity: r.minEquity, Peak: r.track.peak, OpenPositions: open,
		StalePositions: staleN, StaleValue: staleValue,
	})
	r.lastPoint, r.minEquity = tick, equity
}

// markStale records every open position whose market has not traded for
// longer than the stale threshold at tick, and returns how many there are
// and their marked value.
func (r *runner) markStale(tick time.Time) (int, float64) {
	n, value := 0, 0.0
	for _, pos := range r.paper.OpenPositions() {
		f := r.feeds[pos.Market]
		if !f.staleAt(tick, r.cfg.StaleAfter) {
			continue
		}
		n++
		mark := pos.Size * f.last
		value += mark
		i, seen := r.stale[pos.PositionID]
		if !seen {
			r.res.StalePositions = append(r.res.StalePositions, StalePosition{PositionID: pos.PositionID, Market: pos.Market, FirstFlagged: tick})
			i = len(r.res.StalePositions) - 1
			r.stale[pos.PositionID] = i
		}
		sp := &r.res.StalePositions[i]
		sp.LastTrade, sp.Qty, sp.MarkedValue = f.lastBar.Start, pos.Size, mark
	}
	return n, value
}

// finish cancels unfilled orders, values every open position by selling it
// on its market's last bar with trades (less spread and fee), keeps the
// worst case of the stale ones, and fills in totals.
func (r *runner) finish() {
	r.clock = r.cfg.End
	for _, o := range r.pending {
		e := &r.res.Orders[o.log]
		e.Status, e.RejectReason = StatusCancelled, CancelRunEnded
		r.res.RejectionCounts[CancelRunEnded]++
	}
	r.pending = nil
	staleProceeds := 0.0
	for _, pos := range r.paper.OpenPositions() {
		staleProceeds += r.liquidate(pos)
	}

	r.res.EndingCash = r.paper.Cash()
	r.res.EndingEquity = r.paper.TotalEquity(nil)
	r.res.EndingEquityWorstCase = r.res.EndingEquity - staleProceeds
	r.track.final(r.cfg.End, r.res.EndingEquity)
	r.res.TickStats = r.track.stats()
	for _, o := range r.res.Orders {
		if o.Status == StatusFilled {
			r.res.FeesPaid += o.Fee
		}
	}
	for m := range r.evaluated {
		r.res.Markets = append(r.res.Markets, m)
	}
	sort.Strings(r.res.Markets)
	if r.res.Markets == nil {
		r.res.Markets = []string{}
	}
	r.res.Assumptions = assumptions(r.cfg, r.ex, r.learner != nil, r.res)
}

// liquidate closes a position still open at the end with the fill model's
// sell quote on its market's last bar with trades (no size cap). A position
// whose market is stale is recorded as a stale_liquidation; liquidate then
// returns its net proceeds, which the worst case values at zero.
func (r *runner) liquidate(pos domain.PositionRecord) float64 {
	f := r.feeds[pos.Market]
	reason := ReasonEndOfRun
	stale := f.staleAt(r.cfg.End, r.cfg.StaleAfter)
	if stale {
		reason = ReasonStaleLiquidation
		r.markStale(r.cfg.End)
	}
	q := r.ex.exitQuote(f)
	price := roundPriceAgainst(q.price, r.rule(pos.Market).PairDecimals, false)
	notional := pos.Size * price
	fee := notional * r.ex.feeRate()
	r.orderSeq++
	r.res.Orders = append(r.res.Orders, OrderLogEntry{
		ID: fmt.Sprintf("ord-%06d", r.orderSeq), Market: pos.Market, Side: SideSell, Reason: reason,
		PositionID: pos.PositionID, DecisionTime: r.cfg.End, FillTime: r.cfg.End,
		RequestedQty: pos.Size, FilledQty: pos.Size, ReferencePrice: f.last, FillBarClose: f.last, FillBarMid: q.mid, FillPrice: price,
		PriceSource: q.source, HalfSpread: q.halfSpread, LatencyDrift: f.last - q.mid, SpreadCost: q.mid - q.price, RoundingCost: q.price - price,
		Notional: notional, Fee: fee, FeeRate: r.ex.feeRate(), Status: StatusFilled,
	})
	log := len(r.res.Orders) - 1
	if _, ok := r.paper.ClosePosition(pos.PositionID, price, paper.CloseOptions{ApprovalRequestID: "backtest_" + reason}); !ok {
		return 0
	}
	pnl, _ := r.paper.RealisedPnL(pos.PositionID)
	r.res.Orders[log].RealisedPnL = &pnl
	r.addTrade(pos.PositionID, log, pnl)
	if !stale {
		return 0
	}
	sp := &r.res.StalePositions[r.stale[pos.PositionID]]
	sp.AtEnd, sp.LiquidationValue = true, notional-fee
	return notional - fee
}
