'use strict';
/*
 * Trading: open positions, closed positions, the trade ledger, rejected
 * trades, risk rejections and the P&L summary, one tab each (#/trading/<tab>).
 * Position actions (close, forget, reset) and the position helpers live here
 * and are also used by the Overview and Markets views.
 */

// The Trading tabs in display order; the first is the default.
const TRADING_TABS = ['positions', 'closed', 'ledger', 'rejected', 'risk', 'pnl'];

// Readable names for a closed position's exit_reason.
const EXIT_REASON_LABELS = {
    stop_loss: 'Stop Loss', auto: 'Auto', manual_approve: 'Manual',
    operator_reset: 'Reset', manual_reject: 'Rejected',
};

Dashboard.register('trading', {
    tradingTab: 'positions',
    ledger: resource([]),            // /api/trades
    closedTrades: resource([]),      // /api/closed-trades
    rejectedTrades: resource([]),    // /api/rejected-trades
    // Market (multi-select) and direction filters of the filterable tables
    tableFilters: {
        positions: { markets: [], direction: '' },
        closed: { markets: [], direction: '' },
        ledger: { markets: [], direction: '' },
    },

    // Loads the Trading data while the view is shown.
    initTrading() {
        this.pollWhileShown('trading', 15000, () => this.loadLedger());
        this.pollWhileShown('trading', 30000, () => this.loadClosedTrades());
        this.pollWhileShown('trading', 30000, () => this.loadRejectedTrades());
        this.pollWhileShown('trading', 30000, () => this.loadPnlSummary());
    },

    // Opens the tab named in the hash (#/trading/ledger); unknown names open Open positions.
    onTradingShown(arg) {
        this.tradingTab = TRADING_TABS.includes(arg) ? arg : 'positions';
    },

    // Selects a tab through the hash, so it can be bookmarked.
    showTradingTab(tab) {
        this.navigate('trading', tab);
    },

    // ── Loading ──────────────────────────────────────────────────────────────

    // Loads the trade ledger (newest first).
    loadLedger() {
        return this.fetchResource(this.ledger, '/api/trades');
    },

    // Loads the closed positions (newest first).
    loadClosedTrades() {
        return this.fetchResource(this.closedTrades, '/api/closed-trades');
    },

    // Loads the execution-level rejections (newest first).
    loadRejectedTrades() {
        return this.fetchResource(this.rejectedTrades, '/api/rejected-trades');
    },

    // ── Filters ──────────────────────────────────────────────────────────────

    // The rows of a table that pass its market and direction filters.
    filteredRows(table, rows) {
        const filter = this.tableFilters[table];
        return rows.filter(row =>
            (!filter.markets.length || filter.markets.includes(row.market)) &&
            (!filter.direction || row.direction === filter.direction));
    },

    // The distinct markets in a table's rows, sorted, for its filter chips.
    filterMarkets(rows) {
        return [...new Set(rows.map(row => row.market).filter(Boolean))].sort();
    },

    // Adds or removes a market from a table's market filter.
    toggleFilterMarket(table, market) {
        const markets = this.tableFilters[table].markets;
        this.tableFilters[table].markets = markets.includes(market)
            ? markets.filter(m => m !== market)
            : [...markets, market];
    },

    // ── Positions (also used by Overview and Markets) ───────────────────────

    // The ledger trade that opened a position, used when the position lacks a field.
    _positionOpeningTrade(position) {
        const fullId = position.position_id_full || '';
        const shortId = position.position_id || '';
        return this.ledger.data.find(t =>
            t.trade_type === 'open' &&
            ((fullId && t.position_id_full === fullId) || (shortId && t.position_id === shortId))
        );
    },

    // When a position was opened (raw timestamp), from the position or its opening trade.
    _positionOpenedAt(position) {
        const direct = position.opened_at || position.timestamp || position.entry_at || '';
        if (direct) return direct;
        return this._positionOpeningTrade(position)?.timestamp || '';
    },

    // The opening time as a Date, or null.
    positionOpenedDate(position) {
        const openedRaw = this._positionOpenedAt(position);
        return openedRaw ? parseUtc(openedRaw) : null;
    },

    // How long a position has been open ("3h 12m").
    positionAge(position) {
        const opened = this.positionOpenedDate(position);
        return opened ? fmtDuration(Date.now() - opened) : '—';
    },

    // The strategy that opened a position, as a readable label.
    _positionStrategy(position) {
        return position.strategy || position.strategy_id || this._positionOpeningTrade(position)?.strategy || '';
    },

    // Who opened a position (the paper engine records none, so 'system').
    _positionSource(position) {
        return position.source || this._positionOpeningTrade(position)?.source || 'system';
    },

    // The position's status: open, or for live rows entry_pending / exit_pending.
    _positionStatus(position) {
        return position.status || this._positionOpeningTrade(position)?.status || 'open';
    },

    // The status as words ("entry pending").
    positionStatusLabel(position) {
        return this._positionStatus(position).replace(/_/g, ' ');
    },

    // True for a real position on Kraken.
    isLivePosition(position) {
        return position.environment === 'live';
    },

    // Live positions cannot be closed while their buy or sell order is pending on Kraken.
    positionClosable(position) {
        return !this.isLivePosition(position) || position.status === 'open';
    },

    // Size × average entry price.
    positionValue(position) {
        return parseFloat(position.size) * parseFloat(position.avg_price_value ?? position.avg_price);
    },

    // ── Ledger and closed positions ─────────────────────────────────────────

    // Realised P&L of a close order as a percentage of its entry value, or null for opens.
    ledgerPnlPct(trade) {
        if (trade.pnl === null || trade.pnl === undefined) return null;
        const pnl = parseFloat(trade.pnl);
        // entry_value = close_value − pnl (since pnl = close_value − entry_value)
        const entryValue = parseFloat(trade.value) - pnl;
        return entryValue > 0.001 ? pnl / entryValue * 100 : 0;
    },

    // A closed position's exit reason as a readable label.
    closedSourceLabel(trade) {
        return EXIT_REASON_LABELS[trade.exit_reason] || (trade.exit_reason || '').replace(/_/g, ' ') || '—';
    },

    // How long a closed position was open.
    closedDuration(trade) {
        const exitDt = trade.exit_at ? parseUtc(trade.exit_at) : null;
        const entryDt = trade.entry_at ? parseUtc(trade.entry_at) : null;
        return exitDt && entryDt ? fmtDuration(exitDt - entryDt, true) : '—';
    },

    // Sorted [label, value] rows of a P&L summary map (newest day / largest market first).
    pnlRows(map, byDay) {
        const rows = Object.entries(map || {});
        return byDay
            ? rows.sort((a, b) => b[0].localeCompare(a[0]))
            : rows.sort((a, b) => Math.abs(b[1]) - Math.abs(a[1]));
    },

    // ── Position actions ─────────────────────────────────────────────────────

    // Closes a position at the market price after confirmation; live positions report the pending sell.
    async closePosition(positionIdFull) {
        if (!positionIdFull) return;
        if (!this._actionAllowedWhileStale()) return;
        if (!confirm('Close this position at the current market price?')) return;
        try {
            const r = await fetch(`/api/positions/${encodeURIComponent(positionIdFull)}/close`, { method: 'POST' });
            if (!r.ok) {
                this.notify('error', `Could not close position: ${await this._refusalDetail(r, r.statusText)}`);
                return;
            }
            const d = await r.json();
            if (d.status === 'closing') {
                // A live position: a real market sell was sent to Kraken; its fill and P&L arrive later.
                this.notify('info', `The sell order was sent to Kraken (reference price ${this.currencySymbol}${fmtPrice(d.fill_price)}). The P&L will appear once Kraken confirms the fill.`, true);
            } else {
                this.notify('success', `Position closed at ${this.currencySymbol}${fmtPrice(d.fill_price)}. Cash now ${this.currencySymbol}${fmtMoney(d.cash)}.`);
            }
            this.loadDashboard();
            if (this.view === 'trading') {
                this.loadLedger();
                this.loadClosedTrades();
            }
        } catch (e) {
            this.notify('error', `Could not close position — the server could not be reached (${e.message}).`);
        }
    },

    // Removes a live position from the bot's book only. Used when the holding
    // was already sold on Kraken (or is dust) and the bot can no longer close it.
    async forgetPosition(positionIdFull) {
        if (!positionIdFull) return;
        if (!this._actionAllowedWhileStale()) return;
        if (!confirm('Forget this live position?\n\nNo order is sent: the holding on Kraken is not touched. ' +
            'Only do this if it was already sold on Kraken or is too small to sell.')) return;
        try {
            const r = await fetch(`/api/positions/${encodeURIComponent(positionIdFull)}/forget`, { method: 'POST' });
            if (!r.ok) {
                this.notify('error', `Could not forget position: ${await this._refusalDetail(r, r.statusText)}`);
                return;
            }
            this.notify('success', 'Position removed from the bot. No order was sent.');
            this.loadDashboard();
        } catch (e) {
            this.notify('error', `Could not forget position — the server could not be reached (${e.message}).`);
        }
    },

    // Clears every open paper position after confirmation; a refusal shows the server's detail.
    async resetPositions() {
        if (!this._actionAllowedWhileStale()) return;
        if (!confirm('Clear ALL open positions? This removes stale positions from before the one-position-per-pair rule was enforced. Only use this in paper mode to clean up.')) return;
        try {
            const r = await fetch('/api/positions/reset', { method: 'POST' });
            const d = await r.json().catch(() => ({}));
            if (!r.ok) {
                this.notify('error', `Could not reset positions: ${d.detail || `HTTP ${r.status}`}`);
                return;
            }
            this.notify('success', `Cleared ${d.cleared} position(s).`);
            this.loadDashboard();
        } catch (e) {
            this.notify('error', `Could not reset positions — the server could not be reached (${e.message}).`);
        }
    },
});
