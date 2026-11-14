//go:build windows

package secret

import (
	"bytes"
	"testing"
)

// TestDPAPIBox covers sealing secrets with the current Windows login (DPAPI).
func TestDPAPIBox(t *testing.T) {
	box := Default()
	plain := []byte("kraken-api-secret-value")

	t.Run("GIVEN a secret WHEN sealed and opened THEN the original comes back", func(t *testing.T) {
		sealed, err := box.Seal(plain)
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		opened, err := box.Open(sealed)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if !bytes.Equal(opened, plain) {
			t.Fatalf("round trip changed the secret")
		}
	})

	t.Run("GIVEN a sealed secret WHEN inspected THEN the plain text is not in it", func(t *testing.T) {
		sealed, err := box.Seal(plain)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(sealed, plain) {
			t.Fatal("sealed bytes must not contain the plain text")
		}
	})

	t.Run("GIVEN tampered bytes WHEN opened THEN an error is returned", func(t *testing.T) {
		sealed, err := box.Seal(plain)
		if err != nil {
			t.Fatal(err)
		}
		sealed[len(sealed)/2] ^= 0xFF
		if _, err := box.Open(sealed); err == nil {
			t.Fatal("expected tampered data to be rejected")
		}
	})
}
