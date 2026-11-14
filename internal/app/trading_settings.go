package app

import (
	"fmt"
	"net/http"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// LiveConfirmation is the text the operator must type to switch to live trading.
const LiveConfirmation = "LIVE"

// TradingMode returns the active trading mode: the web interface's choice if
// one was saved, otherwise the default (manual).
func (a *App) TradingMode() string { return a.control.TradingMode() }

// TradingEnvironment returns the active environment (paper or live): the web
// interface's choice if one was saved, otherwise the default (paper).
func (a *App) TradingEnvironment() string { return a.control.TradingEnvironment() }

// seedTradingSettings uses the built-in defaults (manual, paper) for any
// trading setting never chosen in the web interface.
func (a *App) seedTradingSettings() {
	s := a.cfg()
	a.control.SeedTradingSettings(s.TradingMode, s.TradingEnvironment)
}

// SetTradingMode changes the trading mode from the web interface. The choice
// is saved and survives restarts.
func (a *App) SetTradingMode(mode string) (map[string]any, error) {
	previous := a.TradingMode()
	if err := a.control.SetTradingMode(mode); err != nil {
		return nil, statusError(http.StatusBadRequest, err.Error())
	}
	if mode != previous {
		a.logger.Info("Trading mode changed", "from", previous, "to", mode)
		a.activity.Warn(fmt.Sprintf("Trading mode changed: %s → %s", previous, mode), "Changed from the web interface.")
		a.bumpVersion()
	}
	return map[string]any{"trading_mode": mode}, nil
}

// SetTradingEnvironment switches between paper and live trading from the web
// interface. Going live needs the typed confirmation and Kraken API keys;
// going back to paper never does. The choice is saved and survives restarts.
func (a *App) SetTradingEnvironment(environment, confirmation string) (map[string]any, error) {
	if environment == string(domain.EnvLive) {
		if confirmation != LiveConfirmation {
			return nil, statusError(http.StatusBadRequest,
				fmt.Sprintf("Type %s to confirm switching to live trading with real money", LiveConfirmation))
		}
		if running := a.cfg(); running.KrakenAPIKey == "" || running.KrakenAPISecret == "" {
			if saved := a.savedSettings(); saved.KrakenAPIKey != "" && saved.KrakenAPISecret != "" {
				return nil, statusError(http.StatusBadRequest,
					"Kraken API keys were saved but are not in use yet: restart the bot, then switch to live")
			}
			return nil, statusError(http.StatusBadRequest,
				"Kraken API keys are not set: add them under Settings → Kraken first")
		}
	}
	previous := a.TradingEnvironment()
	if err := a.control.SetTradingEnvironment(environment); err != nil {
		return nil, statusError(http.StatusBadRequest, err.Error())
	}
	if environment != previous {
		a.logger.Warn("Trading environment changed", "from", previous, "to", environment)
		a.activity.Warn(fmt.Sprintf("Trading environment changed: %s → %s", previous, environment),
			"Changed from the web interface. Live orders only go to markets toggled live.")
		a.bumpVersion()
	}
	return map[string]any{"trading_environment": environment}, nil
}
