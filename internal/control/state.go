// Package control holds operator control state: emergency stop, market
// toggles, live-market routing and strategy selection. Every change is saved
// through the repository so it survives restarts; call SetRepo then LoadFromDB
// once at startup.
package control

import (
	"sort"
	"sync"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
)

var logger = observability.Logger("control")

// DefaultSelectedStrategy is the strategy used when none has been chosen.
const DefaultSelectedStrategy = "basic_and_llm_strategy"

// LegacyStrategyIDs maps old strategy ids to their current canonical ids.
var LegacyStrategyIDs = map[string]string{
	"basic_trend": "basic_strategy",
	"llm_only":    "llm_only_strategy",
}

// CanonicalStrategyID maps a legacy strategy id to its current id.
func CanonicalStrategyID(strategyID string) string {
	if canonical, ok := LegacyStrategyIDs[strategyID]; ok {
		return canonical
	}
	return strategyID
}

// PersistedState is the control state as stored in the database.
type PersistedState struct {
	EmergencyStop      bool
	DisabledMarkets    []string
	DisabledStrategies []string
	LiveMarkets        []string
	SelectedStrategy   string
}

// Repository loads and saves the single control-state row (implemented by the
// storage repository). LoadControlState reports found=false when never saved.
type Repository interface {
	LoadControlState() (state PersistedState, found bool, err error)
	SaveControlState(state PersistedState) error
}

// Snapshot is the API view of control state; JSON keys match the Python dict.
type Snapshot struct {
	EmergencyStop      bool       `json:"emergency_stop"`
	StopSince          *time.Time `json:"stop_since"` // null when not stopped
	DisabledMarkets    []string   `json:"disabled_markets"`
	DisabledStrategies []string   `json:"disabled_strategies"`
	SelectedStrategy   string     `json:"selected_strategy"`
	LiveMarkets        []string   `json:"live_markets"`
}

// State is the operator control state. It is safe for concurrent use.
type State struct {
	mu                 sync.Mutex
	now                func() time.Time
	repo               Repository
	emergencyStop      bool
	stopTimestamp      *time.Time
	disabledMarkets    map[string]bool
	disabledStrategies map[string]bool
	liveMarkets        map[string]bool
	selectedStrategyID string
}

// NewState creates control state with defaults. A nil clock defaults to UTC now.
func NewState(now func() time.Time) *State {
	if now == nil {
		now = domain.NowUTC
	}
	return &State{
		now:                now,
		disabledMarkets:    map[string]bool{},
		disabledStrategies: map[string]bool{},
		liveMarkets:        map[string]bool{},
		selectedStrategyID: DefaultSelectedStrategy,
	}
}

// SetRepo wires in the repository. Call once at startup.
func (s *State) SetRepo(repo Repository) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repo = repo
}

// LoadFromDB restores persisted toggles. On error the current state is kept
// and the error is logged and returned.
func (s *State) LoadFromDB() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.repo == nil {
		return nil
	}
	stored, found, err := s.repo.LoadControlState()
	if err != nil {
		logger.Warn("Could not restore control state from DB", "error", err.Error())
		return err
	}
	if !found {
		return nil
	}
	s.emergencyStop = stored.EmergencyStop
	s.disabledMarkets = toSet(stored.DisabledMarkets)
	s.disabledStrategies = toSet(stored.DisabledStrategies)
	s.liveMarkets = toSet(stored.LiveMarkets)
	selected := stored.SelectedStrategy
	if selected == "" {
		selected = DefaultSelectedStrategy
	}
	s.selectedStrategyID = CanonicalStrategyID(selected)

	if s.emergencyStop {
		logger.Warn("Control state restored — emergency stop is ACTIVE")
	}
	if len(s.disabledMarkets) > 0 {
		logger.Info("Control state restored — disabled markets", "markets", sortedKeys(s.disabledMarkets))
	}
	if len(s.disabledStrategies) > 0 {
		logger.Info("Control state restored — disabled strategies", "strategies", sortedKeys(s.disabledStrategies))
	}
	return nil
}

// EmergencyStop reports whether the emergency stop is active.
func (s *State) EmergencyStop() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.emergencyStop
}

// ActivateStop turns the emergency stop on.
func (s *State) ActivateStop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emergencyStop = true
	ts := s.now().UTC()
	s.stopTimestamp = &ts
	logger.Warn("Emergency stop ACTIVATED")
	s.persistLocked()
}

// Resume clears the emergency stop.
func (s *State) Resume() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emergencyStop = false
	s.stopTimestamp = nil
	logger.Info("Emergency stop cleared — bot resumed")
	s.persistLocked()
}

// DisableMarket stops the bot trading a market.
func (s *State) DisableMarket(market string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disabledMarkets[market] = true
	logger.Info("Market disabled", "market", market)
	s.persistLocked()
}

// EnableMarket allows the bot to trade a market again.
func (s *State) EnableMarket(market string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.disabledMarkets, market)
	logger.Info("Market enabled", "market", market)
	s.persistLocked()
}

// IsMarketEnabled reports whether a market is tradable.
func (s *State) IsMarketEnabled(market string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.disabledMarkets[market]
}

// SetMarketLive sets whether a market routes to live execution.
func (s *State) SetMarketLive(market string, live bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if live {
		s.liveMarkets[market] = true
	} else {
		delete(s.liveMarkets, market)
	}
	s.persistLocked()
}

// IsMarketLive reports whether a market routes to live execution.
func (s *State) IsMarketLive(market string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.liveMarkets[market]
}

// DisableStrategy marks a strategy as disabled.
func (s *State) DisableStrategy(strategyID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disabledStrategies[strategyID] = true
	logger.Info("Strategy disabled", "strategy_id", strategyID)
	s.persistLocked()
}

// EnableStrategy marks a strategy as enabled.
func (s *State) EnableStrategy(strategyID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.disabledStrategies, strategyID)
	logger.Info("Strategy enabled", "strategy_id", strategyID)
	s.persistLocked()
}

// IsStrategyEnabled reports whether a strategy is enabled.
func (s *State) IsStrategyEnabled(strategyID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.disabledStrategies[strategyID]
}

// SelectedStrategyID returns the strategy used by the signal loop.
func (s *State) SelectedStrategyID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectedStrategyID
}

// SelectStrategy selects the single strategy used by the signal loop.
func (s *State) SelectStrategy(strategyID string) {
	canonical := CanonicalStrategyID(strategyID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.selectedStrategyID = canonical
	logger.Info("Strategy selected", "strategy_id", canonical)
	s.persistLocked()
}

// IsStrategySelected reports whether the strategy (legacy ids accepted) is active.
func (s *State) IsStrategySelected(strategyID string) bool {
	canonical := CanonicalStrategyID(strategyID)
	s.mu.Lock()
	defer s.mu.Unlock()
	return canonical == s.selectedStrategyID
}

// Snapshot returns a copy of the control state for the API.
func (s *State) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	var since *time.Time
	if s.stopTimestamp != nil {
		ts := *s.stopTimestamp
		since = &ts
	}
	return Snapshot{
		EmergencyStop:      s.emergencyStop,
		StopSince:          since,
		DisabledMarkets:    sortedKeys(s.disabledMarkets),
		DisabledStrategies: sortedKeys(s.disabledStrategies),
		SelectedStrategy:   s.selectedStrategyID,
		LiveMarkets:        sortedKeys(s.liveMarkets),
	}
}

// persistLocked saves the current state; failures are logged and non-fatal.
// The caller holds s.mu so saves are applied in change order.
func (s *State) persistLocked() {
	if s.repo == nil {
		return
	}
	err := s.repo.SaveControlState(PersistedState{
		EmergencyStop:      s.emergencyStop,
		DisabledMarkets:    sortedKeys(s.disabledMarkets),
		DisabledStrategies: sortedKeys(s.disabledStrategies),
		LiveMarkets:        sortedKeys(s.liveMarkets),
		SelectedStrategy:   s.selectedStrategyID,
	})
	if err != nil {
		logger.Warn("Could not persist control state", "error", err.Error())
	}
}

// toSet converts a list into a membership set.
func toSet(items []string) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, item := range items {
		set[item] = true
	}
	return set
}

// sortedKeys returns the set members in sorted order (never nil, so JSON is []).
func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
