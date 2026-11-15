package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/analysis"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/llm"
	"github.com/James-Chapman/LLMTradingBot/internal/marketdata"
	"github.com/James-Chapman/LLMTradingBot/internal/news"
	"github.com/James-Chapman/LLMTradingBot/internal/paper"
	"github.com/James-Chapman/LLMTradingBot/internal/portfolio"
	"github.com/James-Chapman/LLMTradingBot/internal/risk"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// ---------------------------------------------------------------------------
// Strategy loop
// ---------------------------------------------------------------------------

// strategyLoop resolves the universe, then polls market data and runs the
// selected strategy every strategy interval (STRATEGY_INTERVAL_SECONDS),
// acting according to the trading mode.
func (a *App) strategyLoop(ctx context.Context) {
	a.bootStrategyLoop(ctx)
	for ctx.Err() == nil {
		start := time.Now()
		var skipped bool
		err := a.guarded("Strategy loop error", func() error {
			var tickErr error
			skipped, tickErr = a.strategyTick(ctx)
			return tickErr
		})
		if err != nil {
			a.activity.Error("Strategy loop error", err.Error())
		}
		// Gated ticks wait a full interval; evaluated ticks keep a steady
		// cadence. The interval is read each tick, so a dashboard change
		// applies from the next wait.
		wait := a.strategyInterval()
		if !skipped {
			wait -= time.Since(start)
		}
		if !sleepCtx(ctx, wait) {
			return
		}
	}
}

// bootRetryMax caps the wait between start-up attempts.
const bootRetryMax = 5 * time.Minute

// bootStrategyLoop resolves and validates the market universe, then starts
// the markets. When none of the configured markets validates (Kraken's pair
// list could not be loaded: network not up yet, a 5xx), it retries with a
// doubling wait until one does or ctx is cancelled. Each attempt is guarded,
// so a panic is logged and retried instead of ending the process.
func (a *App) bootStrategyLoop(ctx context.Context) {
	wait := a.intervals.BootRetry
	for attempt := 1; ; attempt++ {
		var started bool
		var configured int
		_ = a.guarded("Strategy loop start-up error", func() error {
			var active []string
			active, configured = a.validateUniverse(ctx)
			// An empty universe cannot improve by retrying.
			if len(active) > 0 || configured == 0 {
				a.startMarkets(ctx, active)
				started = true
			}
			return nil
		})
		if started || ctx.Err() != nil {
			return
		}
		detail := fmt.Sprintf("Attempt %d: none of the %d configured markets was confirmed by Kraken "+
			"(its pair list could not be loaded). Retrying in %s.", attempt, configured, wait)
		a.activity.Warn("No markets could be validated - waiting for Kraken", detail)
		a.logger.Warn("No markets could be validated; retrying start-up", "attempt", attempt, "configured", configured, "retry_in", wait.String())
		if !sleepCtx(ctx, wait) {
			return
		}
		wait = min(wait*2, bootRetryMax)
	}
}

// validateUniverse resolves the universe and returns the markets Kraken
// confirms, plus how many markets were configured.
func (a *App) validateUniverse(ctx context.Context) ([]string, int) {
	u := a.universe.ResolveUniverse(ctx)
	all := append(append([]string{}, u.FixedMarkets...), u.DynamicMarkets...)
	active := a.market.ValidateSymbols(ctx, all)
	if active == nil {
		active = []string{}
	}
	return active, len(all)
}

// startMarkets makes the validated markets active, restores their rolling
// price history, announces start-up and starts the ticker stream.
func (a *App) startMarkets(ctx context.Context, active []string) {
	// Restore rolling price history so momentum is available immediately.
	restored := map[string][]float64{}
	anyRestored := false
	for _, sym := range active {
		saved, err := a.repo.GetRecentPrices(ctx, sym, restoredPricesPerSym)
		if err != nil {
			a.logger.Warn("Could not restore price history", "market", sym, "error", err.Error())
			continue
		}
		restored[sym] = saved
		anyRestored = anyRestored || len(saved) > 0
	}

	a.mu.Lock()
	a.activeMarkets = active
	for sym, saved := range restored {
		a.priceHistory[sym] = trimTail(append(a.priceHistory[sym], saved...), priceHistoryMax)
	}
	a.mu.Unlock()

	if anyRestored {
		parts := make([]string, 0, len(active))
		for _, m := range active {
			parts = append(parts, fmt.Sprintf("%s: %d ticks", m, len(restored[m])))
		}
		a.activity.Info("Price history restored from DB", strings.Join(parts, " | "))
	}

	a.activity.Info(
		fmt.Sprintf("Bot started - %d markets active", len(active)),
		fmt.Sprintf("Markets: %s | Mode: %s", strings.Join(active, ", "), a.TradingMode()),
	)
	a.sendAlert(ctx, "bot_started", map[string]any{"markets": active, "mode": a.TradingMode()})
	a.logger.Info("Active markets confirmed", "markets", active)
	a.market.Start(active, a.cacheMarketSnapshot)
}

// cacheMarketSnapshot stores the newest streamed ticker for strategy ticks.
func (a *App) cacheMarketSnapshot(snap domain.MarketSnapshot) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.latestSnapshots[snap.Symbol] = snap
	a.currentPrices[snap.Symbol] = snap.Price
}

// pricedSnapshot is one market's snapshot in tick order.
type pricedSnapshot struct {
	symbol string
	snap   domain.MarketSnapshot
}

// strategyTick runs one strategy-loop iteration. skipped reports a gated
// tick (stop, no markets, no prices, warm-up) that waits a full interval.
// Live positions are looked after on every tick, gated or not: their
// markets are priced and their pending orders reconcile even during the
// emergency stop (which only pauses their automatic stops), and their stops
// run when every market is disabled or unpriced.
func (a *App) strategyTick(ctx context.Context) (skipped bool, err error) {
	if a.control.EmergencyStop() {
		a.tendLiveBookDuringStop(ctx)
		a.activity.Warn("Emergency stop active - tick skipped", "")
		return true, nil
	}

	// Only evaluate enabled markets.
	a.mu.Lock()
	var active []string
	for _, m := range a.activeMarkets {
		if a.control.IsMarketEnabled(m) {
			active = append(active, m)
		}
	}
	a.mu.Unlock()
	if len(active) == 0 {
		a.tendLiveBook(ctx)
		a.activity.Warn("All markets disabled - tick skipped", "")
		return true, nil
	}

	// Recent streamed snapshots first, then a batch REST call for gaps and
	// stale snapshots; a market with neither is skipped this tick.
	ordered := a.freshSnapshots(ctx, active)
	if len(ordered) == 0 {
		a.tendLiveBook(ctx)
		a.activity.Warn("No market prices available - tick skipped", "")
		return true, nil
	}
	prices := make(map[string]float64, len(ordered))
	for _, ps := range ordered {
		prices[ps.symbol] = ps.snap.Price
	}
	// Live positions on disabled markets, or markets no longer in the active
	// universe, are priced too: their stops must still run.
	livePrices := copyPrices(prices)
	for m, p := range a.liveBookPrices(ctx, prices) {
		livePrices[m] = p
	}

	// Append prices to the rolling history and advance the tick counter.
	a.mu.Lock()
	for _, ps := range ordered {
		a.priceHistory[ps.symbol] = trimTail(append(a.priceHistory[ps.symbol], ps.snap.Price), priceHistoryMax)
	}
	a.sessionTick++
	tick := a.sessionTick
	// Warm-up progress is the longest history among the markets priced this
	// tick (all enabled), so a disabled or never-priced market cannot hold
	// the indicator strategy in warm-up forever.
	ticksCollected := 0
	for _, ps := range ordered {
		ticksCollected = max(ticksCollected, len(a.priceHistory[ps.symbol]))
	}
	ticksCollected = min(ticksCollected, strategy.LookbackTicks+1)
	histories := make(map[string][]float64, len(ordered))
	for _, ps := range ordered {
		histories[ps.symbol] = append([]float64(nil), a.priceHistory[ps.symbol]...)
	}
	ohlc5 := map[string][]domain.Candle{}
	ohlc15 := map[string][]domain.Candle{}
	ohlc240 := map[string][]domain.Candle{}
	for _, ps := range ordered {
		ohlc5[ps.symbol] = a.ohlc5[ps.symbol].Candles
		ohlc15[ps.symbol] = a.ohlc15[ps.symbol].Candles
		ohlc240[ps.symbol] = a.ohlc240[ps.symbol].Candles
	}
	a.mu.Unlock()
	warmingUp := ticksCollected <= strategy.LookbackTicks

	summary := make([]string, 0, len(ordered))
	for _, ps := range ordered {
		summary = append(summary, fmt.Sprintf("%s %s%s", ps.symbol, a.currencySymbol, fmtPrice(ps.snap.Price)))
	}
	priceSummary := strings.Join(summary, "  ")
	if warmingUp {
		a.activity.Info(fmt.Sprintf("Tick #%d - prices fetched (warming up %d/%d ticks)", tick, ticksCollected, strategy.LookbackTicks+1), priceSummary)
	} else {
		a.activity.Info(fmt.Sprintf("Tick #%d - prices fetched", tick), priceSummary)
	}

	marketData := a.buildMarketData(ordered, histories, ohlc5, ohlc15, ohlc240)

	a.mu.Lock()
	a.currentPrices = copyPrices(livePrices)
	a.bumpVersionLocked()
	a.mu.Unlock()
	a.paper.UpdateMarkPrices(prices)
	a.paper.UpdateTrailingPrices(prices)

	// Live fills are applied first so a just-filled live entry is already
	// protected by this tick's stops. Kraken is only called while live orders
	// are pending.
	a.reconcileLive(ctx)

	// Stops run before the database housekeeping below, so a failed write
	// cannot leave a position unprotected for the tick. The equity snapshot
	// that follows then saves the cash from any stop close in the same tick.
	a.checkStops(ctx, prices)
	a.checkLiveStops(ctx, livePrices)

	// Persist price ticks and portfolio value for this market-data tick.
	for _, ps := range ordered {
		if err := a.repo.SavePriceTick(ctx, ps.symbol, ps.snap.Price, nil); err != nil {
			return false, fmt.Errorf("save price tick: %w", err)
		}
	}
	if _, err := a.recordEquitySnapshot(ctx, prices); err != nil {
		return false, err
	}

	// Trim old price ticks, equity snapshots and activity every ~5 minutes.
	if tick%10 == 0 {
		for _, sym := range active {
			if err := a.repo.TrimOldPriceTicks(ctx, sym); err != nil {
				return false, fmt.Errorf("trim price ticks: %w", err)
			}
		}
		if err := a.repo.TrimOldEquitySnapshots(ctx, 0); err != nil {
			return false, fmt.Errorf("trim equity snapshots: %w", err)
		}
		if err := a.repo.TrimOldActivity(ctx, activityKeep); err != nil {
			return false, fmt.Errorf("trim activity: %w", err)
		}
	}

	// Detect the active strategy type early so warm-up matches its needs.
	selected := a.strategyByID(a.control.SelectedStrategyID())
	usesLLM := selected != nil && selected.UsesLLM()

	// Indicator-only strategies wait until the full lookback window fills.
	if warmingUp && !usesLLM {
		return true, nil
	}

	var llmMarketData map[string]strategy.MarketData
	if usesLLM {
		required := selected.RequiredPriceTicks()
		llmMarketData = llmReadyMarketData(marketData, required)
		latestNews := a.News()
		if !llmStrategyDataReady(marketData, latestNews, required) {
			var waiting []string
			if len(llmMarketData) == 0 {
				waiting = append(waiting, fmt.Sprintf("%d price ticks", required))
			}
			if len(latestNews) == 0 {
				waiting = append(waiting, "news")
			}
			a.activity.Info(fmt.Sprintf("Tick #%d - LLM data warmup waiting for %s", tick, strings.Join(waiting, " and ")), "")
			return true, nil
		}
	}

	ideas := a.evaluateStrategy(ctx, marketData, llmMarketData)
	if len(ideas) == 0 {
		a.activity.Info("Strategies evaluated - no signals this tick", "")
	}

	// Apply historical performance learning before the risk gate.
	for i := range ideas {
		ideas[i].Confidence = a.learner.AdjustConfidence(ideas[i].StrategyID, ideas[i].Market, string(ideas[i].Direction), ideas[i].Confidence)
	}

	var signals []SignalView
	for i := range ideas {
		signal, keep, err := a.handleIdea(ctx, &ideas[i], marketData, prices)
		if err != nil {
			return false, err
		}
		if keep {
			signals = append(signals, signal)
		}
	}

	if len(signals) > 0 {
		a.mu.Lock()
		a.latestSignals = mergeSignals(signals, a.latestSignals)
		a.bumpVersionLocked()
		a.mu.Unlock()
	}
	return false, nil
}

// buildMarketData assembles the per-market strategy context for this tick
// with the builder the backtest replay shares (internal/marketdata).
func (a *App) buildMarketData(ordered []pricedSnapshot, histories map[string][]float64, ohlc5, ohlc15, ohlc240 map[string][]domain.Candle) map[string]strategy.MarketData {
	briefing := a.analyser.LatestBriefing()
	lookbackTicks := a.cfg().MomentumLookbackTicks
	out := make(map[string]strategy.MarketData, len(ordered))
	for _, ps := range ordered {
		out[ps.symbol] = marketdata.Build(marketdata.Input{
			Price:        ps.snap.Price,
			Volume:       ps.snap.Volume,
			History:      histories[ps.symbol],
			OHLC5:        ohlc5[ps.symbol],
			OHLC15:       ohlc15[ps.symbol],
			OHLC240:      ohlc240[ps.symbol],
			LLMSentiment: briefingSentimentForMarket(briefing, ps.symbol),
		}, lookbackTicks)
	}
	return out
}

// checkStops closes paper positions whose stop-loss or trailing stop is hit
// (live positions: checkLiveStops).
func (a *App) checkStops(ctx context.Context, prices map[string]float64) {
	for _, pos := range a.paper.OpenPositions() {
		marketPrice, ok := prices[pos.Market]
		if !ok {
			continue
		}
		lossPct := (marketPrice - pos.AvgPrice) / pos.AvgPrice
		if pos.Size > 0 {
			lossPct = (pos.AvgPrice - marketPrice) / pos.AvgPrice
		}
		stopHit := a.paper.StopLossTriggered(pos.PositionID, marketPrice, a.risk.StopLossAssumption())
		trailHit := !stopHit && a.paper.TrailingStopTriggered(pos.PositionID, marketPrice, a.cfg().TrailingStopPct)
		if !stopHit && !trailHit {
			continue
		}
		if _, open := a.paper.Position(pos.PositionID); !open {
			continue // already closed by another path
		}

		closeReason, label := "trailing_stop", "TRAILING-STOP"
		if stopHit {
			closeReason, label = "stop_loss", "STOP-LOSS"
		}
		order, ok := a.paper.ClosePosition(pos.PositionID, marketPrice, paper.CloseOptions{})
		if !ok {
			continue
		}
		pnl, ok := a.paper.RecordClosedTrade(pos.PositionID, order.Price, closeReason, "")
		if !ok {
			continue
		}
		strategyID, direction := a.metaStrategyDirection(pos.PositionID)
		a.recordClosedOutcome(ctx, order.ID, strategyID, pos.Market, direction, pnl, a.paperCloseNotional(pos.PositionID))
		a.activity.Warn(
			fmt.Sprintf("%s: %s [%s] closed at %s%s (%s loss)", label, pos.Market, short8(pos.PositionID), a.currencySymbol, fmtPrice(order.Price), pct1(lossPct)),
			fmt.Sprintf("Entry %s%s  PnL %s%s", a.currencySymbol, fmtPrice(pos.AvgPrice), a.currencySymbol, fmtSignedMoney(pnl)),
		)
		a.sendAlert(ctx, closeReason, map[string]any{"market": pos.Market, "position_id": pos.PositionID, "pnl": pnl})
	}
}

// evaluateStrategy runs the selected strategy (falling back to the default)
// and queues the shadow LLM reviews of its new entries (shadow.go).
func (a *App) evaluateStrategy(ctx context.Context, marketData, llmMarketData map[string]strategy.MarketData) []domain.TradeIdea {
	active := a.strategyByID(a.control.SelectedStrategyID())
	if active == nil {
		a.control.SelectStrategy(strategy.BasicAndLLMID)
		active = a.strategyByID(a.control.SelectedStrategyID())
	}
	if active == nil {
		return nil
	}
	a.mu.Lock()
	equity, cash := a.currentEquity, a.currentCash
	a.mu.Unlock()
	in := strategy.Input{
		MarketData: marketData,
		Portfolio:  strategy.Portfolio{Equity: equity, Cash: cash, OpenPositions: a.strategyPositions()},
	}
	if active.UsesLLM() {
		in.MarketData = llmMarketData
		in.News = newsArticles(a.News())
		in.Analyser = a.analyser
		in.Briefing = a.analyser.LatestBriefing()
	}
	ideas := active.Evaluate(ctx, in)
	a.queueShadowReviews(ctx, active, ideas, in)
	return ideas
}

// strategyPositions returns the open positions the strategy sees: for each
// market, the positions of the route its orders take - the live book on a
// live-routed market, the paper engine elsewhere - so the portfolio the LLM
// is shown matches what an exit signal would actually sell.
func (a *App) strategyPositions() []domain.PositionRecord {
	environment := a.TradingEnvironment()
	positions := []domain.PositionRecord{}
	for _, p := range a.paper.OpenPositions() {
		if !a.liveRouted(p.Market, environment) {
			positions = append(positions, p)
		}
	}
	for _, p := range a.liveBook.Records() {
		if a.liveRouted(p.Market, environment) {
			positions = append(positions, p)
		}
	}
	return positions
}

// signalLLMAnalysis reconstructs the LLM verdict stored with a trade idea.
func signalLLMAnalysis(idea domain.TradeIdea) storage.LLMAnalysis {
	sig := idea.SupportingSignals
	if truthy(sig["llm_only"]) {
		sentiment, _ := numSignal(sig, "llm_sentiment")
		return storage.LLMAnalysis{Used: true, Sentiment: sentiment, ConfidenceScale: 1, Reasoning: strSignal(sig, "llm_reasoning")}
	}
	if idea.StrategyID == strategy.BasicAndLLMID {
		// Veto, scaling and thesis are applied inside the strategy; rebuild
		// the verdict from its supporting signals so the real values persist.
		sentiment, _ := numSignal(sig, "llm_sentiment")
		scale, used := numSignal(sig, "llm_confidence_scale")
		if !used {
			scale = 1
		}
		return storage.LLMAnalysis{Used: used, Sentiment: sentiment, ConfidenceScale: scale, Reasoning: strSignal(sig, "llm_reasoning")}
	}
	return storage.LLMAnalysis{Used: false, Sentiment: 0, ConfidenceScale: 1, Reasoning: ""}
}

// handleIdea risk-checks, persists and acts on one trade idea according to
// the trading mode. keep is false when the idea must not reach the signal
// buffer (fully automated, below the confidence threshold).
func (a *App) handleIdea(ctx context.Context, idea *domain.TradeIdea, marketData map[string]strategy.MarketData, prices map[string]float64) (SignalView, bool, error) {
	md, hasMD := marketData[idea.Market]
	momentum := 0.0
	if hasMD && md.PreviousPrice != 0 {
		momentum = (md.Price - md.PreviousPrice) / md.PreviousPrice * 100
	}
	// Record the pre-LLM subtotal for the signal detail score breakdown.
	if idea.SupportingSignals == nil {
		idea.SupportingSignals = map[string]any{}
	}
	idea.SupportingSignals["pre_llm_confidence"] = idea.Confidence
	llmAnalysis := signalLLMAnalysis(*idea)

	// Read the mode and environment once so a change from the web interface
	// mid-signal cannot split this signal across two modes, or size it from
	// one account and fill it on the other.
	mode, environment := a.TradingMode(), a.TradingEnvironment()
	isLive := a.liveRouted(idea.Market, environment)
	if isLive {
		// From the live position check to the order it sends, nothing else
		// may enter or exit a live position.
		a.liveMu.Lock()
		defer a.liveMu.Unlock()
	}

	a.mu.Lock()
	cash := a.currentCash
	equity := a.currentEquity
	latestNews := append([]storage.NewsEntry(nil), a.latestNews...)
	a.mu.Unlock()

	opts := risk.EvaluateOptions{OpenPositions: a.openPositionsFor(isLive), AvailableCash: &cash}
	if p, ok := prices[idea.Market]; ok {
		opts.MarketPrice = &p
	}
	if hasMD {
		opts.MarketVolume24h = md.Volume
	}
	decision := a.risk.EvaluateTrade(*idea, opts)

	indicators := map[string]any{}
	if hasMD && md.Indicators != nil {
		indicators = map[string]any(md.Indicators)
	}
	err := a.repo.SaveTradeIdea(ctx, *idea, storage.SignalContext{
		MomentumPct:  momentum,
		Indicators:   indicators,
		LLM:          &llmAnalysis,
		NewsContext:  newsForSignal(idea.Market, latestNews),
		RiskDecision: &decision,
	})
	if err != nil {
		return SignalView{}, false, fmt.Errorf("save trade idea: %w", err)
	}

	direction := string(idea.Direction)
	label := directionLabel(direction)
	signal := SignalView{
		Strategy: idea.StrategyID, Market: idea.Market, Direction: direction, Confidence: idea.Confidence,
		Thesis: idea.Thesis, RiskApproved: decision.Approved, RiskReason: decision.Reason, TradeIdeaID: idea.ID,
	}

	// A short on a live market can only sell a tracked, filled live long, and
	// not while automatic sells are paused after Kraken rejected one.
	exitBlock := ""
	if isLive && idea.Direction == domain.Short {
		exitBlock = a.liveExitBlock(idea.Market)
		if exitBlock == "" {
			exitBlock = a.liveExitPaused(idea.Market)
		}
	}

	switch {
	case !decision.Approved:
		a.activity.Warn(fmt.Sprintf("Signal %s %s %s - risk blocked", idea.Market, label, pct0(idea.Confidence)), decision.Reason)
		now := a.now()
		rejection := storage.RiskRejection{
			Market: idea.Market, Direction: direction, Confidence: idea.Confidence, Thesis: idea.Thesis,
			Reason: decision.Reason, TradeIdeaID: idea.ID, Timestamp: portfolio.FormatISO(now),
		}
		a.mu.Lock()
		a.riskRejections = trimHead(append([]storage.RiskRejection{rejection}, a.riskRejections...), riskRejectionsMax)
		a.mu.Unlock()
		if err := a.repo.SaveRiskRejection(ctx, storage.RiskRejectionInput{
			Market: idea.Market, Direction: direction, Confidence: idea.Confidence, Thesis: idea.Thesis,
			Reason: decision.Reason, TradeIdeaID: idea.ID, Timestamp: now,
		}); err != nil {
			return SignalView{}, false, fmt.Errorf("save risk rejection: %w", err)
		}
		if strings.Contains(strings.ToLower(decision.Reason), "daily loss") {
			a.alertDailyLossOnce(idea.Market, decision.Reason)
		}

	case idea.Exit && !a.sameDirectionOpen(idea.Market, "long", isLive):
		// A rule exit only closes a held long; with nothing held it must not
		// become a paper short or an approval request.
		a.activity.Info(fmt.Sprintf("Signal %s %s - ignored (exit with no long held)", idea.Market, label), idea.Thesis)

	case a.control.EmergencyStop():
		// The stop is only checked when a tick starts; a slow evaluation can
		// still be running when the operator presses it.
		a.activity.Warn(fmt.Sprintf("Signal %s %s %s - not acted on (emergency stop active)", idea.Market, label, pct0(idea.Confidence)), idea.Thesis)

	case mode == string(domain.ModeManual):
		a.activity.Info(fmt.Sprintf("Signal %s %s %s - visible in dashboard (manual mode, no action taken)", idea.Market, label, pct0(idea.Confidence)), idea.Thesis)

	case exitBlock != "":
		a.activity.Info(fmt.Sprintf("Signal %s %s - not acted on (%s)", idea.Market, label, exitBlock), idea.Thesis)

	case mode == string(domain.ModeSemiAutomated):
		// One position per pair: skip when a same-direction position is open.
		if a.sameDirectionOpen(idea.Market, direction, isLive) {
			a.activity.Info(fmt.Sprintf("Signal %s %s - skipped (position already open)", idea.Market, label), idea.Thesis)
		} else if _, ok := a.approvals.Submit(*idea, decision); ok {
			a.activity.Success(fmt.Sprintf("Signal %s %s %s - submitted for approval", idea.Market, label, pct0(idea.Confidence)), idea.Thesis)
		} else {
			a.activity.Info(fmt.Sprintf("Signal %s %s - skipped (approval already pending)", idea.Market, label), idea.Thesis)
		}

	case mode == string(domain.ModeFullyAutomated):
		// Gate 1: the configurable confidence threshold. A rule exit of a
		// held long (checked above) is exempt: dropping it would keep the
		// position open after its strategy has said to sell.
		if !idea.Exit && idea.Confidence < a.cfg().MinSignalConfidence {
			a.activity.Info(fmt.Sprintf("Signal %s %s skipped - confidence %s below threshold %s",
				idea.Market, label, pct0(idea.Confidence), pct0(a.cfg().MinSignalConfidence)), "")
			return SignalView{}, false, nil
		}
		marketPrice := prices[idea.Market]
		switch {
		case a.sameDirectionOpen(idea.Market, direction, isLive):
			// Already in this trade; skip silently.
		case marketPrice == 0:
			a.activity.Warn(fmt.Sprintf("Signal %s %s - auto-execution skipped (no live price)", idea.Market, label), "")
		default:
			a.autoExecute(ctx, *idea, decision, marketPrice, equity, prices, isLive)
		}
	}
	return signal, true, nil
}

// sameDirectionOpen reports whether an open position on market already points
// in direction (an opposite-direction signal is a closing trade). Live-routed
// signals check the live book, where a pending entry already counts.
func (a *App) sameDirectionOpen(market, direction string, isLive bool) bool {
	positions := a.paper.PositionsForMarket(market)
	if isLive {
		positions = nil
		for _, p := range a.liveBook.Records() {
			if p.Market == market {
				positions = append(positions, p)
			}
		}
	}
	for _, p := range positions {
		if (p.Size > 0 && direction == "long") || (p.Size < 0 && direction == "short") {
			return true
		}
	}
	return false
}

// sizeInBase converts a risk decision into a base-currency order size.
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

// executeIntent routes an order to Kraken when isLive (the caller decided the
// route from one read of the environment and holds a.liveMu) or to the paper
// engine. In the paper environment a live-toggled market trades on paper.
func (a *App) executeIntent(ctx context.Context, intent domain.ExecutionIntent, idea domain.TradeIdea, marketPrice float64, isLive bool) (domain.OrderRecord, string) {
	if isLive {
		return a.placeLiveTrade(ctx, intent, idea, marketPrice)
	}
	if a.control.IsMarketLive(idea.Market) {
		a.logger.Warn("Live order blocked by paper environment", "market", idea.Market, "trade_idea_id", idea.ID)
		a.activity.Warn(
			fmt.Sprintf("%s is toggled live but TRADING_ENVIRONMENT=paper - executing on paper", idea.Market),
			"Set TRADING_ENVIRONMENT=live to send real orders to Kraken.",
		)
	}
	confidence := idea.Confidence
	return a.paper.Execute(intent, marketPrice, paper.ExecuteOptions{
		StrategyID: idea.StrategyID, SignalConfidence: &confidence,
		Environment: string(domain.EnvPaper), TradeIdeaID: idea.ID,
	})
}

// autoExecute places a fully automated order and records any closed paper
// long. A live close is recorded later, when reconciliation sees the sell fill.
func (a *App) autoExecute(ctx context.Context, idea domain.TradeIdea, decision domain.RiskDecision, marketPrice, equity float64, prices map[string]float64, isLive bool) {
	size := sizeInBase(idea, decision, equity, marketPrice)
	intent := domain.ExecutionIntent{ApprovalRequestID: "auto", Market: idea.Market, Direction: idea.Direction, Size: size}
	order, posID := a.executeIntent(ctx, intent, idea, marketPrice, isLive)

	if !isLive && order.Status == "filled" && idea.Direction == domain.Short && posID != "" {
		if pnl, ok := a.paper.RecordClosedTrade(posID, order.Price, "auto", idea.ID); ok {
			a.recordClosedOutcome(ctx, order.ID, idea.StrategyID, idea.Market, string(idea.Direction), pnl, a.paperCloseNotional(posID))
		}
	}
	if !isLive && order.Status == "filled" {
		a.savePaperEquity(ctx, prices)
	}
	a.activity.Success(
		fmt.Sprintf("Signal %s %s %s - auto-executed (%s)", idea.Market, directionLabel(string(idea.Direction)), pct0(idea.Confidence), order.Status),
		// order.Size is what filled: a closing short sells the whole long.
		fmt.Sprintf("Size: %.6f @ %s%s  Order: %s", order.Size, a.currencySymbol, fmtPrice(marketPrice), short8(order.ID)),
	)
}

// mergeSignals prepends new signals, drops stale ones for the same markets
// and caps the buffer at signalBufferMax.
func mergeSignals(fresh, existing []SignalView) []SignalView {
	newMarkets := map[string]bool{}
	for _, s := range fresh {
		newMarkets[s.Market] = true
	}
	merged := append([]SignalView{}, fresh...)
	for _, s := range existing {
		if !newMarkets[s.Market] {
			merged = append(merged, s)
		}
	}
	return trimHead(merged, signalBufferMax)
}

// ---------------------------------------------------------------------------
// Equity
// ---------------------------------------------------------------------------

// accountSnapshot returns cash, equity and position value for the active
// environment: Kraken balances in live mode, the paper engine otherwise.
func (a *App) accountSnapshot(ctx context.Context, prices map[string]float64) portfolio.EquitySnapshot {
	return a.accountSnapshotFor(ctx, a.TradingEnvironment(), prices)
}

// accountSnapshotFor returns the account values of the given environment, so
// a caller that has read the environment once uses it throughout.
func (a *App) accountSnapshotFor(ctx context.Context, environment string, prices map[string]float64) portfolio.EquitySnapshot {
	if environment == string(domain.EnvLive) {
		snap, err := a.live.GetAccountSnapshot(ctx, a.cfg().BaseCurrency)
		if err == nil && snap != nil {
			return portfolio.EquitySnapshot{Cash: snap.Cash, Equity: snap.Equity, PositionsValue: snap.PositionsValue}
		}
		// Keep last-known live values rather than showing paper cash.
		a.mu.Lock()
		cash, equity := a.currentCash, a.currentEquity
		a.mu.Unlock()
		a.logger.Warn("Kraken Balance unavailable — serving last-known live balance", "cash", cash, "equity", equity)
		return portfolio.EquitySnapshot{Cash: cash, Equity: equity, PositionsValue: math.Max(0, equity-cash)}
	}
	return portfolio.BuildEquitySnapshot(paperEquity{engine: a.paper}, prices, a.now())
}

// recordEquitySnapshot computes, caches and persists a mark-to-market point
// for the current environment. The cached cash handed to the strategy and
// the risk engine is the paper engine's available cash (open shorts reserve
// margin); the stored snapshot keeps the engine's full cash so a restart
// restores it exactly.
func (a *App) recordEquitySnapshot(ctx context.Context, prices map[string]float64) (portfolio.EquitySnapshot, error) {
	environment := a.TradingEnvironment()
	snap := a.accountSnapshotFor(ctx, environment, prices)
	timestamp := snap.Timestamp
	if timestamp == "" {
		timestamp = portfolio.FormatISO(a.now())
	}
	history := a.equityHistoryFor(ctx, environment)
	cash := snap.Cash
	if environment == string(domain.EnvPaper) {
		cash = a.paper.AvailableCash()
	}
	a.mu.Lock()
	a.currentEquity = snap.Equity
	a.currentCash = cash
	if history != nil {
		a.equityHistory, a.equityHistoryEnv = history, environment
	}
	a.equityHistory = trimTail(append(a.equityHistory, portfolio.EquityPoint{Timestamp: timestamp, Equity: snap.Equity}), equityHistoryMax)
	a.mu.Unlock()
	a.risk.UpdateEquity(snap.Equity)
	if err := a.repo.SaveEquitySnapshotFor(ctx, environment, snap.Equity, snap.Cash, snap.PositionsValue); err != nil {
		return snap, fmt.Errorf("save equity snapshot: %w", err)
	}
	return snap, nil
}

// equityHistoryFor returns the stored chart history of environment when the
// in-memory history belongs to another one (the environment was switched),
// or nil when the in-memory history is already the right series.
func (a *App) equityHistoryFor(ctx context.Context, environment string) []portfolio.EquityPoint {
	a.mu.Lock()
	current := a.equityHistoryEnv
	a.mu.Unlock()
	if current == environment {
		return nil
	}
	stored, err := a.repo.GetEquityHistoryFor(ctx, environment, equityHistoryMax)
	if err != nil {
		a.logger.Warn("Could not load equity history after an environment switch", "environment", environment, "error", err.Error())
	}
	history := make([]portfolio.EquityPoint, 0, len(stored)+1)
	for _, p := range stored {
		history = append(history, portfolio.EquityPoint{Timestamp: p.Timestamp, Equity: p.Equity})
	}
	return history
}

// equityTickerLoop records an equity point every 10 s for a smooth chart.
func (a *App) equityTickerLoop(ctx context.Context) {
	for sleepCtx(ctx, a.intervals.Equity) {
		// guarded logs errors and panics, so one bad snapshot never ends the loop.
		_ = a.guarded("Equity ticker error", func() error {
			prices := a.pricesCopy()
			if len(prices) == 0 {
				return nil
			}
			_, err := a.recordEquitySnapshot(ctx, prices)
			return err
		})
	}
}

// ---------------------------------------------------------------------------
// News, briefing and reflection
// ---------------------------------------------------------------------------

// newsLoop fetches news every 5 minutes and triggers briefings for new articles.
func (a *App) newsLoop(ctx context.Context) {
	for ctx.Err() == nil {
		if err := a.guarded("News loop error", func() error { return a.newsCycle(ctx) }); err != nil {
			a.activity.Error("News fetch failed", err.Error())
		}
		if !sleepCtx(ctx, a.intervals.News) {
			return
		}
	}
}

// newsCycle runs one news fetch: store, cache, brief new articles, re-probe
// the LLM. Sources switched off in the dashboard are not fetched and their
// articles leave the cache.
func (a *App) newsCycle(ctx context.Context) error {
	disabled := a.cfg().DisabledNewsSources
	adapters := enabledNewsAdapters(a.newsAdapters, disabled)
	if len(a.newsAdapters) > 0 && len(adapters) == 0 {
		a.mu.Lock()
		a.latestNews = withoutNewsSources(a.latestNews, disabled)
		a.mu.Unlock()
		a.activity.Info("All news feeds are switched off", "Switch a feed on under Settings → News & alerts.")
		a.llmClient.RecheckPrimary(ctx)
		return nil
	}
	var items []domain.NewsItem
	failures := 0
	for _, adapter := range adapters {
		fetched, err := adapter.FetchNews(ctx) // adapters log their own failures
		if err != nil {
			failures++
		}
		items = append(items, fetched...)
	}
	if len(adapters) > 0 && failures == len(adapters) {
		// Nothing was fetched: keep the previous cache rather than emptying it
		// (an empty cache would pause LLM strategies) and brief nothing.
		a.llmClient.RecheckPrimary(ctx)
		return fmt.Errorf("all %d news sources failed - keeping the previous %d cached articles", failures, len(a.News()))
	}
	for i := range items {
		items[i] = news.NormaliseNewsItem(items[i])
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].PublishedAt.After(items[j].PublishedAt) })
	fetched := make([]storage.NewsEntry, 0, len(items))
	for _, item := range items {
		_ = a.repo.UpsertNewsItem(ctx, item) // duplicates are ignored
		fetched = append(fetched, newsEntry(item))
	}

	// Merge into the previous cache (60 newest across all sources) so a feed
	// that failed this cycle keeps its articles. Only articles never briefed
	// before count as new; the briefed set keeps every cached ID and is
	// bounded by briefedNewsIDsMax.
	a.mu.Lock()
	latest := mergeNews(withoutNewsSources(a.latestNews, disabled), fetched, latestNewsMax)
	a.latestNews = latest
	var fresh []storage.NewsEntry
	for _, n := range latest {
		if !a.briefedNewsIDs[n.ID] {
			fresh = append(fresh, n)
			a.briefedNewsIDs[n.ID] = true
		}
	}
	if len(a.briefedNewsIDs) > briefedNewsIDsMax {
		a.briefedNewsIDs = make(map[string]bool, len(latest))
		for _, n := range latest {
			a.briefedNewsIDs[n.ID] = true
		}
	}
	a.mu.Unlock()

	if len(fresh) > 0 {
		a.logger.Info("Market briefing triggered", "new_articles", len(fresh))
		a.startBriefing(ctx, fresh, a.marketBriefingTask)
	}

	sourceSet := map[string]bool{}
	for _, n := range latest {
		sourceSet[n.Source] = true
	}
	sources := make([]string, 0, len(sourceSet))
	for s := range sourceSet {
		sources = append(sources, s)
	}
	sort.Strings(sources)
	a.activity.Info(fmt.Sprintf("News fetched - %d articles", len(latest)), strings.Join(sources, ", "))
	a.logger.Info("News feed updated", "count", len(latest))

	// Re-probe LM Studio each cycle so the client switches backends on change.
	a.llmClient.RecheckPrimary(ctx)
	return nil
}

// marketBriefingTask asks the LLM for a market-wide outlook after new news.
func (a *App) marketBriefingTask(ctx context.Context, articles []storage.NewsEntry) {
	_ = a.guarded("Market briefing task error", func() error {
		a.mu.Lock()
		var markets []llm.MarketData
		for _, m := range a.activeMarkets {
			price, ok := a.currentPrices[m]
			if !ok {
				continue
			}
			hist := append([]float64(nil), a.priceHistory[m]...)
			markets = append(markets, llm.MarketData{
				Market: m, Price: price,
				Indicators: analysis.ComputeAll(hist, analysis.DefaultTickSecs, a.ohlc5[m].Candles),
			})
		}
		a.mu.Unlock()
		if len(markets) == 0 {
			return nil
		}
		briefing := a.analyser.BriefMarket(ctx, newsArticles(articles), markets)
		if briefing == nil {
			return nil
		}
		keys := make([]string, 0, len(briefing.MarketOutlooks))
		for m := range briefing.MarketOutlooks {
			keys = append(keys, m)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, m := range keys {
			parts = append(parts, fmt.Sprintf("%s: %s", m, briefing.MarketOutlooks[m].Bias))
		}
		a.activity.Info(fmt.Sprintf("LLM briefing (%d new article(s)): %s", len(articles), briefing.KeyInsight), strings.Join(parts, "  "))
		return nil
	})
}

// reflectionLoop asks the LLM hourly for patterns in recent closed trades.
func (a *App) reflectionLoop(ctx context.Context) {
	if !sleepCtx(ctx, a.intervals.ReflectionDelay) {
		return
	}
	for ctx.Err() == nil {
		_ = a.guarded("Reflection loop error", func() error { return a.reflectOnce(ctx) })
		if !sleepCtx(ctx, a.intervals.Reflection) {
			return
		}
	}
}

// reflectOnce runs one reflection over the latest closed trades.
func (a *App) reflectOnce(ctx context.Context) error {
	trades, err := a.repo.GetClosedTrades(ctx, closedTradesForReflect)
	if err != nil {
		return err
	}
	outcomes := make([]llm.TradeOutcome, 0, len(trades))
	for _, t := range trades {
		confidence := 0.0
		if t.Confidence != nil {
			confidence = *t.Confidence
		}
		outcomes = append(outcomes, llm.TradeOutcome{
			Market: t.Market, Direction: t.Direction, EntryPrice: t.EntryPrice, ExitPrice: t.ExitPrice,
			PnL: t.PnL, PnLPct: t.PnLPct, Confidence: confidence, ExitReason: t.ExitReason,
			Indicators: analysis.Indicators(t.Indicators),
		})
	}
	reflection := a.analyser.ReflectOnOutcomes(ctx, outcomes)
	if reflection != nil {
		a.activity.Info(
			fmt.Sprintf("LLM reflection: %s", reflection.Pattern),
			fmt.Sprintf("Suggestion: %s (%s confidence)", reflection.Suggestion, pct0(reflection.InsightConfidence)),
		)
	}
	return nil
}

// ---------------------------------------------------------------------------
// OHLC
// ---------------------------------------------------------------------------

// ohlcLoop refreshes 5- and 15-minute candles for each enabled market on a
// slow cycle so Kraken's public rate limit is never exceeded.
func (a *App) ohlcLoop(ctx context.Context) {
	// Wait until the strategy loop has confirmed the active market list.
	for len(a.ActiveMarkets()) == 0 {
		if !sleepCtx(ctx, a.intervals.OHLCWait) {
			return
		}
	}
	for ctx.Err() == nil {
		// guarded logs a panic in one cycle so the loop carries on.
		_ = a.guarded("OHLC loop error", func() error {
			a.ohlcCycle(ctx)
			return nil
		})
		if !sleepCtx(ctx, a.intervals.OHLCRefresh) {
			return
		}
	}
}

// ohlcCycle refreshes the 5-minute, 15-minute and 4-hour candles for every
// enabled market once, pausing between requests; it stops early when ctx is
// cancelled.
func (a *App) ohlcCycle(ctx context.Context) {
	for _, market := range a.ActiveMarkets() {
		if !a.control.IsMarketEnabled(market) {
			continue
		}
		a.refreshOHLC(ctx, market, 5)
		if !sleepCtx(ctx, a.intervals.OHLCIntervalGap) {
			return
		}
		a.refreshOHLC(ctx, market, 15)
		if !sleepCtx(ctx, a.intervals.OHLCIntervalGap) {
			return
		}
		a.refreshOHLC(ctx, market, marketdata.TrendCandleMinutes)
		if !sleepCtx(ctx, a.intervals.OHLCMarketGap) {
			return
		}
	}
}

// refreshOHLC fetches one market's candles for interval and caches non-empty
// results. The 4-hour interval keeps a longer window than the others.
func (a *App) refreshOHLC(ctx context.Context, market string, interval int) {
	limit := ohlcCandleLimit
	if interval == marketdata.TrendCandleMinutes {
		limit = marketdata.TrendCandleLimit
	}
	candles, err := a.market.GetOHLC(ctx, market, interval, limit)
	if err != nil {
		a.logger.Error(fmt.Sprintf("OHLC %dm fetch failed", interval), "market", market, "error", err.Error())
		return
	}
	if len(candles) == 0 {
		return
	}
	a.mu.Lock()
	payload := OHLCPayload{Symbol: market, Interval: interval, Candles: candles}
	switch interval {
	case 15:
		a.ohlc15[market] = payload
	case marketdata.TrendCandleMinutes:
		a.ohlc240[market] = payload
	default:
		a.ohlc5[market] = payload
	}
	a.mu.Unlock()
	a.logger.Debug(fmt.Sprintf("OHLC %dm refreshed", interval), "market", market, "candles", len(candles))
}

// ---------------------------------------------------------------------------
// Alerts and small utilities
// ---------------------------------------------------------------------------

// sendAlert posts a JSON event to the configured webhook, if any. Failures
// are logged and never interrupt trading.
func (a *App) sendAlert(ctx context.Context, eventType string, payload map[string]any) {
	if a.cfg().AlertWebhookURL == "" {
		return
	}
	body := map[string]any{"event_type": eventType, "timestamp": portfolio.FormatISO(a.now())}
	for k, v := range payload {
		body[k] = v
	}
	raw, err := json.Marshal(body)
	if err == nil {
		var req *http.Request
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, a.cfg().AlertWebhookURL, bytes.NewReader(raw))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			var resp *http.Response
			resp, err = a.httpClient.Do(req)
			if err == nil {
				resp.Body.Close()
			}
		}
	}
	if err != nil {
		a.logger.Warn("Alert webhook failed", "event_type", eventType, "error", err.Error())
	}
}

// trimTail keeps the last max elements of s.
func trimTail[T any](s []T, max int) []T {
	if len(s) > max {
		return append([]T(nil), s[len(s)-max:]...)
	}
	return s
}

// trimHead keeps the first max elements of s.
func trimHead[T any](s []T, max int) []T {
	if len(s) > max {
		return s[:max]
	}
	return s
}

// copyPrices returns a copy of a price map.
func copyPrices(prices map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(prices))
	for k, v := range prices {
		out[k] = v
	}
	return out
}
