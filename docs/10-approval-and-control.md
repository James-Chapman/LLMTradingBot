# Approval Service & Control State

---

## Approval Service (`backend/approval/service.py`)

**Class:** `ApprovalService`

Manages the queue of pending trade approvals in `semi_automated` mode. Approvals are held in memory only — the queue does not survive a process restart (any pending approvals are lost on shutdown).

```python
DEFAULT_TTL_MINUTES = 30

class ApprovalService:
    def __init__(self, ttl_minutes: int = DEFAULT_TTL_MINUTES):
        self._ttl:     timedelta                      # approval lifetime
        self._pending: Dict[str, ApprovalRequest]     # id → ApprovalRequest
```

---

### `ApprovalRequest` Model

```python
@dataclass
class ApprovalRequest:
    id:             str            # UUID4
    trade_idea:     TradeIdea
    risk_decision:  RiskDecision
    expires_at:     datetime       # utcnow() + TTL
    status:         str            # "pending" | "approved" | "rejected" | "expired"
```

The `trade_idea` embedded in the request carries the full signal context: market, direction, confidence, thesis, entry/exit plans, and position sizing proposal.

---

### `submit()`

```python
def submit(
    self,
    idea:           TradeIdea,
    risk_decision:  RiskDecision,
) -> Optional[ApprovalRequest]
```

Creates a new approval and adds it to the pending queue. Returns `None` (without adding to queue) if:
- An approval for the same `market` is already pending (prevents duplicate queuing).

```python
if self.has_pending_for_market(idea.market):
    return None

approval = ApprovalRequest(
    id            = str(uuid.uuid4()),
    trade_idea    = idea,
    risk_decision = risk_decision,
    expires_at    = datetime.utcnow() + self._ttl,
    status        = "pending",
)
self._pending[approval.id] = approval
return approval
```

---

### `get_pending()`

```python
def get_pending(self) -> List[ApprovalRequest]
```

Returns all non-expired approvals. Internally calls `_purge_expired()` first to remove stale entries before returning. This is the data source for `GET /api/approvals` and the dashboard approval panel.

---

### `get()`

```python
def get(self, approval_id: str) -> Optional[ApprovalRequest]
```

Returns a single approval by ID, or `None` if it does not exist or has expired.

---

### `approve()` / `reject()`

```python
def approve(self, approval_id: str) -> Optional[ApprovalRequest]
def reject(self, approval_id: str)  -> Optional[ApprovalRequest]
```

Both methods call `_get_if_valid()` first, which checks both existence and expiry. If valid:
- Sets `status` to `"approved"` or `"rejected"`.
- Removes the approval from `_pending`.
- Returns the `ApprovalRequest` (caller uses it to extract `trade_idea` and execute).

Returns `None` if the approval doesn't exist or has expired. The API handler converts this to a `404` response.

---

### `has_pending_for_market()`

```python
def has_pending_for_market(self, market: str) -> bool
```

Used by the strategy loop before calling `submit()` to prevent queuing a second approval for a market that already has one waiting. Iterates non-expired pending approvals.

---

### `_purge_expired()`

```python
def _purge_expired(self) -> None
```

Removes all entries from `_pending` where `expires_at < datetime.utcnow()`. Called automatically on `get_pending()` and before any validation. There is no background task for purging — expiry is evaluated on-demand.

---

### Approval Lifecycle

```
Strategy generates TradeIdea (semi_automated mode)
    │
    ├── Risk engine evaluates → approved
    │
    ├── Check: existing same-direction position? → skip if yes
    │
    ├── Check: pending approval for this market? → skip if yes
    │
    └── approval_service.submit(idea, risk_decision)
              │
              ├── ApprovalRequest created (TTL: 30 min)
              │
              ├── Dashboard polls /api/approvals every 5s
              │
              └── User action:
                    ├── APPROVE → POST /api/approvals/{id}/approve
                    │               │
                    │               ├── validate emergency_stop
                    │               ├── validate live price available
                    │               ├── paper_engine.execute(intent, price)
                    │               ├── record_closed_trade (if SHORT)
                    │               └── activity.success(...)
                    │
                    └── REJECT → POST /api/approvals/{id}/reject
                                    └── approval removed, activity.info(...)
```

If neither action is taken within 30 minutes, the approval expires and is removed on the next `get_pending()` call.

---

### Persistence

The `ApprovalRequestModel` SQLAlchemy table exists for audit trail purposes. The `ApprovalService` in-memory `_pending` dict starts empty on restart — **pending approvals are not recovered after a restart**. Any approval that was awaiting operator action when the bot stopped will be lost. The 30-minute TTL window means this is only a concern if the bot is restarted mid-session.

---

## Control State (`backend/control/state.py`)

**Class:** `ControlState`

A thread-safe control board. Controls which markets are active, which single strategy is selected, which markets route to live execution, and whether the emergency stop is engaged. State is persisted to the `control_state` table on every change and restored at startup.

```python
class ControlState:
    def __init__(self):
        self._emergency_stop:   bool          = False
        self._stop_timestamp:   Optional[datetime] = None
        self._disabled_markets:    Set[str]   = set()
        self._disabled_strategies: Set[str]   = set()
        self._selected_strategy_id: str        = "combined"
        self._lock: threading.Lock
        self._repo = None  # injected via set_repo() at startup
```

The lock is acquired for all reads and writes because toggles can arrive from FastAPI request handlers (via thread-pool workers) while the strategy loop runs in the asyncio event loop on the main thread.

### `set_repo()` / `load_from_db()`

```python
def set_repo(self, repo) -> None
def load_from_db(self) -> None
```

`set_repo()` is called once at startup (from `main.py`) to inject the `Repository`. `load_from_db()` is then called to restore the last persisted state. If the `control_state` table has no row (first ever startup), defaults apply (everything enabled, no emergency stop).

Every state-mutating method (`activate_stop`, `resume`, `disable_market`, `enable_market`, `disable_strategy`, `enable_strategy`) calls the private `_persist()` helper, which writes the current state to `control_state` row `id=1` via an upsert.

---

### Emergency Stop

```python
@property
def emergency_stop(self) -> bool

def activate_stop(self) -> None
    # Sets _emergency_stop = True, records _stop_timestamp

def resume(self) -> None
    # Clears _emergency_stop, clears _stop_timestamp
    # Does NOT clear pending approvals — that is done by the API handler
```

When `emergency_stop` is `True`, the strategy loop checks it at the top of every tick and skips all processing:

```python
if control.emergency_stop:
    await asyncio.sleep(30)
    continue
```

The approval handler also rejects any execution attempt if `emergency_stop` is active.

---

### Market Toggles

```python
def disable_market(self, market: str) -> None
def enable_market(self, market: str) -> None
def is_market_enabled(self, market: str) -> bool
    # Returns True if market NOT in _disabled_markets
```

`_active_markets` in `main.py` is computed each strategy tick:

```python
_active_markets = [m for m in settings.fixed_markets if control.is_market_enabled(m)]
```

Disabling a market removes it from the tick's price fetch and strategy evaluation. Any existing open position for that market is **not** automatically closed — it remains open and is subject to stop-loss monitoring (which runs before the market filter).

---

### Strategy Selection

```python
def select_strategy(self, strategy_id: str) -> None
def is_strategy_selected(self, strategy_id: str) -> bool
```

The strategy loop evaluates only the selected strategy:

```python
active_strategy = _strategy_by_id(control.selected_strategy_id)
strategy_ideas = await active_strategy.evaluate(...)
```

Registered strategy IDs are `"indicator_only"`, `"combined"`, and `"llm"`. The UI selector writes the selected ID through `POST /api/control/strategies/{strategy_id}/select`.

---

### `snapshot()`

```python
def snapshot(self) -> dict
```

Returns the current state as a serialisable dict. Included in every `/api/dashboard` response under the `"control"` key.

```python
{
    "emergency_stop":     bool,
    "stop_since":         "ISO timestamp" | null,
    "disabled_markets":   ["sorted", "list"],
    "disabled_strategies": ["sorted", "list"],
    "selected_strategy":  "combined",
    "live_markets":       ["sorted", "list"]
}
```

---

### Activity Log (`backend/observability/activity.py`)

**Class:** `ActivityLog`

An event feed for the dashboard — persisted to the `activity_log` database table in addition to the in-memory rolling buffer. Not part of Python's standard logging system, which is reserved for developer/operations output.

```python
class ActivityLog:
    def __init__(self, maxlen: int = 200):
        self._entries: deque   # collections.deque(maxlen=200)
        self._repo = None      # injected via set_repo() at startup

    def set_repo(self, repo) -> None
    def seed_from_db(self) -> None   # call once at startup after set_repo()

    def info(self,    message: str, detail: str = "") -> None
    def warn(self,    message: str, detail: str = "") -> None
    def error(self,   message: str, detail: str = "") -> None
    def success(self, message: str, detail: str = "") -> None

    def recent(self, n: int = 100) -> List[dict]
```

**Entry structure:**

```python
{
    "timestamp": "2026-04-23T14:32:10",   # ISO format, UTC
    "level":     "info | warn | error | success",
    "message":   str,
    "detail":    str,   # optional context (e.g., thesis, price)
}
```

Entries are inserted at the front (`appendleft`) so the most recent event is always at index 0. When the buffer reaches 200 entries, the oldest is automatically discarded by the `deque` maxlen. The database retains the last 2,000 entries (trimmed every ~5 minutes).

`recent(n)` returns the first `n` entries (most recent first). The dashboard fetches `recent(60)` as part of `/api/dashboard`.

**Persistence flow:**

1. At startup, `activity.set_repo(repo)` wires in the repository.
2. `activity.seed_from_db()` loads the last 200 entries from the database into the in-memory deque — history is visible in the dashboard from the first page load.
3. Every subsequent `_add()` call writes to both the in-memory deque and `repo.save_activity_log()`. DB failures are silently absorbed (never break the bot loop).

**Usage examples from `main.py`:**

```python
activity.success(f"LONG {market} @ £{price:.2f} — {pos_id[:8]}", idea.thesis)
activity.warn(f"STOP-LOSS: {market} closed at £{price} ({loss_pct:.1%} loss)")
activity.info(f"LLM briefing ({len(new_articles)} new article(s)): {briefing.key_insight}", ...)
activity.error("Strategy loop error", str(e))
```

Level mapping for dashboard display:
- `success` → green badge
- `info` → blue badge
- `warn` → amber badge
- `error` → red badge
