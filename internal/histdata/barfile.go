package histdata

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"
)

// Bar file format, version 1 (all integers little-endian):
//
//	header, 64 bytes
//	  0  [8]byte  magic "HISTBARS"
//	  8  uint16   format version (1)
//	 10  uint16   record size in bytes (88)
//	 12  uint32   bar interval in seconds (30)
//	 16  int64    month start, UNIX seconds (UTC, first day 00:00)
//	 24  uint64   number of records
//	 32  [32]byte SHA-256 of all record bytes
//	records, 88 bytes each, sorted by start, starts unique and inside the month
//	  0  int64    bar start, UNIX seconds
//	  8  float64  open      (IEEE-754 bits, so values round-trip exactly)
//	 16  float64  high
//	 24  float64  low
//	 32  float64  close
//	 40  float64  volume
//	 48  int64    trades
//	 56  float64  buy volume
//	 64  float64  buy notional
//	 72  float64  sell volume
//	 80  float64  sell notional
const (
	barMagic       = "HISTBARS"
	barFileVersion = 1
	barHeaderSize  = 64
	barRecordSize  = 88
)

// encodeBarFile serialises one month of bars.
func encodeBarFile(month time.Time, bars []Bar) []byte {
	records := make([]byte, len(bars)*barRecordSize)
	for i, b := range bars {
		r := records[i*barRecordSize:]
		le := binary.LittleEndian
		le.PutUint64(r[0:], uint64(b.Start.Unix()))
		for j, v := range []float64{b.Open, b.High, b.Low, b.Close, b.Volume} {
			le.PutUint64(r[8+8*j:], math.Float64bits(v))
		}
		le.PutUint64(r[48:], uint64(int64(b.Trades)))
		for j, v := range []float64{b.BuyVolume, b.BuyNotional, b.SellVolume, b.SellNotional} {
			le.PutUint64(r[56+8*j:], math.Float64bits(v))
		}
	}
	sum := sha256.Sum256(records)
	h := make([]byte, barHeaderSize)
	copy(h, barMagic)
	binary.LittleEndian.PutUint16(h[8:], barFileVersion)
	binary.LittleEndian.PutUint16(h[10:], barRecordSize)
	binary.LittleEndian.PutUint32(h[12:], uint32(BarInterval/time.Second))
	binary.LittleEndian.PutUint64(h[16:], uint64(month.Unix()))
	binary.LittleEndian.PutUint64(h[24:], uint64(len(bars)))
	copy(h[32:], sum[:])
	return append(h, records...)
}

// decodeBarFile parses and verifies one month file: header, size, checksum,
// and that bars are aligned, strictly increasing and inside the month.
func decodeBarFile(data []byte) (time.Time, []Bar, error) {
	if len(data) < barHeaderSize || string(data[:8]) != barMagic {
		return time.Time{}, nil, errors.New("not a bar file (bad header)")
	}
	le := binary.LittleEndian
	if v := le.Uint16(data[8:]); v != barFileVersion {
		return time.Time{}, nil, fmt.Errorf("unsupported bar file version %d", v)
	}
	if rs := le.Uint16(data[10:]); rs != barRecordSize {
		return time.Time{}, nil, fmt.Errorf("record size %d, expected %d", rs, barRecordSize)
	}
	if iv := le.Uint32(data[12:]); time.Duration(iv)*time.Second != BarInterval {
		return time.Time{}, nil, fmt.Errorf("bar interval %ds, expected %v", iv, BarInterval)
	}
	month := time.Unix(int64(le.Uint64(data[16:])), 0).UTC()
	count := le.Uint64(data[24:])
	records := data[barHeaderSize:]
	if uint64(len(records)) != count*barRecordSize {
		return time.Time{}, nil, fmt.Errorf("file holds %d record bytes, header says %d records", len(records), count)
	}
	if sum := sha256.Sum256(records); !bytes.Equal(sum[:], data[32:64]) {
		return time.Time{}, nil, errors.New("checksum mismatch (file is corrupt)")
	}
	end := month.AddDate(0, 1, 0)
	bars := make([]Bar, count)
	for i := range bars {
		r := records[i*barRecordSize:]
		f := func(off int) float64 { return math.Float64frombits(le.Uint64(r[off:])) }
		bars[i] = Bar{
			Start: time.Unix(int64(le.Uint64(r[0:])), 0).UTC(),
			Open:  f(8), High: f(16), Low: f(24), Close: f(32), Volume: f(40),
			Trades:    int(int64(le.Uint64(r[48:]))),
			BuyVolume: f(56), BuyNotional: f(64), SellVolume: f(72), SellNotional: f(80),
		}
		s := bars[i].Start
		if s.Before(month) || !s.Before(end) || !s.Truncate(BarInterval).Equal(s) || (i > 0 && !s.After(bars[i-1].Start)) {
			return time.Time{}, nil, fmt.Errorf("record %d: bar start %v is misaligned, out of order or outside %s", i, s, month.Format("2006-01"))
		}
	}
	return month, bars, nil
}

// sha256Hex returns the hex SHA-256 of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// writeFileAtomic writes path through a temporary file in the same directory
// and renames it into place, so readers see the old file or the new one,
// never a partial one. On any failure the temporary file is removed.
func writeFileAtomic(path string, write func(w io.Writer) error) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if err = write(tmp); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// writeBytesAtomic writes data to path atomically.
func writeBytesAtomic(path string, data []byte) error {
	return writeFileAtomic(path, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}
