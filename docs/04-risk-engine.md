# Risk Engine

**File:** `backend/risk/engine.py`  
**Class:** `RiskEngine`

The risk engine is the single universal enforcement point for all portfolio-level trading constraints. Every signal — whether from the strategy loop, a manual approval, or fully-automated execution — must pass through `evaluate_trade()` before any order is placed. The engine is environment-agnostic: it applies identically to paper and live trading.

---

## Design Principles

1. **Universal gate** — risk logic lives in one place. No mode-specific bypass.
2. **Fail closed** — any check that cannot be evaluated (missing data) defaults to safe behaviour.
3. **Context-aware** — the engine receives live portfolio state (open positions, cash) so checks are based on reality, not stale approximations.
4. **Configurable** — all thresholds come from `settings` and can be adjusted via `.env` without code changes.

---

## State

```python
class RiskEngine:
    current_equity: float          # updated each tick via update_equity()
    daily_loss: float              # accumulated losses today (UTC)
    daily_start_equity: float      # equity at the start of today
    _last_reset_date: date         # tracks UTC date for daily reset
```

`current_equity` is updated every strategy tick with the real mark-to-market total equity. This ensures that percentage-based limits (per-trade loss and daily loss) are always computed against the current portfolio value, not the starting capital.

---

## `evaluate_trade()` — Check Order

```python
async def evaluate_trade(
    self,
    trade_idea: TradeIdea,
    *,
    open_positions: Optional[List[PositionRecord]] = None,
    available_cash: Optional[float] = None,
    market_price: Optional[float] = None,
) -> RiskDecision:
```

Checks are applied in strict order. The first failure returns an immediate rejection — later checks are not evaluated.

### Check 1: One Position Per Trading Pair

```python
if open_positions is not None:
    mkt = [p for p in open_positions if p.market == trade_idea.market]
    same_direction = any(
        (p.size > 0 and trade_idea.direction == Direction.LONG) or
        (p.size < 0 and trade_idea.direction == Direction.SHORT)
        for p in mkt
    )
    if same_direction:
        return self._reject(trade_idea, "Position already open for {market} — one position per pair")
```

This check is direction-aware. A LONG signal is blocked if any long already exists for that market. A SHORT (closing) signal is not blocked by an existing long — that is the intended close trade.

If `open_positions` is `None` (not passed by the caller), this check is skipped. All current call-sites pass it.

### Check 2: Target Trade Amount And Cash Adjustment

```python
strategy_size_eur = trade_idea.position_sizing_proposal * self.current_equity
proposed_size_eur = TARGET_TRADE_AMOUNT_EUR if TARGET_TRADE_AMOUNT_EUR > 0 else strategy_size_eur

if proposed_size_eur < MIN_TRADE_SIZE_EUR:
    proposed_size_eur = MIN_TRADE_SIZE_EUR
```

`TARGET_TRADE_AMOUNT` is the preferred fixed notional order value in `BASE_CURRENCY` (default 100). `MIN_TRADE_SIZE` is an absolute floor (default 50), not a percentage. If `TARGET_TRADE_AMOUNT` is set to `0`, the engine falls back to the strategy's percentage sizing.

When a trade requires cash and the account has less than the target plus fee/slippage, risk reduces the notional to spendable cash as long as the result remains at or above `MIN_TRADE_SIZE`:

```python
cost = proposed_size_eur * (1 + _FEE_AND_SLIPPAGE)
if available_cash < cost:
    cash_sized_eur = available_cash / (1 + _FEE_AND_SLIPPAGE)
    if cash_sized_eur < MIN_TRADE_SIZE_EUR:
        return self._reject(trade_idea, "Insufficient cash")
    proposed_size_eur = cash_sized_eur
```

`_FEE_AND_SLIPPAGE = 0.0036` (0.26% taker fee + 0.1% slippage). This is intentionally conservative.

Short signals that fully close an existing long are exempt from the cash requirement. New short exposure still requires cash as margin in paper mode.

### Check 3: Per-Trade Loss Limit

```python
max_loss = self.current_equity * settings.max_loss_per_trade_percent / 100
estimated_loss = proposed_size_eur * STOP_LOSS_ASSUMPTION
if estimated_loss > max_loss:
    return self._reject(trade_idea, f"Estimated loss €{estimated_loss:.2f} exceeds limit €{max_loss:.2f}")
```

Estimated loss assumes the stop-loss fires at exactly `STOP_LOSS_PCT` (5%). This is an upper-bound estimate, not a guarantee.

### Check 4: Daily Loss Limit

```python
daily_limit = self.current_equity * settings.max_daily_loss_percent / 100
if self.daily_loss >= daily_limit:
    return self._reject(trade_idea, f"Daily loss limit reached: €{self.daily_loss:.2f} of €{daily_limit:.2f}")
```

`daily_loss` accumulates throughout the UTC day. At midnight UTC, `_check_daily_reset()` resets it to zero.

### Approval

If all checks pass:

```python
return RiskDecision(
    trade_idea_id=trade_idea.id,
    approved=True,
    reason="All risk checks passed",
    adjusted_sizing=proposed_size_eur / self.current_equity if adjusted else None,
    timestamp=datetime.utcnow(),
)
```

`adjusted_sizing` is returned whenever risk uses the fixed target amount, clamps to the minimum, or reduces to available cash. Existing execution paths already consume this field when converting the final notional amount into base-currency order size.

---

## State Update Methods

### `update_equity(new_equity: float)`

Called every strategy tick from `main.py` immediately after computing the current total equity. Keeps percentage-based limits current.

### `record_trade_result(pnl: float)`

Called after every position close (stop-loss, auto-execution, or manual approval). Updates `daily_loss` and `current_equity`.

```python
def record_trade_result(self, pnl: float) -> None:
    self._check_daily_reset()
    self.daily_loss += max(0.0, -pnl)   # only losses accumulate
    self.current_equity += pnl
```

### `_check_daily_reset()`

Automatically called at the start of `evaluate_trade()` and `record_trade_result()`. Resets `daily_loss` and `daily_start_equity` when the UTC date changes.

---

## Second-Layer Defence: Execution Engine

The risk engine is the primary guard. The `PaperExecutionEngine._has_sufficient_funds()` method provides a secondary defence at execution time:

```python
if intent.direction == Direction.LONG:
    if any(p.size > 0 for p in market_positions):   # hard block
        return False
    return self.cash >= fill_value + fee

# SHORT
if total_long >= intent.size:
    return True   # closing existing long, no cash needed
if any(p.size < 0 for p in market_positions):   # hard block on double-short
    return False
return self.cash >= fill_value + fee
```

This secondary check exists because the risk engine evaluates the *proposed* trade, while the execution engine enforces at the moment of actual state mutation. Both must pass for an order to fill.

---

## Risk Rejection Visibility

All risk rejections are recorded in the module-level `_risk_rejections` deque (maxlen=50) in `main.py` and surfaced on the dashboard. This gives the operator visibility into why signals are being blocked without cluttering the activity feed.

```python
_risk_rejections.appendleft({
    "market":     idea.market,
    "direction":  idea.direction.value,
    "confidence": idea.confidence,
    "thesis":     idea.thesis,
    "reason":     risk_decision.reason,
    "timestamp":  datetime.utcnow().isoformat(),
})
```

The dashboard displays the last 20 rejections in the "Risk Rejections" panel.

---

## Risk Parameter Summary

| Parameter | Setting Key | Default | Description |
|---|---|---|---|
| Target trade amount | `TARGET_TRADE_AMOUNT` | EUR 100 | Preferred fixed notional order value |
| Min trade size | `MIN_TRADE_SIZE` | EUR 50 | Absolute minimum final order value |
| Stop-loss | `STOP_LOSS_PCT` | 5% | Fraction loss triggering auto-close |
| Fee + slippage | `FEE_AND_SLIPPAGE` | 0.36% | Cost estimate for cash sufficiency check |
| Per-trade loss | `MAX_LOSS_PER_TRADE_PERCENT` | 5% | Max estimated loss per trade vs equity |
| Daily loss | `MAX_DAILY_LOSS_PERCENT` | 5% | Max cumulative loss per UTC day |
