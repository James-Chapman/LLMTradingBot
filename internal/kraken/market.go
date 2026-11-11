package kraken

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/krakenfx/api-go/v2/pkg/callback"
	krakenws "github.com/krakenfx/api-go/v2/pkg/kraken"
	"github.com/krakenfx/api-go/v2/pkg/spot"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/observability"
)

// DefaultWebSocketURL is Kraken's public spot WebSocket v2 endpoint.
const DefaultWebSocketURL = "wss://ws.kraken.com/v2"

const (
	defaultPollInterval = 5 * time.Second
	stopTimeout         = 5 * time.Second
)

// TickerStreamFunc streams raw WebSocket messages for the given v2 symbols to
// onMessage. It returns nil once ctx is done, or an error if the stream fails.
type TickerStreamFunc func(ctx context.Context, url string, symbols []string, onMessage func([]byte)) error

// MarketAdapterOptions configures a MarketAdapter; zero values select defaults.
type MarketAdapterOptions struct {
	RESTURL      string           // default DefaultRESTURL
	HTTPClient   *http.Client     // default client with a 15 s timeout
	WebSocketURL string           // default DefaultWebSocketURL
	Stream       TickerStreamFunc // default: SDK spot WebSocket (v2 ticker channel)
	PollInterval time.Duration    // REST polling fallback interval (default 5 s)
	Backoff      BackoffOptions   // retry settings for REST calls (Sleep/Jitter injectable)
}

// MarketAdapter reads Kraken public market data. It never uses API keys, so it
// is safe in paper mode.
type MarketAdapter struct {
	rest         *spot.REST
	httpClient   *http.Client
	backoff      BackoffOptions
	wsURL        string
	stream       TickerStreamFunc
	pollInterval time.Duration
	log          *slog.Logger

	pairMu            sync.Mutex
	altnameToOfficial map[string]string // "XBTEUR" -> "XXBTZEUR"
	officialToAltname map[string]string // "XXBTZEUR" -> "XBTEUR"

	runMu  sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// NewMarketAdapter builds a MarketAdapter using the official Kraken SDK.
func NewMarketAdapter(opts MarketAdapterOptions) *MarketAdapter {
	restURL := opts.RESTURL
	if restURL == "" {
		restURL = DefaultRESTURL
	}
	wsURL := opts.WebSocketURL
	if wsURL == "" {
		wsURL = DefaultWebSocketURL
	}
	stream := opts.Stream
	if stream == nil {
		stream = sdkTickerStream
	}
	poll := opts.PollInterval
	if poll <= 0 {
		poll = defaultPollInterval
	}
	return &MarketAdapter{
		rest:              newREST(restURL, "", ""),
		httpClient:        defaultHTTPClient(opts.HTTPClient),
		backoff:           opts.Backoff,
		wsURL:             wsURL,
		stream:            stream,
		pollInterval:      poll,
		log:               observability.Logger("kraken_adapter"),
		altnameToOfficial: map[string]string{},
		officialToAltname: map[string]string{},
	}
}

// restFor returns an SDK client bound to ctx.
func (a *MarketAdapter) restFor(ctx context.Context) *spot.REST {
	return restWithContext(ctx, a.rest, a.httpClient)
}

// backoffFor returns the adapter's backoff options labelled for one operation.
func (a *MarketAdapter) backoffFor(operation string) BackoffOptions {
	opts := a.backoff
	opts.Logger = a.log
	opts.Operation = operation
	return opts
}

// loadPairMap fetches Kraken's tradable pairs once; failures are retried on the next call.
func (a *MarketAdapter) loadPairMap(ctx context.Context) error {
	a.pairMu.Lock()
	defer a.pairMu.Unlock()
	if len(a.altnameToOfficial) > 0 {
		return nil
	}
	resp, err := CallWithBackoff(ctx, a.backoffFor("Kraken asset pairs"),
		func(ctx context.Context) (*spot.Response[map[string]spot.AssetPair], error) {
			return a.restFor(ctx).AssetPairs(nil)
		})
	if err != nil {
		a.log.Error("Failed to load Kraken pair map", "error", err.Error())
		return fmt.Errorf("load Kraken pair map: %w", err)
	}
	for official, pair := range resp.Result {
		if pair.AltName == "" {
			continue
		}
		a.altnameToOfficial[pair.AltName] = official
		a.officialToAltname[official] = pair.AltName
	}
	a.log.Info("Kraken pair map loaded", "total_pairs", len(a.altnameToOfficial))
	return nil
}

// officialFor returns the official Kraken pair name for an altname, if listed.
func (a *MarketAdapter) officialFor(altname string) (string, bool) {
	a.pairMu.Lock()
	defer a.pairMu.Unlock()
	official, ok := a.altnameToOfficial[altname]
	return official, ok
}

// ValidateSymbols returns only the symbols that exist as Kraken pairs.
func (a *MarketAdapter) ValidateSymbols(ctx context.Context, symbols []string) []string {
	if err := a.loadPairMap(ctx); err != nil {
		return []string{}
	}
	valid := make([]string, 0, len(symbols))
	for _, sym := range symbols {
		altname := ToKrakenPair(sym)
		if _, ok := a.officialFor(altname); ok {
			valid = append(valid, sym)
			continue
		}
		a.log.Warn("Symbol not available on Kraken, skipping", "symbol", sym, "altname_tried", altname)
	}
	return valid
}

// GetTickersBatch fetches many tickers in a single Kraken call to avoid rate limits.
// The returned map is never nil; it is empty when the call fails.
func (a *MarketAdapter) GetTickersBatch(ctx context.Context, symbols []string) (map[string]domain.MarketSnapshot, error) {
	results := map[string]domain.MarketSnapshot{}
	if len(symbols) == 0 {
		return results, nil
	}
	if err := a.loadPairMap(ctx); err != nil {
		return results, err
	}

	// Kraken may key the response by official name or altname; accept both.
	keyToSymbol := map[string]string{}
	var altnames []string
	for _, sym := range symbols {
		altname := ToKrakenPair(sym)
		official, ok := a.officialFor(altname)
		if !ok {
			continue
		}
		if _, seen := keyToSymbol[altname]; !seen {
			altnames = append(altnames, altname)
		}
		keyToSymbol[altname] = sym
		keyToSymbol[official] = sym
	}
	if len(altnames) == 0 {
		a.log.Warn("No valid altnames found for batch ticker", "symbols", symbols)
		return results, nil
	}

	pairString := strings.Join(altnames, ",")
	resp, err := CallWithBackoff(ctx, a.backoffFor("Kraken batch ticker"),
		func(ctx context.Context) (*spot.Response[map[string]spot.AssetTickerInfo], error) {
			return a.restFor(ctx).Ticker(&spot.TickerRequest{Pair: pairString})
		})
	if err != nil {
		if ctx.Err() == nil {
			a.log.Error("Batch ticker failed", "error", err.Error(), "pairs", pairString)
		}
		return results, fmt.Errorf("batch ticker: %w", err)
	}

	now := domain.NowUTC()
	for key, info := range resp.Result {
		sym, ok := keyToSymbol[key]
		if !ok {
			continue
		}
		price, ok := decimalAt(info.Close, 0)
		if !ok {
			continue
		}
		snap := domain.MarketSnapshot{Symbol: sym, Timestamp: now, Price: price}
		if v, ok := decimalAt(info.Volume, 1); ok { // index 1 = rolling 24h volume
			snap.Volume = domain.Float(v)
		}
		if bid, ok := decimalAt(info.Bid, 0); ok {
			snap.Bid = domain.Float(bid)
		}
		if ask, ok := decimalAt(info.Ask, 0); ok {
			snap.Ask = domain.Float(ask)
		}
		results[sym] = snap
	}
	return results, nil
}

// GetTicker fetches one symbol's ticker; it returns nil when the symbol is not listed.
func (a *MarketAdapter) GetTicker(ctx context.Context, symbol string) (*domain.MarketSnapshot, error) {
	results, err := a.GetTickersBatch(ctx, []string{symbol})
	if err != nil {
		return nil, err
	}
	snap, ok := results[symbol]
	if !ok {
		return nil, nil
	}
	return &snap, nil
}

// GetOHLC returns up to limit candles (newest-last) for symbol at intervalMinutes.
// A limit of zero or less returns every candle Kraken sent.
func (a *MarketAdapter) GetOHLC(ctx context.Context, symbol string, intervalMinutes, limit int) ([]domain.Candle, error) {
	altname := ToKrakenPair(symbol)
	resp, err := CallWithBackoff(ctx, a.backoffFor("Kraken OHLC"),
		func(ctx context.Context) (*spot.Response[map[string]any], error) {
			return a.restFor(ctx).OHLC(&spot.OHLCRequest{Pair: altname, Interval: intervalMinutes})
		})
	if err != nil {
		a.log.Error("OHLC fetch failed", "symbol", symbol, "error", err.Error())
		return []domain.Candle{}, fmt.Errorf("OHLC for %s: %w", symbol, err)
	}

	candles, err := parseOHLCResult(resp.Result)
	if err != nil {
		a.log.Error("OHLC parse failed", "symbol", symbol, "error", err.Error())
		return []domain.Candle{}, fmt.Errorf("OHLC for %s: %w", symbol, err)
	}
	if limit > 0 && len(candles) > limit {
		candles = candles[len(candles)-limit:]
	}
	return candles, nil
}

// parseOHLCResult converts Kraken's OHLC result (one pair key plus "last") into candles.
// Each row is [time, open, high, low, close, vwap, volume, count].
func parseOHLCResult(result map[string]any) ([]domain.Candle, error) {
	var rows []any
	for key, value := range result {
		if key == "last" {
			continue
		}
		list, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("unexpected OHLC payload for %s", key)
		}
		rows = list
		break
	}

	candles := make([]domain.Candle, 0, len(rows))
	for _, raw := range rows {
		row, ok := raw.([]any)
		if !ok || len(row) < 7 {
			return nil, errors.New("malformed OHLC row")
		}
		var fields [7]float64
		for i := range fields {
			v, err := anyFloat(row[i])
			if err != nil {
				return nil, fmt.Errorf("OHLC field %d: %w", i, err)
			}
			fields[i] = v
		}
		candles = append(candles, domain.Candle{
			T: int64(fields[0]),
			O: fields[1],
			H: fields[2],
			L: fields[3],
			C: fields[4],
			V: fields[6],
		})
	}
	return candles, nil
}

// wsTickerMessage is the WebSocket v2 ticker channel envelope.
type wsTickerMessage struct {
	Channel string         `json:"channel"`
	Type    string         `json:"type"`
	Data    []wsTickerData `json:"data"`
}

// wsTickerData is one entry of a v2 ticker message.
type wsTickerData struct {
	Symbol string   `json:"symbol"`
	Last   *float64 `json:"last"`
	Volume *float64 `json:"volume"` // rolling 24h volume
	Bid    *float64 `json:"bid"`
	Ask    *float64 `json:"ask"`
}

// ParseTickerMessage parses one Kraken WebSocket v2 message into market snapshots.
// Non-ticker or malformed messages yield an empty slice. wsToSymbol maps v2
// symbols to internal symbols; unknown symbols fall back to alias conversion.
func ParseTickerMessage(raw []byte, wsToSymbol map[string]string, now time.Time) []domain.MarketSnapshot {
	var msg wsTickerMessage
	if err := json.Unmarshal(raw, &msg); err != nil || msg.Channel != "ticker" {
		return nil
	}
	var snaps []domain.MarketSnapshot
	for _, d := range msg.Data {
		if d.Symbol == "" || d.Last == nil {
			continue
		}
		symbol, ok := wsToSymbol[d.Symbol]
		if !ok {
			symbol = FromKrakenSymbol(d.Symbol)
		}
		snaps = append(snaps, domain.MarketSnapshot{
			Symbol:    symbol,
			Timestamp: now,
			Price:     *d.Last,
			Volume:    d.Volume,
			Bid:       d.Bid,
			Ask:       d.Ask,
		})
	}
	return snaps
}

// wsSubscribeAck is a v2 subscribe acknowledgement.
type wsSubscribeAck struct {
	Method  string `json:"method"`
	Success *bool  `json:"success"`
	Error   string `json:"error"`
	Symbol  string `json:"symbol"`
}

// logSubscribeFailure warns when Kraken rejects a ticker subscription for a symbol.
func (a *MarketAdapter) logSubscribeFailure(raw []byte) {
	var ack wsSubscribeAck
	if json.Unmarshal(raw, &ack) != nil || ack.Method != "subscribe" || ack.Success == nil || *ack.Success {
		return
	}
	a.log.Warn("Kraken WebSocket subscription rejected", "symbol", ack.Symbol, "error", ack.Error)
}

// SubscribeTicker streams ticker snapshots to callback until ctx is done. It uses
// the WebSocket v2 ticker channel and falls back to REST polling if streaming fails.
func (a *MarketAdapter) SubscribeTicker(ctx context.Context, symbols []string, cb func(domain.MarketSnapshot)) {
	a.log.Info("Starting ticker subscription", "symbols", symbols, "transport", "websocket")
	err := a.subscribeWebSocket(ctx, symbols, cb)
	if err == nil || ctx.Err() != nil {
		return
	}
	a.log.Warn("Kraken WebSocket ticker failed, falling back to polling", "error", err.Error(), "symbols", symbols)
	a.pollTickers(ctx, symbols, cb)
}

// subscribeWebSocket streams v2 ticker updates for the listed symbols.
func (a *MarketAdapter) subscribeWebSocket(ctx context.Context, symbols []string, cb func(domain.MarketSnapshot)) error {
	if err := a.loadPairMap(ctx); err != nil {
		return err
	}
	// WebSocket v2 uses readable names (BTC/EUR), unlike REST altnames (XBTEUR).
	wsToSymbol := map[string]string{}
	var wsSymbols []string
	for _, sym := range symbols {
		if _, ok := a.officialFor(ToKrakenPair(sym)); !ok {
			continue
		}
		wsSymbol := FromKrakenSymbol(sym)
		if _, seen := wsToSymbol[wsSymbol]; !seen {
			wsSymbols = append(wsSymbols, wsSymbol)
		}
		wsToSymbol[wsSymbol] = sym
	}
	if len(wsSymbols) == 0 {
		a.log.Warn("No valid WebSocket ticker pairs", "symbols", symbols)
		return nil
	}

	return a.stream(ctx, a.wsURL, wsSymbols, func(raw []byte) {
		if ctx.Err() != nil {
			return // stopped: drop late messages
		}
		a.logSubscribeFailure(raw)
		for _, snap := range ParseTickerMessage(raw, wsToSymbol, domain.NowUTC()) {
			cb(snap)
		}
	})
}

// pollTickers fetches batch tickers every pollInterval until ctx is done.
func (a *MarketAdapter) pollTickers(ctx context.Context, symbols []string, cb func(domain.MarketSnapshot)) {
	a.log.Info("Starting ticker subscription fallback (polling)", "symbols", symbols)
	for {
		snapshots, _ := a.GetTickersBatch(ctx, symbols) // failures are logged inside
		for _, sym := range symbols {
			if ctx.Err() != nil {
				return
			}
			if snap, ok := snapshots[sym]; ok {
				cb(snap)
			}
		}
		if ContextSleep(ctx, a.pollInterval) != nil {
			return
		}
	}
}

// Start begins a background ticker subscription, replacing any running one.
func (a *MarketAdapter) Start(symbols []string, cb func(domain.MarketSnapshot)) {
	a.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	a.runMu.Lock()
	a.cancel = cancel
	a.done = done
	a.runMu.Unlock()

	go func() {
		defer close(done)
		a.SubscribeTicker(ctx, symbols, cb)
	}()
}

// Stop cancels the background subscription and waits briefly for it to exit.
func (a *MarketAdapter) Stop() {
	a.runMu.Lock()
	cancel, done := a.cancel, a.done
	a.cancel, a.done = nil, nil
	a.runMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-time.After(stopTimeout):
		a.log.Warn("Ticker subscription did not stop within timeout")
	}
	a.log.Info("Stopped ticker subscriptions")
}

// sdkTickerStream is the default TickerStreamFunc built on the SDK's spot WebSocket.
// SDK auto-reconnect is disabled so a dropped connection surfaces as an error and
// the adapter can fall back to REST polling.
func sdkTickerStream(ctx context.Context, url string, symbols []string, onMessage func([]byte)) error {
	ws := spot.NewWebSocket()
	ws.URL = url
	ws.Reconnect = nil

	disconnected := make(chan error, 1)
	ws.OnDisconnected.Recurring(func(e *callback.Event[error]) {
		select {
		case disconnected <- e.Data:
		default:
		}
	})
	ws.OnReceived.Recurring(func(e *callback.Event[*krakenws.WebSocketMessage]) {
		onMessage(e.Data.Bytes())
	})

	if err := ws.Connect(); err != nil {
		return fmt.Errorf("websocket connect: %w", err)
	}
	if err := ws.SubTicker(symbols); err != nil {
		_ = ws.Disconnect()
		return fmt.Errorf("websocket subscribe ticker: %w", err)
	}

	select {
	case <-ctx.Done():
		_ = ws.Disconnect()
		return nil
	case err := <-disconnected:
		return fmt.Errorf("websocket disconnected: %w", err)
	}
}
