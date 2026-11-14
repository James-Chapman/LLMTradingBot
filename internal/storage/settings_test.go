package storage

import (
	"context"
	"testing"
)

// TestSettingsTable covers persisting dashboard-managed settings.
func TestSettingsTable(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN a fresh database WHEN settings are loaded THEN none are stored", func(t *testing.T) {
		repo := newTestRepo(t)
		values, err := repo.LoadSettings(ctx)
		mustNoErr(t, err)
		if len(values) != 0 {
			t.Fatalf("expected no stored settings, got %v", values)
		}
	})

	t.Run("GIVEN saved settings WHEN reloaded THEN the values come back and later saves overwrite them", func(t *testing.T) {
		repo := newTestRepo(t)
		mustNoErr(t, repo.SaveSettings(ctx, map[string]string{"MIN_SIGNAL_CONFIDENCE": "0.7", "FIXED_MARKETS": `["BTC/EUR"]`}))
		mustNoErr(t, repo.SaveSettings(ctx, map[string]string{"MIN_SIGNAL_CONFIDENCE": "0.9"}))

		values, err := repo.LoadSettings(ctx)
		mustNoErr(t, err)

		if values["MIN_SIGNAL_CONFIDENCE"] != "0.9" || values["FIXED_MARKETS"] != `["BTC/EUR"]` {
			t.Fatalf("unexpected stored settings %v", values)
		}
	})
}
