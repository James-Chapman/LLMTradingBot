# Risk Engine

**File:** `internal/risk/engine.go` (persistence helper in `internal/risk/persistence.go`)  
**Type:** `risk.Engine`

The risk engine is the single universal enforcement point for all portfolio-level trading constraints. Every signal — whether from the strategy loop, a manual approval, or fully-automated execution — must pass through `EvaluateTrade()` before any order is placed. The engine is environment-agnostic: it applies identically to paper and live trading.

---

## Design Principles

1. **Universal gate** — risk logic lives in one place. No mode-specific bypass.
2. **Fail closed** — any check that cannot be evaluated (missing data) defaults to safe behaviour.
3. **Context-aware** — the engine receives live portfolio state (open positions, cash, price, 24-hour volume) so checks are based on reality, not stale approximations.
4. **Configurable** — all thresholds come from `config.Settings` and can be changed in the dashboard's Settings dialog without code changes or a restart (`Engine.SetSettings` swaps them in while keeping equity and today's loss counters).
5. **Concurrency-safe** — every method takes the engine's `sync.Mutex`, so the strategy loop, HTTP handlers, and equity ticker can call it concurrently.

---

## State

```go
type Engine struct {
	mu               sync.Mutex
	settings         config.Settings
	now              func() time.Time // injectable clock; defaults to UTC now
	currentEquity    float64          // updated each tick via UpdateEquity()
	dailyLoss        float64          // accumulated losses today (UTC)
	dailyStartEquity float64          // equity at the start of today
	lastResetDate    time.Time        // UTC midnight of the tracked day
}

func NewEngine(settings config.Settings, now func() time.Time) *Engine
```

`NewEngine` seeds `currentEquity` and `dailyStartEquity` with `STARTING_CAPITAL`. `currentEquity` is then updated every strategy tick (and every equity-ticker tick) with the real mark-to-market total equity. This ensures that percentage-based limits (per-trade loss and daily loss) are always computed against the current portfolio value, not the starting capital.

The daily counters are exposed as `risk.State` (`DailyLoss`, `DailyStartEquity`, `LastResetDate`) through `Snapshot()` and `RestoreState()`.

---

## `EvaluateTrade()` — Check Order

```go
type EvaluateOptions struct {
	OpenPositions   []domain.PositionRecord
	AvailableCash   *float64
	MarketPrice     *float64
	MarketVolume24h *float64
}

func (e *Engine) EvaluateTrade(idea domain.TradeIdea, opts EvaluateOptions) domain.RiskDecision
```

Nil pointer options mean "not supplied" and skip the related check. Checks are applied in strict order. The first failure returns an immediate rejection — later checks are not evaluated.

### Step 0: Daily Reset and Sizing

`checkDailyResetLocked()` runs first. The engine then computes the notional it will evaluate:

```go
strategySize := idea.PositionSizingProposal * e.currentEquity
proposedSize := strategySize
if s.TargetTradeAmount > 0 {
	proposedSize = s.TargetTradeAmount
}
if proposedSize < s.MinTradeSize {
	proposedSize = s.MinTradeSize // a floor, not a rejection
}
```

`TARGET_TRADE_AMOUNT` is the preferred fixed notional order value in `BASE_CURRENCY` (default 100). `MIN_TRADE_SIZE` is an absolute floor (default 50), not a percentage. If `TARGET_TRADE_AMOUNT` is set to `0`, the engine falls back to the strategy's percentage sizing.

### Check 1: Liquidity

```go
if s.Min24hVolume > 0 && opts.MarketVolume24h != nil && *opts.MarketVolume24h < s.Min24hVolume {
	return e.reject(idea, "Insufficient liquidity: 24h volume ... below minimum ...")
}
```

Disabled when `MIN_24H_VOLUME` is `0` (the default) or no volume is supplied.

### Check 2: One Position Per Trading Pair

```go
for _, p := range marketPositions {
	if (p.Size > 0 && idea.Direction == domain.Long) || (p.Size < 0 && idea.Direction == domain.Short) {
		return e.reject(idea, fmt.Sprintf("Position already open for %s - one position per pair", idea.Market))
	}
}
```

This check is direction-aware. A LONG signal is blocked if any long already exists for that market. A SHORT (closing) signal is not blocked by an existing long — that is the intended close trade.

If `OpenPositions` is empty or nil, no position can match and the check passes. Call sites pass the open positions of the route the trade takes: the live book (`App.openPositionsFor(true)`, pending entries included) for a live-routed market, the paper engine's positions otherwise.

### Check 2b: Positive Equity

```go
if !(e.currentEquity > 0) && !closesLong {
	return e.reject(idea, "Account equity is €0.00 - no new trades until equity is positive")
}
```

Every limit below is a share of equity, and `AdjustedSizing` divides by it. With zero equity (for example a live snapshot of an empty Kraken account) or `NaN`, new exposure is refused with this reason instead of producing an infinite or `NaN` sizing that would break the dashboard's JSON. An exit (a short closing an open long) still passes; its decision then carries no `AdjustedSizing`.

### Check 3: Cash Sufficiency And Cash Adjustment

When a trade requires cash and the account has less than the target plus fee/slippage, risk reduces the notional to spendable cash as long as the result remains at or above `MIN_TRADE_SIZE`:

```go
feeFactor := 1 + s.FeeAndSlippage
if opts.AvailableCash != nil && hasPrice && !closesLong {
	cash := *opts.AvailableCash
	if cash < proposedSize*feeFactor {
		cashSized := cash / feeFactor
		if cashSized < s.MinTradeSize {
			return e.reject(idea, "Insufficient cash: need at least ..., have ...")
		}
		proposedSize = cashSized
		adjusted = true
	}
}
```

`FEE_AND_SLIPPAGE = 0.0036` (0.26% taker fee + 0.1% slippage). This is intentionally conservative. The check only runs when both available cash and a positive market price are supplied.

In paper trading the app passes the paper engine's **available** cash (`paper.Engine.AvailableCash()`): cash less the sale proceeds of open paper shorts, which are reserved as margin. `App.recordEquitySnapshot()` caches that figure, so the strategy and this check never count a short's proceeds as spendable.

**Exits are exempt.** A short signal on a market that has an open long is an exit (`closesLong`): execution sells the whole long, whatever size the signal proposes. It needs no cash and adds no exposure, so checks 2b, 3, 4 and 5 are skipped for it. Without this, a day at its loss limit would refuse the very trade that closes a losing position, leaving it open until its stop. New short exposure still requires available cash as margin in paper mode and passes through every check.

### Check 4: Per-Trade Loss Limit

```go
maxLoss := e.currentEquity * s.MaxLossPerTradePercent / 100
estimatedLoss := proposedSize * s.StopLossPct
if estimatedLoss > maxLoss {
	return e.reject(idea, "Estimated loss €X exceeds per-trade limit €Y")
}
```

Estimated loss assumes the stop-loss fires at exactly `STOP_LOSS_PCT` (default 5%, exposed as `Engine.StopLossAssumption()`). This is an upper-bound estimate, not a guarantee.

### Check 5: Daily Loss Limit

```go
dailyLimit := e.currentEquity * s.MaxDailyLossPercent / 100
if e.dailyLoss+estimatedLoss >= dailyLimit {
	return e.reject(idea, "Daily loss limit reached: €A accumulated + €B estimated = €C of €D")
}
```

The check includes the new trade's estimated loss, so a trade that would push the day past the limit is blocked before it happens. `dailyLoss` accumulates throughout the UTC day. When the UTC date changes, `checkDailyResetLocked()` resets it to zero. Currency symbols in reasons come from `config.CurrencySymbol(BASE_CURRENCY)`.

### Approval

If all checks pass:

```go
decision := domain.RiskDecision{
	TradeIdeaID:  idea.ID,
	Approved:     true,
	Reason:       "All risk checks passed",
	ProposedSize: domain.Float(proposedSize),
	Timestamp:    e.now().UTC(),
}
if adjusted && e.currentEquity > 0 {
	decision.AdjustedSizing = domain.Float(proposedSize / e.currentEquity)
}
```

`ProposedSize` (JSON `proposed_size_eur`) is always set on approval and is the exact quote-currency amount to trade; execution paths divide it by the market price to get the base-asset order size. `AdjustedSizing` is set whenever risk uses the fixed target amount, clamps to the minimum, or reduces to available cash (and equity is positive), and is only used as a fallback when `ProposedSize` is absent.

---

## State Update Methods

### `UpdateEquity(newEquity float64)`

Called by `App.recordEquitySnapshot()` on every strategy tick and every equity-ticker tick, immediately after computing the current total equity. Keeps percentage-based limits current.

### `RecordTradeResult(pnl float64)`

Updates `dailyLoss` and `currentEquity`:

```go
func (e *Engine) recordTradeResultLocked(pnl float64) {
	e.checkDailyResetLocked()
	e.dailyLoss += math.Max(0, -pnl) // only losses accumulate
	e.currentEquity += pnl
}
```

In the app, every position close (stop-loss, trailing stop, auto-execution, manual approval, manual close, operator reset) goes through `risk.RecordTradeResultAndPersist(engine, store, pnl)`, which records the result and then saves the daily counters to the `risk_state` table. The engine is always updated even if saving fails; the store error is returned and logged as a warning.

The backtest replay (`internal/backtest`, `docs/14-backtesting.md`) calls `RecordTradeResult` for every closed trade (signal exits and stop exits) and `UpdateEquity` every 30 s tick on the replay clock, so the daily loss limit accumulates and resets at UTC midnight of the replayed time as it does live.

### `RestoreState(state State) bool`

Called at start-up with the row loaded by `Repository.LoadRiskState()`. The persisted counters are applied only if `LastResetDate` is today (UTC); stale state is ignored so the daily reset still happens after a restart on a new day. This means the daily loss limit survives a same-day restart.

### `checkDailyResetLocked()`

Automatically called at the start of `EvaluateTrade()` and `RecordTradeResult()`. Resets `dailyLoss` to zero and `dailyStartEquity` to the current equity when the UTC date changes.

---

## Second-Layer Defence: Execution Engine

The risk engine is the primary guard. `paper.Engine.rejectionReasonLocked()` provides a secondary defence at execution time:

```go
if intent.Direction == domain.Long {
	if hasLong {
		return ReasonLongAlreadyOpen // hard block
	}
	if !affordable { // available cash (cash less short margin) >= fillValue + fee
		return ReasonInsufficientFunds
	}
	return ""
}
// SHORT (Execute has already sized a closing short to the long it closes)
if totalLong >= intent.Size {
	return "" // closing existing long, no cash needed
}
if hasShort {
	return ReasonShortAlreadyOpen // hard block on double-short
}
if !affordable {
	return ReasonInsufficientFunds
}
return ""
```

This secondary check exists because the risk engine evaluates the *proposed* trade, while the execution engine enforces at the moment of actual state mutation (under the paper engine's mutex). Both must pass for an order to fill. The live Kraken engine has its own local guards, described in the execution engine documentation.

---

## Risk Rejection Visibility

All risk rejections are prepended to `App.riskRejections` (capped at 50, newest first) and persisted to the `risk_rejections` table, then surfaced on the dashboard. This gives the operator visibility into why signals are being blocked without cluttering the activity feed.

```go
// internal/app/loops.go (handleIdea)
rejection := storage.RiskRejection{
	Market: idea.Market, Direction: direction, Confidence: idea.Confidence, Thesis: idea.Thesis,
	Reason: decision.Reason, TradeIdeaID: idea.ID, Timestamp: portfolio.FormatISO(now),
}
a.riskRejections = trimHead(append([]storage.RiskRejection{rejection}, a.riskRejections...), riskRejectionsMax)
```

A rejection whose reason mentions the daily loss limit also sends a `daily_loss_limit_hit` webhook alert. The dashboard displays the last 20 rejections (`riskRejectionsShown`) in the "Risk Rejections" panel.

---

## Risk Parameter Summary

| Parameter | Setting Key | Default | Description |
|---|---|---|---|
| Target trade amount | `TARGET_TRADE_AMOUNT` | EUR 100 | Preferred fixed notional order value |
| Min trade size | `MIN_TRADE_SIZE` | EUR 50 | Absolute minimum final order value |
| Stop-loss | `STOP_LOSS_PCT` | 5% | Fraction loss triggering auto-close; also the per-trade loss estimate |
| Fee + slippage | `FEE_AND_SLIPPAGE` | 0.36% | Cost estimate for cash sufficiency check |
| Per-trade loss | `MAX_LOSS_PER_TRADE_PERCENT` | 5% | Max estimated loss per trade vs equity |
| Daily loss | `MAX_DAILY_LOSS_PERCENT` | 5% | Max cumulative (realised + estimated) loss per UTC day |
| Liquidity | `MIN_24H_VOLUME` | 0 (off) | Minimum 24-hour volume for the market |
