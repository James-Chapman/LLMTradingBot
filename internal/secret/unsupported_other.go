//go:build !windows

package secret

// Default returns a box that refuses to store secrets: DPAPI is Windows-only.
func Default() Box { return unsupported{} }

// unsupported refuses every operation with ErrUnsupported.
type unsupported struct{}

// Seal always fails with ErrUnsupported.
func (unsupported) Seal([]byte) ([]byte, error) { return nil, ErrUnsupported }

// Open always fails with ErrUnsupported.
func (unsupported) Open([]byte) ([]byte, error) { return nil, ErrUnsupported }
