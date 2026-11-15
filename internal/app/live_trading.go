package app

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
	"github.com/James-Chapman/LLMTradingBot/internal/domain"
	"github.com/James-Chapman/LLMTradingBot/internal/kraken"
	"github.com/James-Chapman/LLMTradingBot/internal/livebook"
	"github.com/James-Chapman/LLMTradingBot/internal/storage"
)

// Live trading: the positions opened by real Kraken orders live in a.liveBook,
// apart from the paper engine. Every decision that can send a live order (a
// strategy signal, an approval, a stop, a manual close) holds a.liveMu from
// its position check until the order is recorded, so the same position can
// never be entered or exited twice.
//
// Every live order is written to the book BEFORE it is sent: its order ID
// (which is also Kraken's client order id) is allocated first and the
// position is saved as entry_pending / exit_pending with it. Only a definite
// "not placed" answer reverts that. A crash at any point therefore leaves a
// book row that names the order, and reconciliation finds it at Kraken.

// liveBookkeepingTimeout bounds the database writes that follow one live fill.
const liveBookkeepingTimeout = 30 * time.Second

// dustFraction is the share of a position below which a remainder left by a
// partial sell is treated as fully sold.
const dustFraction = 1e-6

// Backoff after Kraken rejects a live sell: no automatic retry for
// exitBackoffFirst, doubling after each further rejection up to exitBackoffMax.
const (
	exitBackoffFirst = 5 * time.Minute
	exitBackoffMax   = time.Hour
)

// liveWarnEvery rate-limits the repeated live-protection warnings (no
// automatic stop during the emergency stop, stale prices).
const liveWarnEvery = 10 * time.Minute

// alertTimeout bounds one background alert webhook call.
const alertTimeout = 10 * time.Second

// reasonBelowMinimum is the rejection reason of a sell refused locally because
// the position is smaller than Kraken's minimum order (it was removed as dust).
const reasonBelowMinimum = "below_order_minimum"

// recoveredExitReason labels a sell found in the order ledger but not in the
// book (its book write was lost): why it was sent is unknown.
const recoveredExitReason = "recovered"

// exitBackoff is the retry pause after a rejected live sell.
type exitBackoff struct {
	until time.Time
	delay time.Duration
}

// LivePositionRecord is a live position for the dashboard positions list.
type LivePositionRecord struct {
	domain.PositionRecord        // Size is the executed size (the requested size while the entry is pending)
	Environment           string // always "live"
	Status                string // entry_pending | open | exit_pending
	StrategyID            string
	TradeIdeaID           string
}

// LivePositions returns the live positions with unrealised P&L at the
// current prices (entry fee included), oldest first.
func (a *App) LivePositions() []LivePositionRecord {
	prices := a.pricesCopy()
	all := a.liveBook.All()
	out := make([]LivePositionRecord, 0, len(all))
	for _, p := range all {
		unrealised := 0.0
		if price, ok := prices[p.Market]; ok && p.Size > 0 {
			unrealised = (price-p.AvgPrice)*p.Size - p.EntryFee
		}
		out = append(out, LivePositionRecord{
			PositionRecord: domain.PositionRecord{
				PositionID: p.ID, Market: p.Market, Size: p.HeldSize(), AvgPrice: p.AvgPrice,
				UnrealizedPnL: unrealised, Timestamp: p.OpenedAt,
			},
			Environment: string(domain.EnvLive),
			Status:      string(p.Status),
			StrategyID:  p.StrategyID,
			TradeIdeaID: p.TradeIdeaID,
		})
	}
	return out
}

// liveRouted reports whether market sends real orders in environment: the
// environment must be live AND the market toggled live.
func (a *App) liveRouted(market, environment string) bool {
	return environment == string(domain.EnvLive) && a.control.IsMarketLive(market)
}

// openPositionsFor returns the positions the risk checks see for a route:
// the live book for live-routed trades, the paper engine otherwise.
func (a *App) openPositionsFor(isLive bool) []domain.PositionRecord {
	if isLive {
		return a.liveBook.Records()
	}
	return a.paper.OpenPositions()
}

// liveExitBlock explains why a short on a live market cannot sell now, or
// returns "" when an open live long can be sold.
func (a *App) liveExitBlock(market string) string {
	positions := a.liveBook.ForMarket(market)
	if len(positions) == 0 {
		return "no live position to sell - the spot account does not open shorts"
	}
	switch positions[0].Status {
	case livebook.EntryPending:
		return "the live entry order is not confirmed yet"
	case livebook.ExitPending:
		return "a live sell for this position is already pending"
	}
	return ""
}

// liveExitPaused explains why an automatic sell of market's open live long
// is paused after Kraken rejected one, or returns "". The caller holds a.liveMu.
func (a *App) liveExitPaused(market string) string {
	pos, ok := a.openLiveLong(market)
	if !ok {
		return ""
	}
	if until, paused := a.exitBackingOff(pos.ID); paused {
		return fmt.Sprintf("Kraken rejected the last sell - automatic retries paused until %s UTC", until.UTC().Format("15:04"))
	}
	return ""
}

// openLiveLong returns the oldest live position on market whose entry has
// filled and that has no sell pending.
func (a *App) openLiveLong(market string) (livebook.Position, bool) {
	for _, p := range a.liveBook.ForMarket(market) {
		if p.Status == livebook.Open && p.Size > 0 {
			return p, true
		}
	}
	return livebook.Position{}, false
}

// placeLiveTrade sends a live buy (entry) or a sell of the tracked long
// (exit) for idea. The caller holds a.liveMu. A short with no open live long
// is refused locally and nothing is sent.
func (a *App) placeLiveTrade(ctx context.Context, intent domain.ExecutionIntent, idea domain.TradeIdea, marketPrice float64) (domain.OrderRecord, string) {
	if intent.Direction == domain.Long {
		return a.placeLiveEntry(ctx, intent, idea, marketPrice)
	}
	pos, ok := a.openLiveLong(idea.Market)
	if !ok {
		a.logger.Warn("Live short refused: no open live position", "market", idea.Market, "trade_idea_id", idea.ID)
		return localRejection(intent, marketPrice, a.now(), "no_live_position"), ""
	}
	reason := "manual_approve"
	if intent.ApprovalRequestID == "auto" {
		reason = "auto"
	}
	order := a.placeLiveExit(ctx, pos, marketPrice, intent.ApprovalRequestID, reason, idea.ID)
	return order, pos.ID
}

// localRejection is an order refused by the app before anything was sent.
func localRejection(intent domain.ExecutionIntent, price float64, at time.Time, reason string) domain.OrderRecord {
	return domain.OrderRecord{
		ID: domain.NewID(), Market: intent.Market, Direction: intent.Direction, Size: intent.Size, Price: price,
		Status: "rejected", Timestamp: at, ExchangeOrderID: reason,
	}
}

// placeLiveEntry records a live buy as an entry-pending position, then sends
// it. The caller holds a.liveMu. When the position cannot be stored the buy
// is not sent: new exposure needs a durable record that survives a crash.
func (a *App) placeLiveEntry(ctx context.Context, intent domain.ExecutionIntent, idea domain.TradeIdea, marketPrice float64) (domain.OrderRecord, string) {
	positionID, orderID := domain.NewID(), domain.NewID()
	confidence := idea.Confidence
	pos := livebook.Position{
		ID: positionID, Market: idea.Market, Status: livebook.EntryPending, RequestedSize: intent.Size,
		AvgPrice: marketPrice, EntryOrderID: orderID, StrategyID: idea.StrategyID, TradeIdeaID: idea.ID,
		Confidence: &confidence, OpenedAt: a.now(),
	}
	if err := a.liveBook.Save(pos); err != nil {
		_ = a.liveBook.Remove(positionID)
		a.logger.Error("Live buy not sent: the position could not be recorded", "market", idea.Market, "error", err.Error())
		a.activity.Error(fmt.Sprintf("Live buy for %s NOT sent - the position could not be saved", idea.Market), err.Error())
		return localRejection(intent, marketPrice, a.now(), "position_not_recorded"), ""
	}

	order, _ := a.live.Execute(ctx, intent, kraken.ExecuteOptions{
		MarketPrice: marketPrice, StrategyID: idea.StrategyID, SignalConfidence: &confidence,
		Environment: string(domain.EnvLive), TradeIdeaID: idea.ID, PositionID: positionID, OrderID: orderID,
	})
	if order.Status == "rejected" {
		// Definitely not placed: the recorded intent is withdrawn.
		if err := a.liveBook.Remove(positionID); err != nil {
			a.logger.Error("Could not remove the position of a rejected live buy", "position_id", positionID, "error", err.Error())
		}
		return order, ""
	}
	a.bumpVersion()
	return order, positionID
}

// placeLiveExit marks pos exit-pending with a new order ID, then sends a
// live market sell of its held size, so a second sell can never follow. The
// caller holds a.liveMu. source is the order's approval_request_id label;
// reason is the exit reason recorded when the sell fills. A position below
// Kraken's minimum order is removed as dust instead (nothing is sent), and a
// sell Kraken rejects re-opens the position and pauses automatic retries.
func (a *App) placeLiveExit(ctx context.Context, pos livebook.Position, marketPrice float64, source, reason, closingIdeaID string) domain.OrderRecord {
	intent := domain.ExecutionIntent{ApprovalRequestID: source, Market: pos.Market, Direction: domain.Short, Size: pos.Size}
	if minimum, below := a.belowOrderMinimum(ctx, pos.Market, pos.Size); below {
		a.removeDust(pos, pos.Size, minimum)
		return localRejection(intent, marketPrice, a.now(), reasonBelowMinimum)
	}

	orderID := domain.NewID()
	pending := pos
	pending.Status, pending.ExitOrderID, pending.ExitReason, pending.ExitTradeIdeaID = livebook.ExitPending, orderID, reason, closingIdeaID
	pending.ExitRequestedAt = a.now()
	if err := a.liveBook.Save(pending); err != nil {
		// The sell is still sent: reducing exposure matters more. The book in
		// memory blocks a second sell this session, and the order row (stamped
		// with the position ID) lets a restart find the sell.
		a.logger.Error("Could not persist the live exit before sending it", "position_id", pos.ID, "order_id", orderID, "error", err.Error())
	}

	order, _ := a.live.Execute(ctx, intent, kraken.ExecuteOptions{
		MarketPrice: marketPrice, StrategyID: pos.StrategyID, SignalConfidence: pos.Confidence,
		Environment: string(domain.EnvLive), TradeIdeaID: closingIdeaID, PositionID: pos.ID, OrderID: orderID,
	})
	if order.Status == "rejected" {
		if err := a.liveBook.Save(pos); err != nil {
			a.logger.Error("Could not re-open live position after a rejected sell", "position_id", pos.ID, "error", err.Error())
		}
		a.noteRejectedExit(pos, order)
		return order
	}
	delete(a.exitBackoff, pos.ID)
	a.bumpVersion()
	return order
}

// noteRejectedExit records a rejected live sell: automatic sells of the
// position pause for exitBackoffFirst, doubling with each further rejection
// up to exitBackoffMax, and the first rejection sends an alert. The caller holds a.liveMu.
func (a *App) noteRejectedExit(pos livebook.Position, order domain.OrderRecord) {
	backoff, seen := a.exitBackoff[pos.ID]
	if seen {
		backoff.delay = min(backoff.delay*2, exitBackoffMax)
	} else {
		backoff.delay = exitBackoffFirst
	}
	backoff.until = a.now().Add(backoff.delay)
	a.exitBackoff[pos.ID] = backoff
	a.activity.Error(
		fmt.Sprintf("Live sell for %s [%s] REJECTED - position stays open, automatic sells paused for %s", pos.Market, short8(pos.ID), backoff.delay),
		order.ExchangeOrderID,
	)
	if !seen {
		a.sendAlertAsync("live_exit_rejected", map[string]any{
			"market": pos.Market, "position_id": pos.ID, "reason": order.ExchangeOrderID, "environment": string(domain.EnvLive),
		})
	}
}

// exitBackingOff reports whether automatic sells of a position are paused
// after a rejection, and until when. The caller holds a.liveMu.
func (a *App) exitBackingOff(positionID string) (time.Time, bool) {
	backoff, ok := a.exitBackoff[positionID]
	return backoff.until, ok && a.now().Before(backoff.until)
}

// belowOrderMinimum reports whether size is below Kraken's minimum order for
// market (and that minimum). An unknown minimum is never "below".
func (a *App) belowOrderMinimum(ctx context.Context, market string, size float64) (float64, bool) {
	minimum, ok := a.live.MinOrderSize(ctx, market)
	return minimum, ok && size < minimum
}

// removeDust removes a live position whose held amount is below Kraken's
// minimum order: the bot can never sell it. The caller holds a.liveMu.
func (a *App) removeDust(pos livebook.Position, left, minimum float64) {
	if err := a.liveBook.Remove(pos.ID); err != nil {
		a.logger.Error("Could not remove live dust position", "position_id", pos.ID, "error", err.Error())
	}
	delete(a.exitBackoff, pos.ID)
	a.activity.Warn(
		fmt.Sprintf("Live position %s [%s] removed as dust: %.8f left, below Kraken's minimum order of %.8f", pos.Market, short8(pos.ID), left, minimum),
		"The amount stays in your Kraken account; the bot no longer tracks or sells it.",
	)
	a.logger.Warn("Live dust position removed", "position_id", pos.ID, "market", pos.Market, "left", left, "minimum", minimum)
	a.bumpVersion()
}

// checkLiveStops sends a live market sell for every open live position whose
// stop-loss or trailing stop has triggered, in any environment and whether or
// not its market is enabled. Trailing highs follow the price first. While the
// emergency stop is active no sell is sent. The position becomes
// exit-pending, so the sell is sent once.
func (a *App) checkLiveStops(ctx context.Context, prices map[string]float64) {
	a.liveBook.UpdateTrailingHighs(prices)
	if a.control.EmergencyStop() {
		return
	}
	for _, pos := range a.liveBook.All() {
		price, ok := prices[pos.Market]
		if !ok || pos.Status != livebook.Open {
			continue
		}
		stopHit := livebook.StopLossHit(pos.AvgPrice, price, a.risk.StopLossAssumption())
		trailHit := !stopHit && livebook.TrailingStopHit(pos.TrailingHigh, pos.AvgPrice, price, a.cfg().TrailingStopPct)
		if !stopHit && !trailHit {
			continue
		}
		reason, label := "trailing_stop", "TRAILING-STOP"
		if stopHit {
			reason, label = "stop_loss", "STOP-LOSS"
		}
		a.sendLiveStop(ctx, pos.ID, price, reason, label)
	}
}

// sendLiveStop re-checks the position (and the emergency stop and any
// rejection backoff) under the live lock and sends its stop sell.
func (a *App) sendLiveStop(ctx context.Context, positionID string, price float64, reason, label string) {
	a.liveMu.Lock()
	defer a.liveMu.Unlock()
	if a.control.EmergencyStop() {
		return // pressed while this tick was running
	}
	pos, ok := a.liveBook.Get(positionID)
	if !ok || pos.Status != livebook.Open {
		return // closed or already selling via another path
	}
	if _, paused := a.exitBackingOff(pos.ID); paused {
		return
	}
	order := a.placeLiveExit(ctx, pos, price, "stop_loss", reason, "")
	if order.Status == "rejected" {
		return
	}
	a.activity.Warn(
		fmt.Sprintf("LIVE %s: %s [%s] market sell sent at %s%s", label, pos.Market, short8(pos.ID), a.currencySymbol, fmtPrice(price)),
		fmt.Sprintf("Entry %s%s  Size %.8f  Order %s", a.currencySymbol, fmtPrice(pos.AvgPrice), pos.Size, short8(order.ID)),
	)
}

// trackedLiveOrders returns the book's pending orders for reconciliation, so
// an order whose ledger row is missing is still looked up at Kraken.
func (a *App) trackedLiveOrders() []kraken.PendingLiveOrder {
	var out []kraken.PendingLiveOrder
	for _, p := range a.liveBook.All() {
		switch {
		case p.Status == livebook.EntryPending && p.EntryOrderID != "":
			out = append(out, kraken.PendingLiveOrder{
				ID: p.EntryOrderID, ClientOrderID: p.EntryOrderID, PositionID: p.ID, Direction: domain.Long,
				Size: p.RequestedSize, Price: p.AvgPrice, Timestamp: p.OpenedAt,
			})
		case p.Status == livebook.ExitPending && p.ExitOrderID != "":
			requested := p.ExitRequestedAt
			if requested.IsZero() {
				requested = p.OpenedAt // a row saved before exit times were stored
			}
			out = append(out, kraken.PendingLiveOrder{
				ID: p.ExitOrderID, ClientOrderID: p.ExitOrderID, PositionID: p.ID, Direction: domain.Short,
				Size: p.Size, Price: p.AvgPrice, Timestamp: requested,
			})
		}
	}
	return out
}

// reconcileLive asks Kraken about pending live orders and applies every
// finished order to the live book. It runs in the live environment, and in
// any environment (also during the emergency stop) while the book holds positions.
func (a *App) reconcileLive(ctx context.Context) {
	if a.TradingEnvironment() != string(domain.EnvLive) && len(a.liveBook.All()) == 0 {
		return
	}
	updates := a.live.ReconcilePendingOrders(ctx, a.trackedLiveOrders())
	if len(updates) == 0 {
		return
	}
	a.liveMu.Lock()
	defer a.liveMu.Unlock()
	for _, u := range updates {
		// Each update's bookkeeping gets its own deadline and must finish even
		// if shutdown starts now.
		bookCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), liveBookkeepingTimeout)
		a.applyLiveUpdate(bookCtx, u)
		cancel()
	}
	a.bumpVersion()
}

// applyLiveUpdate applies one finished order to the position it belongs to:
// the position whose entry or pending exit is that order or, failing that,
// the position named on the order (its book write was lost). Orders of no
// known position are ignored. The caller holds a.liveMu.
func (a *App) applyLiveUpdate(ctx context.Context, u kraken.OrderUpdate) {
	if u.Status == "pending" {
		return
	}
	pos, ok := a.liveBook.ByOrderID(u.OrderID)
	if !ok {
		pos, ok = a.adoptOrder(u)
	}
	if !ok {
		return
	}
	switch {
	case pos.EntryOrderID == u.OrderID && pos.Status == livebook.EntryPending:
		a.ensureLiveOrderRow(ctx, pos, u, domain.Long)
		a.applyLiveEntryUpdate(ctx, pos, u)
	case pos.ExitOrderID == u.OrderID && pos.Status == livebook.ExitPending:
		a.ensureLiveOrderRow(ctx, pos, u, domain.Short)
		a.applyLiveExitUpdate(ctx, pos, u, false)
	}
}

// adoptOrder links an order the book does not reference to the position
// named on it: a sell of an open position becomes its pending exit, a buy of
// an entry-pending position without an order ID its entry. Anything else is
// reported for the operator to check on Kraken. The caller holds a.liveMu.
func (a *App) adoptOrder(u kraken.OrderUpdate) (livebook.Position, bool) {
	if u.PositionID == "" {
		return livebook.Position{}, false
	}
	pos, ok := a.liveBook.Get(u.PositionID)
	if !ok {
		return livebook.Position{}, false
	}
	switch {
	case u.Direction == domain.Short && pos.Status == livebook.Open:
		pos.Status, pos.ExitOrderID, pos.ExitReason, pos.ExitTradeIdeaID = livebook.ExitPending, u.OrderID, recoveredExitReason, ""
		pos.ExitRequestedAt = a.now()
	case u.Direction == domain.Long && pos.Status == livebook.EntryPending && pos.EntryOrderID == "":
		pos.EntryOrderID = u.OrderID
	default:
		a.logger.Error("Live order does not match its position - check it on Kraken",
			"order_id", u.OrderID, "position_id", pos.ID, "position_status", string(pos.Status), "direction", string(u.Direction))
		a.activity.Error(fmt.Sprintf("Live order %s for %s [%s] does not match the position - check it on Kraken", short8(u.OrderID), pos.Market, short8(pos.ID)), "")
		return livebook.Position{}, false
	}
	if err := a.liveBook.Save(pos); err != nil {
		a.logger.Error("Could not persist adopted live order", "position_id", pos.ID, "order_id", u.OrderID, "error", err.Error())
	}
	a.logger.Warn("Live order matched to its position by position ID", "order_id", u.OrderID, "position_id", pos.ID)
	return pos, true
}

// ensureLiveOrderRow writes the order_records row of a reconciled fill when
// it is missing (its save failed, or the bot stopped before it was written),
// so the trade ledger shows it and its P&L stamp - the marker that a close
// was recorded - has a row to land on.
func (a *App) ensureLiveOrderRow(ctx context.Context, pos livebook.Position, u kraken.OrderUpdate, direction domain.Direction) {
	if u.Status != "filled" {
		return
	}
	_, _, found, err := a.repo.GetOrderExecution(ctx, u.OrderID)
	if err != nil || found {
		return
	}
	order := domain.OrderRecord{
		ID: u.OrderID, Market: pos.Market, Direction: direction, Size: u.ExecutedSize, Price: u.AvgPrice,
		Status: u.Status, Timestamp: a.now(), ExchangeOrderID: u.ExchangeOrderID, PositionID: pos.ID,
	}
	ideaID := pos.TradeIdeaID
	if direction == domain.Short {
		ideaID = pos.ExitTradeIdeaID
	}
	err = a.repo.SaveOrder(ctx, order, storage.SaveOrderOptions{
		Fee: u.Fee, Environment: string(domain.EnvLive), TradeIdeaID: ideaID, ClientOrderID: u.OrderID,
	})
	if err != nil {
		a.logger.Error("Could not write the missing live order row", "order_id", u.OrderID, "error", err.Error())
	}
}

// recoverLiveBook applies order results that reached order_records but not
// the live book because the bot stopped between the two writes. It runs at
// start-up, after the book is restored. A pending position whose order has
// no ledger row is left to reconciliation, which looks it up at Kraken by
// client order id. An open position with an unrecorded sell in the ledger
// gets that sell applied (or tracked, while it is still pending). A close
// whose P&L is already stamped on its order is not recorded twice.
func (a *App) recoverLiveBook(ctx context.Context) {
	for _, pos := range a.liveBook.All() {
		switch pos.Status {
		case livebook.EntryPending:
			a.recoverPendingOrder(ctx, pos, pos.EntryOrderID)
		case livebook.ExitPending:
			a.recoverPendingOrder(ctx, pos, pos.ExitOrderID)
		case livebook.Open:
			a.recoverUnrecordedSell(ctx, pos)
		}
	}
}

// recoverPendingOrder applies the ledger's result for a pending position's order.
func (a *App) recoverPendingOrder(ctx context.Context, pos livebook.Position, orderID string) {
	exec, pnlRecorded, found, err := a.repo.GetOrderExecution(ctx, orderID)
	if err != nil {
		a.logger.Warn("Could not check live order at start-up", "order_id", orderID, "error", err.Error())
		return
	}
	if !found {
		a.logger.Warn("Live order has no ledger row - it will be looked up at Kraken by client order id",
			"position_id", pos.ID, "order_id", orderID)
		return
	}
	if exec.Status == "pending" {
		return // still open on Kraken: normal reconciliation handles it
	}
	u := kraken.OrderUpdate{
		OrderID: orderID, ExchangeOrderID: exec.ExchangeOrderID, PositionID: pos.ID, Status: exec.Status, KrakenStatus: exec.Status,
		AvgPrice: exec.AvgPrice, Fee: exec.Fee,
	}
	if exec.Status == "filled" {
		u.ExecutedSize = exec.ExecutedSize
	}
	a.logger.Info("Applying live order result found at start-up", "position_id", pos.ID, "order_id", orderID, "status", exec.Status)
	if pos.Status == livebook.EntryPending {
		a.applyLiveEntryUpdate(ctx, pos, u)
	} else {
		a.applyLiveExitUpdate(ctx, pos, u, pnlRecorded)
	}
}

// recoverUnrecordedSell finds a sell of an open position that the book never
// recorded (the bot stopped between the order and the book write): a pending
// one becomes the position's pending exit, a filled one whose P&L is not
// stamped is applied.
func (a *App) recoverUnrecordedSell(ctx context.Context, pos livebook.Position) {
	orders, err := a.repo.GetLiveOrdersForPosition(ctx, pos.ID)
	if err != nil {
		a.logger.Warn("Could not check live position orders at start-up", "position_id", pos.ID, "error", err.Error())
		return
	}
	for _, o := range orders {
		if o.Direction != string(domain.Short) || o.PnLRecorded {
			continue
		}
		switch o.Status {
		case "pending":
			pos.Status, pos.ExitOrderID, pos.ExitReason, pos.ExitTradeIdeaID = livebook.ExitPending, o.OrderID, recoveredExitReason, ""
			pos.ExitRequestedAt = o.Timestamp
			if err := a.liveBook.Save(pos); err != nil {
				a.logger.Error("Could not persist recovered live exit", "position_id", pos.ID, "error", err.Error())
			}
			a.logger.Warn("Live sell found in the ledger but not in the book - now tracked", "position_id", pos.ID, "order_id", o.OrderID)
			return
		case "filled":
			pos.ExitOrderID, pos.ExitReason = o.OrderID, recoveredExitReason
			u := kraken.OrderUpdate{
				OrderID: o.OrderID, ExchangeOrderID: o.ExchangeOrderID, PositionID: pos.ID, Direction: domain.Short,
				Status: o.Status, KrakenStatus: o.Status, ExecutedSize: o.Size, AvgPrice: o.Price, Fee: o.Fee,
			}
			a.logger.Warn("Applying a filled live sell missing from the book", "position_id", pos.ID, "order_id", o.OrderID)
			a.applyLiveExitUpdate(ctx, pos, u, false)
			current, ok := a.liveBook.Get(pos.ID)
			if !ok || current.Status != livebook.Open {
				return // sold out (or removed as dust)
			}
			pos = current
		}
	}
}

// applyLiveEntryUpdate records what the entry order executed: the executed
// size, average price and fee become the position; nothing executed removes it.
func (a *App) applyLiveEntryUpdate(ctx context.Context, pos livebook.Position, u kraken.OrderUpdate) {
	if u.ExecutedSize <= 0 {
		if err := a.liveBook.Remove(pos.ID); err != nil {
			a.logger.Error("Could not remove unfilled live position", "position_id", pos.ID, "error", err.Error())
		}
		a.activity.Warn(fmt.Sprintf("Live entry %s [%s] not executed (%s) - no position", pos.Market, short8(pos.ID), u.Status), "")
		return
	}
	pos.Status, pos.Size, pos.AvgPrice, pos.EntryFee = livebook.Open, u.ExecutedSize, u.AvgPrice, u.Fee
	pos.TrailingHigh = u.AvgPrice
	if err := a.liveBook.Save(pos); err != nil {
		a.logger.Error("Could not persist live fill", "position_id", pos.ID, "error", err.Error())
	}
	detail := ""
	if u.ExecutedSize < pos.RequestedSize*(1-dustFraction) {
		detail = fmt.Sprintf("Partly filled: %.8f of %.8f (Kraken status %s)", u.ExecutedSize, pos.RequestedSize, u.KrakenStatus)
	}
	a.activity.Success(
		fmt.Sprintf("Live entry filled: %s [%s] %.8f @ %s%s", pos.Market, short8(pos.ID), u.ExecutedSize, a.currencySymbol, fmtPrice(u.AvgPrice)),
		detail,
	)
}

// applyLiveExitUpdate realises the P&L of what the sell executed (entry and
// exit fees included) through the same path paper closes use - unless
// closeRecorded says that already happened - then removes the position or,
// after a partial sell, keeps the remainder open. A remainder below Kraken's
// minimum order is removed as dust. A sell that executed nothing re-opens
// the position so a later exit can be sent.
func (a *App) applyLiveExitUpdate(ctx context.Context, pos livebook.Position, u kraken.OrderUpdate, closeRecorded bool) {
	executed := math.Min(u.ExecutedSize, pos.Size)
	if executed <= 0 {
		pos.Status, pos.ExitOrderID, pos.ExitReason, pos.ExitTradeIdeaID = livebook.Open, "", "", ""
		pos.ExitRequestedAt = time.Time{}
		if err := a.liveBook.Save(pos); err != nil {
			a.logger.Error("Could not re-open live position", "position_id", pos.ID, "error", err.Error())
		}
		a.activity.Warn(fmt.Sprintf("Live sell for %s [%s] not executed (%s) - position open again", pos.Market, short8(pos.ID), u.Status), "")
		return
	}

	entryFee := pos.EntryFee * executed / pos.Size
	pnl := (u.AvgPrice-pos.AvgPrice)*executed - entryFee - u.Fee
	if !closeRecorded {
		a.recordLiveClose(ctx, pos, u, executed, pnl)
	}

	remaining := pos.Size - executed
	if remaining <= pos.Size*dustFraction {
		if err := a.liveBook.Remove(pos.ID); err != nil {
			a.logger.Error("Could not remove closed live position", "position_id", pos.ID, "error", err.Error())
		}
		delete(a.exitBackoff, pos.ID)
		return
	}
	pos.Size, pos.EntryFee = remaining, pos.EntryFee-entryFee
	pos.Status, pos.ExitOrderID, pos.ExitReason, pos.ExitTradeIdeaID = livebook.Open, "", "", ""
	pos.ExitRequestedAt = time.Time{}
	if minimum, below := a.belowOrderMinimum(ctx, pos.Market, remaining); below {
		a.removeDust(pos, remaining, minimum)
		return
	}
	if err := a.liveBook.Save(pos); err != nil {
		a.logger.Error("Could not persist partly sold live position", "position_id", pos.ID, "error", err.Error())
	}
	a.activity.Warn(fmt.Sprintf("Live sell for %s [%s] partly filled - %.8f still held", pos.Market, short8(pos.ID), remaining), "")
}

// recordLiveClose feeds a realised live P&L to the order row, the risk engine
// (daily loss), the learner and the closed-trade history, and alerts on stops.
// The alert is sent in the background so the live lock is never held for it.
func (a *App) recordLiveClose(ctx context.Context, pos livebook.Position, u kraken.OrderUpdate, executed, pnl float64) {
	strategyID := pos.StrategyID
	if strategyID == "" {
		strategyID = "unknown"
	}
	a.recordClosedOutcome(ctx, u.OrderID, strategyID, pos.Market, string(domain.Long), pnl, pos.AvgPrice*executed)
	err := a.repo.SaveSignalOutcome(ctx, storage.SignalOutcomeInput{
		StrategyID: strategyID, Market: pos.Market, Direction: string(domain.Long),
		EntryPrice: pos.AvgPrice, ExitPrice: u.AvgPrice, Size: executed, PnL: pnl, Confidence: pos.Confidence,
		ExitReason: pos.ExitReason, EntryAt: pos.OpenedAt, PositionID: pos.ID,
		TradeIdeaID: pos.TradeIdeaID, ClosingTradeIdeaID: pos.ExitTradeIdeaID,
	})
	if err != nil {
		a.logger.Error("Could not record live closed trade", "position_id", pos.ID, "error", err.Error())
	}
	a.activity.Success(
		fmt.Sprintf("Live position closed (%s): %s [%s] %.8f @ %s%s", pos.ExitReason, pos.Market, short8(pos.ID), executed, a.currencySymbol, fmtPrice(u.AvgPrice)),
		fmt.Sprintf("Entry %s%s  PnL %s%s", a.currencySymbol, fmtPrice(pos.AvgPrice), a.currencySymbol, fmtSignedMoney(pnl)),
	)
	if pos.ExitReason == "stop_loss" || pos.ExitReason == "trailing_stop" {
		a.sendAlertAsync(pos.ExitReason, map[string]any{"market": pos.Market, "position_id": pos.ID, "pnl": pnl, "environment": string(domain.EnvLive)})
	}
}

// closeLivePosition handles a dashboard close of a live position: it sends a
// real market sell of the held size. It is allowed in any environment and
// during the emergency stop (an operator action that reduces exposure), and
// is not held back by a rejection backoff. The P&L is recorded when the sell fills.
func (a *App) closeLivePosition(ctx context.Context, positionID string) (map[string]any, error) {
	a.liveMu.Lock()
	defer a.liveMu.Unlock()
	pos, ok := a.liveBook.Get(positionID)
	if !ok {
		return nil, statusError(http.StatusNotFound, "Position not found or already closed")
	}
	if pos.Status != livebook.Open {
		return nil, statusError(http.StatusConflict, fmt.Sprintf("Position %s has a live order pending (%s) - try again once it settles", short8(pos.ID), pos.Status))
	}
	marketPrice := a.pricesCopy()[pos.Market]
	if marketPrice == 0 {
		return nil, statusError(http.StatusServiceUnavailable, fmt.Sprintf("No live price for %s", pos.Market))
	}
	order := a.placeLiveExit(ctx, pos, marketPrice, "manual_close", "manual_close", "")
	if order.Status == "rejected" {
		if order.ExchangeOrderID == reasonBelowMinimum {
			return nil, statusError(http.StatusConflict, fmt.Sprintf("Position %s is below Kraken's minimum order and was removed as dust - nothing was sent", short8(pos.ID)))
		}
		return nil, statusError(http.StatusBadGateway, fmt.Sprintf("Kraken rejected the sell: %s", order.ExchangeOrderID))
	}
	a.mu.Lock()
	cash := a.currentCash
	a.mu.Unlock()
	a.activity.Success(
		fmt.Sprintf("Live position close requested: %s [%s]", pos.Market, short8(pos.ID)),
		fmt.Sprintf("Market sell %.8f sent @ ~%s%s  Order %s", pos.Size, a.currencySymbol, fmtPrice(marketPrice), short8(order.ID)),
	)
	a.logger.Info("Live position close requested", "position_id", pos.ID, "market", pos.Market, "order_id", order.ID)
	return map[string]any{
		"status":       "closing",
		"position_id":  pos.ID,
		"order_id":     order.ID,
		"order_status": order.Status,
		"environment":  string(domain.EnvLive),
		"fill_price":   marketPrice, // reference price; the real fill is recorded on reconciliation
		"pnl":          nil,
		"cash":         cash,
	}, nil
}

// ForgetLivePosition removes a live position from the book without sending
// any order, for a holding sold or moved outside the bot. Only live
// positions can be forgotten (full ID or the dashboard's 8-character prefix).
func (a *App) ForgetLivePosition(positionID string) (map[string]any, error) {
	a.liveMu.Lock()
	defer a.liveMu.Unlock()
	pos, ok := a.liveBook.Find(positionID)
	if !ok {
		if a.isPaperPosition(positionID) {
			return nil, statusError(http.StatusConflict, "Only live positions can be forgotten - close a paper position instead")
		}
		return nil, statusError(http.StatusNotFound, "Position not found")
	}
	if err := a.liveBook.Remove(pos.ID); err != nil {
		a.logger.Error("Could not delete forgotten live position", "position_id", pos.ID, "error", err.Error())
		return nil, statusError(http.StatusInternalServerError,
			fmt.Sprintf("Position %s was removed for now but could not be deleted from the database: it returns after a restart", short8(pos.ID)))
	}
	delete(a.exitBackoff, pos.ID)
	a.activity.Warn(
		fmt.Sprintf("Live position forgotten: %s [%s] %.8f (%s) - no order sent", pos.Market, short8(pos.ID), pos.HeldSize(), pos.Status),
		"Removed from the bot's book by the operator. The holding on Kraken is untouched; any order still pending for it is no longer tracked.",
	)
	a.logger.Warn("Live position forgotten by operator", "position_id", pos.ID, "market", pos.Market, "status", string(pos.Status), "size", pos.HeldSize())
	a.bumpVersion()
	return map[string]any{"status": "forgotten", "position_id": pos.ID, "market": pos.Market}, nil
}

// isPaperPosition reports whether id (or its 8-character prefix) is an open paper position.
func (a *App) isPaperPosition(id string) bool {
	if _, ok := a.paper.Position(id); ok {
		return true
	}
	for _, p := range a.paper.OpenPositions() {
		if id != "" && strings.HasPrefix(p.PositionID, id) {
			return true
		}
	}
	return false
}

// liveBookSummary describes the live positions on market ("" for every
// market), e.g. "BTC/EUR [3fa85f64] open", for refusal messages.
func (a *App) liveBookSummary(market string) (string, int) {
	var parts []string
	for _, p := range a.liveBook.All() {
		if market == "" || p.Market == market {
			parts = append(parts, fmt.Sprintf("%s [%s] %s", p.Market, short8(p.ID), strings.ReplaceAll(string(p.Status), "_", " ")))
		}
	}
	return strings.Join(parts, ", "), len(parts)
}

// liveMarkets returns the markets that hold a live position, oldest first.
func (a *App) liveMarkets() []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range a.liveBook.All() {
		if !seen[p.Market] {
			seen[p.Market] = true
			out = append(out, p.Market)
		}
	}
	return out
}

// tendLiveBook keeps the live positions protected on a tick that evaluates
// no strategy (every market disabled, or no prices for them): their markets
// are priced, pending orders reconcile and their stops run.
func (a *App) tendLiveBook(ctx context.Context) {
	if len(a.liveBook.All()) == 0 {
		return
	}
	prices := a.liveBookPrices(ctx, nil)
	a.mergePrices(prices)
	a.reconcileLive(ctx)
	a.checkLiveStops(ctx, prices)
}

// tendLiveBookDuringStop keeps the live positions tracked while the
// emergency stop is active: their markets are priced (so a manual close has
// a price), pending orders reconcile and trailing highs follow the price, but
// no automatic sell is sent. A warning that they have no automatic stop is
// posted at most once per liveWarnEvery.
func (a *App) tendLiveBookDuringStop(ctx context.Context) {
	if len(a.liveBook.All()) == 0 {
		return
	}
	prices := a.liveBookPrices(ctx, nil)
	a.mergePrices(prices)
	a.reconcileLive(ctx)
	a.liveBook.UpdateTrailingHighs(prices)
	n := len(a.liveBook.All())
	if n == 0 || !a.warnDue(&a.liveStopWarnedAt) {
		return
	}
	summary, _ := a.liveBookSummary("")
	a.activity.Warn(
		fmt.Sprintf("Emergency stop: %d live position(s) have no automatic stop", n),
		fmt.Sprintf("%s. Stop-losses and trailing stops are paused until you resume; you can still close a position from the dashboard.", summary),
	)
}

// warnDue reports whether a rate-limited warning last posted at *last may be
// posted again now, and if so records now. It takes a.mu.
func (a *App) warnDue(last *time.Time) bool {
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if !last.IsZero() && now.Sub(*last) < liveWarnEvery {
		return false
	}
	*last = now
	return true
}

// liveBookPrices returns fresh prices for the markets holding a live
// position that are not already in have, whether or not they are enabled
// or in the active universe.
func (a *App) liveBookPrices(ctx context.Context, have map[string]float64) map[string]float64 {
	var markets []string
	for _, m := range a.liveMarkets() {
		if _, ok := have[m]; !ok {
			markets = append(markets, m)
		}
	}
	out := map[string]float64{}
	for _, ps := range a.freshSnapshots(ctx, markets) {
		out[ps.symbol] = ps.snap.Price
	}
	return out
}

// mergePrices adds prices to the current prices without dropping others.
func (a *App) mergePrices(prices map[string]float64) {
	if len(prices) == 0 {
		return
	}
	a.mu.Lock()
	for m, p := range prices {
		a.currentPrices[m] = p
	}
	a.bumpVersionLocked()
	a.mu.Unlock()
}

// staleAfter is the age beyond which a streamed snapshot is not trusted:
// two strategy intervals.
func (a *App) staleAfter() time.Duration {
	return 2 * a.strategyInterval()
}

// strategyInterval is the time between strategy ticks: the interval fixed by
// the start-up options if any (tests), otherwise STRATEGY_INTERVAL_SECONDS.
func (a *App) strategyInterval() time.Duration {
	if a.intervals.Strategy > 0 {
		return a.intervals.Strategy
	}
	seconds := a.cfg().StrategyIntervalSeconds
	if seconds <= 0 {
		seconds = config.Defaults().StrategyIntervalSeconds
	}
	return time.Duration(seconds) * time.Second
}

// freshSnapshots returns a snapshot per market, in markets order: the
// streamed one when it is recent, otherwise one fetched over REST. A market
// whose streamed snapshot is stale and whose REST price is unavailable is
// left out (nothing is evaluated on its stale price) and reported in the
// activity feed at most once per liveWarnEvery.
func (a *App) freshSnapshots(ctx context.Context, markets []string) []pricedSnapshot {
	if len(markets) == 0 {
		return nil
	}
	now := a.now()
	a.mu.Lock()
	cached := map[string]domain.MarketSnapshot{}
	stale := map[string]domain.MarketSnapshot{}
	for _, m := range markets {
		snap, ok := a.latestSnapshots[m]
		switch {
		case !ok:
		case now.Sub(snap.Timestamp) <= a.staleAfter():
			cached[m] = snap
		default:
			stale[m] = snap
		}
	}
	a.mu.Unlock()

	var missing []string
	for _, m := range markets {
		if _, ok := cached[m]; !ok {
			missing = append(missing, m)
		}
	}
	fetched := map[string]domain.MarketSnapshot{}
	if len(missing) > 0 {
		fetched, _ = a.market.GetTickersBatch(ctx, missing) // never nil; empty on failure
	}
	var out []pricedSnapshot
	for _, m := range markets {
		if snap, ok := cached[m]; ok {
			out = append(out, pricedSnapshot{m, snap})
		} else if snap, ok := fetched[m]; ok {
			out = append(out, pricedSnapshot{m, snap})
		} else if snap, ok := stale[m]; ok {
			a.warnStalePrice(m, now.Sub(snap.Timestamp))
		}
	}
	return out
}

// warnStalePrice reports, at most once per liveWarnEvery per market, that a
// market was skipped because its only price is stale.
func (a *App) warnStalePrice(market string, age time.Duration) {
	now := a.now()
	a.mu.Lock()
	last := a.staleWarnedAt[market]
	due := last.IsZero() || now.Sub(last) >= liveWarnEvery
	if due {
		a.staleWarnedAt[market] = now
	}
	a.mu.Unlock()
	if !due {
		return
	}
	a.activity.Warn(
		fmt.Sprintf("Price for %s is stale (no update for %s) - stops and strategies skip it", market, age.Round(time.Second)),
		"The ticker stream sent nothing recent and Kraken's REST ticker did not answer. It is used again as soon as a fresh price arrives.",
	)
}

// sendAlertAsync posts an alert webhook in the background with its own
// timeout, tracked by a.tasks so shutdown waits for it. Callers may hold
// a.liveMu: the webhook never delays live decisions.
func (a *App) sendAlertAsync(eventType string, payload map[string]any) {
	if a.cfg().AlertWebhookURL == "" {
		return
	}
	a.tasks.Add(1)
	go func() {
		defer a.tasks.Done()
		ctx, cancel := context.WithTimeout(context.Background(), alertTimeout)
		defer cancel()
		a.sendAlert(ctx, eventType, payload)
	}()
}

// alertDailyLossOnce sends the daily_loss_limit_hit webhook at most once per
// UTC day, in the background (it can be reached under a.liveMu).
func (a *App) alertDailyLossOnce(market, reason string) {
	day := a.now().UTC().Format(time.DateOnly)
	a.mu.Lock()
	if a.lossAlertDay == day {
		a.mu.Unlock()
		return
	}
	a.lossAlertDay = day
	a.mu.Unlock()
	a.sendAlertAsync("daily_loss_limit_hit", map[string]any{"market": market, "reason": reason})
}
