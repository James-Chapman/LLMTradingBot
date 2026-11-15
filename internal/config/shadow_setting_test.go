package config

import "testing"

// TestShadowReviewSetting covers the LLM_SHADOW_REVIEW setting.
func TestShadowReviewSetting(t *testing.T) {
	t.Run("GIVEN default settings WHEN read THEN shadow reviews are on", func(t *testing.T) {
		if !Defaults().LLMShadowReview {
			t.Fatal("LLM_SHADOW_REVIEW should default to true")
		}
	})

	t.Run("GIVEN the settings fields WHEN listed THEN LLM_SHADOW_REVIEW is a live true/false signal setting", func(t *testing.T) {
		for _, f := range Fields() {
			if f.Key != "LLM_SHADOW_REVIEW" {
				continue
			}
			if f.Kind != KindBool || f.Group != GroupSignals || !f.Live {
				t.Fatalf("field %+v", f)
			}
			return
		}
		t.Fatal("LLM_SHADOW_REVIEW is not a settings field")
	})
}
