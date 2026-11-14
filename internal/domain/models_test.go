package domain

import (
	"regexp"
	"testing"
)

// TestNewID covers UUID format and uniqueness.
func TestNewID(t *testing.T) {
	uuidV4 := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

	t.Run("GIVEN a new id WHEN generated THEN it is a v4 UUID", func(t *testing.T) {
		if id := NewID(); !uuidV4.MatchString(id) {
			t.Fatalf("not a v4 UUID: %s", id)
		}
	})

	t.Run("GIVEN many ids WHEN generated THEN none collide", func(t *testing.T) {
		seen := map[string]bool{}
		for range 1000 {
			id := NewID()
			if seen[id] {
				t.Fatalf("duplicate id %s", id)
			}
			seen[id] = true
		}
	})
}
