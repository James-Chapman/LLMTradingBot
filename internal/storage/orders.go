package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/James-Chapman/LLMTradingBot/internal/domain"
)

// SaveTradeIdea upserts a trade idea with its full signal context for
// post-trade traceability. created_at is only set on first insert.
func (r *Repository) SaveTradeIdea(ctx context.Context, idea domain.TradeIdea, sc SignalContext) error {
	supporting, err := encodeJSON(nonNilMap(idea.SupportingSignals))
	if err != nil {
		return err
	}
	modes, err := encodeJSON(modeStrings(idea.ModeEligibility))
	if err != nil {
		return err
	}
	indicators, err := encodeJSON(nonNilMap(sc.Indicators))
	if err != nil {
		return err
	}
	news := sc.NewsContext
	if news == nil {
		news = []NewsContextItem{}
	}
	newsJSON, err := encodeJSON(news)
	if err != nil {
		return err
	}

	// LLM detail is only recorded when the LLM was actually consulted.
	llmUsed := false
	var llmSentiment, llmScale, llmReasoning any
	if sc.LLM != nil {
		llmUsed = sc.LLM.Used
		if sc.LLM.Used {
			llmSentiment, llmScale, llmReasoning = sc.LLM.Sentiment, sc.LLM.ConfidenceScale, sc.LLM.Reasoning
		}
	}
	var riskApproved, riskReason any
	if sc.RiskDecision != nil {
		riskApproved, riskReason = boolInt(sc.RiskDecision.Approved), sc.RiskDecision.Reason
	}

	_, err = r.db.ExecContext(ctx, `
		INSERT INTO trade_ideas (
			id, strategy_id, market, direction, thesis, supporting_signals, confidence,
			entry_plan, exit_plan, stop_or_invalidation, position_sizing_proposal, mode_eligibility,
			created_at, momentum_pct, indicators, llm_used, llm_sentiment, llm_confidence_scale,
			llm_reasoning, news_context, risk_approved, risk_reason
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			strategy_id = excluded.strategy_id,
			market = excluded.market,
			direction = excluded.direction,
			thesis = excluded.thesis,
			supporting_signals = excluded.supporting_signals,
			confidence = excluded.confidence,
			entry_plan = excluded.entry_plan,
			exit_plan = excluded.exit_plan,
			stop_or_invalidation = excluded.stop_or_invalidation,
			position_sizing_proposal = excluded.position_sizing_proposal,
			mode_eligibility = excluded.mode_eligibility,
			momentum_pct = excluded.momentum_pct,
			indicators = excluded.indicators,
			llm_used = excluded.llm_used,
			llm_sentiment = excluded.llm_sentiment,
			llm_confidence_scale = excluded.llm_confidence_scale,
			llm_reasoning = excluded.llm_reasoning,
			news_context = excluded.news_context,
			risk_approved = excluded.risk_approved,
			risk_reason = excluded.risk_reason`,
		idea.ID, idea.StrategyID, idea.Market, string(idea.Direction), idea.Thesis, supporting, idea.Confidence,
		idea.EntryPlan, idea.ExitPlan, idea.StopOrInvalidation, idea.PositionSizingProposal, modes,
		formatDBTime(time.Now()), sc.MomentumPct, indicators, boolInt(llmUsed), llmSentiment, llmScale,
		llmReasoning, newsJSON, riskApproved, riskReason,
	)
	if err != nil {
		return fmt.Errorf("save trade idea %s: %w", idea.ID, err)
	}
	return nil
}

// GetSignalDetail returns the full signal context for one trade idea, or nil if not found.
func (r *Repository) GetSignalDetail(ctx context.Context, tradeIdeaID string) (*SignalDetail, error) {
	var (
		d                                                   SignalDetail
		strategy, market, direction, thesis                 sql.NullString
		entryPlan, exitPlan, stop, llmReasoning, riskReason sql.NullString
		supporting, indicators, news                        sql.NullString
		confidence, momentum, llmSentiment, llmScale        sql.NullFloat64
		llmUsed, riskApproved                               sql.NullBool
		createdAt                                           nullTime
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT id, strategy_id, market, direction, thesis, supporting_signals, confidence,
			momentum_pct, indicators, llm_used, llm_sentiment, llm_confidence_scale, llm_reasoning,
			news_context, risk_approved, risk_reason, entry_plan, exit_plan, stop_or_invalidation, created_at
		FROM trade_ideas WHERE id = ?`, tradeIdeaID,
	).Scan(&d.ID, &strategy, &market, &direction, &thesis, &supporting, &confidence,
		&momentum, &indicators, &llmUsed, &llmSentiment, &llmScale, &llmReasoning,
		&news, &riskApproved, &riskReason, &entryPlan, &exitPlan, &stop, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get signal detail %s: %w", tradeIdeaID, err)
	}

	d.StrategyID, d.Market, d.Direction, d.Thesis = strategy.String, market.String, direction.String, thesis.String
	d.Confidence = confidence.Float64
	d.MomentumPct = floatPtr(momentum)
	d.LLMUsed = llmUsed.Bool
	d.LLMSentiment, d.LLMConfidenceScale = floatPtr(llmSentiment), floatPtr(llmScale)
	d.LLMReasoning = stringPtr(llmReasoning)
	d.RiskApproved, d.RiskReason = boolPtr(riskApproved), stringPtr(riskReason)
	d.EntryPlan, d.ExitPlan, d.StopOrInvalidation = entryPlan.String, exitPlan.String, stop.String
	d.CreatedAt = createdAt.iso()
	if err := decodeJSON(supporting, &d.SupportingSignals); err != nil {
		return nil, err
	}
	if err := decodeJSON(indicators, &d.Indicators); err != nil {
		return nil, err
	}
	if err := decodeJSON(news, &d.NewsContext); err != nil {
		return nil, err
	}
	d.SupportingSignals = nonNilMap(d.SupportingSignals)
	d.Indicators = nonNilMap(d.Indicators)
	if d.NewsContext == nil {
		d.NewsContext = []NewsContextItem{}
	}
	return &d, nil
}

// SaveApprovalRequest persists a pending approval request and its reconstructable context.
func (r *Repository) SaveApprovalRequest(ctx context.Context, req domain.ApprovalRequest) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := insertTradeIdeaIfMissing(ctx, tx, req.TradeIdea); err != nil {
		return err
	}
	riskID, err := upsertApprovalRiskDecision(ctx, tx, req.ID, req.RiskDecision)
	if err != nil {
		return err
	}

	status := req.Status
	if status == "" {
		status = "pending"
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO approval_requests (id, trade_idea_id, risk_decision_id, expires_at, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			trade_idea_id = excluded.trade_idea_id,
			risk_decision_id = excluded.risk_decision_id,
			expires_at = excluded.expires_at,
			status = excluded.status`,
		req.ID, req.TradeIdea.ID, riskID, dbTimeOrNil(req.ExpiresAt), status, formatDBTime(time.Now()))
	if err != nil {
		return fmt.Errorf("save approval request %s: %w", req.ID, err)
	}
	return tx.Commit()
}

// insertTradeIdeaIfMissing writes the basic idea row only when absent, so a
// prior SaveTradeIdea's full signal context is never overwritten with NULLs.
func insertTradeIdeaIfMissing(ctx context.Context, tx *sql.Tx, idea domain.TradeIdea) error {
	supporting, err := encodeJSON(nonNilMap(idea.SupportingSignals))
	if err != nil {
		return err
	}
	modes, err := encodeJSON(modeStrings(idea.ModeEligibility))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO trade_ideas (
			id, strategy_id, market, direction, thesis, supporting_signals, confidence,
			entry_plan, exit_plan, stop_or_invalidation, position_sizing_proposal, mode_eligibility, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		idea.ID, idea.StrategyID, idea.Market, string(idea.Direction), idea.Thesis, supporting, idea.Confidence,
		idea.EntryPlan, idea.ExitPlan, idea.StopOrInvalidation, idea.PositionSizingProposal, modes,
		formatDBTime(time.Now()))
	if err != nil {
		return fmt.Errorf("insert trade idea %s: %w", idea.ID, err)
	}
	return nil
}

// upsertApprovalRiskDecision updates the approval's existing risk_decisions row,
// or inserts a new one, and returns its ID.
func upsertApprovalRiskDecision(ctx context.Context, tx *sql.Tx, approvalID string, risk domain.RiskDecision) (int64, error) {
	var existingID sql.NullInt64
	err := tx.QueryRowContext(ctx, `
		SELECT rd.id FROM approval_requests ar
		JOIN risk_decisions rd ON rd.id = ar.risk_decision_id
		WHERE ar.id = ?`, approvalID).Scan(&existingID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("look up risk decision: %w", err)
	}

	if existingID.Valid {
		_, err = tx.ExecContext(ctx,
			"UPDATE risk_decisions SET approved = ?, reason = ?, adjusted_sizing = ?, timestamp = ? WHERE id = ?",
			boolInt(risk.Approved), risk.Reason, nullableFloat(risk.AdjustedSizing), dbTimeOrNil(risk.Timestamp), existingID.Int64)
		if err != nil {
			return 0, fmt.Errorf("update risk decision: %w", err)
		}
		return existingID.Int64, nil
	}

	res, err := tx.ExecContext(ctx,
		"INSERT INTO risk_decisions (trade_idea_id, approved, reason, adjusted_sizing, timestamp) VALUES (?, ?, ?, ?, ?)",
		risk.TradeIdeaID, boolInt(risk.Approved), risk.Reason, nullableFloat(risk.AdjustedSizing), dbTimeOrNil(risk.Timestamp))
	if err != nil {
		return 0, fmt.Errorf("insert risk decision: %w", err)
	}
	return res.LastInsertId()
}

// UpdateApprovalStatus updates a persisted approval's status if it exists.
func (r *Repository) UpdateApprovalStatus(ctx context.Context, approvalID, status string) error {
	_, err := r.db.ExecContext(ctx, "UPDATE approval_requests SET status = ? WHERE id = ?", status, approvalID)
	if err != nil {
		return fmt.Errorf("update approval status %s: %w", approvalID, err)
	}
	return nil
}

// ClearPendingApprovals marks every pending approval as cleared and returns the count.
func (r *Repository) ClearPendingApprovals(ctx context.Context) (int, error) {
	res, err := r.db.ExecContext(ctx, "UPDATE approval_requests SET status = 'cleared' WHERE status = 'pending'")
	if err != nil {
		return 0, fmt.Errorf("clear pending approvals: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// pendingApprovalRow is the raw approval_requests data needed for reconstruction.
type pendingApprovalRow struct {
	id, ideaID, status string
	riskID             sql.NullInt64
	expiresAt          time.Time
}

// LoadPendingApprovalRequests returns pending, non-expired approvals rebuilt from
// storage. Approvals whose trade idea or risk decision is missing are skipped.
func (r *Repository) LoadPendingApprovalRequests(ctx context.Context, now time.Time) ([]domain.ApprovalRequest, error) {
	pending, err := r.pendingApprovalRows(ctx, now)
	if err != nil {
		return nil, err
	}
	result := []domain.ApprovalRequest{}
	for _, row := range pending {
		idea, err := r.loadTradeIdea(ctx, row.ideaID)
		if err != nil {
			return nil, err
		}
		risk, err := r.loadRiskDecision(ctx, row.riskID)
		if err != nil {
			return nil, err
		}
		if idea == nil || risk == nil {
			continue
		}
		result = append(result, domain.ApprovalRequest{
			ID: row.id, TradeIdea: *idea, RiskDecision: *risk, ExpiresAt: row.expiresAt, Status: row.status,
		})
	}
	return result, nil
}

// pendingApprovalRows returns pending approval rows expiring after now.
// Expiry is compared on parsed times rather than text so mixed formats work.
func (r *Repository) pendingApprovalRows(ctx context.Context, now time.Time) ([]pendingApprovalRow, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT id, trade_idea_id, risk_decision_id, expires_at, status FROM approval_requests WHERE status = 'pending'")
	if err != nil {
		return nil, fmt.Errorf("load pending approvals: %w", err)
	}
	defer rows.Close()

	var out []pendingApprovalRow
	for rows.Next() {
		var row pendingApprovalRow
		var ideaID, status sql.NullString
		var expires nullTime
		if err := rows.Scan(&row.id, &ideaID, &row.riskID, &expires, &status); err != nil {
			return nil, err
		}
		if !expires.Valid || !expires.Time.After(now) {
			continue
		}
		row.ideaID, row.status, row.expiresAt = ideaID.String, status.String, expires.Time
		out = append(out, row)
	}
	return out, rows.Err()
}

// loadTradeIdea rebuilds a domain.TradeIdea from trade_ideas, or nil if absent.
func (r *Repository) loadTradeIdea(ctx context.Context, id string) (*domain.TradeIdea, error) {
	var (
		idea                                         domain.TradeIdea
		strategy, market, direction, thesis          sql.NullString
		entryPlan, exitPlan, stop, supporting, modes sql.NullString
		confidence, sizing                           sql.NullFloat64
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT id, strategy_id, market, direction, thesis, supporting_signals, confidence,
			entry_plan, exit_plan, stop_or_invalidation, position_sizing_proposal, mode_eligibility
		FROM trade_ideas WHERE id = ?`, id,
	).Scan(&idea.ID, &strategy, &market, &direction, &thesis, &supporting, &confidence,
		&entryPlan, &exitPlan, &stop, &sizing, &modes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load trade idea %s: %w", id, err)
	}

	idea.StrategyID, idea.Market, idea.Thesis = strategy.String, market.String, thesis.String
	idea.Direction = domain.Direction(direction.String)
	idea.Confidence, idea.PositionSizingProposal = confidence.Float64, sizing.Float64
	idea.EntryPlan, idea.ExitPlan, idea.StopOrInvalidation = entryPlan.String, exitPlan.String, stop.String
	if err := decodeJSON(supporting, &idea.SupportingSignals); err != nil {
		return nil, err
	}
	idea.SupportingSignals = nonNilMap(idea.SupportingSignals)
	var modeNames []string
	if err := decodeJSON(modes, &modeNames); err != nil {
		return nil, err
	}
	idea.ModeEligibility = []domain.TradingMode{}
	for _, m := range modeNames {
		idea.ModeEligibility = append(idea.ModeEligibility, domain.TradingMode(m))
	}
	return &idea, nil
}

// loadRiskDecision rebuilds a domain.RiskDecision, or nil if absent.
func (r *Repository) loadRiskDecision(ctx context.Context, id sql.NullInt64) (*domain.RiskDecision, error) {
	if !id.Valid {
		return nil, nil
	}
	var (
		risk     domain.RiskDecision
		ideaID   sql.NullString
		reason   sql.NullString
		approved sql.NullBool
		sizing   sql.NullFloat64
		ts       nullTime
	)
	err := r.db.QueryRowContext(ctx,
		"SELECT trade_idea_id, approved, reason, adjusted_sizing, timestamp FROM risk_decisions WHERE id = ?", id.Int64,
	).Scan(&ideaID, &approved, &reason, &sizing, &ts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load risk decision %d: %w", id.Int64, err)
	}
	risk.TradeIdeaID, risk.Approved, risk.Reason = ideaID.String, approved.Bool, reason.String
	risk.AdjustedSizing = floatPtr(sizing)
	risk.Timestamp = timeOrNow(ts.orZero())
	return &risk, nil
}

// SaveOrder persists an order for the trade ledger. Rejected orders are
// redirected to the rejected-trades register instead.
func (r *Repository) SaveOrder(ctx context.Context, order domain.OrderRecord, opts SaveOrderOptions) error {
	if order.Status == "rejected" {
		reason := order.ExchangeOrderID
		if reason == "" {
			reason = "rejected"
		}
		return r.SaveRejectedTrade(ctx, RejectedTradeInput{
			Market: order.Market, Direction: string(order.Direction), Size: order.Size, Price: order.Price,
			Reason: reason, TradeIdeaID: opts.TradeIdeaID, Timestamp: order.Timestamp,
		})
	}

	env := opts.Environment
	if env == "" {
		env = string(domain.EnvPaper)
	}
	// Upsert every column except pnl, which UpdateOrderPnL stamps separately.
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO order_records (
			id, approval_request_id, market, direction, size, price, fee, status,
			environment, timestamp, exchange_order_id, position_id, trade_idea_id, client_order_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			approval_request_id = excluded.approval_request_id,
			market = excluded.market,
			direction = excluded.direction,
			size = excluded.size,
			price = excluded.price,
			fee = excluded.fee,
			status = excluded.status,
			environment = excluded.environment,
			timestamp = excluded.timestamp,
			exchange_order_id = excluded.exchange_order_id,
			position_id = excluded.position_id,
			trade_idea_id = excluded.trade_idea_id,
			client_order_id = excluded.client_order_id`,
		order.ID, nullIfEmpty(opts.ApprovalID), order.Market, string(order.Direction), order.Size, order.Price,
		opts.Fee, order.Status, env, formatDBTime(timeOrNow(order.Timestamp)),
		nullIfEmpty(order.ExchangeOrderID), nullIfEmpty(order.PositionID), nullIfEmpty(opts.TradeIdeaID),
		nullIfEmpty(opts.ClientOrderID),
	)
	if err != nil {
		return fmt.Errorf("save order %s: %w", order.ID, err)
	}
	return nil
}

// ledgerOrder is one order_records row as read for the trade ledger.
type ledgerOrder struct {
	id                                    string
	approvalID, market, direction, status sql.NullString
	environment, exchangeID               sql.NullString
	positionID, tradeIdeaID               sql.NullString
	size, price, fee, pnl                 sql.NullFloat64
	timestamp                             nullTime
}

// GetTradeLedger returns visible (filled/pending/submitted) orders newest first.
//
// trade_type is derived from each position's opening order across full
// history: the earliest order for a position_id opens it, any later one closes
// it. A close without its own signal inherits the opener's strategy.
func (r *Repository) GetTradeLedger(ctx context.Context, limit int) ([]LedgerEntry, error) {
	orders, err := r.ledgerOrders(ctx, orDefault(limit, DefaultLedgerLimit))
	if err != nil {
		return nil, err
	}

	openerID := map[string]string{}
	openerIdea := map[string]string{}
	for _, o := range orders {
		pos := o.positionID.String
		if pos == "" {
			continue
		}
		if _, done := openerID[pos]; done {
			continue
		}
		id, ideaID, err := r.positionOpener(ctx, pos)
		if err != nil {
			return nil, err
		}
		openerID[pos], openerIdea[pos] = id, ideaID
	}

	ideaIDs := []string{}
	for _, o := range orders {
		ideaIDs = append(ideaIDs, o.tradeIdeaID.String)
	}
	for _, id := range openerIdea {
		ideaIDs = append(ideaIDs, id)
	}
	strategyByIdea, err := r.strategiesForIdeas(ctx, ideaIDs)
	if err != nil {
		return nil, err
	}

	result := make([]LedgerEntry, 0, len(orders))
	for _, o := range orders {
		pos := o.positionID.String
		tradeType := "open" // no position_id → unmatched, treat as open
		if pos != "" && openerID[pos] != o.id {
			tradeType = "close"
		}
		strategyIdea := o.tradeIdeaID.String
		if strategyIdea == "" {
			strategyIdea = openerIdea[pos]
		}
		env := o.environment.String
		if env == "" {
			env = string(domain.EnvPaper)
		}
		result = append(result, LedgerEntry{
			ID:              short8(o.id),
			PositionID:      short8(pos),
			PositionIDFull:  pos,
			TradeIdeaID:     o.tradeIdeaID.String,
			Strategy:        strategyByIdea[strategyIdea],
			Market:          o.market.String,
			Direction:       o.direction.String,
			TradeType:       tradeType,
			Size:            o.size.Float64,
			Price:           o.price.Float64,
			Fee:             o.fee.Float64,
			Value:           o.size.Float64 * o.price.Float64,
			PnL:             floatPtr(o.pnl),
			Status:          o.status.String,
			Environment:     env,
			ExchangeOrderID: o.exchangeID.String,
			Source:          orderSource(o.approvalID.String),
			Timestamp:       o.timestamp.iso(),
		})
	}
	return result, nil
}

// ledgerOrders reads the newest visible orders.
func (r *Repository) ledgerOrders(ctx context.Context, limit int) ([]ledgerOrder, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, approval_request_id, market, direction, size, price, fee, status, environment,
			timestamp, exchange_order_id, position_id, pnl, trade_idea_id
		FROM order_records
		WHERE status IN ('filled', 'pending', 'submitted')
		ORDER BY timestamp DESC, rowid DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("get trade ledger: %w", err)
	}
	defer rows.Close()

	var out []ledgerOrder
	for rows.Next() {
		var o ledgerOrder
		if err := rows.Scan(&o.id, &o.approvalID, &o.market, &o.direction, &o.size, &o.price, &o.fee,
			&o.status, &o.environment, &o.timestamp, &o.exchangeID, &o.positionID, &o.pnl, &o.tradeIdeaID); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// positionOpener returns the ID and trade idea of a position's earliest order.
func (r *Repository) positionOpener(ctx context.Context, positionID string) (string, string, error) {
	var id string
	var ideaID sql.NullString
	err := r.db.QueryRowContext(ctx, `
		SELECT id, trade_idea_id FROM order_records
		WHERE position_id = ?
		ORDER BY timestamp ASC, rowid ASC
		LIMIT 1`, positionID).Scan(&id, &ideaID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("find opener for position %s: %w", positionID, err)
	}
	return id, ideaID.String, nil
}

// strategiesForIdeas maps trade idea IDs to their strategy_id (empty IDs ignored).
func (r *Repository) strategiesForIdeas(ctx context.Context, ids []string) (map[string]string, error) {
	unique := map[string]bool{}
	args := []any{}
	for _, id := range ids {
		if id != "" && !unique[id] {
			unique[id] = true
			args = append(args, id)
		}
	}
	result := map[string]string{}
	if len(args) == 0 {
		return result, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(args)), ", ")
	rows, err := r.db.QueryContext(ctx,
		"SELECT id, strategy_id FROM trade_ideas WHERE id IN ("+placeholders+")", args...)
	if err != nil {
		return nil, fmt.Errorf("look up strategies: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var strategy sql.NullString
		if err := rows.Scan(&id, &strategy); err != nil {
			return nil, err
		}
		result[id] = strategy.String
	}
	return result, rows.Err()
}

// orderSource labels how an order was initiated from its approval_request_id.
func orderSource(approvalID string) string {
	switch approvalID {
	case "stop_loss":
		return "stop-loss"
	case "auto":
		return "auto"
	case "":
		return "system"
	default:
		return "manual"
	}
}

// UpdateOrderPnL stamps the realised P&L onto a close order.
func (r *Repository) UpdateOrderPnL(ctx context.Context, orderID string, pnl float64) error {
	_, err := r.db.ExecContext(ctx, "UPDATE order_records SET pnl = ? WHERE id = ?", pnl, orderID)
	if err != nil {
		return fmt.Errorf("update order pnl %s: %w", orderID, err)
	}
	return nil
}

// GetPendingLiveOrders returns pending live orders awaiting reconciliation:
// those with a Kraken txid, and those known only by the client order id they
// were sent with (their AddOrder reply was lost).
func (r *Repository) GetPendingLiveOrders(ctx context.Context) ([]PendingLiveOrder, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, exchange_order_id, client_order_id, position_id, direction, size, price, timestamp FROM order_records
		WHERE status = 'pending' AND environment = 'live'
			AND (exchange_order_id IS NOT NULL OR client_order_id IS NOT NULL)`)
	if err != nil {
		return nil, fmt.Errorf("get pending live orders: %w", err)
	}
	defer rows.Close()

	result := []PendingLiveOrder{}
	for rows.Next() {
		var o PendingLiveOrder
		var txid, clientID, positionID, direction sql.NullString
		var size, price sql.NullFloat64
		var timestamp nullTime
		if err := rows.Scan(&o.ID, &txid, &clientID, &positionID, &direction, &size, &price, &timestamp); err != nil {
			return nil, err
		}
		o.ExchangeOrderID, o.ClientOrderID = txid.String, clientID.String
		o.PositionID, o.Direction = positionID.String, direction.String
		o.Size, o.Price = size.Float64, price.Float64
		o.Timestamp = timestamp.orZero()
		result = append(result, o)
	}
	return result, rows.Err()
}

// UpdateOrderStatusAndFill sets a live order's status, fill price and fee after reconciliation.
func (r *Repository) UpdateOrderStatusAndFill(ctx context.Context, orderID, status string, fillPrice, fee float64) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE order_records SET status = ?, price = ?, fee = ? WHERE id = ?", status, fillPrice, fee, orderID)
	if err != nil {
		return fmt.Errorf("update order fill %s: %w", orderID, err)
	}
	return nil
}

// UpdateOrderExecution records what reconciliation learned about a live
// order: status, average price and fee, the executed size (kept when 0) and
// the Kraken txid (kept when empty).
func (r *Repository) UpdateOrderExecution(ctx context.Context, e OrderExecution) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE order_records SET
			status = ?,
			price = ?,
			fee = ?,
			size = CASE WHEN ? > 0 THEN ? ELSE size END,
			exchange_order_id = COALESCE(?, exchange_order_id)
		WHERE id = ?`,
		e.Status, e.AvgPrice, e.Fee, e.ExecutedSize, e.ExecutedSize, nullIfEmpty(e.ExchangeOrderID), e.OrderID)
	if err != nil {
		return fmt.Errorf("update order execution %s: %w", e.OrderID, err)
	}
	return nil
}

// SaveFill records a fill against an order.
func (r *Repository) SaveFill(ctx context.Context, fill domain.FillRecord) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO fill_records (order_id, fill_price, fill_size, fee, timestamp) VALUES (?, ?, ?, ?, ?)",
		fill.OrderID, fill.FillPrice, fill.FillSize, fill.Fee, formatDBTime(timeOrNow(fill.Timestamp)))
	if err != nil {
		return fmt.Errorf("save fill for %s: %w", fill.OrderID, err)
	}
	return nil
}

// nonNilMap returns m, or an empty map when m is nil (Python's `x or {}`).
func nonNilMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// modeStrings converts trading modes to their stored string values.
func modeStrings(modes []domain.TradingMode) []string {
	out := make([]string, 0, len(modes))
	for _, m := range modes {
		out = append(out, string(m))
	}
	return out
}
