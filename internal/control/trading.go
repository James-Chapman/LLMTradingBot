package control

import (
	"fmt"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// validModes and validEnvironments are the accepted trading settings values.
var (
	validModes = map[string]bool{
		string(domain.ModeManual):         true,
		string(domain.ModeSemiAutomated):  true,
		string(domain.ModeFullyAutomated): true,
	}
	validEnvironments = map[string]bool{
		string(domain.EnvPaper): true,
		string(domain.EnvLive):  true,
	}
)

// SeedTradingSettings fills in the default trading mode and environment for
// any value not already restored from the database. Call after LoadFromDB, so
// choices made in the web interface win over the defaults.
func (s *State) SeedTradingSettings(mode, environment string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tradingMode == "" {
		s.tradingMode = mode
	}
	if s.tradingEnvironment == "" {
		s.tradingEnvironment = environment
	}
}

// TradingMode returns the active trading mode (manual, semi_automated, fully_automated).
func (s *State) TradingMode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tradingMode
}

// TradingEnvironment returns the active environment (paper or live).
func (s *State) TradingEnvironment() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tradingEnvironment
}

// SetTradingMode changes and persists the trading mode; unknown modes are refused.
func (s *State) SetTradingMode(mode string) error {
	if !validModes[mode] {
		return fmt.Errorf("unknown trading mode %q (want manual, semi_automated or fully_automated)", mode)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tradingMode = mode
	s.persistLocked()
	return nil
}

// SetTradingEnvironment changes and persists the environment; unknown values
// are refused. Safety checks for going live (confirmation, API keys) belong to
// the caller.
func (s *State) SetTradingEnvironment(environment string) error {
	if !validEnvironments[environment] {
		return fmt.Errorf("unknown trading environment %q (want paper or live)", environment)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tradingEnvironment = environment
	s.persistLocked()
	return nil
}
