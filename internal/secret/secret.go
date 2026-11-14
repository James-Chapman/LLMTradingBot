// Package secret seals API keys before they are written to the settings
// table. On Windows the key used is the current user's login (DPAPI), so a
// copied database cannot be decrypted by another account or machine.
package secret

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// SealedPrefix marks a stored value as sealed; the rest is base64.
const SealedPrefix = "sealed:"

// ErrUnsupported is returned where no secret store is available.
var ErrUnsupported = errors.New("storing API keys needs Windows (DPAPI); not supported on this system")

// Box seals and opens secret bytes.
type Box interface {
	Seal(plain []byte) ([]byte, error)
	Open(sealed []byte) ([]byte, error)
}

// Encode seals a secret into its stored text form. An empty secret stays
// empty, meaning "not set".
func Encode(box Box, plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	sealed, err := box.Seal([]byte(plain))
	if err != nil {
		return "", err
	}
	return SealedPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decode opens a stored secret. Empty means not set; anything without the
// sealed marker is refused so a plain-text key is never trusted.
func Decode(box Box, stored string) (string, error) {
	if stored == "" {
		return "", nil
	}
	encoded, ok := strings.CutPrefix(stored, SealedPrefix)
	if !ok {
		return "", errors.New("stored secret is not sealed")
	}
	sealed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("stored secret is corrupt: %w", err)
	}
	plain, err := box.Open(sealed)
	if err != nil {
		return "", fmt.Errorf("cannot decrypt stored secret (was the database copied from another Windows account?): %w", err)
	}
	return string(plain), nil
}

// Reversible returns a box that only obscures bytes. It is NOT secure and
// exists so tests run on every OS without DPAPI.
func Reversible() Box { return reversible{} }

// reversible XORs every byte with a constant.
type reversible struct{}

// Seal obscures plain.
func (reversible) Seal(plain []byte) ([]byte, error) { return xor(plain), nil }

// Open reverses Seal.
func (reversible) Open(sealed []byte) ([]byte, error) { return xor(sealed), nil }

// xor returns a copy of b with every byte XORed with 0x5A.
func xor(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		out[i] = c ^ 0x5A
	}
	return out
}
