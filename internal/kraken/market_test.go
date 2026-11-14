package kraken

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

const assetPairsBody = `{"error":[],"result":{
	"XXBTZEUR":{"altname":"XBTEUR","wsname":"XBT/EUR","base":"XXBT","quote":"ZEUR"},
	"XETHZEUR":{"altname":"ETHEUR","wsname":"ETH/EUR","base":"XETH","quote":"ZEUR"},
	"XDGEUR":{"altname":"XDGEUR","wsname":"XDG/EUR","base":"XXDG","quote":"ZEUR"}}}`

// newTestMarketAdapter builds a MarketAdapter pointed at a fake Kraken server.
func newTestMarketAdapter(f *fakeKraken, sleeps *[]time.Duration) *MarketAdapter {
	var discard []time.Duration
	if sleeps == nil {
		sleeps = &discard
	}
	return NewMarketAdapter(MarketAdapterOptions{
		RESTURL:    f.server.URL,
		HTTPClient: f.server.Client(),
		Backoff:    BackoffOptions{Sleep: recordingSleep(sleeps), Jitter: zeroJitter},
	})
}

// TestMarketAdapterTickers covers pair-map loading and REST ticker fetching.
func TestMarketAdapterTickers(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN Kraken temporarily rate-limits ticker calls WHEN batch prices are fetched THEN the adapter backs off and retries", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/public/AssetPairs", assetPairsBody)
		f.on("/0/public/Ticker",
			`{"error":["EAPI:Rate limit exceeded"]}`,
			`{"error":[],"result":{"XXBTZEUR":{"c":["50000.25","0.1"],"v":["1.0","2.5"],"a":["50001","1","1"],"b":["49999","1","1"]}}}`)
		var sleeps []time.Duration
		adapter := newTestMarketAdapter(f, &sleeps)

		snapshots, err := adapter.GetTickersBatch(ctx, []string{"BTC/EUR"})

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n := len(f.calls("/0/public/Ticker")); n != 2 {
			t.Fatalf("expected 2 ticker calls, got %d", n)
		}
		if len(sleeps) != 1 || sleeps[0] != 500*time.Millisecond {
			t.Fatalf("expected one 500ms backoff, got %v", sleeps)
		}
		snap, ok := snapshots["BTC/EUR"]
		if !ok {
			t.Fatalf("expected BTC/EUR snapshot, got %v", snapshots)
		}
		if snap.Price != 50000.25 || snap.Volume == nil || *snap.Volume != 2.5 {
			t.Fatalf("expected price 50000.25 and 24h volume 2.5, got %+v", snap)
		}
		if snap.Timestamp.Location() != time.UTC {
			t.Fatalf("expected UTC timestamp, got %v", snap.Timestamp.Location())
		}
	})

	t.Run("GIVEN several symbols WHEN batch prices are fetched THEN one Ticker call carries every Kraken altname", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/public/AssetPairs", assetPairsBody)
		f.on("/0/public/Ticker", `{"error":[],"result":{
			"XXBTZEUR":{"c":["50000","0.1"],"v":["1","2"]},
			"XETHZEUR":{"c":["2000","0.1"],"v":["10","20"]}}}`)
		adapter := newTestMarketAdapter(f, nil)

		snapshots, err := adapter.GetTickersBatch(ctx, []string{"BTC/EUR", "ETH/EUR", "FOO/EUR"})

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		calls := f.calls("/0/public/Ticker")
		if len(calls) != 1 {
			t.Fatalf("expected a single ticker call, got %d", len(calls))
		}
		pair := calls[0].Query.Get("pair")
		if !strings.Contains(pair, "XBTEUR") || !strings.Contains(pair, "ETHEUR") || strings.Contains(pair, "FOO") {
			t.Fatalf("unexpected pair list %q", pair)
		}
		if len(snapshots) != 2 || snapshots["ETH/EUR"].Price != 2000 {
			t.Fatalf("expected BTC and ETH snapshots, got %v", snapshots)
		}
	})

	t.Run("GIVEN a mix of listed and unlisted symbols WHEN validated THEN only Kraken pairs survive including aliased bases", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/public/AssetPairs", assetPairsBody)
		adapter := newTestMarketAdapter(f, nil)

		valid := adapter.ValidateSymbols(ctx, []string{"BTC/EUR", "DOGE/EUR", "FOO/EUR"})

		if strings.Join(valid, ",") != "BTC/EUR,DOGE/EUR" {
			t.Fatalf("unexpected valid symbols %v", valid)
		}
	})

	t.Run("GIVEN the pair map is already loaded WHEN more calls are made THEN AssetPairs is not fetched again", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/public/AssetPairs", assetPairsBody)
		adapter := newTestMarketAdapter(f, nil)

		adapter.ValidateSymbols(ctx, []string{"BTC/EUR"})
		adapter.ValidateSymbols(ctx, []string{"ETH/EUR"})

		if n := len(f.calls("/0/public/AssetPairs")); n != 1 {
			t.Fatalf("expected pair map to be cached, got %d loads", n)
		}
	})

	t.Run("GIVEN the pair map cannot be loaded WHEN prices are fetched THEN an error and no snapshots are returned and the next call retries", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/public/AssetPairs", `{"error":["EGeneral:Internal error"]}`, assetPairsBody)
		f.on("/0/public/Ticker", `{"error":[],"result":{"XXBTZEUR":{"c":["1","0"],"v":["1","1"]}}}`)
		adapter := newTestMarketAdapter(f, nil)

		snapshots, err := adapter.GetTickersBatch(ctx, []string{"BTC/EUR"})
		if err == nil || len(snapshots) != 0 {
			t.Fatalf("expected error and no snapshots, got %v err=%v", snapshots, err)
		}

		snapshots, err = adapter.GetTickersBatch(ctx, []string{"BTC/EUR"})
		if err != nil || len(snapshots) != 1 {
			t.Fatalf("expected recovery on retry, got %v err=%v", snapshots, err)
		}
	})

	t.Run("GIVEN a single symbol WHEN GetTicker is called THEN the matching snapshot or nil is returned", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/public/AssetPairs", assetPairsBody)
		f.on("/0/public/Ticker", `{"error":[],"result":{"XXBTZEUR":{"c":["42","0"],"v":["1","3"]}}}`)
		adapter := newTestMarketAdapter(f, nil)

		snap, err := adapter.GetTicker(ctx, "BTC/EUR")
		if err != nil || snap == nil || snap.Price != 42 {
			t.Fatalf("expected BTC/EUR at 42, got %+v err=%v", snap, err)
		}
		missing, _ := adapter.GetTicker(ctx, "FOO/EUR")
		if missing != nil {
			t.Fatalf("expected nil for unlisted symbol, got %+v", missing)
		}
	})
}

// TestMarketAdapterOHLC covers candle fetching through the SDK OHLC endpoint.
func TestMarketAdapterOHLC(t *testing.T) {
	ctx := context.Background()

	t.Run("GIVEN Kraken OHLC rows WHEN candles are requested with a limit THEN the newest candles are returned newest-last", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/public/AssetPairs", assetPairsBody)
		f.on("/0/public/OHLC", `{"error":[],"result":{"XXBTZEUR":[
			[1700000000,"10.0","11.0","9.0","10.5","10.2","100.0",5],
			[1700000300,"10.5","12.0","10.0","11.5","11.0","200.0",6],
			[1700000600,"11.5","13.0","11.0","12.5","12.0","300.0",7]],"last":1700000600}}`)
		adapter := newTestMarketAdapter(f, nil)

		candles, err := adapter.GetOHLC(ctx, "BTC/EUR", 5, 2)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(candles) != 2 {
			t.Fatalf("expected 2 candles, got %d", len(candles))
		}
		if candles[0].T >= candles[1].T || candles[1].T != 1700000600 {
			t.Fatalf("expected newest-last ordering ending at the latest bar, got %+v", candles)
		}
		if candles[1] != (domain.Candle{T: 1700000600, O: 11.5, H: 13, L: 11, C: 12.5, V: 300}) {
			t.Fatalf("unexpected candle fields %+v", candles[1])
		}
		calls := f.calls("/0/public/OHLC")
		if calls[0].Query.Get("pair") != "XBTEUR" || calls[0].Query.Get("interval") != "5" {
			t.Fatalf("unexpected OHLC query %v", calls[0].Query)
		}
	})

	t.Run("GIVEN Kraken returns an OHLC error WHEN candles are requested THEN an error and no candles are returned", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/public/AssetPairs", assetPairsBody)
		f.on("/0/public/OHLC", `{"error":["EQuery:Unknown asset pair"]}`)
		adapter := newTestMarketAdapter(f, nil)

		candles, err := adapter.GetOHLC(ctx, "BTC/EUR", 5, 10)

		if err == nil || len(candles) != 0 {
			t.Fatalf("expected error and no candles, got %v err=%v", candles, err)
		}
	})
}

// TestParseTickerMessage covers the pure WebSocket v2 ticker parser.
func TestParseTickerMessage(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	symbols := map[string]string{"BTC/EUR": "BTC/EUR"}

	t.Run("GIVEN a Kraken WebSocket v2 ticker message WHEN parsed THEN the polling MarketSnapshot contract is returned", func(t *testing.T) {
		msg := `{"channel":"ticker","type":"update","data":[{"symbol":"BTC/EUR","bid":50123.3,"bid_qty":1.2,"ask":50123.5,"ask_qty":0.5,"last":50123.4,"volume":9.7,"vwap":50000.1,"low":49000,"high":51000,"change":12.3,"change_pct":0.02}]}`

		snaps := ParseTickerMessage([]byte(msg), symbols, now)

		if len(snaps) != 1 {
			t.Fatalf("expected one snapshot, got %d", len(snaps))
		}
		snap := snaps[0]
		if snap.Symbol != "BTC/EUR" || snap.Price != 50123.4 || snap.Volume == nil || *snap.Volume != 9.7 {
			t.Fatalf("unexpected snapshot %+v", snap)
		}
		if snap.Bid == nil || snap.Ask == nil || *snap.Bid >= *snap.Ask {
			t.Fatalf("expected bid below ask, got %+v", snap)
		}
		if !snap.Timestamp.Equal(now) {
			t.Fatalf("expected supplied timestamp, got %v", snap.Timestamp)
		}
	})

	t.Run("GIVEN a ticker for a Kraken-native base name WHEN parsed THEN it is mapped back to the internal symbol", func(t *testing.T) {
		msg := `{"channel":"ticker","type":"snapshot","data":[{"symbol":"XBT/EUR","last":1.5,"volume":2}]}`

		snaps := ParseTickerMessage([]byte(msg), nil, now)

		if len(snaps) != 1 || snaps[0].Symbol != "BTC/EUR" {
			t.Fatalf("expected BTC/EUR, got %+v", snaps)
		}
	})

	t.Run("GIVEN non-ticker or malformed messages WHEN parsed THEN nothing is emitted", func(t *testing.T) {
		for _, msg := range []string{
			`{"channel":"heartbeat"}`,
			`{"method":"subscribe","success":true,"result":{"channel":"ticker","symbol":"BTC/EUR"}}`,
			`{"channel":"status","type":"update","data":[{"system":"online"}]}`,
			`{"channel":"ticker","type":"update","data":[{"symbol":"BTC/EUR"}]}`,
			`not json`,
		} {
			if snaps := ParseTickerMessage([]byte(msg), symbols, now); len(snaps) != 0 {
				t.Errorf("expected no snapshot for %s, got %+v", msg, snaps)
			}
		}
	})
}

// snapshotCollector gathers snapshots delivered to a subscription callback.
type snapshotCollector struct {
	mu    sync.Mutex
	snaps []domain.MarketSnapshot
	ch    chan domain.MarketSnapshot
}

// newSnapshotCollector returns a collector with a buffered notification channel.
func newSnapshotCollector() *snapshotCollector {
	return &snapshotCollector{ch: make(chan domain.MarketSnapshot, 100)}
}

// add records a snapshot; used as the subscription callback.
func (c *snapshotCollector) add(s domain.MarketSnapshot) {
	c.mu.Lock()
	c.snaps = append(c.snaps, s)
	c.mu.Unlock()
	select {
	case c.ch <- s:
	default:
	}
}

// wait returns the next snapshot or fails the test after a timeout.
func (c *snapshotCollector) wait(t *testing.T) domain.MarketSnapshot {
	t.Helper()
	select {
	case s := <-c.ch:
		return s
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for snapshot")
		return domain.MarketSnapshot{}
	}
}

// TestMarketAdapterSubscription covers WebSocket streaming and the REST polling fallback.
func TestMarketAdapterSubscription(t *testing.T) {
	t.Run("GIVEN a working WebSocket stream WHEN a subscription starts THEN v2 ticker symbols are subscribed and snapshots are delivered", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/public/AssetPairs", assetPairsBody)
		var gotSymbols []string
		adapter := newTestMarketAdapter(f, nil)
		adapter.stream = func(ctx context.Context, url string, symbols []string, onMessage func([]byte)) error {
			gotSymbols = symbols
			onMessage([]byte(`{"channel":"ticker","type":"snapshot","data":[{"symbol":"BTC/EUR","last":123.5,"volume":7}]}`))
			<-ctx.Done()
			return nil
		}
		collector := newSnapshotCollector()

		adapter.Start([]string{"BTC/EUR", "FOO/EUR"}, collector.add)
		snap := collector.wait(t)
		adapter.Stop()

		if snap.Symbol != "BTC/EUR" || snap.Price != 123.5 {
			t.Fatalf("unexpected snapshot %+v", snap)
		}
		if strings.Join(gotSymbols, ",") != "BTC/EUR" {
			t.Fatalf("expected only listed v2 symbols to be subscribed, got %v", gotSymbols)
		}
		if n := len(f.calls("/0/public/Ticker")); n != 0 {
			t.Fatalf("expected no REST polling while streaming, got %d ticker calls", n)
		}
	})

	t.Run("GIVEN the WebSocket stream fails WHEN subscribed THEN the adapter falls back to REST polling", func(t *testing.T) {
		f := newFakeKraken(t)
		f.on("/0/public/AssetPairs", assetPairsBody)
		f.on("/0/public/Ticker", `{"error":[],"result":{"XXBTZEUR":{"c":["99.5","0"],"v":["1","4"]}}}`)
		adapter := newTestMarketAdapter(f, nil)
		adapter.pollInterval = 10 * time.Millisecond
		adapter.stream = func(ctx context.Context, url string, symbols []string, onMessage func([]byte)) error {
			return errors.New("dial failed")
		}
		collector := newSnapshotCollector()

		adapter.Start([]string{"BTC/EUR"}, collector.add)
		first := collector.wait(t)
		collector.wait(t) // a second poll proves the loop keeps running
		adapter.Stop()

		if first.Symbol != "BTC/EUR" || first.Price != 99.5 {
			t.Fatalf("unexpected polled snapshot %+v", first)
		}
		callsAfterStop := len(f.calls("/0/public/Ticker"))
		time.Sleep(50 * time.Millisecond)
		if len(f.calls("/0/public/Ticker")) != callsAfterStop {
			t.Fatal("expected polling to stop after Stop")
		}
	})

	t.Run("GIVEN no subscription WHEN Stop is called THEN it returns safely", func(t *testing.T) {
		f := newFakeKraken(t)
		adapter := newTestMarketAdapter(f, nil)

		adapter.Stop()

		if f.totalRequests() != 0 {
			t.Fatal("expected no Kraken calls")
		}
	})
}

// TestSymbolConversion covers internal ↔ Kraken symbol aliases.
func TestSymbolConversion(t *testing.T) {
	t.Run("GIVEN internal symbols WHEN converted THEN Kraken altnames use XBT and XDG aliases", func(t *testing.T) {
		cases := map[string]string{"BTC/EUR": "XBTEUR", "DOGE/USD": "XDGUSD", "ETH/EUR": "ETHEUR", "XBTEUR": "XBTEUR"}
		for in, want := range cases {
			if got := ToKrakenPair(in); got != want {
				t.Errorf("ToKrakenPair(%q) = %q, want %q", in, got, want)
			}
		}
	})

	t.Run("GIVEN Kraken slash symbols WHEN converted back THEN internal BTC and DOGE names are used", func(t *testing.T) {
		cases := map[string]string{"XBT/EUR": "BTC/EUR", "XDG/USD": "DOGE/USD", "ETH/EUR": "ETH/EUR"}
		for in, want := range cases {
			if got := FromKrakenSymbol(in); got != want {
				t.Errorf("FromKrakenSymbol(%q) = %q, want %q", in, got, want)
			}
		}
	})
}
