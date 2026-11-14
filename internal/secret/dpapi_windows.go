//go:build windows

package secret

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// entropy is mixed into every seal so other DPAPI users' blobs cannot be
// swapped in.
var entropy = []byte("LLMTradingBot settings v1")

// Default returns the DPAPI box bound to the current Windows user.
func Default() Box { return dpapi{} }

// dpapi seals with CryptProtectData for the current user.
type dpapi struct{}

// Seal encrypts plain for the current Windows user.
func (dpapi) Seal(plain []byte) ([]byte, error) {
	return dpapiCall(plain, true)
}

// Open decrypts bytes sealed by Seal under the same Windows user.
func (dpapi) Open(sealed []byte) ([]byte, error) {
	return dpapiCall(sealed, false)
}

// dpapiCall runs CryptProtectData (protect) or CryptUnprotectData and copies
// the result out of the LocalAlloc'd buffer Windows returns.
func dpapiCall(data []byte, protect bool) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("dpapi: empty input")
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	extra := windows.DataBlob{Size: uint32(len(entropy)), Data: &entropy[0]}
	var out windows.DataBlob
	var err error
	if protect {
		err = windows.CryptProtectData(&in, nil, &extra, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, &extra, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}
