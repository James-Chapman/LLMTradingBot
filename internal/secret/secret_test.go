package secret

import (
	"strings"
	"testing"
)

// TestEncodedSecrets covers the text form secrets take in the settings table.
func TestEncodedSecrets(t *testing.T) {
	box := Reversible() // test box: no OS dependency

	t.Run("GIVEN a secret WHEN encoded THEN it is marked sealed and decodes back", func(t *testing.T) {
		stored, err := Encode(box, "my-api-key")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(stored, SealedPrefix) || strings.Contains(stored, "my-api-key") {
			t.Fatalf("stored form must be sealed and hide the value, got %q", stored)
		}
		got, err := Decode(box, stored)
		if err != nil || got != "my-api-key" {
			t.Fatalf("decode: %q, %v", got, err)
		}
	})

	t.Run("GIVEN an empty secret WHEN encoded THEN it stays empty (not set)", func(t *testing.T) {
		stored, err := Encode(box, "")
		if err != nil || stored != "" {
			t.Fatalf("expected empty stored value, got %q, %v", stored, err)
		}
		if got, err := Decode(box, ""); err != nil || got != "" {
			t.Fatalf("expected empty decode, got %q, %v", got, err)
		}
	})

	t.Run("GIVEN a stored value without the sealed marker WHEN decoded THEN it is refused", func(t *testing.T) {
		if _, err := Decode(box, "plain-text-key"); err == nil {
			t.Fatal("unsealed secrets must be refused")
		}
	})
}
