# Approval Service & Control State

---

## Approval Service (`internal/approval/service.go`)

**Type:** `approval.Service`

Manages the queue of pending trade approvals in `semi_automated` mode. The queue is held in memory and mirrored to the `approval_requests` table, so pending approvals survive a restart. The same service backs the HTTP API and the CLI (which approves and rejects through the running bot's API), so both approval paths see identical state. It is safe for concurrent use.

```go
const DefaultTTL = 30 * time.Minute

type Service struct {
	mu      sync.Mutex
	ttl     time.Duration            // approval lifetime
	repo    Repository               // persistence; nil = in-memory only
	now     func() time.Time         // injectable clock (UTC)
	pending []domain.ApprovalRequest // submission order
}

func NewService(ttl time.Duration, repo Repository, now func() time.Time) *Service
```

The app builds it with `approval.NewService(approval.DefaultTTL, approvalStore{repo: repo}, nil)`.

---

### `ApprovalRequest` Model (`internal/domain`)

```go
type ApprovalRequest struct {
	ID           string       `json:"id"`         // UUID
	TradeIdea    TradeIdea    `json:"trade_idea"`
	RiskDecision RiskDecision `json:"risk_decision"`
	ExpiresAt    time.Time    `json:"expires_at"` // now (UTC) + TTL
	Status       string       `json:"status"`     // pending | approved | rejected | expired
}
```

The `TradeIdea` embedded in the request carries the full signal context: market, direction, strategy, confidence, thesis, entry/exit plans, and position sizing proposal.

---

### `Submit()`

```go
func (s *Service) Submit(idea domain.TradeIdea, risk domain.RiskDecision) (domain.ApprovalRequest, bool)
```

Purges expired entries, then creates a new approval and adds it to the pending queue. Returns `false` (without adding to the queue) if an approval for the same `market` is already pending, which prevents duplicate queuing.

```go
if s.hasMarketLocked(idea.Market) {
	return domain.ApprovalRequest{}, false
}
req := domain.ApprovalRequest{
	ID:           domain.NewID(),
	TradeIdea:    idea,
	RiskDecision: risk,
	ExpiresAt:    s.now().UTC().Add(s.ttl),
	Status:       "pending",
}
s.pending = append(s.pending, req)
// ...then persisted with repo.SaveApprovalRequest(req); failures are logged
```

---

### `GetPending()`

```go
func (s *Service) GetPending() []domain.ApprovalRequest
```

Returns all non-expired approvals in submission order. Internally purges expired entries first. This is the data source for `GET /api/approvals` and the dashboard approval panel.

---

### `Get()`

```go
func (s *Service) Get(approvalID string) (domain.ApprovalRequest, bool)
```

Returns a single approval by ID, or `false` if it does not exist or has expired.

---

### `Approve()` / `Reject()`

```go
func (s *Service) Approve(approvalID string) (domain.ApprovalRequest, bool)
func (s *Service) Reject(approvalID string) (domain.ApprovalRequest, bool)
```

Both methods call `resolve()`, which checks existence and expiry (`validIndexLocked()`). If valid:
- Sets `Status` to `"approved"` or `"rejected"`.
- Removes the approval from the queue.
- Persists the new status.
- Returns the `ApprovalRequest` (the caller uses it to extract the trade idea and execute).

Returns `false` if the approval doesn't exist or has expired. The API handler converts this to a `404` response.

---

### `HasPendingForMarket()`

```go
func (s *Service) HasPendingForMarket(market string) bool
```

Reports whether a non-expired approval exists for a market. `Submit()` performs the same check itself, so the strategy loop simply calls `Submit()` and logs "approval already pending" when it returns `false`.

---

### `ClearPending()`

```go
func (s *Service) ClearPending() int
```

Discards every pending approval, in memory and in the database, and returns how many were cleared. Called when the emergency stop is activated.

---

### `purgeExpiredLocked()`

Removes every queued approval whose `ExpiresAt` is in the past and persists status `"expired"` for each. Called automatically by `GetPending()`, `Submit()` and `HasPendingForMarket()`. `Get()`, `Approve()` and `Reject()` expire the single approval they look at. There is no background goroutine for purging; expiry is evaluated on demand.

---

### Approval Lifecycle

```
Strategy generates TradeIdea (semi_automated mode)
    │
    ├── Risk engine evaluates → approved
    │
    ├── Check: existing same-direction position? → skip if yes
    │
    └── approvals.Submit(idea, decision)
              │
              ├── pending approval for this market? → skip (returns false)
              │
              ├── ApprovalRequest created and persisted (TTL: 30 min)
              │
              ├── Dashboard polls /api/dashboard every 5s
              │
              └── User action:
                    ├── APPROVE → POST /api/approvals/{id}/approve
                    │               │
                    │               ├── emergency stop active? → 503
                    │               ├── approval missing or expired? → 404
                    │               ├── no live price? → 503, approval stays pending
                    │               ├── live SHORT with no open live long? → 409
                    │               ├── re-run risk with current positions and cash
                    │               │     └── refused? → 409, approval removed
                    │               ├── execute on Kraken (live market) or paper engine
                    │               ├── RecordClosedTrade (if paper SHORT closed a position)
                    │               └── activity.Success(...)
                    │
                    └── REJECT → POST /api/approvals/{id}/reject
                                    └── approval removed, activity.Warn(...)
```

If neither action is taken within 30 minutes, the approval expires and is removed the next time the queue is read.

#### Risk re-check at approval time

An approval can wait up to 30 minutes, and the account may change in that time (other trades, the daily loss limit, a position on the same market). `App.ApproveTrade()` therefore does not trust the risk decision made when the approval was queued:

1. **Price first.** With no live price for the market it returns `503` ("the approval is still pending; try again when a price is available"). The approval is not consumed and can be approved again later. (Earlier versions consumed the approval and returned `approved_no_price`.)
2. **Route once.** It decides once whether the trade goes to Kraken or to paper, reading mode and environment one time, and holds `App.liveMu` for a live trade until the order is sent.
3. **Live shorts.** A SELL on a live market needs an open live long to sell. If the long's entry or exit is still pending, it returns `409` and the approval stays. If there is no live position at all, it returns `409` and the approval is rejected and removed. No order is sent.
4. **Risk.** It takes a fresh account snapshot and runs `risk.EvaluateTrade` again with the positions and cash of that route and the current price. If risk refuses, the approval is rejected and removed, and the call returns `409` "Risk check failed at approval time: …". The trade size comes from this fresh decision, not the queued one.
5. Only then is the approval marked approved and the order sent. The response has an extra `environment` key (`"live"` or `"paper"`).

---

### Persistence

Every approval is written to `approval_requests` when submitted, and every status change (`approved`, `rejected`, `expired`) is written back. Persistence failures are logged and never block the queue. At startup the app calls `LoadPendingFromRepository()`, which restores the persisted approvals that are still `pending` and not expired. Approvals awaiting operator action therefore survive a restart within their 30-minute TTL.

---

## Control State (`internal/control/state.go`)

**Type:** `control.State`

A thread-safe control board. Controls which markets are active, which single strategy is selected, which markets route to live execution, and whether the emergency stop is engaged. State is persisted to the `control_state` table on every change and restored at startup.

```go
type State struct {
	mu                 sync.Mutex
	now                func() time.Time
	repo               Repository
	emergencyStop      bool
	stopTimestamp      *time.Time
	disabledMarkets    map[string]bool
	disabledStrategies map[string]bool
	liveMarkets        map[string]bool
	selectedStrategyID string // DefaultSelectedStrategy = "basic_and_llm_strategy"
	tradingMode        string // manual | semi_automated | fully_automated
	tradingEnvironment string // paper | live
}
```

The mutex is taken for all reads and writes because toggles arrive from HTTP handler goroutines while the strategy loop runs in its own goroutine.

### `SetRepo()` / `LoadFromDB()`

```go
func (s *State) SetRepo(repo Repository)
func (s *State) LoadFromDB() error
```

`SetRepo()` is called once at startup (from `app.New`) to inject the repository. `LoadFromDB()` is then called to restore the last persisted state. If the `control_state` table has no row (first ever startup), defaults apply: everything enabled, no live markets, no emergency stop. Legacy strategy IDs (`basic_trend`, `llm_only`) are mapped to their canonical IDs on load.

Every state-mutating method (`ActivateStop`, `Resume`, `DisableMarket`, `EnableMarket`, `SetMarketLive`, `DisableStrategy`, `EnableStrategy`, `SelectStrategy`, `SetTradingMode`, `SetTradingEnvironment`) calls the private `persistLocked()` helper while holding the lock, so saves are applied in change order. Save failures are logged and non-fatal.

---

### Emergency Stop

```go
func (s *State) EmergencyStop() bool

func (s *State) ActivateStop() // sets emergencyStop = true, records stopTimestamp
func (s *State) Resume()       // clears emergencyStop and stopTimestamp
```

`Resume()` does not touch the approval queue. Clearing pending approvals is done by `App.ActivateEmergencyStop()` when the stop is engaged.

When the emergency stop is active, the strategy loop checks it at the top of every tick, tends the live book, logs "Emergency stop active - tick skipped" to the activity feed, and waits a full interval:

```go
if a.control.EmergencyStop() {
	a.tendLiveBookDuringStop(ctx) // price, reconcile, trailing highs; no automatic sell
	a.activity.Warn("Emergency stop active - tick skipped", "")
	return true, nil // skipped
}
```

**Live positions during the stop.** No strategy runs and no automatic order is sent — stop-losses and trailing stops on live positions are **paused** (a sell already decided re-checks the stop under `liveMu` and is not sent). The live book is still looked after: its markets are priced, pending Kraken orders reconcile (so a fill that happens during the stop is recorded), and trailing highs follow the price. While any live position is held, the activity feed warns "Emergency stop: N live position(s) have no automatic stop" at most once every 10 minutes.

The approve handler refuses with `503` while the emergency stop is active. A **manual close** from the dashboard is allowed during the stop, for paper and live positions alike: it is the operator's way to get out while automation is halted.

---

### Market Toggles

```go
func (s *State) DisableMarket(market string)
func (s *State) EnableMarket(market string)
func (s *State) IsMarketEnabled(market string) bool // true if market is not disabled

func (s *State) SetMarketLive(market string, live bool)
func (s *State) IsMarketLive(market string) bool
```

The strategy loop filters the validated market list on every tick:

```go
for _, m := range a.activeMarkets {
	if a.control.IsMarketEnabled(m) {
		active = append(active, m)
	}
}
```

Disabling a market removes it from strategy evaluation, and the OHLC loop stops refreshing its candles. Any existing open position for that market is **not** automatically closed. A market that holds a **live** position is still priced every tick and its live stops still run, as they do for a market that has left the active universe.

`IsMarketLive()` decides whether an approved or automated order for that market goes to the Kraken live engine or the paper engine. The live flag only takes effect when the trading environment is `live`; in the paper environment the order is executed on paper and a warning is logged.

`App.ToggleMarketLive()` refuses with `409` to turn the live flag **off** for a market that holds a live position (any status); the message names the position. Close it first, or use **Forget** if it was already sold on Kraken. Turning the flag on is always allowed. The check runs under `App.liveMu`, so no live entry can land between the check and the change.

---

### Trading Settings

```go
func (s *State) SeedTradingSettings(mode, environment string) // fills only unset values
func (s *State) TradingMode() string
func (s *State) TradingEnvironment() string
func (s *State) SetTradingMode(mode string) error               // refuses unknown modes
func (s *State) SetTradingEnvironment(environment string) error // refuses unknown values
```

The trading mode and paper/live environment live in control state (`internal/control/trading.go`), so they can change while the bot runs and survive restarts. At start-up `app.New` calls `LoadFromDB()` first, then `SeedTradingSettings(settings.TradingMode, settings.TradingEnvironment)`: values saved from the web interface win, and the defaults (`manual`, `paper`) only fill values that were never saved. This happens before `restoreState`, which needs the effective environment to choose the equity source.

`App.TradingMode()` / `App.TradingEnvironment()` (`internal/app/trading_settings.go`) are the only way the app reads these values; nothing reads `settings.TradingMode` / `settings.TradingEnvironment` after start-up. The strategy loop reads the mode once per signal, so a change mid-tick cannot split one signal across two modes.

`App.SetTradingMode()` and `App.SetTradingEnvironment(environment, confirmation)` add the safety rules on top:

| Change | Requirement |
|---|---|
| Any mode change | Valid mode. The dashboard shows a confirm dialog first. |
| Paper → live | `confirmation == "LIVE"` (`app.LiveConfirmation`) **and** Kraken API key + secret in use. Keys saved in Settings but not loaded yet (no restart since) are refused with a message to restart first. |
| Live → paper | No live position may be open (any status): otherwise `409` "Cannot switch to paper while N live position(s) are open: …". The check runs under `App.liveMu`. The dashboard shows a confirm dialog first. |

Each change writes a `warn` activity entry ("Trading mode changed: manual → semi_automated") and bumps the dashboard version. Changing mode does not clear pending approvals; approvals queued in semi-automated mode can still be approved or rejected.

---

### Strategy Selection

```go
func (s *State) SelectStrategy(strategyID string)
func (s *State) SelectedStrategyID() string
func (s *State) IsStrategySelected(strategyID string) bool
```

The strategy loop evaluates only the selected strategy:

```go
active := a.strategyByID(a.control.SelectedStrategyID())
```

Registered strategy IDs are `"basic_strategy"`, `"basic_and_llm_strategy"`, `"llm_only_strategy"`, `"trend_strategy"`, `"trend_and_llm_strategy"`, `"donchian_strategy"`, `"donchian_and_llm_strategy"`, `"rsi_dip_strategy"`, `"rsi_dip_and_llm_strategy"`, `"macd_strategy"`, `"macd_and_llm_strategy"`, `"squeeze_strategy"`, `"squeeze_and_llm_strategy"`, `"rotation_strategy"` and `"rotation_and_llm_strategy"`. The **Strategy** section of the dashboard's Trading settings dialog (between Trading mode and Money) writes the selected ID through `POST /api/control/strategies/{strategy_id}/select`, after a confirm dialog.

---

### `Snapshot()`

```go
func (s *State) Snapshot() Snapshot
```

Returns the current state as a JSON-serialisable struct. Included in every `/api/dashboard` response under the `"control"` key and returned by `GET /api/control`.

```go
type Snapshot struct {
	EmergencyStop      bool       `json:"emergency_stop"`
	StopSince          *time.Time `json:"stop_since"` // null when not stopped
	DisabledMarkets    []string   `json:"disabled_markets"`    // sorted
	DisabledStrategies []string   `json:"disabled_strategies"` // sorted
	SelectedStrategy   string     `json:"selected_strategy"`
	LiveMarkets        []string   `json:"live_markets"`        // sorted
	TradingMode        string     `json:"trading_mode"`
	TradingEnvironment string     `json:"trading_environment"`
}
```

---

### Activity Log (`internal/observability/activity.go`)

**Type:** `observability.ActivityLog`

An event feed for the dashboard, persisted to the `activity_log` database table in addition to the in-memory rolling buffer. It is separate from the `slog` application log, which is reserved for developer and operations output. It is safe for concurrent use.

```go
func NewActivityLog(maxLen int) *ActivityLog // the app uses maxLen = 200

func (a *ActivityLog) SetStore(store ActivityStore) // wire in persistence
func (a *ActivityLog) SeedFromStore()               // call once at startup after SetStore

func (a *ActivityLog) Info(message, detail string)
func (a *ActivityLog) Warn(message, detail string)
func (a *ActivityLog) Error(message, detail string)
func (a *ActivityLog) Success(message, detail string)

func (a *ActivityLog) Recent(n int) []ActivityEntry
```

**Entry structure:**

```go
type ActivityEntry struct {
	Timestamp string `json:"timestamp"` // RFC 3339, UTC
	Level     string `json:"level"`     // info | warn | error | success
	Message   string `json:"message"`
	Detail    string `json:"detail"`    // optional context (e.g. thesis, price)
}
```

Entries are prepended so the most recent event is always at index 0. When the buffer exceeds 200 entries, the oldest is discarded. The database retains the last 2,000 entries (trimmed by the strategy loop every 10 ticks, about every 5 minutes).

`Recent(n)` returns the first `n` entries (most recent first). The dashboard includes `Recent(60)` in `/api/dashboard`.

**Persistence flow:**

1. At startup, `activity.SetStore(repo)` wires in the repository.
2. `activity.SeedFromStore()` loads the last 200 entries from the database into the buffer, so history is visible in the dashboard from the first page load.
3. Every subsequent entry is written to both the buffer and `store.SaveActivityLog()`. DB failures are ignored so they never break a bot loop.

**Usage examples from `internal/app`:**

```go
a.activity.Success(fmt.Sprintf("Signal %s %s %s - submitted for approval", idea.Market, label, pct0(idea.Confidence)), idea.Thesis)
a.activity.Warn(fmt.Sprintf("Manual rejection: %s %s - operator declined", market, label), "")
a.activity.Info(fmt.Sprintf("LLM briefing (%d new article(s)): %s", len(articles), briefing.KeyInsight), outlooks)
a.activity.Error("News fetch failed", err.Error())
```

Level mapping for dashboard display:
- `success` → green badge
- `info` → blue badge
- `warn` → amber badge
- `error` → red badge
