# Execution Engines

**File:** `backend/execution/paper.py`  
**Class:** `PaperExecutionEngine`

Simulates Kraken spot order execution with realistic fee and slippage modelling. All state is held in memory and persisted to SQLite so positions survive restarts.

Live execution is handled by `backend/execution/kraken.py` / `KrakenExecutionEngine`. It mirrors the paper engine's `execute()` signature, submits Kraken `AddOrder` private REST calls, stores the returned Kraken transaction ID in `order_records.exchange_order_id`, and returns an empty `position_id` because exchange balances/fills are the live source of truth.

Per-market routing is controlled by `ControlState.live_markets`: paper markets use `PaperExecutionEngine`; live markets use `KrakenExecutionEngine`.

Kraken private REST calls use `kraken_retry.call_with_kraken_backoff()`. `AddOrder` and `QueryOrders` retry transient rate-limit, temporary-lockout, service-unavailable, and too-many-requests responses with exponential backoff before rejecting/logging the operation.

---

## Constants

```python
TAKER_FEE_RATE     = 0.0026   # 0.26% Kraken taker fee (low-volume account)
SLIPPAGE_RATE      = 0.001    # 0.10% one-way conservative slippage estimate
SIMULATED_LATENCY_MS = 150    # logged only; not actually awaited
```

---

## Internal State

```python
self.cash: float                          # liquid cash in quote currency
self.positions: Dict[str, PositionRecord] # position_id → PositionRecord
self.orders: List[OrderRecord]            # all orders this session
self.fills: List[FillRecord]              # all fills this session
self._repo: Optional[Repository]         # DB access; None in unit tests
self._position_meta: Dict[str, dict]     # position_id → metadata dict
```

### `_position_meta` Schema

For each position, metadata is stored separately to avoid bloating the domain model:

```python
{
    "strategy_id":      str,          # source strategy
    "direction":        str,          # "long" or "short"
    "confidence":       float,        # signal confidence at entry
    "opened_at":        datetime,     # position open time
    "market":           str,          # trading pair
    "trade_idea_id":    str,          # UUID of the opening signal (persisted to DB so it survives restarts)
    # Set when closing:
    "avg_price_at_close": float,      # entry price (used for P&L in record_closed_trade)
    "size_at_close":      float,      # quantity closed
}
```

---

## `execute()` — Main Fill Method

```python
async def execute(
    self,
    intent: ExecutionIntent,
    market_price: float,
    strategy_id: str = "",
    signal_confidence: Optional[float] = None,
    environment: str = "paper",
    trade_idea_id: str = "",
) -> Tuple[OrderRecord, str]:
```

Returns `(OrderRecord, position_id)`. For a rejection, `position_id` is `""`.

### Execution Flow

```
1.  Apply slippage to market_price         → fill_price
2.  Compute fill_value = size * fill_price
3.  Compute fee = fill_value * TAKER_FEE_RATE
4.  Check _has_sufficient_funds()          → reject if False
5.  Set order.status = "filled"
6.  Create FillRecord
7.  Call _apply_fill()                     → mutates cash + positions; returns position_id
8.  Set order.position_id = position_id
9.  Persist to DB: order, fill, position upsert/delete
10. Return (order, position_id)
```

---

## `_apply_slippage()`

```python
def _apply_slippage(self, direction: Direction, price: float) -> float:
    return price * (1 + SLIPPAGE_RATE) if direction == Direction.LONG else price * (1 - SLIPPAGE_RATE)
```

LONGs pay more (adverse slippage on buy). SHORTs receive less (adverse slippage on sell). This models realistic market impact conservatively.

---

## `_has_sufficient_funds()`

Secondary enforcement layer (primary is the risk engine).

**For LONG:**
- Block if any long already exists for this market → returns `False`
- Otherwise: require `cash >= fill_value + fee`

**For SHORT:**
- If existing longs cover the short size (`total_long >= intent.size`) → allow (closing a long, no cash needed)
- Block if any short already exists for this market → returns `False`
- Otherwise (paper short, no existing long): require `cash >= fill_value + fee`

---

## `_apply_fill()`

Mutates `self.cash` and `self.positions`. Returns the affected `position_id`.

### LONG Path

```python
self.cash -= fill_value + fee
position_id = str(uuid.uuid4())
self.positions[position_id] = PositionRecord(
    position_id=position_id, market=market, size=intent.size,
    avg_price=fill_price, unrealized_pnl=0.0, timestamp=datetime.utcnow()
)
self._position_meta[position_id] = {
    "strategy_id": strategy_id, "direction": "long",
    "confidence": signal_confidence, "opened_at": datetime.utcnow(),
    "market": market, "trade_idea_id": trade_idea_id,
}
return position_id
```

### SHORT Path

```python
self.cash += fill_value - fee   # receive cash from sale

# FIFO: find oldest long for this market
market_longs = sorted(
    [(pid, p) for pid, p in self.positions.items() if p.market == market and p.size > 0],
    key=lambda x: x[1].timestamp,
)

if market_longs:
    pid, pos = market_longs[0]
    meta[pid]["avg_price_at_close"] = pos.avg_price
    meta[pid]["size_at_close"] = pos.size
    del self.positions[pid]     # position closed
    return pid                  # return the CLOSED position's ID
else:
    # Open a paper short (no existing long to close)
    position_id = str(uuid.uuid4())
    self.positions[position_id] = PositionRecord(
        ..., size=-intent.size, ...   # negative size = short
    )
    return position_id
```

The returned `position_id` is the same ID as the original LONG order. This is what links the BUY and SELL entries in the trade ledger — both `order_records` rows share the same `position_id`.

---

## `close_position()` — Targeted Stop-Loss Closure

```python
async def close_position(
    self,
    position_id: str,
    market_price: float,
    environment: str = "paper",
    approval_request_id: str = "stop_loss",
) -> Optional[OrderRecord]:
```

Unlike `execute()`, this targets a specific position by ID. Used by the stop-loss loop in `main.py`. It does **not** go through `_has_sufficient_funds()` — stop-losses always execute.

```
1.  Look up position by position_id        → return None if not found
2.  Determine exit direction               → SHORT if pos.size > 0 (closing long)
3.  Apply slippage
4.  Update cash (receive proceeds if long, pay if short)
5.  Create order + fill records
6.  Save close metadata to _position_meta
7.  Delete from self.positions
8.  Persist to DB: order, fill, delete open_positions row
9.  Return the OrderRecord
```

---

## `record_closed_trade()`

`close_position()` is called only after the stop-loss loop has checked `stop_loss_triggered(position_id, market_price, stop_loss_pct)`. That helper compares the current price with the entry price and returns `True` only when the position is losing by at least the configured percentage. Trailing high/low metadata is not used for `stop_loss` exits.

Manual UI closes also use `close_position()`, but pass `approval_request_id="manual_close"` so the trade ledger is not labelled as a stop-loss.

Called after `execute()` or `close_position()` to write a `SignalOutcomeModel` row. This is the source of truth for the learning system and LLM reflection.

```python
def record_closed_trade(
    self,
    position_id: str,
    exit_price: float,
    exit_reason: str = "manual",
    closing_trade_idea_id: str = "",
) -> None:
    meta = self._position_meta.get(position_id, {})
    avg_price = meta.get("avg_price_at_close")
    size      = meta.get("size_at_close")
    direction = meta.get("direction", "")
    signed_size = size if direction == "long" else -size
    pnl = signed_size * (exit_price - avg_price)
    self._repo.save_signal_outcome(..., closing_trade_idea_id=closing_trade_idea_id)
```

- `closing_trade_idea_id` — the `trade_idea_id` of the SHORT signal that triggered the close. Populated only for `"auto"` (fully-automated mode) and `"manual_approve"` closes. Empty for stop-loss and manual UI closes (no signal is available in those paths).

This is **not** called automatically — the caller in `main.py` is responsible for calling it at the right time with the right `exit_reason`.

---

## `update_mark_prices()`

Called every tick from the strategy loop:

```python
def update_mark_prices(self, prices: Dict[str, float]) -> None:
    for pid, pos in self.positions.items():
        price = prices.get(pos.market)
        if price is not None:
            pnl = pos.size * (price - pos.avg_price)
            self.positions[pid] = pos.model_copy(update={"unrealized_pnl": pnl})
```

Uses Pydantic's `model_copy(update=...)` to produce an immutable update (avoids mutating the original model directly).

---

## `restore_from_db()`

Called at startup to reload open positions:

```python
def restore_from_db(self) -> None:
    rows = self._repo.get_open_positions()
    for row in rows:
        self.positions[row.position_id] = PositionRecord(...)
        self._position_meta[row.position_id] = {
            "strategy_id":  row.strategy_id,
            "direction":    row.direction,
            "confidence":   row.signal_confidence,
            "opened_at":    row.opened_at,
            "market":       row.market,
            "trade_idea_id": row.trade_idea_id or "",  # restored for signal linkage
        }
```

`trade_idea_id` is now persisted to `open_positions` so the link from a position to its opening signal survives restarts. Without this, any close after a restart would produce an unlinked `signal_outcomes` row.

The cash balance is restored separately in `main.py` via `repo.get_latest_cash()` before `restore_from_db()` is called.

---

## `get_total_equity()`

```python
def get_total_equity(self, prices: Dict[str, float]) -> float:
    positions_value = sum(
        pos.size * prices.get(pos.market, pos.avg_price)
        for pos in self.positions.values()
    )
    return self.cash + positions_value
```

Falls back to `avg_price` if no live price is available for a market.

---

## Position Reset (Admin)

`POST /api/positions/reset` (paper mode only).

Before clearing anything, the endpoint iterates every open position and writes a full audit trail:

1. Sets `avg_price_at_close` and `size_at_close` in `_position_meta` so `record_closed_trade()` can proceed.
2. Calls `paper_engine.record_closed_trade(position_id, exit_price, "operator_reset")` — writes a `signal_outcomes` row with `exit_reason = "operator_reset"`. Exit price is the last known market price, falling back to `avg_price` if no live price is cached.
3. Calls `risk_engine.record_trade_result(pnl)` and `learner.record_outcome(...)` so the in-memory learner and drawdown tracker reflect the forced close.

After all outcomes are recorded, positions are cleared:

```python
repo.clear_all_open_positions()
paper_engine.positions.clear()
paper_engine._position_meta.clear()
```

This means a reset no longer produces unexplained "CLOSED" entries — every reset position has a `signal_outcomes` row visible in the Closed Trades panel and available to the LLM reflection loop.

---

## Fee and P&L Accounting

For a LONG → SELL cycle:

```
Cash paid on BUY:   fill_value + fee      = size × buy_price × (1 + slippage) × (1 + fee_rate)
Cash received on SELL: fill_value - fee   = size × sell_price × (1 - slippage) × (1 - fee_rate)

Gross P&L = size × (sell_price - buy_price)
Net  P&L  = Gross P&L - (buy_fee + sell_fee + buy_slippage_cost + sell_slippage_cost)
```

The `pnl` stored in `order_records` and `signal_outcomes` is computed as:

```python
pnl = closing_long.size * (market_price - closing_long.avg_price)
```

This is the gross P&L before fees. Fees are captured separately in `order_records.fee` and `fill_records.fee`.
