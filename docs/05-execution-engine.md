# Execution Engines

**File:** `internal/paper/engine.go` (operator reset in `internal/paper/operator_reset.go`)  
**Type:** `paper.Engine`

Simulates Kraken spot order execution with realistic fee and slippage modelling. All state is held in memory behind a `sync.Mutex` and persisted to SQLite through the `paper.Repository` interface so positions survive restarts. Persistence failures are logged; the in-memory state stays authoritative.

Live execution is handled by `internal/kraken/execution.go` / `kraken.ExecutionEngine`, built on the official Kraken Go SDK (`github.com/krakenfx/api-go/v2`). Its `Execute()` returns the same `(domain.OrderRecord, positionID)` pair as the paper engine, submits a Kraken `AddOrder` private REST call (market order, or limit order when `ExecutionIntent.Price` is set), stores accepted orders in `order_records` with status `pending` and the returned Kraken transaction ID, and returns `ExecuteOptions.PositionID` (the live position the order belongs to, see [Live Position Book](#live-position-book)). Every `AddOrder` carries a client order id (`cl_ord_id`, the local order ID, stored in `order_records.client_order_id`). Order volume and limit price are formatted with the pair's lot and tick precision from the SDK's `spot.Normalizer` (loaded lazily from Kraken's public asset metadata, falling back to 8 decimal places). The float size and price are converted with `exactDecimal()` (via their shortest decimal string), not the SDK's `decimal.NewFromFloat64`, which multiplies in binary and truncates: it turned a size of 0.29 into 0.28. Live submission failures are stored in `rejected_trades`, not the trade ledger.

The live engine has two local guards that refuse an order before any network call, recording it in `rejected_trades` with the reason in `OrderRecord.ExchangeOrderID`:

- `paper_mode_read_only` — `ExecuteOptions.Environment` is anything other than `"live"`. This guard is new in the Go version; the Python engine had no equivalent.
- `missing_api_credentials` — `KRAKEN_API_KEY` or `KRAKEN_API_SECRET` is empty.

Per-market routing is controlled by `control.State.IsMarketLive()` (`control_state.live_markets`) together with `TRADING_ENVIRONMENT`: `App.executeIntent()` sends an order to `kraken.ExecutionEngine` with `Environment: "live"` only when the environment is `live` and the market is flagged live. Otherwise it goes to `paper.Engine`. A live-flagged market in the paper environment is executed on paper, and the app records a `warn` activity entry plus a "Live order blocked by paper environment" log line (since v0.6.1; earlier versions sent real orders regardless of `TRADING_ENVIRONMENT`).

Kraken private REST calls use `kraken.CallWithBackoff()` with exponential backoff and up to 10% jitter (3 attempts, starting at 500 ms). `QueryOrders`, `OpenOrders`, `ClosedOrders` and `CancelOrder` retry rate-limit, temporary-lockout, service-unavailable and too-many-requests errors. `AddOrder` is different, because a retry of an order that Kraken did place would buy twice:

- It retries only errors that prove the order was **not placed** (`kraken.IsOrderNotPlacedError`: rate limit, temporary lockout, too many requests). Every retry sends the same `cl_ord_id`.
- An **unknown outcome** — a transport error or timeout, an unreadable (e.g. HTML) reply, `EService:Unavailable`, `EService:Busy` or `EGeneral:Internal error` — is not retried. The order is stored as `pending` without a transaction ID and is found later by its client order id.
- Any other Kraken error rejects the order (`rejected_trades`).
- The call is sent with `context.WithoutCancel` plus a 30 s timeout, so a shutdown that starts while the order is in flight does not lose the reply or the order row. A context that is already cancelled before submission refuses the order locally (`cancelled_before_submission`) without a network call.

**Order ID and volume.** The caller may pass `ExecuteOptions.OrderID`; the live book does, so the position row naming the order is written *before* the order is sent (see [Live Position Book](#live-position-book)). That ID is the `cl_ord_id`. The volume stored in `order_records.size` is the volume actually sent (after lot rounding), not the requested float. **Sell** volumes are rounded **down** to the pair's lot decimals (8 when the metadata is unavailable), so a sell never asks for more than the bot holds; buys keep normal rounding. `MinOrderSize(market)` returns Kraken's `ordermin` for a pair (false when the metadata is unavailable).

**Not sent vs unknown.** `AddOrder` runs through an SDK executor hook that records whether an HTTP request actually started. An error before that (for example the SDK failing to build the request) rejects the order as `order not sent: …` — it is certain nothing reached Kraken. An error after the request started follows the unknown-outcome rule above.

Pending live orders are reconciled by `ReconcilePendingOrders(ctx, tracked)`, which returns one `kraken.OrderUpdate` per order whose state changed. `tracked` is the list of orders the live book is waiting for (its `entry_pending` / `exit_pending` order IDs); it covers an order whose `order_records` row could not be written:

- Orders with a transaction ID are queried with `QueryOrders` in batches of 50.
- Orders with only a client order id — and every tracked order the ledger does not list as pending — are looked up with `OpenOrders` and then `ClosedOrders` (`cl_ord_id` filter). An entry is used only if its `cl_ord_id` equals the one asked for, so an unrelated order Kraken returns is never taken as ours. If the order is still open, its transaction ID is stored and it stays `pending`. If nothing is found after a 5-minute grace period from submission, the order becomes `not_found` (it was never placed). A tracked order is looked up once per pass even when the ledger also lists it.
- If the ledger read fails, the pass continues with the tracked orders alone.
- The executed volume is read from `vol_exec`, the average price from `price` (or `cost / vol_exec`), and the fee from `fee`. An order that executed any volume is `filled` with the executed size written to `order_records.size`, including a partial fill that was then cancelled or expired. A `canceled` or `expired` order with no executed volume keeps its requested size. A `closed` reply without `vol_exec` (older replies) is treated as fully executed with the volume that was sent.
- Every update carries the order's `PositionID` and `Direction`, so the app can match it to a position even when the order ID is unknown to the book.

In live mode, dashboard account totals use `kraken.ExecutionEngine.GetAccountSnapshot()`. The quote-currency balance from Kraken `Balance` (`ZEUR`, falling back to the bare `EUR` key) becomes Available Cash, and Total Equity is Kraken's `TradeBalance` equivalent balance (`eb`), falling back to cash if that call fails. If the balance lookup fails entirely, the app logs a warning and keeps serving the last-known live cash and equity. This differs from the Python version, which fell back to the paper snapshot.

---

## Constants

```go
const (
	TakerFeeRate       = 0.008  // 0.80% Kraken Pro Tier 1 taker fee (0.26% before 0.21.0)
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

A short on a market with an open long closes the oldest long **in full**. `Execute` sets `intent.Size` to that long's size before it computes the fill value and fee, so cash, the order record, the fill and the realised P&L all use the same size. The signal's own size is a currency amount divided by today's price, so it rarely equals the size that was bought; before 0.9.2 cash was credited for the signal's size while the P&L used the long's size, and the two disagreed.

```go
// FIFO: the oldest long for this market (oldestLongLocked).
if oldest, found := e.oldestLongLocked(intent.Market); found {
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

`POST /api/positions/reset` calls `App.ResetPositions()`, which is refused with HTTP 403 in live mode. It only touches paper positions; live positions are never reset.

`paper.ClosePositionsForOperatorReset()` iterates every open paper position and closes it through the normal accounting path:

1. Calls `ClosePosition(positionID, exitPrice, CloseOptions{ApprovalRequestID: "operator_reset"})`. The exit price is the last known market price, falling back to `AvgPrice` if no live price is cached. That close removes the position from memory and deletes its own `open_positions` row.
2. Calls `RecordClosedTrade(positionID, order.Price, "operator_reset", "")` — writes a `signal_outcomes` row with `exit_reason = "operator_reset"`.
3. Records the P&L with the risk engine (and persists the daily counters), stamps it on the close order (`UpdateOrderPnL`), and calls `learner.RecordOutcomeWithNotional(...)` (notional = entry price × size) so the learner and drawdown tracker reflect the forced close.

Nothing is blanket-cleared afterwards (there is no `ClearAllOpenPositions` / `ClearPositions` call), so a position opened while the reset runs stays open instead of vanishing without a close. Finally an equity snapshot is saved (`a.savePaperEquity(ctx, prices)`).

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

---

## Live Position Book

**Files:** `internal/livebook/book.go` (`livebook.Book`), `internal/app/live_trading.go`, table `live_positions` (`internal/storage/live_positions.go`)

Positions opened by real Kraken orders are tracked in their own book, separate from the paper engine. A paper position and a live position on the same market never mix: the risk engine, the same-direction check and stops each use the book of the route the idea takes. The book is held in memory and written through to `live_positions`, so it survives a restart (`App.restoreState()` calls `Book.Restore()`; a load failure stops start-up).

A live position has one of three statuses:

| Status | Meaning |
|---|---|
| `entry_pending` | The buy order is being sent or was accepted and has not been reconciled yet. `requested_size` is what was sent; `entry_order_id` names the order. |
| `open` | The buy order executed. `size`, `avg_price` and `entry_fee` are the executed values from Kraken. |
| `exit_pending` | A sell order for `size` is being sent or was accepted and has not been reconciled yet. `exit_order_id` and `exit_requested_at` name the order and when it was sent. |

The book writes to `live_positions` under its own mutex, so a background write (for example a new trailing high) can never overwrite a newer status. `trailing_high` and `exit_requested_at` are stored columns (added by the idempotent migration).

**Intent first.** The order ID is allocated before the order is sent and passed as `ExecuteOptions.OrderID` (it is also the `cl_ord_id`):

- **Entry:** the `entry_pending` row is written first. If that write fails, the buy is **not sent** (local rejection `position_not_recorded`). If Kraken rejects the buy, the row is removed.
- **Exit:** the `exit_pending` row (new `exit_order_id`, `exit_requested_at`) is written first. If that write fails the sell is **still sent** — protecting the holding matters more — and recovery falls back to the order ledger. If Kraken rejects the sell, the position returns to `open`.

So a crash at any point after the row is written leaves a pending position whose order ID reconciliation can look up by client order id; the 5-minute `not_found` rule then removes an entry that never reached Kraken or re-opens an exit.

**Once-only rules.** Every live decision and the order it sends run under `App.liveMu`, so two ticks, an approval and a dashboard close can never send for the same position at the same time.

- A buy is not sent while the market already has a live long in any status (`entry_pending` counts), and a restart restores that block.
- A sell signal sells the oldest `open` live long on that market, using the executed size. The spot account never opens a short: with no live long the signal is not acted on ("no live position to sell"), and no order is sent.
- While the entry or the exit of that long is pending, a sell signal is ignored instead of sending a second order.

**Reconciliation** (every strategy tick while the environment is live **or** the book is not empty, `App.reconcileLive()`). The book's pending order IDs are passed as `tracked`. Each update's bookkeeping runs with its own timeout, so one slow write does not starve the rest. An update is matched to a position by its order ID first; if no position names that order (the row was written but the order ID was not, or vice versa), it falls back to the order's `position_id` and adopts the order. A filled order whose ledger row is missing gets the row inserted.

- Entry update with executed volume → the position becomes `open` with the executed size, average price and fee (a partial fill holds only what executed). With no executed volume (cancelled, expired, `not_found`) the position is removed and a new entry is allowed.
- Exit update with executed volume → the realised P&L is stamped on `order_records.pnl` **first**, then recorded through the normal closed-outcome path (`signal_outcomes`, daily loss, the activity log). The P&L is `(exit − entry) × executed − pro-rata entry fee − exit fee`. A partial exit keeps the remainder `open`; a remainder below Kraken's `ordermin` is removed as dust; a full exit removes the position. With no executed volume the position returns to `open`.

**Live stops.** Every tick checks each `open` live long against `STOP_LOSS_PCT` and `TRAILING_STOP_PCT` (`livebook.StopLossHit` / `TrailingStopHit`, the same rules as paper longs) and sends one sell with exit reason `stop_loss` or `trailing_stop`. Stops run in any environment and regardless of the per-market enabled and live toggles or whether the market is still in the active universe, because a position already held on Kraken must still be protected; prices for those markets are fetched alongside the active ones. They do **not** send during the emergency stop (see [Emergency Stop](10-approval-and-control.md#emergency-stop)). The trailing high is stored: when absent it is seeded with `max(entry, price)`, it only rises, and it survives a restart. It keeps updating during the emergency stop.

**Dust.** Before a sell is sent, a position whose size is below the pair's `ordermin` is removed from the book as dust (rejection `below_order_minimum`, activity warning); nothing is sent. When the minimum is unknown the sell is sent.

**Rejected exits back off.** When Kraken rejects a sell, automatic exits (stops and sell signals) for that position pause for 5 minutes, doubling on each further rejection up to 1 hour. The first rejection raises a `live_exit_rejected` alert. A manual close or an approval is not held back. An accepted sell clears the back-off.

**Forget.** `App.ForgetLivePosition()` (`POST /api/positions/{id}/forget`) removes a live position from the book without sending an order. It is for a holding already sold on Kraken by hand or one too small to sell. It is recorded in the activity log ("Live position forgotten … - no order sent").

**Start-up recovery.** `App.recoverLiveBook()` runs after the book is restored:

- For every `entry_pending` or `exit_pending` position whose order row is no longer `pending` (reconciled before the bot stopped), it applies the stored execution. A close whose P&L is already stamped on the order row is not recorded twice.
- A pending position whose order row is missing stays pending; reconciliation looks it up by client order id.
- For an `open` position, a filled sell in the ledger that the book never applied (the exit row could not be written) is found through `order_records.position_id` and applied.

**Known limits.**

- If the bot crashes after the P&L is stamped but before the trade result is recorded, that result is missing from daily loss and the learner (under-counted, never double-counted).
- If the P&L stamp itself fails to write, a restart could apply that sell a second time to the stats.
- A live buy placed by an older version (before intent-first writes) that has no position row is not reconstructed; it shows on Kraken only.
