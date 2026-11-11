package llm

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/James-Chapman/LLMTradingBot/internal/analysis"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy/calc"
)

// analyserLogger also writes to llm.log.
var analyserLogger = observability.Logger("llm_analyser")

// ── System prompts (textually identical to the Python bot) ──────────────────

const (
	systemBriefer = "You are a crypto market analyst. " +
		"You receive current prices, technical indicators, and breaking news. " +
		"Give a concise outlook for each watched trading pair. " +
		"Respond ONLY with a valid JSON object — no prose, no markdown."

	systemAnalyst = "You are a concise crypto trading analyst. " +
		"You receive live market data, technical indicators, a market briefing, " +
		"portfolio state, and recent news. " +
		"Assess whether the signal is supported or contradicted by the evidence. " +
		"Respond ONLY with a valid JSON object — no prose, no markdown."

	systemRecommender = "You are a concise crypto trading decision engine. " +
		"You receive live market data, technical indicators, a market briefing, " +
		"portfolio state, and recent news. " +
		"Recommend exactly one action: long, short, or hold. " +
		"Use indicators as context, but do not require indicator consensus. " +
		"Respond ONLY with a valid JSON object — no prose, no markdown."

	systemReflector = "You are a quantitative trading coach reviewing paper trade results. " +
		"Find one clear pattern and give one concrete, actionable improvement. " +
		"Respond ONLY with a valid JSON object — no prose, no markdown."
)

// ── Result and input types ──────────────────────────────────────────────────

// MarketOutlook is the LLM's view of one market inside a briefing.
type MarketOutlook struct {
	Bias  string  `json:"bias"`  // bullish | bearish | neutral (free text from the model)
	Score float64 `json:"score"` // -1.0 … +1.0
	Note  string  `json:"note"`
}

// MarketBriefing is the cached result of a news-triggered market-wide assessment.
type MarketBriefing struct {
	MarketOutlooks   map[string]MarketOutlook `json:"market_outlooks"`
	OverallSentiment float64                  `json:"overall_sentiment"` // -1.0 … +1.0
	KeyInsight       string                   `json:"key_insight"`
	ArticleCount     int                      `json:"article_count"` // new articles that triggered it
	GeneratedAt      time.Time                `json:"generated_at"`
}

// clone returns a deep copy so callers cannot mutate the cached briefing.
func (b *MarketBriefing) clone() *MarketBriefing {
	if b == nil {
		return nil
	}
	cp := *b
	cp.MarketOutlooks = make(map[string]MarketOutlook, len(b.MarketOutlooks))
	for k, v := range b.MarketOutlooks {
		cp.MarketOutlooks[k] = v
	}
	return &cp
}

// SignalAnalysis is the LLM's assessment of a strategy signal.
type SignalAnalysis struct {
	Sentiment       float64 `json:"sentiment"`        // -1.0 … +1.0
	ConfidenceScale float64 `json:"confidence_scale"` // 0.5 … 2.0; below the veto threshold the trade is skipped
	Reasoning       string  `json:"reasoning"`
	LLMUsed         bool    `json:"llm_used"`
}

// LLMTradeRecommendation is an independent long/short/hold decision.
type LLMTradeRecommendation struct {
	Action     string  `json:"action"`     // long | short | hold
	Confidence float64 `json:"confidence"` // 0.0 … 0.95
	Sentiment  float64 `json:"sentiment"`  // -1.0 … +1.0
	Reasoning  string  `json:"reasoning"`
	LLMUsed    bool    `json:"llm_used"`
}

// Reflection is a pattern found in closed trades plus one suggestion.
type Reflection struct {
	Pattern           string    `json:"pattern"`
	Suggestion        string    `json:"suggestion"`
	InsightConfidence float64   `json:"insight_confidence"`
	GeneratedAt       time.Time `json:"generated_at"`
}

// NewsArticle is a news item as fed to the prompts (Summary is a short excerpt).
type NewsArticle struct {
	ID      string `json:"id"`
	Source  string `json:"source"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

// MarketData is one market's price and indicators for a briefing; a slice
// preserves the watched-market order used in the prompt.
type MarketData struct {
	Market     string
	Price      float64
	Indicators analysis.Indicators
}

// TradeOutcome is a closed trade used for reflection; Indicators is the
// snapshot stored at entry time (nil when none was linked).
type TradeOutcome struct {
	Market     string
	Direction  string
	EntryPrice float64
	ExitPrice  float64
	PnL        float64
	PnLPct     float64 // fraction, e.g. 0.012 for +1.2%
	Confidence float64 // confidence at entry, 0..1
	ExitReason string
	Indicators analysis.Indicators
}

// SignalRequest is the input to AnalyseSignal.
type SignalRequest struct {
	Market         string
	Direction      string // long | short
	MomentumPct    float64
	BaseConfidence float64
	News           []NewsArticle
	CurrentPrice   float64 // 0 omits the price from the prompt
	Indicators     analysis.Indicators
	Equity         float64
	Cash           float64
	OpenPositions  []domain.PositionRecord
}

// RecommendationRequest is the input to RecommendTrade.
type RecommendationRequest struct {
	Market        string
	CurrentPrice  float64
	PreviousPrice float64
	PriceHistory  []float64
	Indicators    analysis.Indicators
	News          []NewsArticle
	Equity        float64
	Cash          float64
	OpenPositions []domain.PositionRecord
}

// BriefingStore persists the latest briefing and reflection so they survive
// restarts. Load methods return (nil, nil) when nothing is stored.
type BriefingStore interface {
	SaveBriefing(ctx context.Context, briefing MarketBriefing, briefedNewsIDs []string) error
	LoadLatestBriefing(ctx context.Context) (*MarketBriefing, error)
	SaveReflection(ctx context.Context, reflection Reflection) error
	LoadLatestReflection(ctx context.Context) (*Reflection, error)
}

// neutralAnalysis is returned when the LLM cannot adjust a signal.
func neutralAnalysis() SignalAnalysis {
	return SignalAnalysis{Sentiment: 0, ConfidenceScale: 1.0, Reasoning: "LLM unavailable — no adjustment"}
}

// holdRecommendation is a no-trade result when the LLM gives no usable decision.
func holdRecommendation(reason string) LLMTradeRecommendation {
	return LLMTradeRecommendation{Action: "hold", Reasoning: reason}
}

// ── Analyser ────────────────────────────────────────────────────────────────

// LLMAnalyser builds trading prompts, calls the LLM and parses its answers.
// It caches the latest briefing and reflection and is safe for concurrent use.
type LLMAnalyser struct {
	client   Client
	currency string

	mu               sync.RWMutex
	store            BriefingStore
	latestBriefing   *MarketBriefing
	latestReflection *Reflection
}

// NewAnalyser returns an analyser using client; currencySymbol (e.g. "€")
// prefixes money values in prompts.
func NewAnalyser(client Client, currencySymbol string) *LLMAnalyser {
	return &LLMAnalyser{client: client, currency: currencySymbol}
}

// SetStore wires in persistence for briefings and reflections. Call once at startup.
func (a *LLMAnalyser) SetStore(store BriefingStore) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.store = store
}

// getStore returns the configured store, or nil.
func (a *LLMAnalyser) getStore() BriefingStore {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.store
}

// LatestBriefing returns a copy of the cached briefing, or nil.
func (a *LLMAnalyser) LatestBriefing() *MarketBriefing {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.latestBriefing.clone()
}

// LatestReflection returns a copy of the cached reflection, or nil.
func (a *LLMAnalyser) LatestReflection() *Reflection {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.latestReflection == nil {
		return nil
	}
	cp := *a.latestReflection
	return &cp
}

// LoadFromDB restores the latest briefing and reflection from the store.
// Failures are logged and leave the cache empty. Call once at startup.
func (a *LLMAnalyser) LoadFromDB(ctx context.Context) {
	store := a.getStore()
	if store == nil {
		return
	}
	if b, err := store.LoadLatestBriefing(ctx); err != nil {
		analyserLogger.Warn("Could not restore LLM briefing from DB", "error", err.Error())
	} else if b != nil {
		restored := b.clone()
		restored.MarketOutlooks = normaliseTypedOutlooks(restored.MarketOutlooks)
		restored.GeneratedAt = utcOrNow(restored.GeneratedAt)
		a.mu.Lock()
		a.latestBriefing = restored
		a.mu.Unlock()
		analyserLogger.Info("LLM briefing restored from DB", "insight", restored.KeyInsight)
	}

	if r, err := store.LoadLatestReflection(ctx); err != nil {
		analyserLogger.Warn("Could not restore LLM reflection from DB", "error", err.Error())
	} else if r != nil {
		restored := *r
		restored.GeneratedAt = utcOrNow(restored.GeneratedAt)
		a.mu.Lock()
		a.latestReflection = &restored
		a.mu.Unlock()
		analyserLogger.Info("LLM reflection restored from DB", "pattern", restored.Pattern)
	}
}

// snapshot returns copies of the cached briefing and reflection for prompt building.
func (a *LLMAnalyser) snapshot() (*MarketBriefing, *Reflection) {
	return a.LatestBriefing(), a.LatestReflection()
}

// chatJSON sends a system+user prompt and returns the parsed JSON, or false.
func (a *LLMAnalyser) chatJSON(ctx context.Context, system, user string) (map[string]any, bool) {
	reply, err := a.client.Chat(ctx, []Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}, true)
	if err != nil || reply.JSON == nil {
		return nil, false
	}
	return reply.JSON, true
}

// ── Market briefing ─────────────────────────────────────────────────────────

// BriefMarket produces a market-wide outlook when new news arrives, caches it
// as the latest briefing and persists it. Returns nil when the LLM cannot be
// used or its answer cannot be parsed.
func (a *LLMAnalyser) BriefMarket(ctx context.Context, newArticles []NewsArticle, markets []MarketData) *MarketBriefing {
	if !a.client.CanAttempt() || len(markets) == 0 {
		return nil
	}

	priceLines := make([]string, 0, len(markets))
	known := make([]string, 0, len(markets))
	for _, m := range markets {
		known = append(known, m.Market)
		priceLines = append(priceLines, a.briefingPriceLine(m))
	}
	marketsList := strings.Join(known, ", ")
	newsBlock := formatNewsBlock(newArticles[:min(len(newArticles), 8)])

	userMsg := "Breaking news update — " + itoa(len(newArticles)) + " new article(s).\n\n" +
		"Current market prices:\n" + strings.Join(priceLines, "\n") + "\n\n" +
		"New articles:\n" + newsBlock + "\n\n" +
		"Assess each pair: " + marketsList + "\n\n" +
		"Use exactly these market symbols as JSON object keys: " + marketsList + "\n\n" +
		"Return JSON with exactly these keys:\n" +
		"  market_outlooks   — object: each market key maps to " +
		"{\"bias\": \"bullish|bearish|neutral\", \"score\": float -1 to 1, \"note\": max 12 words}\n" +
		"  overall_sentiment — float -1.0 to 1.0\n" +
		"  key_insight       — string, max 20 words, most important takeaway"

	result, ok := a.chatJSON(ctx, systemBriefer, userMsg)
	if !ok {
		return nil
	}

	overall, err := floatField(result, "overall_sentiment", 0.0)
	if err != nil {
		analyserLogger.Warn("LLM briefing parse error", "error", err.Error(), "raw", result)
		return nil
	}
	briefing := &MarketBriefing{
		MarketOutlooks:   NormaliseMarketOutlooks(pyGet(result, "market_outlooks", map[string]any{}), known),
		OverallSentiment: clamp(overall, -1.0, 1.0),
		KeyInsight:       strField(result, "key_insight", ""),
		ArticleCount:     len(newArticles),
		GeneratedAt:      utcNow(),
	}

	a.mu.Lock()
	a.latestBriefing = briefing
	store := a.store
	a.mu.Unlock()

	outlookKeys := make([]string, 0, len(briefing.MarketOutlooks))
	for k := range briefing.MarketOutlooks {
		outlookKeys = append(outlookKeys, k)
	}
	sort.Strings(outlookKeys)
	analyserLogger.Info("LLM market briefing completed",
		"markets", outlookKeys, "overall", briefing.OverallSentiment, "insight", briefing.KeyInsight)

	// Persist so the briefing context survives restarts.
	if store != nil {
		ids := make([]string, 0, len(newArticles))
		for _, n := range newArticles {
			if n.ID != "" {
				ids = append(ids, n.ID)
			}
		}
		if err := store.SaveBriefing(ctx, *briefing.clone(), ids); err != nil {
			analyserLogger.Warn("Could not persist LLM briefing", "error", err.Error())
		}
	}
	return briefing.clone()
}

// briefingPriceLine renders one market's price and indicator summary.
func (a *LLMAnalyser) briefingPriceLine(m MarketData) string {
	ind := map[string]any(m.Indicators)
	chg := "—"
	if changes, ok := asGroup(ind["price_changes"]); ok && len(changes) > 0 {
		chg = formatChanges(changes)
	}
	bbPos := any("?")
	if bb, ok := asGroup(ind["bb"]); ok && len(bb) > 0 {
		bbPos = pyGet(bb, "position", "?")
	}
	macd, _ := asGroup(ind["macd"])
	stoch, _ := asGroup(ind["stoch"])
	return "  " + m.Market + ": " + a.currency + fmtThousands(m.Price, 2) +
		" | " + chg +
		" | RSI " + pyRepr(pyGet(ind, "rsi_14", "?")) +
		" | EMA " + pyRepr(pyGet(ind, "ema_cross", "?")) +
		" | BB " + pyRepr(bbPos) + "%" +
		" | MACD " + pyRepr(pyGet(macd, "bias", "?")) +
		" | Stoch " + pyRepr(pyGet(stoch, "k", "?")) +
		" | WR " + pyRepr(pyGet(ind, "williams_r", "?"))
}

// ── Signal analysis ─────────────────────────────────────────────────────────

// AnalyseSignal asks the LLM whether a strategy signal is supported by the
// evidence, enriched with the latest briefing and reflection. It returns a
// neutral analysis (LLMUsed=false) when the LLM is unavailable or unparseable.
func (a *LLMAnalyser) AnalyseSignal(ctx context.Context, req SignalRequest) SignalAnalysis {
	if !a.client.CanAttempt() {
		return neutralAnalysis()
	}
	briefing, reflection := a.snapshot()

	priceStr := ""
	if req.CurrentPrice != 0 {
		priceStr = " | Price: " + a.currency + fmtThousands(req.CurrentPrice, 2)
	}
	userMsg := "Signal: " + req.Market + " " + strings.ToUpper(req.Direction) + "\n" +
		"Confidence: " + fmtPercent(req.BaseConfidence, 0) + " | Momentum: " + fmtSigned(req.MomentumPct, 2) + "%" +
		priceStr + "\n" +
		briefingBlock(briefing, req.Market) +
		reflectionBlock(reflection, "Apply this advice when assessing the current signal.\n") + "\n" +
		"Technical indicators (30s ticks):\n" + formatIndicators(req.Indicators) + "\n\n" +
		a.portfolioBlock(req.Equity, req.Cash, req.OpenPositions) +
		"Recent news (" + assetName(req.Market) + "):\n" + formatNewsBlock(relevantNews(req.Market, req.News, 5)) + "\n\n" +
		"Return JSON with exactly these keys:\n" +
		"  sentiment        — float -1.0 to 1.0\n" +
		"  confidence_scale — float 0.5 to 2.0 (multiplied by base confidence; " +
		"use >1.0 when evidence strongly supports the signal, <1.0 when it contradicts, " +
		"<0.7 only when you would actively oppose the trade)\n" +
		"  reasoning        — string, max 20 words"

	result, ok := a.chatJSON(ctx, systemAnalyst, userMsg)
	if !ok {
		return neutralAnalysis()
	}
	sentiment, err1 := floatField(result, "sentiment", 0.0)
	scale, err2 := floatField(result, "confidence_scale", 1.0)
	if err1 != nil || err2 != nil {
		analyserLogger.Warn("LLM response parse error", "error", firstErr(err1, err2).Error(), "raw", result)
		return neutralAnalysis()
	}
	out := SignalAnalysis{
		Sentiment:       clamp(sentiment, -1.0, 1.0),
		ConfidenceScale: clamp(scale, 0.5, 2.0),
		Reasoning:       strField(result, "reasoning", ""),
		LLMUsed:         true,
	}
	analyserLogger.Info("LLM signal analysis", "market", req.Market, "direction", req.Direction,
		"sentiment", out.Sentiment, "scale", out.ConfidenceScale, "reasoning", out.Reasoning)
	return out
}

// ── Trade recommendation ────────────────────────────────────────────────────

// actionAliases maps common model wordings onto long/short/hold.
var actionAliases = map[string]string{
	"buy":      "long",
	"bullish":  "long",
	"sell":     "short",
	"bearish":  "short",
	"none":     "hold",
	"no_trade": "hold",
	"no trade": "hold",
}

// RecommendTrade asks the LLM to independently recommend long, short or hold.
// It returns a hold (LLMUsed=false) when the LLM is unavailable or unparseable.
func (a *LLMAnalyser) RecommendTrade(ctx context.Context, req RecommendationRequest) LLMTradeRecommendation {
	if !a.client.CanAttempt() {
		return holdRecommendation("LLM unavailable — no recommendation")
	}
	briefing, reflection := a.snapshot()

	previous := req.PreviousPrice
	momentum, ok := calc.CalculateMomentum(req.CurrentPrice, &previous, req.PriceHistory)
	if !ok {
		momentum = 0
	}

	userMsg := "Market: " + req.Market + "\n" +
		"Price: " + a.currency + fmtThousands(req.CurrentPrice, 2) + " | " +
		"Previous: " + a.currency + fmtThousands(req.PreviousPrice, 2) + " " +
		"| Momentum: " + fmtSigned(momentum*100, 2) + "%\n" +
		briefingBlock(briefing, req.Market) +
		reflectionBlock(reflection, "Apply this advice when choosing the current action.\n") + "\n" +
		"Technical indicators (context only, do not require consensus):\n" + formatIndicators(req.Indicators) + "\n\n" +
		a.portfolioBlock(req.Equity, req.Cash, req.OpenPositions) +
		"Recent news (" + assetName(req.Market) + "):\n" + formatNewsBlock(relevantNews(req.Market, req.News, 5)) + "\n\n" +
		"Return JSON with exactly these keys:\n" +
		"  action     — string: long, short, or hold\n" +
		"  confidence — float 0.0 to 0.95, only high when the trade is actionable\n" +
		"  sentiment  — float -1.0 to 1.0\n" +
		"  reasoning  — string, max 20 words"

	result, ok := a.chatJSON(ctx, systemRecommender, userMsg)
	if !ok {
		return holdRecommendation("LLM unavailable — no recommendation")
	}

	action := strings.ToLower(strings.TrimSpace(strField(result, "action", "hold")))
	if alias, found := actionAliases[action]; found {
		action = alias
	}
	if action != "long" && action != "short" && action != "hold" {
		action = "hold"
	}
	confidence, err1 := floatField(result, "confidence", 0.0)
	sentiment, err2 := floatField(result, "sentiment", 0.0)
	if err1 != nil || err2 != nil {
		analyserLogger.Warn("LLM recommendation parse error", "error", firstErr(err1, err2).Error(), "raw", result)
		return holdRecommendation("LLM response could not be parsed")
	}
	rec := LLMTradeRecommendation{
		Action:     action,
		Confidence: clamp(confidence, 0.0, 0.95),
		Sentiment:  clamp(sentiment, -1.0, 1.0),
		Reasoning:  strField(result, "reasoning", ""),
		LLMUsed:    true,
	}
	analyserLogger.Info("LLM trade recommendation", "market", req.Market, "action", rec.Action,
		"confidence", rec.Confidence, "sentiment", rec.Sentiment, "reasoning", rec.Reasoning)
	return rec
}

// ── Outcome reflection ──────────────────────────────────────────────────────

// ReflectOnOutcomes asks the LLM for one pattern across recent closed trades
// (at least five; the newest 20 are used), caches and persists the result.
func (a *LLMAnalyser) ReflectOnOutcomes(ctx context.Context, outcomes []TradeOutcome) *Reflection {
	if !a.client.CanAttempt() || len(outcomes) < 5 {
		return nil
	}
	sample := outcomes[:min(len(outcomes), 20)]

	var wins, losses []float64
	total := 0.0
	for _, o := range sample {
		total += o.PnL
		if o.PnL > 0 {
			wins = append(wins, o.PnL)
		} else if o.PnL < 0 {
			losses = append(losses, o.PnL)
		}
	}
	winRate := float64(len(wins)) / float64(len(sample))

	tradeLines := make([]string, 0, len(sample))
	for _, o := range sample {
		tradeLines = append(tradeLines, a.tradeLine(o))
	}

	userMsg := "Performance summary (" + itoa(len(sample)) + " trades):\n" +
		"  Win rate: " + fmtPercent(winRate, 0) + " | Total P&L: " + a.currency + fmtSigned(total, 2) + "\n" +
		"  Avg win: " + a.currency + fmtSigned(mean(wins), 2) + " | " +
		"Avg loss: " + a.currency + fmtSigned(mean(losses), 2) + "\n\n" +
		"Individual trades with entry-time indicators:\n" + strings.Join(tradeLines, "\n") + "\n\n" +
		"Look for indicator-level patterns (e.g. RSI levels, EMA direction, MACD bias) " +
		"that correlate with wins or losses. Give one specific, actionable finding.\n\n" +
		"Return JSON with exactly these keys:\n" +
		"  pattern            — string, one pattern observed (max 25 words)\n" +
		"  suggestion         — string, one concrete adjustment (max 25 words)\n" +
		"  insight_confidence — float 0.0 to 1.0"

	result, ok := a.chatJSON(ctx, systemReflector, userMsg)
	if !ok {
		return nil
	}
	confidence, err := floatField(result, "insight_confidence", 0.5)
	if err != nil {
		analyserLogger.Warn("LLM reflection parse error", "error", err.Error())
		return nil
	}
	reflection := &Reflection{
		Pattern:           strField(result, "pattern", ""),
		Suggestion:        strField(result, "suggestion", ""),
		InsightConfidence: clamp(confidence, 0.0, 1.0),
		GeneratedAt:       utcNow(),
	}

	a.mu.Lock()
	a.latestReflection = reflection
	store := a.store
	a.mu.Unlock()
	analyserLogger.Info("LLM reflection", "pattern", reflection.Pattern, "suggestion", reflection.Suggestion)

	// Persist so the reflection survives restarts.
	if store != nil {
		if err := store.SaveReflection(ctx, *reflection); err != nil {
			analyserLogger.Warn("Could not persist LLM reflection", "error", err.Error())
		}
	}
	cp := *reflection
	return &cp
}

// tradeLine renders one closed trade with a compact entry-indicator summary.
func (a *LLMAnalyser) tradeLine(o TradeOutcome) string {
	ind := map[string]any(o.Indicators)
	var parts []string
	if v, ok := ind["rsi_14"]; ok && v != nil {
		parts = append(parts, "RSI "+pyRepr(v))
	}
	if v := ind["ema_cross"]; pyTruthy(v) {
		parts = append(parts, "EMA "+pyRepr(v))
	}
	if macd, _ := asGroup(ind["macd"]); pyTruthy(macd["bias"]) {
		parts = append(parts, "MACD "+pyRepr(macd["bias"]))
	}
	if stoch, _ := asGroup(ind["stoch"]); stoch["k"] != nil {
		parts = append(parts, "Stoch "+pyRepr(stoch["k"]))
	}
	if v, ok := ind["williams_r"]; ok && v != nil {
		parts = append(parts, "WR "+pyRepr(v))
	}
	if v, ok := ind["atr_pct"]; ok && v != nil {
		parts = append(parts, "ATR "+strconvFixed(numOr(ind, "atr_pct"), 2)+"%")
	}
	indStr := "no indicators"
	if len(parts) > 0 {
		indStr = strings.Join(parts, " | ")
	}
	return "  " + orDefault(o.Market, "?") + " " + strings.ToUpper(o.Direction) +
		" | entry " + a.currency + strconvFixed(o.EntryPrice, 2) +
		" exit " + a.currency + strconvFixed(o.ExitPrice, 2) +
		" | P&L " + a.currency + fmtSigned(o.PnL, 2) +
		" (" + fmtSignedPercent(o.PnLPct, 1) + ")" +
		" conf " + fmtPercent(o.Confidence, 0) +
		" | " + orDefault(o.ExitReason, "?") +
		" | at entry: " + indStr
}

// ── Shared prompt blocks ────────────────────────────────────────────────────

// briefingBlock renders the latest briefing (and this market's outlook) or "".
func briefingBlock(b *MarketBriefing, market string) string {
	if b == nil {
		return ""
	}
	block := "\nLatest market briefing (" + itoa(ageMinutesSince(b.GeneratedAt)) + "m ago, " +
		itoa(b.ArticleCount) + " new article(s)):\n" +
		"  Key insight: " + b.KeyInsight + "\n" +
		"  Overall sentiment: " + fmtSigned(b.OverallSentiment, 2) + "\n"
	if raw, ok := b.MarketOutlooks[market]; ok {
		outlook := normaliseTypedOutlook(raw)
		block += "  " + market + " outlook: " + outlook.Bias +
			" (score " + fmtSigned(outlook.Score, 2) + ") — " + outlook.Note + "\n"
	}
	return block
}

// reflectionBlock renders the latest self-reflection with a closing instruction, or "".
func reflectionBlock(r *Reflection, instruction string) string {
	if r == nil {
		return ""
	}
	return "\nYour most recent self-reflection (" + itoa(ageMinutesSince(r.GeneratedAt)) + "m ago, " +
		"confidence " + fmtPercent(r.InsightConfidence, 0) + "):\n" +
		"  Pattern:    " + r.Pattern + "\n" +
		"  Suggestion: " + r.Suggestion + "\n" +
		instruction
}

// portfolioBlock renders equity, cash, exposure and open positions.
func (a *LLMAnalyser) portfolioBlock(equity, cash float64, positions []domain.PositionRecord) string {
	exposure := 0.0
	if equity > 0 {
		exposure = pyRound1((equity - cash) / equity * 100)
	}
	return "Portfolio:\n" +
		"  Equity: " + a.currency + fmtThousands(equity, 2) + " | " +
		"Cash: " + a.currency + fmtThousands(cash, 2) + " | Exposure: " + pyFloatRepr(exposure) + "%\n" +
		"Open positions:\n" + formatPositions(positions, a.currency) + "\n\n"
}

// ── News, indicator and position formatting ─────────────────────────────────

// assetAliases maps an asset ticker to the keywords that identify its news.
var assetAliases = map[string][]string{
	"btc":  {"bitcoin", "btc"},
	"eth":  {"ethereum", "eth"},
	"sol":  {"solana", "sol"},
	"xrp":  {"ripple", "xrp"},
	"ada":  {"cardano", "ada"},
	"link": {"chainlink", "link"},
	"ltc":  {"litecoin", "ltc"},
	"dot":  {"polkadot", "dot"},
	"bnb":  {"binance", "bnb"},
	"avax": {"avalanche", "avax"},
}

// assetName returns the base asset of a market symbol ("BTC/EUR" → "BTC").
func assetName(market string) string {
	base, _, _ := strings.Cut(market, "/")
	return base
}

// relevantNews picks up to maxItems articles mentioning the market's asset,
// padding with general news when fewer than three match.
func relevantNews(market string, news []NewsArticle, maxItems int) []NewsArticle {
	asset := strings.ToLower(assetName(market))
	keywords, ok := assetAliases[asset]
	if !ok {
		keywords = []string{asset}
	}
	picked := make([]bool, len(news))
	var relevant []NewsArticle
	for i, n := range news {
		if len(relevant) >= maxItems {
			break
		}
		text := strings.ToLower(n.Title + n.Summary)
		for _, kw := range keywords {
			if strings.Contains(text, kw) {
				relevant = append(relevant, n)
				picked[i] = true
				break
			}
		}
	}
	if len(relevant) < 3 {
		for i, n := range news {
			if len(relevant) >= maxItems {
				break
			}
			if !picked[i] {
				relevant = append(relevant, n)
			}
		}
	}
	return relevant
}

// formatNewsBlock renders news as "[source] title — summary" lines.
func formatNewsBlock(items []NewsArticle) string {
	if len(items) == 0 {
		return "  No recent news"
	}
	lines := make([]string, 0, len(items))
	for _, n := range items {
		source := orDefault(n.Source, "?")
		summary := []rune(strings.TrimSpace(n.Summary))
		if len(summary) == 0 {
			lines = append(lines, "  ["+source+"] "+n.Title)
			continue
		}
		short := strings.TrimRightFunc(string(summary[:min(len(summary), 120)]), unicode.IsSpace)
		if len(summary) > 120 {
			short += "…"
		}
		lines = append(lines, "  ["+source+"] "+n.Title+" — "+short)
	}
	return strings.Join(lines, "\n")
}

// formatChanges renders sorted price changes as "1h: +0.50% | 5m: -0.10%".
func formatChanges(changes map[string]any) string {
	keys := make([]string, 0, len(changes))
	for k := range changes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+fmtSigned(numOr(changes, k), 2)+"%")
	}
	return strings.Join(parts, " | ")
}

// formatIndicators renders the indicator snapshot as prompt lines.
func formatIndicators(indicators analysis.Indicators) string {
	ind := map[string]any(indicators)
	var lines []string
	if changes, ok := asGroup(ind["price_changes"]); ok && len(changes) > 0 {
		lines = append(lines, "  Change — "+formatChanges(changes))
	}
	if rsi, ok := ind["rsi_14"]; ok && rsi != nil {
		lines = append(lines, "  RSI(14): "+pyRepr(rsi)+" — "+pyRepr(pyGet(ind, "rsi_signal", "")))
	}
	if pyTruthy(ind["ema9"]) && pyTruthy(ind["ema21"]) {
		lines = append(lines, "  EMA9/21: "+fmtThousands(numOr(ind, "ema9"), 2)+" / "+fmtThousands(numOr(ind, "ema21"), 2)+
			" — "+pyRepr(pyGet(ind, "ema_cross", ""))+" crossover")
	}
	if bb, ok := asGroup(ind["bb"]); ok && len(bb) > 0 {
		lines = append(lines, "  BB: pos "+pyRepr(bb["position"])+"% of band | "+
			"upper "+fmtThousands(numOr(bb, "upper"), 2)+" lower "+fmtThousands(numOr(bb, "lower"), 2)+
			" | width "+pyRepr(bb["width_pct"])+"%")
	}
	if m, ok := asGroup(ind["macd"]); ok && len(m) > 0 {
		histStr, sigStr := "", ""
		if _, has := m["histogram"]; has {
			histStr = " | histogram " + fmtSigned(numOr(m, "histogram"), 4)
		}
		if _, has := m["signal"]; has {
			sigStr = " | signal " + fmtSigned(numOr(m, "signal"), 4) + " (" + pyRepr(pyGet(m, "signal_bias", "?")) + ")"
		}
		lines = append(lines, "  MACD: "+fmtSigned(numOr(m, "line"), 4)+" ("+pyRepr(m["bias"])+")"+sigStr+histStr)
	}
	if stoch, ok := asGroup(ind["stoch"]); ok && len(stoch) > 0 {
		lines = append(lines, "  Stoch: %K "+pyRepr(stoch["k"])+" / %D "+pyRepr(stoch["d"])+" — "+pyRepr(stoch["bias"]))
	}
	if wr, ok := ind["williams_r"]; ok && wr != nil {
		lines = append(lines, "  Williams %R: "+pyRepr(wr)+" — "+pyRepr(pyGet(ind, "williams_r_signal", "")))
	}
	if atrPct, ok := ind["atr_pct"]; ok && atrPct != nil {
		lines = append(lines, "  ATR: "+pyRepr(pyGet(ind, "atr", "?"))+" ("+strconvFixed(numOr(ind, "atr_pct"), 3)+"% of price)")
	}
	if len(lines) == 0 {
		return "  Insufficient price history for indicators"
	}
	return strings.Join(lines, "\n")
}

// formatPositions renders open positions as LONG/SHORT lines.
func formatPositions(positions []domain.PositionRecord, currency string) string {
	if len(positions) == 0 {
		return "  None"
	}
	lines := make([]string, 0, len(positions))
	for _, p := range positions {
		direction := "SHORT"
		if p.Size > 0 {
			direction = "LONG"
		}
		size := p.Size
		if size < 0 {
			size = -size
		}
		lines = append(lines, "  "+orDefault(p.Market, "?")+" "+direction+" "+
			"× "+strconvFixed(size, 6)+" "+
			"@ "+currency+fmtThousands(p.AvgPrice, 2)+" "+
			"unrealised "+currency+fmtSigned(p.UnrealizedPnL, 2))
	}
	return strings.Join(lines, "\n")
}

// ── Outlook normalisation ───────────────────────────────────────────────────

// nonAlnum matches characters ignored when comparing market symbols.
var nonAlnum = regexp.MustCompile(`[^a-z0-9]`)

// normaliseMarketKey lowercases a market label and strips separators for matching.
func normaliseMarketKey(value string) string {
	return nonAlnum.ReplaceAllString(strings.ToLower(value), "")
}

// matchMarketKey maps an LLM-returned key (e.g. "btc eur") back to a known symbol.
func matchMarketKey(raw string, known []string) string {
	for _, m := range known {
		if m == raw {
			return raw
		}
	}
	wanted := normaliseMarketKey(raw)
	for _, m := range known {
		if normaliseMarketKey(m) == wanted {
			analyserLogger.Warn("LLM market key normalised", "raw_key", raw, "matched_market", m)
			return m
		}
	}
	analyserLogger.Warn("LLM market key did not match watched markets", "raw_key", raw)
	return raw
}

// NormaliseOutlookValue converts one raw outlook (an object, a bare bias
// string, or anything else) into a MarketOutlook with a clamped score.
func NormaliseOutlookValue(value any) MarketOutlook {
	switch v := value.(type) {
	case MarketOutlook:
		return normaliseTypedOutlook(v)
	case map[string]any:
		score := 0.0
		if raw := pyGet(v, "score", 0.0); pyTruthy(raw) {
			if f, err := pyFloat(raw); err == nil {
				score = f
			}
		}
		bias, note := "neutral", ""
		if raw := pyGet(v, "bias", "neutral"); pyTruthy(raw) {
			bias = pyRepr(raw)
		}
		if raw := v["note"]; pyTruthy(raw) {
			note = pyRepr(raw)
		}
		return MarketOutlook{Bias: bias, Score: clamp(score, -1.0, 1.0), Note: note}
	case string:
		return MarketOutlook{Bias: orDefault(v, "neutral")}
	default:
		return MarketOutlook{Bias: "neutral"}
	}
}

// NormaliseMarketOutlooks converts a raw market→outlook object (from the LLM
// or legacy persisted JSON) into typed outlooks. When known is non-empty,
// keys are mapped back onto those exact market symbols.
func NormaliseMarketOutlooks(raw any, known []string) map[string]MarketOutlook {
	out := map[string]MarketOutlook{}
	var obj map[string]any
	switch v := raw.(type) {
	case map[string]any:
		obj = v
	case map[string]MarketOutlook:
		obj = make(map[string]any, len(v))
		for k, o := range v {
			obj[k] = o
		}
	default:
		return out
	}
	// Sorted keys make the result deterministic if two raw keys collide.
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		market := k
		if len(known) > 0 {
			market = matchMarketKey(k, known)
		}
		out[market] = NormaliseOutlookValue(obj[k])
	}
	return out
}

// normaliseTypedOutlook defaults an empty bias to neutral and clamps the score.
func normaliseTypedOutlook(o MarketOutlook) MarketOutlook {
	return MarketOutlook{Bias: orDefault(o.Bias, "neutral"), Score: clamp(o.Score, -1.0, 1.0), Note: o.Note}
}

// normaliseTypedOutlooks applies normaliseTypedOutlook to every entry.
func normaliseTypedOutlooks(in map[string]MarketOutlook) map[string]MarketOutlook {
	out := make(map[string]MarketOutlook, len(in))
	for k, v := range in {
		out[k] = normaliseTypedOutlook(v)
	}
	return out
}

// ── Small helpers ───────────────────────────────────────────────────────────

// ageMinutesSince returns whole minutes elapsed since t, never negative.
func ageMinutesSince(t time.Time) int {
	return max(0, int(time.Since(t).Minutes()))
}

// utcOrNow returns t in UTC, or the current time when t is zero.
func utcOrNow(t time.Time) time.Time {
	if t.IsZero() {
		return utcNow()
	}
	return t.UTC()
}

// orDefault returns s, or def when s is empty.
func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// mean returns the average of values, or 0 when empty.
func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// firstErr returns the first non-nil error.
func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
