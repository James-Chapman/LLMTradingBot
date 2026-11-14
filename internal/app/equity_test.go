package app

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/kraken"
	"github.com/James-Chapman/LLMTradingBot/internal/paper"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
)

// switchEnvironment saves a trading environment choice, as the dashboard does;
// it also applies after a restart.
func switchEnvironment(t *testing.T, env *testEnv, environment string) {
	t.Helper()
	if err := env.app.control.SetTradingEnvironment(environment); err != nil {
		t.Fatalf("switch to %s: %v", environment, err)
	}
}

// TestEquitySnapshotsPerEnvironment covers keeping paper and live account
// values apart in the equity snapshots, the restored paper cash and the chart.
func TestEquitySnapshotsPerEnvironment(t *testing.T) {
	ctx := context.Background()
	prices := map[string]float64{"BTC/EUR": 50000}

	t.Run("GIVEN snapshots written in the live environment WHEN the bot restarts in paper THEN paper cash comes from the last paper snapshot", func(t *testing.T) {
		env := newEnv(t, nil, func(repo *storage.Repository) {
			_ = repo.SaveEquitySnapshotFor(ctx, "paper", 321, 321, 0)
		})
		switchEnvironment(t, env, "live")
		env.restart(t, nil)
		env.live.Account = &kraken.AccountSnapshot{Cash: 9000, Equity: 9000}
		for i := 0; i < 3; i++ {
			if _, err := env.app.recordEquitySnapshot(ctx, prices); err != nil {
				t.Fatalf("record live snapshot: %v", err)
			}
		}

		switchEnvironment(t, env, "paper")
		env.restart(t, nil)

		if got := env.app.paper.Cash(); got != 321 {
			t.Fatalf("paper cash %v, want 321 (the live balance leaked in)", got)
		}
		if d := env.app.Dashboard(); d.Cash != "321.00" {
			t.Fatalf("dashboard cash %s, want 321.00", d.Cash)
		}
	})

	t.Run("GIVEN paper and live snapshots WHEN the dashboard is built THEN the equity history holds only the current environment", func(t *testing.T) {
		env := newEnv(t, nil, func(repo *storage.Repository) {
			_ = repo.SaveEquitySnapshotFor(ctx, "paper", 321, 321, 0)
			_ = repo.SaveEquitySnapshotFor(ctx, "live", 9000, 9000, 0)
		})

		for _, p := range env.app.Dashboard().EquityHistory {
			if p.Equity == 9000 {
				t.Fatalf("paper chart contains a live point: %+v", env.app.Dashboard().EquityHistory)
			}
		}

		switchEnvironment(t, env, "live")
		env.restart(t, nil)
		for _, p := range env.app.Dashboard().EquityHistory {
			if p.Equity == 321 {
				t.Fatalf("live chart contains a paper point: %+v", env.app.Dashboard().EquityHistory)
			}
		}
	})

	t.Run("GIVEN a paper chart WHEN the environment is switched to live THEN the next snapshots start the live series", func(t *testing.T) {
		env := newEnv(t, nil, func(repo *storage.Repository) {
			_ = repo.SaveEquitySnapshotFor(ctx, "paper", 321, 321, 0)
		})
		env.live.Account = &kraken.AccountSnapshot{Cash: 9000, Equity: 9000}

		switchEnvironment(t, env, "live")
		if _, err := env.app.recordEquitySnapshot(ctx, prices); err != nil {
			t.Fatalf("record: %v", err)
		}

		for _, p := range env.app.Dashboard().EquityHistory {
			if p.Equity == 321 {
				t.Fatalf("live chart still shows the paper series: %+v", env.app.Dashboard().EquityHistory)
			}
		}
		paperCash, err := env.repo.GetLatestCashFor(ctx, "paper")
		if err != nil || paperCash == nil || *paperCash != 321 {
			t.Fatalf("the live snapshot overwrote the paper cash: %v %v", paperCash, err)
		}
	})
}

// TestPaperShortMarginInApp covers the cash the app hands to the strategy and
// risk engine while a paper short is open.
func TestPaperShortMarginInApp(t *testing.T) {
	t.Run("GIVEN an open paper short WHEN an equity snapshot is recorded THEN the app's cash excludes the margin but the stored cash does not", func(t *testing.T) {
		ctx := context.Background()
		env := newEnv(t, nil, nil)
		env.app.paper.Execute(domain.ExecutionIntent{Market: "BTC/EUR", Direction: domain.Short, Size: 0.002}, 50000, paper.ExecuteOptions{})

		if _, err := env.app.recordEquitySnapshot(ctx, map[string]float64{"BTC/EUR": 50000}); err != nil {
			t.Fatalf("record: %v", err)
		}

		env.app.mu.Lock()
		cash := env.app.currentCash
		env.app.mu.Unlock()
		if cash != env.app.paper.AvailableCash() || cash >= env.app.paper.Cash() {
			t.Fatalf("app cash %v should be the available cash %v, below cash %v", cash, env.app.paper.AvailableCash(), env.app.paper.Cash())
		}
		stored, err := env.repo.GetLatestCashFor(ctx, "paper")
		if err != nil || stored == nil || *stored != env.app.paper.Cash() {
			t.Fatalf("stored cash %v, want the engine's cash %v so a restart restores it", stored, env.app.paper.Cash())
		}
	})
}

// TestResetPositionsDuringTrading covers an operator reset while orders are
// still being placed.
func TestResetPositionsDuringTrading(t *testing.T) {
	t.Run("GIVEN positions being opened while a reset runs WHEN the reset finishes THEN no position is silently deleted", func(t *testing.T) {
		ctx := context.Background()
		env := newEnv(t, func(s *config.Settings) { s.StartingCapital = 1_000_000 }, nil)
		for i := 0; i < 20; i++ {
			env.app.paper.Execute(domain.ExecutionIntent{Market: fmt.Sprintf("SEED%d/EUR", i), Direction: domain.Long, Size: 1}, 10, paper.ExecuteOptions{})
		}

		// Keep opening new positions until the reset has returned.
		var (
			mu     sync.Mutex
			opened []string
			wg     sync.WaitGroup
		)
		started := make(chan struct{})
		done := make(chan struct{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-done:
					return
				default:
				}
				_, id := env.app.paper.Execute(domain.ExecutionIntent{Market: fmt.Sprintf("NEW%d/EUR", i), Direction: domain.Long, Size: 1}, 10, paper.ExecuteOptions{})
				if id != "" {
					mu.Lock()
					opened = append(opened, id)
					mu.Unlock()
				}
				if i == 0 {
					close(started)
				}
			}
		}()
		<-started
		if _, err := env.app.ResetPositions(ctx); err != nil {
			t.Fatalf("reset: %v", err)
		}
		close(done)
		wg.Wait()

		outcomes, err := env.repo.GetSignalOutcomes(ctx, "", 10_000)
		if err != nil {
			t.Fatalf("outcomes: %v", err)
		}
		closed := map[string]bool{}
		for _, o := range outcomes {
			if o.PositionID != nil {
				closed[*o.PositionID] = true
			}
		}
		for _, id := range opened {
			if _, open := env.app.paper.Position(id); !open && !closed[id] {
				t.Fatalf("position %s was opened during the reset and vanished without a close (%d opened)", id, len(opened))
			}
		}
	})
}
