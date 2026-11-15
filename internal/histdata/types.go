// Package histdata holds historical market data for backtesting: it imports
// exchange trade archives, validates them, and stores them locally as
// 30-second bars that a replay can stream in time order.
//
// It only reads public market data. It must never import the packages that
// can place orders or read secrets (internal/kraken's execution engine,
// internal/app, internal/secret, internal/server); a test enforces this.
package histdata

import (
	"iter"
	"time"
)

// BarInterval is the length of one stored bar. It matches the bot's strategy
// tick, so a bar's close is the last traded price the live bot would have
// seen at that tick.
const BarInterval = 30 * time.Second

// Trade is one executed trade from an exchange archive.
type Trade struct {
	Time   time.Time // execution time, UTC
	Price  float64   // in the quote currency
	Volume float64   // in the base asset
	Buy    bool      // true when the taker bought (lifted the ask)
	ID     int64     // exchange trade id; 0 when the source has none
}

// Bar summarises the trades of one BarInterval. Intervals with no trades have
// no bar: the reader of a series carries the previous close forward, as the
// live ticker does when nothing trades.
type Bar struct {
	Start  time.Time // start of the interval, UTC, aligned to BarInterval
	Open   float64   // first trade price
	High   float64
	Low    float64
	Close  float64 // last trade price
	Volume float64 // base asset traded
	Trades int     // number of trades

	// Taker-side totals. BuyNotional/BuyVolume is the average price buyers
	// paid in the interval and SellNotional/SellVolume the average price
	// sellers received; their difference estimates the spread actually
	// crossed. A side with no trades has zero volume.
	BuyVolume    float64
	BuyNotional  float64 // sum of price × volume over taker buys
	SellVolume   float64
	SellNotional float64 // sum of price × volume over taker sells
}

// Reader streams stored bars. Implementations read from the local cache only
// and never from the network.
type Reader interface {
	// Markets lists the markets the cache holds, sorted.
	Markets() []string
	// Coverage returns the first and last bar start stored for market; ok is
	// false when the cache has none.
	Coverage(market string) (first, last time.Time, ok bool)
	// Bars streams market's bars with from <= Start < to, in time order. A
	// read or integrity error ends the stream with a non-nil error.
	Bars(market string, from, to time.Time) iter.Seq2[Bar, error]
}
