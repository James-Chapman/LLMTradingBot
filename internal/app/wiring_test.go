package app

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/app/apptest"
	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/learner"
	"github.com/James-Chapman/LLMTradingBot/internal/paper"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// fullyAutomated is a settings mutator for fully automated paper trading.
func fullyAutomated(s *config.Settings) { s.TradingMode = "fully_automated" }

// ideasOnce returns a strategy idea function that emits ideas on its first
// evaluation and nothing afterwards.
func ideasOnce(ideas ...domain.TradeIdea) func(strategy.Input) []domain.TradeIdea {
	fired := false
	return func(strategy.Input) []domain.TradeIdea {
		if fired {
			return nil
		}
		fired = true
		return append([]domain.TradeIdea(nil), ideas...)
	}
}

// near reports whether a and b differ by less than 1e-9.
func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// closedTradeFor returns the closed trade of positionID, failing when it is missing.
func closedTradeFor(t *testing.T, repo *storage.Repository, positionID string) storage.ClosedTrade {
	t.Helper()
	trades, err := repo.GetClosedTrades(context.Background(), 50)
	if err != nil {
		t.Fatalf("GetClosedTrades: %v", err)
	}
	for _, tr := range trades {
		if tr.PositionIDFull == positionID {
			return tr
		}
	}
	t.Fatalf("no closed trade for %s in %+v", positionID, trades)
	return storage.ClosedTrade{}
}

// learnerScore returns the learner's quality score for a key, failing when the key is missing.
func learnerScore(t *testing.T, rows []learner.SummaryRow, strategyID, market, direction string) float64 {
	t.Helper()
	for _, r := range rows {
		if r.Strategy == strategyID && r.Market == market && r.Direction == direction {
			return r.QualityScore
		}
	}
	t.Fatalf("no learner row for %s %s %s in %+v", strategyID, market, direction, rows)
	return 0
}

// seedWinningOutcome stores a closed BTC/EUR long of the basic strategy that
// made 50 on a notional of 1000 (a 5% return).
func seedWinningOutcome(repo *storage.Repository) {
	_ = repo.SaveSignalOutcome(context.Background(), storage.SignalOutcomeInput{
		StrategyID: strategy.BasicID, Market: "BTC/EUR", Direction: "long",
		EntryPrice: 100, ExitPrice: 105, Size: 10, PnL: 50, ExitReason: "auto",
		EntryAt: time.Now().UTC().Add(-time.Hour), PositionID: "seed-1",
	})
}

// TestPaperPositionsSurviveRestart covers the entry fee and short proceeds of paper positions.
func TestPaperPositionsSurviveRestart(t *testing.T) {
	t.Run("GIVEN a paper long and a paper short WHEN the bot restarts and both are closed THEN the P&L includes the entry fee and the short settles from its stored proceeds", func(t *testing.T) {
		env := newEnv(t, fullyAutomated, seedPrices(40))
		env.strategy.Ideas = ideasOnce(
			apptest.Idea(strategy.BasicID, "BTC/EUR", domain.Long, 0.9),
			apptest.Idea(strategy.BasicID, "ETH/EUR", domain.Short, 0.9),
		)
		env.boot(t)
		env.tick(t)

		stored, err := env.repo.GetOpenPositions(context.Background())
		if err != nil || len(stored) != 2 {
			t.Fatalf("expected a stored long and short, got %+v (%v)", stored, err)
		}
		storedByMarket := map[string]storage.OpenPosition{}
		for _, p := range stored {
			storedByMarket[p.Market] = p
		}
		long, short := storedByMarket["BTC/EUR"], storedByMarket["ETH/EUR"]
		if long.EntryFee <= 0 || short.EntryFee <= 0 {
			t.Fatalf("entry fees must be stored, got long %v short %v", long.EntryFee, short.EntryFee)
		}
		if short.ShortProceeds == nil || !near(*short.ShortProceeds, short.AvgPrice*math.Abs(short.Size)) {
			t.Fatalf("the short's sale proceeds must be stored, got %+v", short.ShortProceeds)
		}

		env.restart(t, fullyAutomated)
		env.boot(t)
		env.market.SetPrice("BTC/EUR", 50_500)
		env.market.SetPrice("ETH/EUR", 2_970)
		env.tick(t)

		if _, err := env.app.ClosePosition(context.Background(), long.PositionID); err != nil {
			t.Fatalf("close long: %v", err)
		}
		cashBefore := env.app.paper.Cash()
		result, err := env.app.ClosePosition(context.Background(), short.PositionID)
		if err != nil {
			t.Fatalf("close short: %v", err)
		}

		longTrade := closedTradeFor(t, env.repo, long.PositionID)
		wantLong := (longTrade.ExitPrice-longTrade.EntryPrice)*longTrade.Size - long.EntryFee - longTrade.ExitPrice*longTrade.Size*paper.TakerFeeRate
		if !near(longTrade.PnL, wantLong) {
			t.Errorf("long P&L %.6f, want %.6f (entry fee %.6f included)", longTrade.PnL, wantLong, long.EntryFee)
		}
		shortTrade := closedTradeFor(t, env.repo, short.PositionID)
		size := math.Abs(shortTrade.Size)
		exitFee := shortTrade.ExitPrice * size * paper.TakerFeeRate
		wantShort := (shortTrade.EntryPrice-shortTrade.ExitPrice)*size - short.EntryFee - exitFee
		if !near(shortTrade.PnL, wantShort) {
			t.Errorf("short P&L %.6f, want %.6f (entry fee %.6f included)", shortTrade.PnL, wantShort, short.EntryFee)
		}
		wantCash := cashBefore + *short.ShortProceeds - shortTrade.ExitPrice*size - exitFee
		if cash, _ := result["cash"].(float64); !near(cash, wantCash) {
			t.Errorf("cash after the short close %.6f, want %.6f", cash, wantCash)
		}
	})
}

// TestEmergencyStopTimeSurvivesRestart covers the persisted emergency-stop timestamp.
func TestEmergencyStopTimeSurvivesRestart(t *testing.T) {
	t.Run("GIVEN an active emergency stop WHEN the bot restarts THEN the stop keeps its original start time", func(t *testing.T) {
		env := newEnv(t, nil, nil)
		env.app.ActivateEmergencyStop(context.Background())
		before := env.app.ControlSnapshot().StopSince
		if before == nil {
			t.Fatal("an active stop must have a start time")
		}
		time.Sleep(2 * time.Millisecond) // a restored "now" would differ from the original

		env.restart(t, nil)

		after := env.app.ControlSnapshot().StopSince
		if after == nil || !after.Equal(before.Truncate(time.Microsecond)) {
			t.Errorf("stop time after restart %v, want %v", after, before)
		}
	})
}

// TestLearnerUsesTradeNotional covers percentage returns for paper and live closes.
func TestLearnerUsesTradeNotional(t *testing.T) {
	// referenceScores returns the quality score of the seeded 5% win followed
	// by a loss: with returns on notional, and with every outcome as currency P&L.
	referenceScores := func(pnl, notional float64) (withNotional, currencyOnly float64) {
		pct, cur := learner.New(), learner.New()
		pct.RecordOutcomeWithNotional(strategy.BasicID, "BTC/EUR", "long", 50, 1000)
		cur.RecordOutcome(strategy.BasicID, "BTC/EUR", "long", 50)
		pct.RecordOutcomeWithNotional(strategy.BasicID, "BTC/EUR", "long", pnl, notional)
		cur.RecordOutcome(strategy.BasicID, "BTC/EUR", "long", pnl)
		return pct.Summary()[0].QualityScore, cur.Summary()[0].QualityScore
	}

	t.Run("GIVEN a stored winning trade WHEN a paper long is closed and the bot restarts THEN the learner scores both as returns on their notional", func(t *testing.T) {
		env := newEnv(t, fullyAutomated, func(repo *storage.Repository) {
			seedPrices(40)(repo)
			seedWinningOutcome(repo)
		})
		env.strategy.Ideas = ideasOnce(apptest.Idea(strategy.BasicID, "BTC/EUR", domain.Long, 0.9))
		env.boot(t)
		env.tick(t)
		positionID := env.app.paper.OpenPositions()[0].PositionID
		env.market.SetPrice("BTC/EUR", 49_500)
		env.tick(t)
		if _, err := env.app.ClosePosition(context.Background(), positionID); err != nil {
			t.Fatalf("ClosePosition: %v", err)
		}

		trade := closedTradeFor(t, env.repo, positionID)
		want, currencyOnly := referenceScores(trade.PnL, trade.EntryPrice*math.Abs(trade.Size))
		if near(want, currencyOnly) {
			t.Fatalf("test setup cannot tell returns from currency P&L (%v)", want)
		}
		if got := learnerScore(t, env.app.LearningSummary(), strategy.BasicID, "BTC/EUR", "long"); !near(got, want) {
			t.Errorf("score after the close %v, want %v (currency-only would be %v)", got, want, currencyOnly)
		}

		env.restart(t, fullyAutomated)
		if got := learnerScore(t, env.app.LearningSummary(), strategy.BasicID, "BTC/EUR", "long"); !near(got, want) {
			t.Errorf("score after restart %v, want %v (currency-only would be %v)", got, want, currencyOnly)
		}
	})

	t.Run("GIVEN a stored winning trade WHEN a live long is stopped out THEN the learner scores the loss as a return on its notional", func(t *testing.T) {
		env := newEnv(t, liveSettings("fully_automated"), func(repo *storage.Repository) {
			seedPrices(40)(repo)
			seedWinningOutcome(repo)
		})
		env.boot(t)
		env.app.control.SetMarketLive("BTC/EUR", true)
		env.strategy.Ideas = btcIdeas(domain.Long)
		env.tick(t)
		env.live.FillLastOrder(50_000, 0.36)
		env.tick(t)
		env.market.SetPrice("BTC/EUR", 47_000)
		env.tick(t)
		env.live.FillLastOrder(47_000, 0.34)
		env.tick(t)

		trades, err := env.repo.GetClosedTrades(context.Background(), 10)
		if err != nil || len(trades) != 2 {
			t.Fatalf("expected the seeded and the live trade, got %+v (%v)", trades, err)
		}
		want, currencyOnly := referenceScores(trades[0].PnL, 50_000*0.002)
		if got := learnerScore(t, env.app.LearningSummary(), strategy.BasicID, "BTC/EUR", "long"); !near(got, want) {
			t.Errorf("score after the live close %v, want %v (currency-only would be %v)", got, want, currencyOnly)
		}
	})
}

// TestStrategySeesRoutedPositions covers the open positions passed to the strategy.
func TestStrategySeesRoutedPositions(t *testing.T) {
	t.Run("GIVEN a paper long on a market now routed live and a live long there WHEN the strategy evaluates THEN it sees the live position for that market and paper positions elsewhere", func(t *testing.T) {
		env := newEnv(t, func(s *config.Settings) { withKrakenKeys(s); s.TradingMode = "fully_automated" }, seedPrices(40))
		env.boot(t)
		env.app.control.SetMarketLive("BTC/EUR", true)
		var seen []domain.PositionRecord
		evaluations := 0
		env.strategy.Ideas = func(in strategy.Input) []domain.TradeIdea {
			evaluations++
			seen = in.Portfolio.OpenPositions
			switch evaluations {
			case 1: // paper environment: both markets trade on paper
				return []domain.TradeIdea{
					apptest.Idea(strategy.BasicID, "BTC/EUR", domain.Long, 0.9),
					apptest.Idea(strategy.BasicID, "ETH/EUR", domain.Long, 0.9),
				}
			case 2: // live environment: a live entry on BTC/EUR
				return []domain.TradeIdea{apptest.Idea(strategy.BasicID, "BTC/EUR", domain.Long, 0.9)}
			}
			return nil
		}
		env.tick(t)
		if _, err := env.app.SetTradingEnvironment("live", LiveConfirmation); err != nil {
			t.Fatalf("SetTradingEnvironment: %v", err)
		}
		env.tick(t)
		env.live.FillLastOrder(50_000, 0.36)
		env.tick(t)

		livePositions := env.app.LivePositions()
		if len(livePositions) != 1 {
			t.Fatalf("expected one live position, got %+v", livePositions)
		}
		var btc, eth []domain.PositionRecord
		for _, p := range seen {
			switch p.Market {
			case "BTC/EUR":
				btc = append(btc, p)
			case "ETH/EUR":
				eth = append(eth, p)
			}
		}
		if len(btc) != 1 || btc[0].PositionID != livePositions[0].PositionID {
			t.Errorf("BTC/EUR must show only the live position %s, got %+v", livePositions[0].PositionID, btc)
		}
		if len(eth) != 1 || eth[0].Size <= 0 {
			t.Errorf("ETH/EUR is not live-routed and must show its paper long, got %+v", eth)
		}
	})
}

// positionByID returns the dashboard position with the full id, failing when it is missing.
func positionByID(t *testing.T, d DashboardView, id string) PositionView {
	t.Helper()
	for _, p := range d.Positions {
		if p.PositionIDFull == id {
			return p
		}
	}
	t.Fatalf("position %s missing from dashboard: %+v", id, d.Positions)
	return PositionView{}
}

// containsString reports whether items contains s.
func containsString(items []string, s string) bool {
	for _, item := range items {
		if item == s {
			return true
		}
	}
	return false
}

// TestDashboardShowsLivePositions covers live positions in the dashboard payload.
func TestDashboardShowsLivePositions(t *testing.T) {
	t.Run("GIVEN a live entry WHEN it is pending, filled and being sold THEN the dashboard lists it as a live position with that status", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		env.strategy.Ideas = btcIdeas(domain.Long)
		env.tick(t)
		id := env.app.LivePositions()[0].PositionID

		d := env.app.Dashboard()
		pending := positionByID(t, d, id)
		if pending.Environment != "live" || pending.Status != "entry_pending" || pending.Market != "BTC/EUR" || pending.Direction != "long" {
			t.Errorf("expected a pending live long, got %+v", pending)
		}
		if !containsString(d.OpenPositionIDs, id) {
			t.Errorf("open_position_ids must include the live position, got %v", d.OpenPositionIDs)
		}

		env.live.FillLastOrder(50_000, 0.36)
		env.tick(t)
		if open := positionByID(t, env.app.Dashboard(), id); open.Status != "open" || open.Size != "0.002000" {
			t.Errorf("expected the filled live position open with its executed size, got %+v", open)
		}

		if _, err := env.app.ClosePosition(context.Background(), id); err != nil {
			t.Fatalf("ClosePosition: %v", err)
		}
		if selling := positionByID(t, env.app.Dashboard(), id); selling.Status != "exit_pending" {
			t.Errorf("expected exit_pending while the sell is open, got %+v", selling)
		}
	})

	t.Run("GIVEN a paper position WHEN the dashboard is built THEN it is marked as paper and open", func(t *testing.T) {
		env := newEnv(t, fullyAutomated, seedPrices(40))
		env.strategy.Ideas = ideasOnce(apptest.Idea(strategy.BasicID, "BTC/EUR", domain.Long, 0.9))
		env.boot(t)
		env.tick(t)
		d := env.app.Dashboard()
		if len(d.Positions) != 1 || d.Positions[0].Environment != "paper" || d.Positions[0].Status != "open" {
			t.Errorf("expected one open paper position, got %+v", d.Positions)
		}
	})
}

// activityText returns every recent activity message and detail joined by newlines.
func (e *testEnv) activityText() string {
	var b strings.Builder
	for _, entry := range e.app.activity.Recent(200) {
		b.WriteString(entry.Message + " | " + entry.Detail + "\n")
	}
	return b.String()
}

// TestSubCentPricesInActivity covers price precision in activity messages.
func TestSubCentPricesInActivity(t *testing.T) {
	t.Run("GIVEN a market priced below a cent WHEN it is traded, stopped out and reported THEN every price in the activity feed keeps its significant digits", func(t *testing.T) {
		env := newEnv(t, fullyAutomated, func(repo *storage.Repository) {
			for i := 0; i < 40; i++ {
				_ = repo.SavePriceTick(context.Background(), "BTC/EUR", 0.0000085, nil)
				_ = repo.SavePriceTick(context.Background(), "ETH/EUR", 3000, nil)
			}
		})
		env.market.SetPrice("BTC/EUR", 0.0000085)
		env.strategy.Ideas = ideasOnce(apptest.Idea(strategy.BasicID, "BTC/EUR", domain.Long, 0.9))
		env.boot(t)
		env.tick(t)
		env.market.SetPrice("BTC/EUR", 0.0000070)
		env.tick(t)

		text := env.activityText()
		sym := env.app.currencySymbol
		for _, want := range []string{
			"BTC/EUR " + sym + "0.000008500", // tick price summary
			"@ " + sym + "0.000008500",       // auto-execute
			"closed at " + sym + "0.00000",   // stop-loss exit price
			"Entry " + sym + "0.00000",       // stop-loss entry price
		} {
			if !strings.Contains(text, want) {
				t.Errorf("activity feed is missing %q:\n%s", want, text)
			}
		}
		if strings.Contains(text, sym+"0.00 ") || strings.Contains(text, sym+"0.00\n") {
			t.Errorf("a sub-cent price was shown as 0.00:\n%s", text)
		}
	})

	t.Run("GIVEN a price of one or more WHEN it is shown in an activity message THEN it keeps 2 decimals and thousands separators", func(t *testing.T) {
		if got := fmtPrice(59611.3); got != "59,611.30" {
			t.Errorf("fmtPrice(59611.3) = %q, want 59,611.30", got)
		}
		if got := fmtPrice(0.0000085); got != "0.000008500" {
			t.Errorf("fmtPrice(0.0000085) = %q, want 0.000008500", got)
		}
	})
}
