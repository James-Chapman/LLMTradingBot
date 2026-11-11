package storage

import (
	"context"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// sameDay reports whether two times fall on the same UTC calendar date.
func sameDay(a, b time.Time) bool {
	ay, am, ad := a.UTC().Date()
	by, bm, bd := b.UTC().Date()
	return ay == by && am == bm && ad == bd
}

// TestRiskStatePersistence ports the storage parts of tests/test_risk_state_persistence_bdd.py.
func TestRiskStatePersistence(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN a recorded loss WHEN the risk state is saved THEN a reload restores it", func(t *testing.T) {
		repo := newTestRepo(t)
		mustNoErr(t, repo.SaveRiskState(ctx, 30, 1000, domain.NowUTC()))

		state, err := repo.LoadRiskState(ctx)
		mustNoErr(t, err)
		if state == nil || !sameDay(state.LastResetDate, domain.NowUTC()) || state.DailyLoss != 30 {
			t.Fatalf("risk state not restored: %+v", state)
		}
	})

	t.Run("GIVEN no prior risk state WHEN it is loaded THEN nil is returned", func(t *testing.T) {
		repo := newTestRepo(t)
		state, err := repo.LoadRiskState(ctx)
		mustNoErr(t, err)
		if state != nil {
			t.Errorf("expected nil, got %+v", state)
		}
	})

	t.Run("GIVEN a risk state saved on a previous day WHEN compared with today THEN it is recognisably stale", func(t *testing.T) {
		repo := newTestRepo(t)
		mustNoErr(t, repo.SaveRiskState(ctx, 99, 500, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)))

		state, err := repo.LoadRiskState(ctx)
		mustNoErr(t, err)
		if state == nil || sameDay(state.LastResetDate, domain.NowUTC()) {
			t.Errorf("stale state must not match today: %+v", state)
		}
	})

	t.Run("GIVEN consecutive saves WHEN loaded THEN the latest values are returned", func(t *testing.T) {
		repo := newTestRepo(t)
		mustNoErr(t, repo.SaveRiskState(ctx, 10, 500, domain.NowUTC()))
		mustNoErr(t, repo.SaveRiskState(ctx, 25, 500, domain.NowUTC()))

		state, err := repo.LoadRiskState(ctx)
		mustNoErr(t, err)
		if state == nil || state.DailyLoss != 25 || state.DailyStartEquity != 500 {
			t.Errorf("expected latest values, got %+v", state)
		}
	})

	t.Run("GIVEN a corrupt reset date in the DB WHEN loaded THEN nil is returned like Python", func(t *testing.T) {
		repo := newTestRepo(t)
		_, err := repo.db.Exec("INSERT INTO risk_state (id, daily_loss, daily_start_equity, last_reset_date) VALUES (1, 5, 5, 'garbage')")
		mustNoErr(t, err)

		state, err := repo.LoadRiskState(ctx)
		mustNoErr(t, err)
		if state != nil {
			t.Errorf("corrupt date should be ignored, got %+v", state)
		}
	})
}

// TestNewsURLConstraint ports tests/unit/test_news_url_constraint_bdd.py.
func TestNewsURLConstraint(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN two news items with the same URL WHEN both are saved THEN both persist", func(t *testing.T) {
		repo := newTestRepo(t)
		url := "https://example.com/article-1"
		mustNoErr(t, repo.UpsertNewsItem(ctx, domain.NewsItem{ID: "hash-a", Source: "CoinDesk", Title: "Bitcoin rallies", Content: "BTC/EUR up 5%", URL: url}))
		mustNoErr(t, repo.UpsertNewsItem(ctx, domain.NewsItem{ID: "hash-b", Source: "CoinNews", Title: "Bitcoin rallies (syndicated)", Content: "BTC/EUR up 5%", URL: url}))

		var count int
		mustNoErr(t, repo.db.QueryRow("SELECT COUNT(*) FROM news_items WHERE url = ?", url).Scan(&count))
		if count != 2 {
			t.Errorf("expected 2 rows sharing the URL, got %d", count)
		}
	})

	t.Run("GIVEN two news items without URLs WHEN both are saved THEN both persist with NULL url", func(t *testing.T) {
		repo := newTestRepo(t)
		mustNoErr(t, repo.UpsertNewsItem(ctx, domain.NewsItem{ID: "hash-c", Source: "A", Title: "T1", Content: "C1"}))
		mustNoErr(t, repo.UpsertNewsItem(ctx, domain.NewsItem{ID: "hash-d", Source: "B", Title: "T2", Content: "C2"}))

		var count int
		mustNoErr(t, repo.db.QueryRow("SELECT COUNT(*) FROM news_items WHERE url IS NULL AND published_at IS NULL").Scan(&count))
		if count != 2 {
			t.Errorf("expected 2 rows with NULL url and published_at, got %d", count)
		}
	})
}
