# Execution Engines

**File:** `internal/paper/engine.go` (operator reset in `internal/paper/operator_reset.go`)  
**Type:** `paper.Engine`

Simulates Kraken spot order execution with realistic fee and slippage modelling. All state is held in memory behind a `sync.Mutex` and persisted to SQLite through the `paper.Repository` interface so positions survive restarts. Persistence failures are logged; the in-memory state stays authoritative.

Live execution is handled by `internal/kraken/execution.go` / `kraken.ExecutionEngine`, built on the official Kraken Go SDK (`github.com/krakenfx/api-go/v2`). Its `Execute()` returns the same `(domain.OrderRecord, positionID)` pair as the paper engine, submits a Kraken `AddOrder` private REST call (market order, or limit order when `ExecutionIntent.Price` is set), stores accepted orders in `order_records` with status `pending` and the returned Kraken transaction ID, and returns an empty position ID because exchange balances/fills are the live source of truth. Order volume and limit price are formatted with the pair's lot and tick precision from the SDK's `spot.Normalizer` (loaded lazily from Kraken's public asset metadata, falling back to 8 decimal places). Live submission failures are stored in `rejected_trades`, not the trade ledger.

The live engine has two local guards that refuse an order before any network call, recording it in `rejected_trades` with the reason in `OrderRecord.ExchangeOrderID`:

- `paper_mode_read_only` — `ExecuteOptions.Environment` is anything other than `"live"`. This guard is new in the Go version; the Python engine had no equivalent.
- `missing_api_credentials` — `KRAKEN_API_KEY` or `KRAKEN_API_SECRET` is empty.

Per-market routing is controlled by `control.State.IsMarketLive()` (`control_state.live_markets`) together with `TRADING_ENVIRONMENT`: `App.executeIntent()` sends an order to `kraken.ExecutionEngine` with `Environment: "live"` only when the environment is `live` and the market is flagged live. Otherwise it goes to `paper.Engine`. A live-flagged market in the paper environment is executed on paper, and the app records a `warn` activity entry plus a "Live order blocked by paper environment" log line (since v0.6.1; earlier versions sent real orders regardless of `TRADING_ENVIRONMENT`).

Kraken private REST calls use `kraken.CallWithBackoff()`. `AddOrder`, `QueryOrders` and `CancelOrder` retry transient rate-limit, temporary-lockout, service-unavailable, and too-many-requests errors with exponential backoff and up to 10% jitter (3 attempts, starting at 500 ms) before rejecting/logging the operation. The SDK reports Kraken's in-body `error` array as a Go error, so both transport and in-response rate limits are retried.

Pending live orders are reconciled every 2 strategy ticks (~60 s) in the live environment by `ReconcilePendingOrders()`, which queries Kraken in batches of 50 transaction IDs and marks orders `filled` (with fill price and fee), `canceled`, or `expired`.

In live mode, dashboard account totals use `kraken.ExecutionEngine.GetAccountSnapshot()`. The quote-currency balance from Kraken `Balance` (`ZEUR`, falling back to the bare `EUR` key) becomes Available Cash, and Total Equity is Kraken's `TradeBalance` equivalent balance (`eb`), falling back to cash if that call fails. If the balance lookup fails entirely, the app logs a warning and keeps serving the last-known live cash and equity. This differs from the Python version, which fell back to the paper snapshot.

---

## Constants

```go
const (
	TakerFeeRate       = 0.0026 // 0.26% Kraken taker fee (low-volume account)
	SlippageRate       = 0.001  // 0.10% one-way conservative slippage estimate
	SimulatedLatencyMS = 150    // nominal only; never slept
)

// Execution rejection reasons, persisted with rejected trades.
const (
	ReasonInsufficientFunds = "insufficient_funds"
	ReasonLongAlreadyOpen   = "long_position_already_open"
	ReasonShortAlreadyOpen  = "short_position_already_open"
)
```

---

## Internal State

```go
type Engine struct {
	mu        sync.Mutex
	repo      Repository                       // DB access; nil in unit tests
	now       func() time.Time                 // injectable clock
	cash      float64                          // liquid cash in quote currency
	positions map[string]domain.PositionRecord // position_id → PositionRecord
	openOrder []string                         // open position ids in insertion order
	meta      map[string]*PositionMeta         // position_id → metadata
	orders    []domain.OrderRecord             // all orders this session
	fills     []domain.FillRecord              // all fills this session
}
```

### `PositionMeta`

For each position, metadata is stored separately to avoid bloating the domain model. It outlives the position after a close so the outcome can be recorded:

```go
type PositionMeta struct {
	StrategyID    string    // source strategy
	Direction     string    // "long" | "short" ("" if unknown)
	Confidence    *float64  // signal confidence at entry
	OpenedAt      time.Time // position open time
	Market        string    // trading pair
	TradeIdeaID   string    // opening signal (persisted to DB so it survives restarts)
	EntryFee      float64   // fee paid on the opening fill
	ShortProceeds *float64  // locked sale value of a paper short; nil for longs/restored rows
	TrailingHigh  *float64  // best price seen for a long
	TrailingLow   *float64  // best price seen for a short

	// Set when closing:
	Closed           bool
	AvgPriceAtClose  float64 // entry price
	SizeAtClose      float64 // quantity closed
	ExitPriceAtClose float64 // fill price of the closing order
	ExitFeeAtClose   float64 // fee paid on the closing fill
}
```

`Engine.Meta(positionID)` returns a copy.

---

## `Execute()` — Main Fill Method

```go
type ExecuteOptions struct {
	StrategyID       string
	SignalConfidence *float64
	Environment      string // default "paper"
	TradeIdeaID      string
}

func (e *Engine) Execute(intent domain.ExecutionIntent, marketPrice float64, opts ExecuteOptions) (domain.OrderRecord, string)
```

Returns `(OrderRecord, positionID)`. For a rejection, `positionID` is `""`.

### Execution Flow

```
1.  Apply slippage to marketPrice             → fillPrice
2.  Compute fillValue = size * fillPrice
3.  Compute fee = fillValue * TakerFeeRate
4.  rejectionReasonLocked()                   → save rejected_trades row and return status "rejected" if non-empty
5.  Create FillRecord
6.  applyFillLocked()                         → mutates cash + positions; returns positionID
7.  Set order.Status = "filled", order.PositionID = positionID
8.  Persist to DB: order, fill, and position upsert (or delete when a short closed a long)
9.  Return (order, positionID)
```

Rejected orders return `order.Status == "rejected"` and `positionID == ""`, but they are not saved to `order_records`. The rejected-trades register stores timestamp, market, direction, size, attempted price, signal confidence, reason, and `trade_idea_id`.

---

## `applySlippage()`

```go
func applySlippage(direction domain.Direction, price float64) float64 {
	if direction == domain.Long {
		return price * (1 + SlippageRate)
	}
	return price * (1 - SlippageRate)
}
```

LONGs pay more (adverse slippage on buy). SHORTs receive less (adverse slippage on sell). This models realistic market impact conservatively.

---

## `rejectionReasonLocked()`

Secondary enforcement layer (primary is the risk engine). Returns `""` when the intent can fill.

**For LONG:**
- Any long already open for this market → `long_position_already_open`
- Otherwise require `cash >= fillValue + fee` → else `insufficient_funds`

**For SHORT:**
- If existing longs cover the short size (`totalLong >= intent.Size`) → allow (closing a long, no cash needed)
- Any short already open for this market → `short_position_already_open`
- Otherwise (paper short, no existing long): require `cash >= fillValue + fee` → else `insufficient_funds`

---

## `applyFillLocked()`

Mutates `cash` and `positions`. Returns the affected position ID.

### LONG Path

```go
e.cash -= fillValue + fee
id := e.openPositionLocked(intent.Market, intent.Size, fillPrice, now) // new UUID
e.meta[id] = &PositionMeta{
	StrategyID: opts.StrategyID, Direction: "long", Confidence: copyFloat(opts.SignalConfidence),
	OpenedAt: now, Market: intent.Market, TradeIdeaID: opts.TradeIdeaID,
	EntryFee: fee, TrailingHigh: domain.Float(fillPrice),
}
return id
```

### SHORT Path

```go
// FIFO: find the oldest long for this market.
var longs []domain.PositionRecord
for _, id := range e.openOrder {
	if pos := e.positions[id]; pos.Market == intent.Market && pos.Size > 0 {
		longs = append(longs, pos)
	}
}
sort.SliceStable(longs, func(i, j int) bool { return longs[i].Timestamp.Before(longs[j].Timestamp) })
if len(longs) > 0 {
	oldest := longs[0]
	e.cash += fillValue - fee // receive sale proceeds minus fee
	markClosed(e.metaLocked(oldest.PositionID), oldest.AvgPrice, oldest.Size, fillPrice, fee)
	e.removePositionLocked(oldest.PositionID)
	return oldest.PositionID // return the CLOSED position's ID
}

// No long to close: open a paper short. Only the fee leaves cash; the sale
// proceeds are locked as margin so free cash cannot increase.
e.cash -= fee
id := e.openPositionLocked(intent.Market, -intent.Size, fillPrice, now) // negative size = short
e.meta[id] = &PositionMeta{ /* ... */ EntryFee: fee, ShortProceeds: domain.Float(fillValue), TrailingLow: domain.Float(fillPrice)}
return id
```

The returned position ID is the same ID as the original LONG order. This is what links the BUY and SELL entries in the trade ledger — both `order_records` rows share the same `position_id`.

---

## `ClosePosition()` — Targeted Closure

```go
type CloseOptions struct {
	Environment       string // default "paper"
	ApprovalRequestID string // close source recorded on the order; default "stop_loss"
}

func (e *Engine) ClosePosition(positionID string, marketPrice float64, opts CloseOptions) (domain.OrderRecord, bool)
```

Unlike `Execute()`, this targets a specific position by ID. It is used by the stop-loss and trailing-stop checks in `App.checkStops()`, by manual UI closes (`ApprovalRequestID: "manual_close"`), and by the operator reset (`"operator_reset"`). It does **not** go through `rejectionReasonLocked()` — stop-losses always execute.

```
1.  Look up position by ID                 → return (OrderRecord{}, false) if not open
2.  Determine exit direction               → SHORT if pos.Size > 0 (closing long), else LONG
3.  Apply slippage; compute fee
4.  Update cash:
      closing a long  → cash += fillValue - fee
      closing a short → cash += ShortProceeds - fillValue - fee
                        (restored shorts fall back to AvgPrice × size)
5.  Create order + fill records (status "filled")
6.  Save close details to PositionMeta (markClosed)
7.  Remove from positions
8.  Persist to DB: order, fill, delete open_positions row
9.  Return (order, true)
```

The stop-loss check calls `StopLossTriggered(positionID, marketPrice, stopLossPct)` first. That helper compares the current price with the entry price and returns `true` only when the position is losing by at least the configured percentage. The trailing stop is a separate check: `UpdateTrailingPrices()` keeps a high watermark for longs and a low watermark for shorts every tick, and `TrailingStopTriggered(positionID, marketPrice, trailPct)` fires when price retraces `TRAILING_STOP_PCT` from that watermark. Watermarks of restored positions start empty and are seeded from the first live price, not the stale entry price.

---

## `RecordClosedTrade()`

Called after `Execute()` or `ClosePosition()` to write a `signal_outcomes` row and return the realised P&L. This is the source of truth for the learning system and LLM reflection.

```go
func (e *Engine) RecordClosedTrade(positionID string, exitPrice float64, exitReason, closingTradeIdeaID string) (float64, bool)
```

It returns `false` when no repository is configured or the position has not been closed. The exit price recorded is the close order's actual fill price (`ExitPriceAtClose`), falling back to the `exitPrice` argument. Missing strategy IDs are stored as `"unknown"`.

- `closingTradeIdeaID` — the `trade_idea_id` of the SHORT signal that triggered the close. Populated only for `"auto"` (fully-automated mode) and `"manual_approve"` closes. Empty for stop-loss, trailing-stop, manual UI, and operator-reset closes (no signal is available in those paths).

This is **not** called automatically — the caller in `internal/app` is responsible for calling it at the right time with the right `exitReason`, then passing the returned P&L to `App.recordClosedOutcome()` (risk engine, `order_records.pnl`, learner).

---

## `UpdateMarkPrices()`

Called every tick from the strategy loop:

```go
func (e *Engine) UpdateMarkPrices(prices map[string]float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, pos := range e.positions {
		if price, ok := prices[pos.Market]; ok {
			pos.UnrealizedPnL = pos.Size * (price - pos.AvgPrice)
			e.positions[id] = pos
		}
	}
}
```

`PositionRecord` is stored by value, so the update copies the record, changes it, and writes it back.

---

## `RestoreFromDB()`

Called at start-up to reload open positions:

```go
func (e *Engine) RestoreFromDB() error {
	rows, err := e.repo.GetOpenPositions()
	// ...
	for _, row := range rows {
		e.positions[row.PositionID] = domain.PositionRecord{ /* ... */ }
		e.meta[row.PositionID] = &PositionMeta{
			StrategyID:  row.StrategyID,
			Direction:   row.Direction,
			Confidence:  copyFloat(row.SignalConfidence),
			OpenedAt:    openedAt,
			Market:      row.Market,
			TradeIdeaID: row.TradeIdeaID, // restored for signal linkage
		}
	}
	return nil
}
```

`trade_idea_id` is persisted to `open_positions` so the link from a position to its opening signal survives restarts. Without this, any close after a restart would produce an unlinked `signal_outcomes` row. Entry fees and short proceeds are not persisted, so a restored position's P&L excludes its entry fee and a restored short closes against `AvgPrice × size`.

The cash balance is restored separately in `App.restoreState()` via `repo.GetLatestCash()` and `paper.Engine.SetCash()` before `RestoreFromDB()` is called.

---

## `TotalEquity()`

```go
func (e *Engine) TotalEquity(prices map[string]float64) float64 {
	positionsValue := 0.0
	for _, pos := range e.positions {
		price, ok := prices[pos.Market]
		if !ok {
			price = pos.AvgPrice
		}
		if pos.Size > 0 {
			positionsValue += pos.Size * price // long: mark-to-market value
		} else {
			positionsValue += (pos.AvgPrice - price) * math.Abs(pos.Size) // short: unrealised P&L
		}
	}
	return e.cash + positionsValue
}
```

Falls back to `AvgPrice` if no live price is available for a market. Shorts contribute only their unrealised P&L because their sale proceeds never entered free cash.

---

## Position Reset (Admin)

`POST /api/positions/reset` calls `App.ResetPositions()`, which is refused with HTTP 403 in live mode.

Before clearing anything, `paper.ClosePositionsForOperatorReset()` iterates every open position and closes it through the normal accounting path:

1. Calls `ClosePosition(positionID, exitPrice, CloseOptions{ApprovalRequestID: "operator_reset"})`. The exit price is the last known market price, falling back to `AvgPrice` if no live price is cached.
2. Calls `RecordClosedTrade(positionID, order.Price, "operator_reset", "")` — writes a `signal_outcomes` row with `exit_reason = "operator_reset"`.
3. Records the P&L with the risk engine (and persists the daily counters), stamps it on the close order (`UpdateOrderPnL`), and calls `learner.RecordOutcome(...)` so the learner and drawdown tracker reflect the forced close.

After all outcomes are recorded, positions are cleared and an equity snapshot is saved:

```go
a.repo.ClearAllOpenPositions(ctx)
a.paper.ClearPositions() // drops positions and metadata from memory
a.savePaperEquity(ctx, prices)
```

This means a reset never produces unexplained "CLOSED" entries — every reset position has a close order in the ledger and a `signal_outcomes` row visible in the Closed Trades panel and available to the LLM reflection loop.

---

## Fee and P&L Accounting

For a LONG → SELL cycle:

```
Cash paid on BUY:      fillValue + fee   = size × price × (1 + slippage) × (1 + fee_rate)
Cash received on SELL: fillValue - fee   = size × price × (1 - slippage) × (1 - fee_rate)
```

The `pnl` stored in `order_records` and `signal_outcomes` is the realised P&L net of both fees:

```go
func realisedPnL(meta *PositionMeta) float64 {
	signedSize := -meta.SizeAtClose
	if meta.Direction == "long" {
		signedSize = meta.SizeAtClose
	}
	return signedSize*(meta.ExitPriceAtClose-meta.AvgPriceAtClose) - meta.EntryFee - meta.ExitFeeAtClose
}
```

Both prices are actual fill prices, so slippage is already included. Fees are also recorded individually in `order_records.fee` and `fill_records.fee`. Any direction other than `"long"` is treated as a short.
