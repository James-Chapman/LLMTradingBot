package app

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
)

// fakeRanker returns fixed pairs (or an error) and records each call.
type fakeRanker struct {
	pairs []string
	err   error
	calls []string // quote currency per call
	n     int
}

// TopPairsByVolume records the request and returns the canned result.
func (f *fakeRanker) TopPairsByVolume(_ context.Context, quote string, n int) ([]string, error) {
	f.calls = append(f.calls, quote)
	f.n = n
	return f.pairs, f.err
}

// openTestRepo opens an empty database in a temp directory.
func openTestRepo(t *testing.T) *storage.Repository {
	t.Helper()
	repo, err := storage.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	return repo
}

// TestLiveDefaultMarkets covers choosing the default markets from Kraken at start-up.
func TestLiveDefaultMarkets(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN no markets saved WHEN the bot starts THEN the biggest pairs in the base currency are used", func(t *testing.T) {
		repo := openTestRepo(t)
		s := config.Defaults()
		ranker := &fakeRanker{pairs: []string{"BTC/EUR", "SOL/EUR"}}

		used, err := UseLiveDefaultMarkets(ctx, repo, &s, ranker)

		if err != nil || !used {
			t.Fatalf("expected the live list to be used, got %v, %v", used, err)
		}
		if !slices.Equal(s.FixedMarkets, []string{"BTC/EUR", "SOL/EUR"}) {
			t.Fatalf("markets not replaced: %v", s.FixedMarkets)
		}
		if len(ranker.calls) != 1 || ranker.calls[0] != "EUR" || ranker.n != config.DefaultMarketCount {
			t.Fatalf("expected one request for %d EUR pairs, got %v n=%d", config.DefaultMarketCount, ranker.calls, ranker.n)
		}
	})

	t.Run("GIVEN markets saved in the dashboard WHEN the bot starts THEN they are kept and Kraken is not asked", func(t *testing.T) {
		repo := openTestRepo(t)
		if err := repo.SaveSettings(ctx, map[string]string{"FIXED_MARKETS": `["ADA/EUR"]`}); err != nil {
			t.Fatal(err)
		}
		s := config.Defaults()
		s.FixedMarkets = []string{"ADA/EUR"}
		ranker := &fakeRanker{pairs: []string{"BTC/EUR"}}

		used, err := UseLiveDefaultMarkets(ctx, repo, &s, ranker)

		if err != nil || used || len(ranker.calls) != 0 || !slices.Equal(s.FixedMarkets, []string{"ADA/EUR"}) {
			t.Fatalf("saved markets must win: used=%v err=%v calls=%v markets=%v", used, err, ranker.calls, s.FixedMarkets)
		}
	})

	t.Run("GIVEN Kraken cannot be reached WHEN the bot starts THEN the built-in list stays and the error is reported", func(t *testing.T) {
		repo := openTestRepo(t)
		s := config.Defaults()
		builtIn := slices.Clone(s.FixedMarkets)
		ranker := &fakeRanker{err: errors.New("network down")}

		used, err := UseLiveDefaultMarkets(ctx, repo, &s, ranker)

		if err == nil || used || !slices.Equal(s.FixedMarkets, builtIn) {
			t.Fatalf("expected the built-in list and an error, got used=%v err=%v", used, err)
		}
	})

	t.Run("GIVEN Kraken returns no pairs WHEN the bot starts THEN the built-in list stays", func(t *testing.T) {
		repo := openTestRepo(t)
		s := config.Defaults()
		builtIn := slices.Clone(s.FixedMarkets)

		used, err := UseLiveDefaultMarkets(ctx, repo, &s, &fakeRanker{})

		if err == nil || used || !slices.Equal(s.FixedMarkets, builtIn) {
			t.Fatalf("an empty ranking must not empty the market list, got used=%v err=%v %v", used, err, s.FixedMarkets)
		}
	})
}
