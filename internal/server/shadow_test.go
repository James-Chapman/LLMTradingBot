package server

import (
	"net/http"
	"testing"
)

// TestShadowReviewsEndpoint covers GET /api/shadow-reviews.
func TestShadowReviewsEndpoint(t *testing.T) {
	h := newHarness(t, harnessConfig{})

	t.Run("GIVEN no shadow reviews yet WHEN the endpoint is read THEN it returns the setting and empty lists", func(t *testing.T) {
		rec := h.do(http.MethodGet, "/api/shadow-reviews", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		body := decodeObject(t, rec)
		requireKeys(t, body, "enabled", "veto_threshold", "min_scored_vetoes", "strategies", "recent")
		if body["enabled"] != true {
			t.Fatalf("enabled %v, want the default true", body["enabled"])
		}
		for _, key := range []string{"strategies", "recent"} {
			if list, ok := body[key].([]any); !ok || len(list) != 0 {
				t.Fatalf("%s = %v, want an empty list", key, body[key])
			}
		}
	})
}
