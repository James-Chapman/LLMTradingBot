package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/app/apptest"
	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/kraken"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
	"github.com/James-Chapman/LLMTradingBot/internal/strategy"
)

// liveSettings returns a mutator for the live environment in mode with a 5% stop-loss.
func liveSettings(mode string) func(*config.Settings) {
	return func(s *config.Settings) {
		s.TradingMode = mode
		s.TradingEnvironment = string(domain.EnvLive)
		s.StopLossPct = 0.05
	}
}

// newLiveEnv builds a booted live-environment app with BTC/EUR toggled live.
func newLiveEnv(t *testing.T, mode string) *testEnv {
	t.Helper()
	env := newEnv(t, liveSettings(mode), seedPrices(40))
	env.boot(t)
	env.app.control.SetMarketLive("BTC/EUR", true)
	return env
}

// btcIdeas returns a strategy idea function emitting the queued directions,
// one per evaluation, then nothing.
func btcIdeas(directions ...domain.Direction) func(strategy.Input) []domain.TradeIdea {
	queue := append([]domain.Direction(nil), directions...)
	return func(strategy.Input) []domain.TradeIdea {
		if len(queue) == 0 {
			return nil
		}
		d := queue[0]
		queue = queue[1:]
		if d == "" {
			return nil
		}
		return []domain.TradeIdea{apptest.Idea(strategy.Basic1hID, "BTC/EUR", d, 0.9)}
	}
}

// alwaysLong emits the same BTC/EUR long on every evaluation.
func alwaysLong(strategy.Input) []domain.TradeIdea {
	return []domain.TradeIdea{apptest.Idea(strategy.Basic1hID, "BTC/EUR", domain.Long, 0.9)}
}

// lastIntent returns the most recent live intent.
func lastIntent(t *testing.T, live *apptest.Live) domain.ExecutionIntent {
	t.Helper()
	if len(live.Executed) == 0 {
		t.Fatal("no live order was sent")
	}
	return live.Executed[len(live.Executed)-1]
}

// statusCode returns the HTTP status of a *StatusError, or 0.
func statusCode(err error) int {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Status
	}
	return 0
}

// TestLiveEntriesAreSentOnce covers the once-only guarantee for live entries.
func TestLiveEntriesAreSentOnce(t *testing.T) {
	t.Run("GIVEN fully automated live trading WHEN the same long signal persists over several ticks THEN exactly one real order is sent", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		env.strategy.Ideas = alwaysLong

		env.tick(t)
		env.tick(t)
		env.live.FillLastOrder(50_000, 0.36) // the entry fills between ticks
		env.tick(t)
		env.tick(t)

		if n := env.live.ExecutedCount(); n != 1 {
			t.Fatalf("expected exactly one live order, got %d", n)
		}
		positions := env.app.LivePositions()
		if len(positions) != 1 || positions[0].Status != "open" || positions[0].Size != 0.002 || positions[0].Environment != "live" {
			t.Fatalf("expected one open live position of the executed size, got %+v", positions)
		}
		if len(env.app.paper.OpenPositions()) != 0 {
			t.Errorf("a live order must not open a paper position")
		}
	})

	t.Run("GIVEN semi-automated live trading WHEN an approved live order has filled THEN the persisting signal is not queued again", func(t *testing.T) {
		env := newLiveEnv(t, "semi_automated")
		env.strategy.Ideas = alwaysLong
		env.tick(t)
		pending := env.app.PendingApprovals()
		if len(pending) != 1 {
			t.Fatalf("expected one approval, got %d", len(pending))
		}
		if _, err := env.app.ApproveTrade(context.Background(), pending[0].ID); err != nil {
			t.Fatalf("ApproveTrade: %v", err)
		}

		env.tick(t) // entry still pending
		env.live.FillLastOrder(50_000, 0.36)
		env.tick(t) // entry filled

		if n := len(env.app.PendingApprovals()); n != 0 {
			t.Errorf("the open live position must stop the signal being queued again, got %d approvals", n)
		}
		if n := env.live.ExecutedCount(); n != 1 {
			t.Errorf("expected one live order, got %d", n)
		}
	})

	t.Run("GIVEN a live entry that is still pending WHEN the bot restarts THEN the restored position blocks a duplicate entry", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		env.strategy.Ideas = alwaysLong
		env.tick(t)

		env.restart(t, liveSettings("fully_automated"))
		env.boot(t)
		env.app.control.SetMarketLive("BTC/EUR", true)
		env.tick(t)

		if n := env.live.ExecutedCount(); n != 1 {
			t.Fatalf("a restored live position must block a second entry, got %d orders", n)
		}
		if len(env.app.LivePositions()) != 1 {
			t.Errorf("the live position was not restored: %+v", env.app.LivePositions())
		}
	})

	t.Run("GIVEN an entry fill recorded on the order but not on the position WHEN the bot restarts THEN the position is brought up to date", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		env.strategy.Ideas = btcIdeas(domain.Long)
		env.tick(t)
		order := env.live.LastOrder()
		// What the real engine writes: the pending order, then its fill - and then the bot crashed.
		ctx := context.Background()
		if err := env.repo.SaveOrder(ctx, order, storage.SaveOrderOptions{Environment: "live", ClientOrderID: order.ID}); err != nil {
			t.Fatal(err)
		}
		if err := env.repo.UpdateOrderExecution(ctx, storage.OrderExecution{OrderID: order.ID, Status: "filled", ExecutedSize: 0.0019, AvgPrice: 50_100, Fee: 0.3}); err != nil {
			t.Fatal(err)
		}

		env.restart(t, liveSettings("fully_automated"))

		positions := env.app.LivePositions()
		if len(positions) != 1 || positions[0].Status != "open" || positions[0].Size != 0.0019 || positions[0].AvgPrice != 50_100 {
			t.Fatalf("the recorded fill must be applied at start-up, got %+v", positions)
		}
	})

	t.Run("GIVEN a live entry WHEN Kraken cancels it with part executed THEN the position holds the executed size and price", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		env.strategy.Ideas = btcIdeas(domain.Long)
		env.tick(t)
		order := env.live.LastOrder()
		env.live.QueueUpdate(kraken.OrderUpdate{
			OrderID: order.ID, Status: "filled", KrakenStatus: "canceled", ExecutedSize: 0.0005, AvgPrice: 50_100, Fee: 0.1,
		})
		env.tick(t)

		positions := env.app.LivePositions()
		if len(positions) != 1 || positions[0].Size != 0.0005 || positions[0].AvgPrice != 50_100 || positions[0].Status != "open" {
			t.Fatalf("expected a position of the executed size, got %+v", positions)
		}
	})

	t.Run("GIVEN a live entry WHEN Kraken cancels it with nothing executed THEN the position is removed and a new entry may follow", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		env.strategy.Ideas = btcIdeas(domain.Long, "", domain.Long)
		env.tick(t)
		order := env.live.LastOrder()
		env.live.QueueUpdate(kraken.OrderUpdate{OrderID: order.ID, Status: "canceled", KrakenStatus: "canceled", AvgPrice: 50_000})
		env.tick(t)

		if n := len(env.app.LivePositions()); n != 0 {
			t.Fatalf("a cancelled entry with no execution must leave no position, got %+v", env.app.LivePositions())
		}
		env.tick(t)
		if n := env.live.ExecutedCount(); n != 2 {
			t.Errorf("after the cancelled entry a new entry may be sent, got %d orders", n)
		}
	})
}

// TestLiveExits covers selling a tracked live position.
func TestLiveExits(t *testing.T) {
	t.Run("GIVEN a filled live long WHEN the price falls past the stop-loss THEN exactly one live sell is sent and the loss counts toward the daily limit", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		env.strategy.Ideas = btcIdeas(domain.Long)
		env.tick(t)
		env.live.FillLastOrder(50_000, 0.36)
		env.tick(t)

		env.market.SetPrice("BTC/EUR", 47_000)
		env.tick(t)
		env.tick(t) // the sell is still pending: it must not be sent again

		if n := env.live.ExecutedCount(); n != 2 {
			t.Fatalf("expected the entry and exactly one stop sell, got %d orders", n)
		}
		sell := lastIntent(t, env.live)
		if sell.Direction != domain.Short || sell.Size != 0.002 || sell.ApprovalRequestID != "stop_loss" {
			t.Fatalf("expected a stop-loss sell of the held size, got %+v", sell)
		}

		env.live.FillLastOrder(47_000, 0.34)
		env.tick(t)

		if n := len(env.app.LivePositions()); n != 0 {
			t.Fatalf("the filled stop must remove the position, got %+v", env.app.LivePositions())
		}
		wantLoss := 3_000*0.002 + 0.36 + 0.34
		if got := env.app.risk.Snapshot().DailyLoss; got < wantLoss-1e-9 || got > wantLoss+1e-9 {
			t.Errorf("expected daily loss %.4f from the live stop, got %.4f", wantLoss, got)
		}
		trades, err := env.repo.GetClosedTrades(context.Background(), 10)
		if err != nil || len(trades) != 1 || trades[0].PnL >= 0 || trades[0].ExitReason != "stop_loss" {
			t.Errorf("expected one losing stop-loss trade, got %+v (%v)", trades, err)
		}
	})

	t.Run("GIVEN a tracked live long WHEN a short signal fires THEN the tracked size is sold once, not the signal's size", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		env.strategy.Ideas = btcIdeas(domain.Long, domain.Short, domain.Short)
		env.tick(t)
		order := env.live.LastOrder()
		env.live.QueueUpdate(kraken.OrderUpdate{OrderID: order.ID, Status: "filled", KrakenStatus: "closed", ExecutedSize: 0.0013, AvgPrice: 50_000, Fee: 0.2})
		env.tick(t) // fill reconciled, short signal sells
		env.tick(t) // second short while the sell is pending

		if n := env.live.ExecutedCount(); n != 2 {
			t.Fatalf("expected the entry and one sell, got %d orders", n)
		}
		if sell := lastIntent(t, env.live); sell.Direction != domain.Short || sell.Size != 0.0013 {
			t.Fatalf("expected a sell of the tracked 0.0013, got %+v", sell)
		}

		env.market.SetPrice("BTC/EUR", 52_000)
		env.live.FillLastOrder(52_000, 0.2)
		env.tick(t)
		trades, _ := env.repo.GetClosedTrades(context.Background(), 10)
		if len(env.app.LivePositions()) != 0 || len(trades) != 1 || trades[0].PnL <= 0 {
			t.Errorf("expected the exit to close the position at a profit, got %+v / %+v", env.app.LivePositions(), trades)
		}
	})

	t.Run("GIVEN no tracked live long WHEN a short signal fires on a live market THEN no order is sent", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		env.strategy.Ideas = btcIdeas(domain.Short)
		env.tick(t)

		if n := env.live.ExecutedCount(); n != 0 {
			t.Fatalf("a spot account cannot short: expected no order, got %d", n)
		}
		if !env.activityContains("no live position to sell") {
			t.Errorf("expected the skipped short in the activity feed")
		}
	})

	t.Run("GIVEN a filled live long WHEN it is closed from the dashboard THEN one real sell of the held size is sent", func(t *testing.T) {
		env := newLiveEnv(t, "fully_automated")
		env.strategy.Ideas = btcIdeas(domain.Long)
		env.tick(t)
		env.live.FillLastOrder(50_000, 0.36)
		env.tick(t)
		id := env.app.LivePositions()[0].PositionID

		result, err := env.app.ClosePosition(context.Background(), id[:8])
		if err != nil {
			t.Fatalf("ClosePosition: %v", err)
		}
		if result["position_id"] != id || result["order_status"] != "pending" {
			t.Errorf("unexpected close result %v", result)
		}
		if _, err := env.app.ClosePosition(context.Background(), id); statusCode(err) != http.StatusConflict {
			t.Errorf("a second close while the sell is pending must be refused, got %v", err)
		}
		if n := env.live.ExecutedCount(); n != 2 || lastIntent(t, env.live).Size != 0.002 {
			t.Errorf("expected one manual sell of the held size, got %d orders", n)
		}
	})

	t.Run("GIVEN a paper position and a live position on the same market WHEN a live short closes the live one THEN the paper position is untouched", func(t *testing.T) {
		env := newEnv(t, func(s *config.Settings) { withKrakenKeys(s); s.TradingMode = "fully_automated" }, seedPrices(40))
		env.boot(t)
		env.app.control.SetMarketLive("BTC/EUR", true)
		env.strategy.Ideas = btcIdeas(domain.Long, domain.Long, "", domain.Short)
		env.tick(t) // paper environment: the live-toggled market trades on paper
		if _, err := env.app.SetTradingEnvironment("live", LiveConfirmation); err != nil {
			t.Fatalf("SetTradingEnvironment: %v", err)
		}
		env.tick(t) // live environment: a live entry despite the paper long
		env.live.FillLastOrder(50_000, 0.36)
		env.tick(t)
		env.tick(t) // the short exits the live position only

		if n := len(env.app.paper.OpenPositions()); n != 1 {
			t.Fatalf("the paper position must stay open, got %d", n)
		}
		if n := env.live.ExecutedCount(); n != 2 || lastIntent(t, env.live).Direction != domain.Short {
			t.Fatalf("expected a live entry and a live sell, got %d orders", n)
		}
	})
}

// TestApprovalRecheck covers re-running the risk checks when an approval is executed.
func TestApprovalRecheck(t *testing.T) {
	t.Run("GIVEN a queued approval WHEN the daily loss limit is reached before it is approved THEN it is refused and nothing executes", func(t *testing.T) {
		env := newEnv(t, func(s *config.Settings) { s.TradingMode = "semi_automated" }, seedPrices(40))
		env.strategy.Ideas = btcIdeas(domain.Long)
		env.boot(t)
		env.tick(t)
		pending := env.app.PendingApprovals()
		if len(pending) != 1 {
			t.Fatalf("expected one approval, got %d", len(pending))
		}
		env.app.recordTradeResult(-100) // a loss elsewhere uses up the day's limit

		_, err := env.app.ApproveTrade(context.Background(), pending[0].ID)

		if statusCode(err) != http.StatusConflict || !strings.Contains(err.Error(), "Daily loss limit") {
			t.Fatalf("expected a 409 naming the daily loss limit, got %v", err)
		}
		if n := len(env.app.paper.OpenPositions()); n != 0 {
			t.Errorf("a refused approval must not execute, got %d positions", n)
		}
		if n := len(env.app.PendingApprovals()); n != 0 {
			t.Errorf("a refused approval is removed from the queue, got %d", n)
		}
	})

	t.Run("GIVEN a queued approval WHEN there is no price at approval time THEN it is not executed and stays pending", func(t *testing.T) {
		env := newEnv(t, func(s *config.Settings) { s.TradingMode = "semi_automated" }, seedPrices(40))
		env.strategy.Ideas = btcIdeas(domain.Long)
		env.boot(t)
		env.tick(t)
		id := env.app.PendingApprovals()[0].ID
		env.app.mu.Lock()
		delete(env.app.currentPrices, "BTC/EUR")
		env.app.mu.Unlock()

		_, err := env.app.ApproveTrade(context.Background(), id)

		if statusCode(err) != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 without a price, got %v", err)
		}
		if n := len(env.app.PendingApprovals()); n != 1 {
			t.Fatalf("the approval must stay pending so it can be retried, got %d", n)
		}
	})

	t.Run("GIVEN a short approval on a live market WHEN there is no live position to sell THEN it is refused without sending an order", func(t *testing.T) {
		env := newLiveEnv(t, "semi_automated")
		env.tick(t) // prices only: the strategy emits nothing
		idea := apptest.Idea(strategy.Basic1hID, "BTC/EUR", domain.Short, 0.9)
		req, ok := env.app.approvals.Submit(idea, domain.RiskDecision{TradeIdeaID: idea.ID, Approved: true, ProposedSize: domain.Float(100)})
		if !ok {
			t.Fatal("could not queue the approval")
		}

		_, err := env.app.ApproveTrade(context.Background(), req.ID)

		if statusCode(err) != http.StatusConflict || env.live.ExecutedCount() != 0 {
			t.Fatalf("expected a 409 and no order, got %v after %d orders", err, env.live.ExecutedCount())
		}
	})
}

// TestDailyLossAlertOncePerDay covers the daily_loss_limit_hit webhook.
func TestDailyLossAlertOncePerDay(t *testing.T) {
	t.Run("GIVEN the daily loss limit is reached WHEN signals keep being refused THEN the webhook fires once that day", func(t *testing.T) {
		var alerts atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			buf := make([]byte, 4096)
			n, _ := r.Body.Read(buf)
			if strings.Contains(string(buf[:n]), "daily_loss_limit_hit") {
				alerts.Add(1)
			}
		}))
		t.Cleanup(srv.Close)
		env := newEnv(t, func(s *config.Settings) { s.TradingMode = "fully_automated"; s.AlertWebhookURL = srv.URL }, seedPrices(40))
		env.strategy.Ideas = func(strategy.Input) []domain.TradeIdea {
			return []domain.TradeIdea{
				apptest.Idea(strategy.Basic1hID, "BTC/EUR", domain.Long, 0.9),
				apptest.Idea(strategy.Basic1hID, "ETH/EUR", domain.Long, 0.9),
			}
		}
		env.boot(t)
		env.app.recordTradeResult(-100)

		env.tick(t)
		env.tick(t)
		env.tick(t)
		env.app.Wait() // the alert is sent in the background

		if n := alerts.Load(); n != 1 {
			t.Errorf("expected one daily-loss alert, got %d", n)
		}
	})
}
