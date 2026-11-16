package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/kraken"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// errSimulatedCrash is panicked by a fake to stop the bot at an exact point.
var errSimulatedCrash = errors.New("simulated crash")

// crashDuring runs fn and requires it to stop with the simulated crash, as if
// the process had died at that point.
func crashDuring(t *testing.T, fn func()) {
	t.Helper()
	crashed := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				if r != errSimulatedCrash {
					panic(r)
				}
				crashed = true
			}
		}()
		fn()
	}()
	if !crashed {
		t.Fatal("expected the simulated crash")
	}
}

// saveLikeEngine writes an accepted order to order_records the way the real
// execution engine does.
func saveLikeEngine(t *testing.T, repo *storage.Repository, order domain.OrderRecord) {
	t.Helper()
	if err := repo.SaveOrder(context.Background(), order, storage.SaveOrderOptions{Environment: "live", ClientOrderID: order.ID}); err != nil {
		t.Fatal(err)
	}
}

// restartLive restarts the bot from the database with mutate's settings and
// BTC/EUR toggled live.
func restartLive(t *testing.T, env *testEnv, mutate func(*config.Settings)) {
	t.Helper()
	env.restart(t, mutate)
	env.boot(t)
	env.app.control.SetMarketLive("BTC/EUR", true)
}

// openLiveLong runs ticks until a filled live long of 0.002 BTC bought at
// 50 000 (fee 0.36) is open, and returns its position ID.
func openLiveLong(t *testing.T, env *testEnv) string {
	t.Helper()
	env.strategy.Ideas = btcIdeas(domain.Long)
	env.tick(t)
	env.live.FillLastOrder(50_000, 0.36)
	env.tick(t)
	positions := env.app.LivePositions()
	if len(positions) != 1 || positions[0].Status != "open" {
		t.Fatalf("expected one open live long, got %+v", positions)
	}
	return positions[0].PositionID
}

// fillOf is a filled reconciliation update for order.
func fillOf(order domain.OrderRecord, size, price, fee float64) kraken.OrderUpdate {
	return kraken.OrderUpdate{
		OrderID: order.ID, ExchangeOrderID: "FAKE-TXID", PositionID: order.PositionID, Direction: order.Direction,
		Status: "filled", KrakenStatus: "closed", ExecutedSize: size, AvgPrice: price, Fee: fee,
	}
}

// activityCount returns how many recent activity messages contain text.
func (e *testEnv) activityCount(text string) int {
	n := 0
	for _, entry := range e.app.activity.Recent(200) {
		if strings.Contains(entry.Message, text) {
			n++
		}
	}
	return n
}

// dailyLoss returns the risk engine's daily loss.
func (e *testEnv) dailyLoss() float64 {
	return e.app.risk.Snapshot().DailyLoss
}

// TestLiveCrashRecovery covers a stop between Kraken accepting an order and the book being updated.
func TestLiveCrashRecovery(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN a live sell WHEN Kraken is asked to place it THEN the position is already stored as exit-pending with that order ID", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		openLiveLong(t, env)
		var stored []storage.LivePosition
		env.live.OnAccepted = func(order domain.OrderRecord) {
			stored, _ = env.repo.GetLivePositions(ctx)
			if len(stored) != 1 || stored[0].Status != "exit_pending" || stored[0].ExitOrderID != order.ID || stored[0].ExitRequestedAt.IsZero() {
				t.Errorf("the sell must be recorded before it is sent, stored %+v for order %s", stored, order.ID)
			}
		}
		env.market.SetPrice("BTC/EUR", 47_000)
		env.tick(t)
		if stored == nil {
			t.Fatal("no sell was sent")
		}
	})

	t.Run("GIVEN a live buy WHEN Kraken is asked to place it THEN the position is already stored as entry-pending with that order ID", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		env.strategy.Ideas = btcIdeas(domain.Long)
		sent := false
		env.live.OnAccepted = func(order domain.OrderRecord) {
			sent = true
			stored, _ := env.repo.GetLivePositions(ctx)
			if len(stored) != 1 || stored[0].Status != "entry_pending" || stored[0].EntryOrderID != order.ID {
				t.Errorf("the buy must be recorded before it is sent, stored %+v for order %s", stored, order.ID)
			}
		}
		env.tick(t)
		if !sent {
			t.Fatal("no buy was sent")
		}
	})

	t.Run("GIVEN the bot stops right after Kraken accepted a stop sell WHEN it restarts and the sell fills THEN the P&L is recorded once and no second sell is sent", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		openLiveLong(t, env)
		env.live.OnAccepted = func(order domain.OrderRecord) {
			saveLikeEngine(t, env.repo, order) // the engine saved the order, then the process died
			panic(errSimulatedCrash)
		}
		env.market.SetPrice("BTC/EUR", 47_000)
		crashDuring(t, func() { _, _ = env.app.strategyTick(ctx) })
		env.live.OnAccepted = nil
		sell := env.live.LastOrder()

		restartLive(t, env, liveSettings("fully_automated"))
		env.live.QueueUpdate(fillOf(sell, 0.002, 47_000, 0.34))
		env.tick(t)
		env.tick(t)

		if n := env.live.SellCount(); n != 1 {
			t.Fatalf("expected exactly one real sell, got %d", n)
		}
		if n := len(env.app.LivePositions()); n != 0 {
			t.Fatalf("the filled sell must close the position, got %+v", env.app.LivePositions())
		}
		if want := 3_000*0.002 + 0.36 + 0.34; !near(env.dailyLoss(), want) {
			t.Errorf("expected the loss %.4f counted once, got %.4f", want, env.dailyLoss())
		}
		if trades, _ := env.repo.GetClosedTrades(ctx, 10); len(trades) != 1 {
			t.Errorf("expected one closed trade, got %+v", trades)
		}
	})

	t.Run("GIVEN the bot stops right after Kraken accepted a buy WHEN it restarts and the buy fills THEN the fill is tracked as an open position and no second buy is sent", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		env.strategy.Ideas = alwaysLong
		env.live.OnAccepted = func(domain.OrderRecord) { panic(errSimulatedCrash) } // died before the order row was written
		crashDuring(t, func() { _, _ = env.app.strategyTick(ctx) })
		env.live.OnAccepted = nil
		buy := env.live.LastOrder()

		restartLive(t, env, liveSettings("fully_automated"))
		env.tick(t)
		if tracked := env.live.LastTracked(); len(tracked) != 1 || tracked[0].ID != buy.ID {
			t.Fatalf("the restored pending buy must be looked up at Kraken by client order id, got %+v", tracked)
		}
		env.live.QueueUpdate(fillOf(buy, 0.002, 50_000, 0.36))
		env.tick(t)
		env.tick(t)

		if n := env.live.ExecutedCount(); n != 1 {
			t.Fatalf("expected no duplicate entry, got %d orders", n)
		}
		positions := env.app.LivePositions()
		if len(positions) != 1 || positions[0].Status != "open" || positions[0].Size != 0.002 {
			t.Fatalf("expected the fill tracked as an open position, got %+v", positions)
		}
	})

	t.Run("GIVEN a filled exit whose P&L is already stamped WHEN the bot restarts THEN the position is removed without counting the loss again", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		id := openLiveLong(t, env)
		if _, err := env.app.ClosePosition(ctx, id); err != nil {
			t.Fatal(err)
		}
		sell := env.live.LastOrder()
		saveLikeEngine(t, env.repo, sell)
		if err := env.repo.UpdateOrderExecution(ctx, storage.OrderExecution{OrderID: sell.ID, Status: "filled", ExecutedSize: 0.002, AvgPrice: 47_000, Fee: 0.34}); err != nil {
			t.Fatal(err)
		}
		pnl := -(3_000*0.002 + 0.36 + 0.34)
		if err := env.repo.UpdateOrderPnL(ctx, sell.ID, pnl); err != nil {
			t.Fatal(err)
		}
		env.app.recordTradeResult(pnl) // the result was recorded too, then the bot stopped

		restartLive(t, env, liveSettings("fully_automated"))

		if n := len(env.app.LivePositions()); n != 0 {
			t.Fatalf("the recorded exit must remove the position, got %+v", env.app.LivePositions())
		}
		if !near(env.dailyLoss(), -pnl) {
			t.Errorf("expected the loss counted once (%.4f), got %.4f", -pnl, env.dailyLoss())
		}
	})
}

// TestLiveTrackedReconciliation covers live orders the order ledger may not know about.
func TestLiveTrackedReconciliation(t *testing.T) {
	t.Run("GIVEN a pending live entry WHEN reconciliation runs THEN the book's own order is passed for a client-order-id lookup", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		env.strategy.Ideas = btcIdeas(domain.Long)
		env.tick(t)
		buy := env.live.LastOrder()

		env.tick(t)

		tracked := env.live.LastTracked()
		if len(tracked) != 1 || tracked[0].ID != buy.ID || tracked[0].PositionID != buy.PositionID || tracked[0].Direction != domain.Long || tracked[0].Timestamp.IsZero() {
			t.Fatalf("expected the pending buy with its position, side and time, got %+v", tracked)
		}
	})

	t.Run("GIVEN a live entry whose order row was never written WHEN Kraken reports it filled THEN the position opens", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		env.strategy.Ideas = btcIdeas(domain.Long)
		env.tick(t) // the fake engine writes no order row, like a failed SaveOrder
		env.live.QueueUpdate(fillOf(env.live.LastOrder(), 0.002, 50_000, 0.36))
		env.tick(t)

		if p := env.app.LivePositions(); len(p) != 1 || p[0].Status != "open" {
			t.Fatalf("expected the fill applied, got %+v", p)
		}
	})

	t.Run("GIVEN a live entry Kraken never received WHEN reconciliation reports it not found THEN the position is removed with a warning", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		env.strategy.Ideas = btcIdeas(domain.Long)
		env.tick(t)
		buy := env.live.LastOrder()
		env.live.QueueUpdate(kraken.OrderUpdate{OrderID: buy.ID, PositionID: buy.PositionID, Direction: domain.Long, Status: "not_found", AvgPrice: 50_000})
		env.tick(t)

		if n := len(env.app.LivePositions()); n != 0 || !env.activityContains("not executed (not_found)") {
			t.Fatalf("expected the position removed with a warning, got %+v", env.app.LivePositions())
		}
	})

	t.Run("GIVEN a live sell Kraken never received WHEN reconciliation reports it not found THEN the position is open again with a warning", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		id := openLiveLong(t, env)
		if _, err := env.app.ClosePosition(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		sell := env.live.LastOrder()
		env.live.QueueUpdate(kraken.OrderUpdate{OrderID: sell.ID, PositionID: id, Direction: domain.Short, Status: "not_found", AvgPrice: 50_000})
		env.tick(t)

		if p := env.app.LivePositions(); len(p) != 1 || p[0].Status != "open" || !env.activityContains("position open again") {
			t.Fatalf("expected the position open again with a warning, got %+v", p)
		}
	})

	t.Run("GIVEN an open position whose sell is not in the book WHEN Kraken reports that sell filled THEN it is matched by its position and recorded", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		id := openLiveLong(t, env)
		// A sell recorded in order_records but never written to the book.
		sell := domain.OrderRecord{ID: domain.NewID(), Market: "BTC/EUR", Direction: domain.Short, Size: 0.002, Price: 51_000, Status: "pending", PositionID: id}
		env.live.QueueUpdate(fillOf(sell, 0.002, 51_000, 0.3))
		env.tick(t)

		if n := len(env.app.LivePositions()); n != 0 {
			t.Fatalf("the sell must close the position, got %+v", env.app.LivePositions())
		}
		if trades, _ := env.repo.GetClosedTrades(context.Background(), 10); len(trades) != 1 {
			t.Errorf("expected the close recorded, got %+v", trades)
		}
	})

	t.Run("GIVEN an open position and an unrecorded filled sell in the order ledger WHEN the bot restarts THEN the sell is applied once", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		id := openLiveLong(t, env)
		sell := domain.OrderRecord{ID: domain.NewID(), Market: "BTC/EUR", Direction: domain.Short, Size: 0.002, Price: 51_000, Status: "pending", PositionID: id, Timestamp: time.Now().UTC()}
		saveLikeEngine(t, env.repo, sell)
		if err := env.repo.UpdateOrderExecution(context.Background(), storage.OrderExecution{OrderID: sell.ID, Status: "filled", ExecutedSize: 0.002, AvgPrice: 51_000, Fee: 0.3}); err != nil {
			t.Fatal(err)
		}

		restartLive(t, env, liveSettings("fully_automated"))
		env.tick(t)

		if n := len(env.app.LivePositions()); n != 0 {
			t.Fatalf("the recorded sell must close the position, got %+v", env.app.LivePositions())
		}
		if n := env.live.SellCount(); n != 0 {
			t.Errorf("no sell may be sent for a position already sold, got %d", n)
		}
	})
}

// TestLiveProtectionStates covers live positions in the emergency stop, the paper environment and disabled markets.
func TestLiveProtectionStates(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN the emergency stop with an open live long past its stop WHEN ticks run THEN nothing is sold automatically, a warning is posted once, orders still reconcile and a manual close sends one sell", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		env.strategy.Ideas = btcIdeas(domain.Long)
		env.tick(t)
		buy := env.live.LastOrder()
		env.app.ActivateEmergencyStop(ctx)
		env.live.QueueUpdate(fillOf(buy, 0.002, 50_000, 0.36)) // the entry fills during the stop
		env.market.SetPrice("BTC/EUR", 47_000)
		reconciles := env.live.ReconcileCount()

		env.tick(t)
		env.tick(t)

		if n := env.live.SellCount(); n != 0 {
			t.Fatalf("the emergency stop must pause automatic sells, got %d", n)
		}
		if env.live.ReconcileCount()-reconciles != 2 {
			t.Errorf("pending live orders must reconcile on every stopped tick")
		}
		if p := env.app.LivePositions(); len(p) != 1 || p[0].Status != "open" {
			t.Fatalf("the fill during the stop must be applied, got %+v", p)
		}
		if n := env.activityCount("no automatic stop"); n != 1 {
			t.Errorf("expected one warning about unprotected live positions, got %d", n)
		}

		result, err := env.app.ClosePosition(ctx, env.app.LivePositions()[0].PositionID)
		if err != nil || result["status"] != "closing" {
			t.Fatalf("a manual close must be allowed during the stop, got %v / %v", result, err)
		}
		if n := env.live.SellCount(); n != 1 {
			t.Errorf("expected exactly one manual sell, got %d", n)
		}
		if intent := lastIntent(t, env.live); intent.Size != 0.002 {
			t.Errorf("expected a sell of the held size, got %+v", intent)
		}
	})

	t.Run("GIVEN the emergency stop WHEN a paper position is closed from the dashboard THEN it closes", func(t *testing.T) {
		env := newEnv(t, func(s *config.Settings) { s.TradingMode = "fully_automated" }, seedPrices(40))
		env.boot(t)
		env.strategy.Ideas = btcIdeas(domain.Long)
		env.tick(t)
		env.app.ActivateEmergencyStop(ctx)

		if _, err := env.app.ClosePosition(ctx, env.app.paper.OpenPositions()[0].PositionID); err != nil {
			t.Fatalf("a manual close reduces exposure and must be allowed, got %v", err)
		}
		if n := len(env.app.paper.OpenPositions()); n != 0 {
			t.Errorf("expected the paper position closed, got %d open", n)
		}
	})

	t.Run("GIVEN an open live position WHEN switching to paper THEN the switch is refused naming the position", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		openLiveLong(t, env)

		_, err := env.app.SetTradingEnvironment("paper", "")

		if statusCode(err) != http.StatusConflict || !strings.Contains(err.Error(), "BTC/EUR") {
			t.Fatalf("expected a 409 naming BTC/EUR, got %v", err)
		}
		if env.app.TradingEnvironment() != "live" {
			t.Errorf("the environment must stay live")
		}
	})

	t.Run("GIVEN an open live position WHEN its market's live flag is turned off THEN it is refused, and other markets still toggle", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		openLiveLong(t, env)

		_, err := env.app.ToggleMarketLive("BTC/EUR")

		if statusCode(err) != http.StatusConflict || !strings.Contains(err.Error(), "BTC/EUR") || !env.app.control.IsMarketLive("BTC/EUR") {
			t.Fatalf("expected a 409 and BTC/EUR still live, got %v", err)
		}
		if result, err := env.app.ToggleMarketLive("ETH/EUR"); err != nil || result["live"] != true {
			t.Errorf("a market without a live position must still toggle, got %v / %v", result, err)
		}
	})

	t.Run("GIVEN a live position on a disabled market WHEN its stop is hit THEN exactly one sell is sent", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		openLiveLong(t, env)
		env.app.ToggleMarket("BTC/EUR")
		env.market.SetPrice("BTC/EUR", 47_000)

		env.tick(t)
		env.tick(t)

		if n := env.live.SellCount(); n != 1 {
			t.Fatalf("expected one stop sell for the disabled market, got %d", n)
		}
	})

	t.Run("GIVEN a live position and every market disabled WHEN its stop is hit THEN one sell is sent and orders reconcile", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		openLiveLong(t, env)
		env.app.ToggleMarket("BTC/EUR")
		env.app.ToggleMarket("ETH/EUR")
		env.market.SetPrice("BTC/EUR", 47_000)

		env.tick(t)
		env.live.FillLastOrder(47_000, 0.34)
		env.tick(t)

		if n := env.live.SellCount(); n != 1 || len(env.app.LivePositions()) != 0 {
			t.Fatalf("expected one sell that then fills, got %d sells and %+v", n, env.app.LivePositions())
		}
	})

	t.Run("GIVEN a live position on a market no longer in the universe after a restart WHEN its stop is hit THEN it is priced and sold", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		openLiveLong(t, env)
		restartLive(t, env, func(s *config.Settings) { liveSettings("fully_automated")(s); s.FixedMarkets = []string{"ETH/EUR"} })
		env.market.SetPrice("BTC/EUR", 47_000)

		env.tick(t)

		if n := env.live.SellCount(); n != 1 {
			t.Fatalf("expected the position outside the universe to be sold, got %d sells", n)
		}
	})

	t.Run("GIVEN a live position while the environment is paper WHEN its stop is hit THEN it is still sold", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		openLiveLong(t, env)
		if err := env.app.control.SetTradingEnvironment("paper"); err != nil { // e.g. saved before this version
			t.Fatal(err)
		}
		env.market.SetPrice("BTC/EUR", 47_000)

		env.tick(t)

		if n := env.live.SellCount(); n != 1 {
			t.Fatalf("expected the live stop to run in the paper environment, got %d sells", n)
		}
	})
}

// trailingSettings is the live environment with a 3% trailing stop.
func trailingSettings(s *config.Settings) {
	liveSettings("fully_automated")(s)
	s.TrailingStopPct = 0.03
}

// TestLiveTrailingStop covers the trailing stop of live positions across restarts.
func TestLiveTrailingStop(t *testing.T) {
	t.Run("GIVEN a live long that rose to a new high WHEN the bot restarts and the price retraces past the trail THEN the persisted high triggers the sell", func(t *testing.T) {
		env := newEnv(t, trailingSettings, seedPrices(40))
		env.boot(t)
		env.app.control.SetMarketLive("BTC/EUR", true)
		openLiveLong(t, env)
		env.market.SetPrice("BTC/EUR", 60_000)
		env.tick(t)

		restartLive(t, env, trailingSettings)
		env.market.SetPrice("BTC/EUR", 58_000) // 3.3% below the 60 000 high
		env.tick(t)

		if n := env.live.SellCount(); n != 1 {
			t.Fatalf("expected the trailing stop from the persisted high, got %d sells", n)
		}
	})

	t.Run("GIVEN a live long with no recorded high WHEN the bot restarts below the entry THEN the entry price is the trailing reference", func(t *testing.T) {
		env := newEnv(t, trailingSettings, seedPrices(40))
		env.boot(t)
		env.app.control.SetMarketLive("BTC/EUR", true)
		openLiveLong(t, env)
		rows, _ := env.repo.GetLivePositions(context.Background())
		rows[0].TrailingHigh = 0 // a row saved before highs were stored
		if err := env.repo.SaveLivePosition(context.Background(), rows[0]); err != nil {
			t.Fatal(err)
		}

		restartLive(t, env, trailingSettings)
		env.market.SetPrice("BTC/EUR", 48_000) // 4% below the entry: past the 3% trail, short of the 5% stop
		env.tick(t)

		if n := env.live.SellCount(); n != 1 {
			t.Fatalf("expected the trailing stop measured from the entry price, got %d sells", n)
		}
	})
}

// TestLiveExitReconciliation covers what a live sell's execution does to the position.
func TestLiveExitReconciliation(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN a live long WHEN its sell executes partly THEN the remainder stays open with its share of the entry fee", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		id := openLiveLong(t, env)
		if _, err := env.app.ClosePosition(ctx, id); err != nil {
			t.Fatal(err)
		}
		env.live.QueueUpdate(fillOf(env.live.LastOrder(), 0.001, 52_000, 0.2))
		env.tick(t)

		pos, ok := env.app.liveBook.Get(id)
		if !ok || pos.Status != "open" || !near(pos.Size, 0.001) || !near(pos.EntryFee, 0.18) {
			t.Fatalf("expected 0.001 left open with fee 0.18, got %+v", pos)
		}
		trades, _ := env.repo.GetClosedTrades(ctx, 10)
		if want := 2_000*0.001 - 0.18 - 0.2; len(trades) != 1 || !near(trades[0].PnL, want) {
			t.Errorf("expected the sold part's P&L %.4f, got %+v", want, trades)
		}
	})

	t.Run("GIVEN a live long WHEN its sell executes nothing THEN the position is open again and can be sold again", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		id := openLiveLong(t, env)
		if _, err := env.app.ClosePosition(ctx, id); err != nil {
			t.Fatal(err)
		}
		sell := env.live.LastOrder()
		env.live.QueueUpdate(kraken.OrderUpdate{OrderID: sell.ID, PositionID: id, Direction: domain.Short, Status: "canceled", KrakenStatus: "canceled", AvgPrice: 50_000})
		env.tick(t)

		if p, _ := env.app.liveBook.Get(id); p.Status != "open" || p.Size != 0.002 {
			t.Fatalf("expected the position open again, got %+v", p)
		}
		if _, err := env.app.ClosePosition(ctx, id); err != nil || env.live.SellCount() != 2 {
			t.Errorf("a new sell must be possible, got %v after %d sells", err, env.live.SellCount())
		}
	})

	t.Run("GIVEN a live sell leaves less than Kraken's order minimum WHEN it is reconciled THEN the remainder is removed as dust with a note", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		env.live.MinOrder = map[string]float64{"BTC/EUR": 0.0015}
		id := openLiveLong(t, env)
		if _, err := env.app.ClosePosition(ctx, id); err != nil {
			t.Fatal(err)
		}
		env.live.QueueUpdate(fillOf(env.live.LastOrder(), 0.001, 52_000, 0.2))
		env.tick(t)

		if n := len(env.app.LivePositions()); n != 0 || !env.activityContains("dust") {
			t.Fatalf("expected the 0.001 remainder removed as dust, got %+v", env.app.LivePositions())
		}
	})

	t.Run("GIVEN a live position smaller than Kraken's order minimum WHEN its stop is hit THEN no sell is sent and it is removed as dust", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		env.live.MinOrder = map[string]float64{"BTC/EUR": 0.005}
		openLiveLong(t, env)
		env.market.SetPrice("BTC/EUR", 47_000)
		env.tick(t)

		if n := env.live.SellCount(); n != 0 || len(env.app.LivePositions()) != 0 || !env.activityContains("dust") {
			t.Fatalf("expected no sell and the position removed as dust, got %d sells and %+v", n, env.app.LivePositions())
		}
	})

	t.Run("GIVEN Kraken rejects a live stop sell WHEN the next ticks run THEN it is not re-sent until the backoff has passed", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		openLiveLong(t, env)
		env.live.SetStatus("rejected")
		env.market.SetPrice("BTC/EUR", 47_000)

		env.tick(t)
		env.tick(t)

		if n := env.live.SellCount(); n != 1 {
			t.Fatalf("a rejected sell must not be re-sent every tick, got %d sells", n)
		}
		if p := env.app.LivePositions(); len(p) != 1 || p[0].Status != "open" {
			t.Fatalf("the position stays open after a rejected sell, got %+v", p)
		}
		later := time.Now().UTC().Add(6 * time.Minute)
		env.app.now = func() time.Time { return later }
		env.tick(t)
		if n := env.live.SellCount(); n != 2 {
			t.Errorf("after the backoff the sell is tried again, got %d sells", n)
		}
	})
}

// TestForgetLivePosition covers removing a live position without trading.
func TestForgetLivePosition(t *testing.T) {
	t.Run("GIVEN an open live position WHEN it is forgotten THEN its row is removed, no order is sent and a warning is posted", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		id := openLiveLong(t, env)
		orders := env.live.ExecutedCount()

		result, err := env.app.ForgetLivePosition(id[:8])

		if err != nil || result["status"] != "forgotten" || result["position_id"] != id {
			t.Fatalf("unexpected forget result %v / %v", result, err)
		}
		if n := len(env.app.LivePositions()); n != 0 {
			t.Errorf("the position must be gone from the book, got %d", n)
		}
		if rows, _ := env.repo.GetLivePositions(context.Background()); len(rows) != 0 {
			t.Errorf("the position must be gone from the database, got %+v", rows)
		}
		if env.live.ExecutedCount() != orders {
			t.Errorf("forgetting must not send an order")
		}
		if !env.activityContains("forgotten") {
			t.Errorf("expected a warning in the activity feed")
		}
	})

	t.Run("GIVEN a paper position WHEN forget is requested THEN it is refused", func(t *testing.T) {
		env := newEnv(t, func(s *config.Settings) { s.TradingMode = "fully_automated" }, seedPrices(40))
		env.boot(t)
		env.strategy.Ideas = btcIdeas(domain.Long)
		env.tick(t)

		_, err := env.app.ForgetLivePosition(env.app.paper.OpenPositions()[0].PositionID)

		if statusCode(err) != http.StatusConflict || len(env.app.paper.OpenPositions()) != 1 {
			t.Fatalf("expected a 409 and the paper position kept, got %v", err)
		}
		if _, err := env.app.ForgetLivePosition("nope"); statusCode(err) != http.StatusNotFound {
			t.Errorf("expected 404 for an unknown position, got %v", err)
		}
	})
}

// TestLiveExitRace covers several exit paths acting on one live position at once.
func TestLiveExitRace(t *testing.T) {
	t.Run("GIVEN an open live long WHEN an approved short, a dashboard close and a stop race THEN at most one sell is sent", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		id := openLiveLong(t, env)
		idea := apptestShort()
		req, ok := env.app.approvals.Submit(idea, domain.RiskDecision{TradeIdeaID: idea.ID, Approved: true, ProposedSize: domain.Float(100)})
		if !ok {
			t.Fatal("could not queue the approval")
		}
		ctx := context.Background()
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, run := range []func(){
			func() { _, _ = env.app.ApproveTrade(ctx, req.ID) },
			func() { _, _ = env.app.ClosePosition(ctx, id) },
			func() { env.app.checkLiveStops(ctx, map[string]float64{"BTC/EUR": 47_000}) },
		} {
			wg.Add(1)
			go func(run func()) {
				defer wg.Done()
				<-start
				run()
			}(run)
		}
		close(start)
		wg.Wait()

		if n := env.live.SellCount(); n != 1 {
			t.Fatalf("expected exactly one sell, got %d", n)
		}
	})
}

// apptestShort returns a BTC/EUR short idea.
func apptestShort() domain.TradeIdea {
	idea := domain.TradeIdea{ID: domain.NewID(), StrategyID: strategy.Basic1hID, Market: "BTC/EUR", Direction: domain.Short,
		Thesis: "exit", SupportingSignals: map[string]any{}, Confidence: 0.9, PositionSizingProposal: 0.2}
	return idea
}

// TestLiveStalePrices covers ticker snapshots that stopped updating.
func TestLiveStalePrices(t *testing.T) {
	t.Run("GIVEN a cached snapshot older than two strategy intervals WHEN a tick runs THEN the market is priced over REST instead", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		openLiveLong(t, env)
		env.app.cacheMarketSnapshot(domain.MarketSnapshot{Symbol: "BTC/EUR", Price: 47_000, Timestamp: time.Now().UTC().Add(-10 * time.Minute)})

		env.tick(t)

		if n := env.live.SellCount(); n != 0 {
			t.Fatalf("a stale price must not trigger the stop, got %d sells", n)
		}
		if price := env.app.pricesCopy()["BTC/EUR"]; price != 50_000 {
			t.Errorf("expected the REST price 50 000, got %v", price)
		}
	})

	t.Run("GIVEN a stale snapshot and no REST price WHEN a tick runs THEN stops are not evaluated on the stale price and the feed says so", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		openLiveLong(t, env)
		env.app.cacheMarketSnapshot(domain.MarketSnapshot{Symbol: "BTC/EUR", Price: 47_000, Timestamp: time.Now().UTC().Add(-10 * time.Minute)})
		env.market.RemovePrice("BTC/EUR")

		env.tick(t)
		env.tick(t)

		if n := env.live.SellCount(); n != 0 {
			t.Fatalf("a stale price must not trigger the stop, got %d sells", n)
		}
		if n := env.activityCount("stale"); n != 1 {
			t.Errorf("expected one (rate-limited) stale-price warning, got %d", n)
		}
	})
}

// TestLiveAlertsDoNotBlock covers webhook alerts sent around live bookkeeping.
func TestLiveAlertsDoNotBlock(t *testing.T) {
	t.Run("GIVEN a slow alert webhook WHEN a live stop sell fills THEN the tick finishes without waiting for the webhook", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "stop_loss") {
				<-release
			}
		}))
		t.Cleanup(srv.Close)
		env := newEnv(t, func(s *config.Settings) { liveSettings("fully_automated")(s); s.AlertWebhookURL = srv.URL }, seedPrices(40))
		env.boot(t)
		env.app.control.SetMarketLive("BTC/EUR", true)
		t.Cleanup(func() { close(release); env.app.Wait() })
		openLiveLong(t, env)
		env.market.SetPrice("BTC/EUR", 47_000)
		env.tick(t)
		env.live.FillLastOrder(47_000, 0.34)

		done := make(chan struct{})
		go func() {
			_, _ = env.app.strategyTick(context.Background())
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("the tick waited for the alert webhook")
		}
		if n := len(env.app.LivePositions()); n != 0 {
			t.Errorf("the stop fill must be recorded, got %+v", env.app.LivePositions())
		}
	})
}
