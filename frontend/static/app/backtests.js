'use strict';
/*
 * Backtests: a read-only viewer for the result folders written by
 * `go run ./cmd/backtest … --output results/<name>`.
 *
 *   #/backtests        the list of runs (newest first, filter by kind and text)
 *   #/backtests/<id>   one run; ids contain slashes for child runs
 *                      (split_btc/runs/01_in_sample)
 *
 * A result is easy to over-trust, so the detail page shows the backtester's
 * "Read this first" lines before any number, then its verdict exactly as
 * written, then the figures. Nothing here recomputes a metric or derives a
 * verdict: every value is read from the files (GET /api/backtests/{id}).
 * Nothing is ever written: the view only sends GET requests.
 */

// Rows per page of the trades and orders tables (server-side paging).
const BT_PAGE_SIZE = 100;

// Most equity points requested; the server keeps each bucket's low and high.
const BT_EQUITY_POINTS = 2000;

// The kinds a results folder can hold, in filter order.
const BT_KINDS = ['run', 'split', 'walkforward', 'regimes', 'sensitivity'];

// Verdict rules that state a positive result, the only ones shown in green:
// positive_not_validated and above_buy_and_hold (verdict.go),
// positive_in_and_out_of_sample (robustness.go, split) and
// positive_at_every_tested_fee (sensitivity.go). Descriptive rules such as
// test_windows_profitable or sign_survives_every_stress also fire for losing
// runs, so they stay neutral.
const BT_POSITIVE_RULES = ['positive_not_validated', 'above_buy_and_hold', 'positive_in_and_out_of_sample', 'positive_at_every_tested_fee'];

// Verdict rules that say the numbers themselves cannot be trusted as they stand.
const BT_WARNING_RULES = ['accounting_warning', 'data_coverage_warning', 'inconclusive_stale_positions'];

// The fixed "Read this first" lines every multi-run summary.md starts with
// (header() in internal/backtest/render_multi.go; a test keeps them in step).
const BT_MULTI_RUN_READ_FIRST = [
    'Every run here replays one historical path; robustness tests reduce, but do not remove, the chance that a result is luck or fitted to that path.',
    'Survivorship, fee tier and in-sample warnings of each run are in its own summary.md under runs/.',
];

// Columns of trades.csv shown in the Trades table, with how each is formatted.
const BT_TRADE_COLUMNS = [
    { key: 'position_id', label: 'Position', format: 'text' },
    { key: 'market', label: 'Market', format: 'text' },
    { key: 'entry_time', label: 'Entry (UTC)', format: 'time' },
    { key: 'exit_time', label: 'Exit (UTC)', format: 'time' },
    { key: 'holding_seconds', label: 'Held', format: 'duration' },
    { key: 'qty', label: 'Qty', format: 'qty' },
    { key: 'entry_fill_price', label: 'Entry fill', format: 'price' },
    { key: 'exit_fill_price', label: 'Exit fill', format: 'price' },
    { key: 'gross_pnl', label: 'Gross P&L', format: 'pnl' },
    { key: 'execution_cost', label: 'Exec. cost', format: 'money' },
    { key: 'fees', label: 'Fees', format: 'money' },
    { key: 'net_pnl', label: 'Net P&L', format: 'pnl' },
    { key: 'return_pct', label: 'Return', format: 'pnlPct' },
    { key: 'exit_reason', label: 'Exit reason', format: 'label' },
];

// Columns of orders.csv shown in the Orders table.
const BT_ORDER_COLUMNS = [
    { key: 'id', label: 'Order', format: 'text' },
    { key: 'market', label: 'Market', format: 'text' },
    { key: 'side', label: 'Side', format: 'label' },
    { key: 'reason', label: 'Reason', format: 'label' },
    { key: 'decision_time', label: 'Decision (UTC)', format: 'time' },
    { key: 'fill_time', label: 'Fill (UTC)', format: 'time' },
    { key: 'requested_qty', label: 'Requested', format: 'qty' },
    { key: 'filled_qty', label: 'Filled', format: 'qty' },
    { key: 'fill_price', label: 'Fill price', format: 'price' },
    { key: 'fee', label: 'Fee', format: 'money' },
    { key: 'realised_pnl', label: 'Realised P&L', format: 'pnl' },
    { key: 'status', label: 'Status', format: 'label' },
    { key: 'reject_reason', label: 'Reject reason', format: 'label' },
];

// The single equity chart. Kept outside Alpine so Chart.js is not wrapped in a reactive proxy.
let backtestChart = null;

// ── URL helpers (global, usable in Alpine expressions) ───────────────────────

// Encodes a backtest id for a URL path: each segment on its own, joined with '/'.
function backtestPath(id) {
    return String(id).split('/').map(encodeURIComponent).join('/');
}

// The API URL of a run, with optional query parameters ({ part: 'trades', limit: 100 }).
function backtestUrl(id, params) {
    const query = params ? '?' + new URLSearchParams(params).toString() : '';
    return '/api/backtests/' + backtestPath(id) + query;
}

// The in-page link to a run's detail (a hash route, never a server URL).
function backtestHref(id) {
    return '#/backtests/' + backtestPath(id);
}

// ── Formatting helpers ───────────────────────────────────────────────────────

// Formats a value from the files. null or undefined is a metric the backtester
// did not report (its JSON carries the reason), so it shows 'n/a', never 0.
function btValue(value, format) {
    if (value === null || value === undefined) return 'n/a';
    if (value === '') return '—';                     // an empty text cell of a CSV file
    const v = Number(value);
    switch (format) {
        case 'text': return String(value);
        case 'label': return String(value).replace(/_/g, ' ');
        case 'time': return btUtcTime(value);
        case 'date': return btUtcDate(value);
        case 'duration': return Number.isFinite(v) ? fmtDuration(v * 1000) : String(value);
        default: break;
    }
    if (!Number.isFinite(v)) return String(value);   // a non-numeric cell is shown as written
    switch (format) {
        case 'money': return fmtMoney(v);
        case 'pnl':
        case 'signedMoney': return `${v < 0 ? '−' : '+'}${fmtMoney(Math.abs(v))}`;
        case 'pct': return `${v.toFixed(2)}%`;
        case 'pnlPct':
        case 'signedPct': return fmtSignedPct(v);
        case 'pp': return `${v < 0 ? '−' : '+'}${Math.abs(v).toFixed(2)} pp`;
        case 'ratio': return v.toFixed(3);
        case 'int': return v.toLocaleString('en-GB', { maximumFractionDigits: 0 });
        case 'qty': return v.toLocaleString('en-GB', { maximumFractionDigits: 8 });
        case 'price': return fmtPrice(v);
        case 'bp': return `${v.toLocaleString('en-GB', { maximumFractionDigits: 2 })} bp`;
        default: return String(value);
    }
}

// The CSS class of a table cell: P&L columns are coloured by sign, numbers right-aligned.
function btCellClass(row, column) {
    const numeric = !['text', 'label', 'time'].includes(column.format);
    const signed = column.format === 'pnl' || column.format === 'pnlPct';
    return [numeric ? 'num' : '', signed ? pnlClass(row[column.key]) : ''].join(' ');
}

// A UTC timestamp as "2026-01-03 09:15" (backtests run on UTC throughout).
function btUtcTime(value) {
    const date = parseUtc(value);
    return Number.isNaN(date.getTime()) ? String(value) : date.toISOString().slice(0, 16).replace('T', ' ');
}

// A UTC timestamp as its date, "2026-01-03".
function btUtcDate(value) {
    const date = parseUtc(value);
    return Number.isNaN(date.getTime()) ? String(value) : date.toISOString().slice(0, 10);
}

// A period as "2026-01-03 → 2026-01-15" (the end is exclusive, as the files record it).
function btPeriod(start, end) {
    return `${btValue(start, 'date')} → ${btValue(end, 'date')}`;
}

// The class of a verdict line: green only for a positive rule, amber for a rule
// that says the numbers cannot be trusted, neutral for everything else.
function btVerdictClass(rule) {
    if (BT_POSITIVE_RULES.includes(rule)) return 'is-positive';
    if (BT_WARNING_RULES.includes(rule)) return 'is-warning';
    return '';
}

// True for an honesty line the backtester marks as a caveat with an upper-case
// prefix ("NOT THE LIVE STRATEGY:", "ACCOUNTING WARNING:", "DATA WARNING:",
// "DATA:", "STALE POSITIONS:", "RISK SIZING:"); these are styled as warnings.
function btIsWarningLine(line) {
    return /^[A-Z][A-Z -]{2,}:/.test(String(line));
}

// The pill class of an assumption's bias.
function btBiasClass(bias) {
    return { pessimistic: 'pill-info', optimistic: 'pill-warning', unknown: 'pill-outline' }[bias] || '';
}

// The warnings a multi-run row (RunRow) carries, as short labels of its fields.
function btRowFlags(row) {
    if (!row) return [];
    const flags = [];
    if (row.reconcile_warning) flags.push('ACCOUNTING WARNING');
    if (row.data_warnings > 0) flags.push(`DATA WARNING: ${row.data_warnings} data warning(s)`);
    if (row.stale_decides_sign) flags.push('STALE POSITIONS decide the sign of the result');
    else if (row.stale_positions > 0) flags.push(`${row.stale_positions} stale position(s)`);
    return flags;
}

// The child directory name RunDirName (render_multi.go) gives a run label: characters
// other than letters, digits, '.', '-', '_' and '=' become '_'.
function btRunDirLabel(label) {
    return String(label ?? '').replace(/[^A-Za-z0-9.\-_=]/g, '_');
}

// A fresh holder for one request of the view (see resource() in core.js) plus the HTTP status.
function btResource() {
    return { ...resource(null), status: 0 };
}

Dashboard.register('backtests', {
    backtestList: btResource(),      // GET /api/backtests
    btKinds: BT_KINDS,
    btKindFilter: '',
    btSearch: '',
    btExpanded: [],                  // ids of multi-run parents whose children are shown

    btDetailId: '',                  // the run shown, '' on the list
    btDetail: btResource(),          // GET /api/backtests/{id}
    btEquity: btResource(),          // ?part=equity
    btTrades: btResource(),          // ?part=trades
    btOrders: btResource(),          // ?part=orders
    btSummaryMd: btResource(),       // ?part=summary_md (text)
    btRowsTab: 'trades',             // 'trades' | 'orders'
    btTradesOffset: 0,
    btOrdersOffset: 0,
    btOrdersStatus: '',              // '' | 'filled' | 'rejected' | 'cancelled'
    btSort: { key: '', desc: false },// client-side sort of the shown page
    btRegimeTable: 'cells',          // 'cells' | 'by_trend' | 'by_volatility'
    btTradeColumns: BT_TRADE_COLUMNS,
    btOrderColumns: BT_ORDER_COLUMNS,

    // Nothing to start: the view loads only when it is shown.
    initBacktests() {},

    // Shows the list (refreshed on every entry) or the run named in the hash.
    onBacktestsShown(arg) {
        if (!arg) {
            this._resetBacktestDetail();
            this.loadBacktests();
            return;
        }
        if (arg !== this.btDetailId) this.openBacktestDetail(arg);
    },

    // Leaving the view drops the shown run, its chart and any request still in flight.
    onBacktestsHidden() {
        this._resetBacktestDetail();
    },

    // ── Requests ─────────────────────────────────────────────────────────────

    // Loads url into res. Every request is numbered and a response that is not the
    // newest for res (another run was opened, or the run was left) is dropped.
    // Returns true when the response was applied.
    async _btFetch(res, url, as = 'json') {
        const seq = ++res.seq;
        if (res.state !== 'ready') res.state = 'loading';
        try {
            const r = await fetch(url);
            const body = r.ok ? (as === 'text' ? await r.text() : await r.json()) : null;
            const refusal = r.ok ? '' : await this._refusalDetail(r, `HTTP ${r.status}`);
            if (seq !== res.seq) return false;
            res.status = r.status;
            if (!r.ok) {
                res.error = refusal;
                if (!res.at) res.state = 'error';
                return true;
            }
            res.data = body;
            res.state = 'ready';
            res.error = '';
            res.at = new Date();
            return true;
        } catch (e) {
            if (seq !== res.seq) return false;
            console.error(url, e);
            res.error = e.message;
            if (!res.at) res.state = 'error';
            return true;
        }
    },

    // Loads (or refreshes) the list of runs.
    loadBacktests() {
        return this._btFetch(this.backtestList, '/api/backtests');
    },

    // Forgets the shown run: the chart is destroyed and late responses are dropped.
    _resetBacktestDetail() {
        this._destroyBacktestChart();
        for (const res of [this.btDetail, this.btEquity, this.btTrades, this.btOrders, this.btSummaryMd]) {
            res.seq++;   // any response still in flight no longer matches
            Object.assign(res, { state: 'idle', error: '', at: null, data: null, status: 0 });
        }
        this.btDetailId = '';
    },

    // Opens one run: its stored JSON first, then the parts its kind has.
    async openBacktestDetail(id) {
        this._resetBacktestDetail();
        this.btDetailId = id;
        this.btRowsTab = 'trades';
        this.btTradesOffset = 0;
        this.btOrdersOffset = 0;
        this.btOrdersStatus = '';
        this.btSort = { key: '', desc: false };
        this.btRegimeTable = 'cells';
        window.scrollTo(0, 0);   // a run opened from another run's page starts at its top
        if (await this._btFetch(this.btDetail, backtestUrl(id))) this._afterBacktestDetail(id);
    },

    // Loads the parts of a run once its JSON arrived; a run that no longer exists
    // sends the user back to the list with a message.
    _afterBacktestDetail(id) {
        if (id !== this.btDetailId) return;
        if (this.btDetail.status === 404) {
            this.notify('warning', `Backtest "${id}" was not found; it may have been moved or deleted.`);
            history.replaceState(null, '', '#/backtests');
            this._applyRoute(false);
            return;
        }
        if (this.btDetail.state !== 'ready') return;
        if (this.btShape === 'run') {
            this.loadBacktestEquity();
            this.loadBacktestTrades();
        } else if (this.btShape === 'unknown') {
            this.loadBacktestSummaryMd();   // the fallback shows the raw summary
        }
    },

    // Loads the equity curve and draws it.
    async loadBacktestEquity() {
        const id = this.btDetailId;
        if (await this._btFetch(this.btEquity, backtestUrl(id, { part: 'equity', max_points: BT_EQUITY_POINTS }))) {
            this.$nextTick(() => this._renderBacktestChart());
        }
    },

    // Loads one page of trades.csv.
    loadBacktestTrades() {
        const params = { part: 'trades', limit: BT_PAGE_SIZE, offset: this.btTradesOffset };
        return this._btFetch(this.btTrades, backtestUrl(this.btDetailId, params));
    },

    // Loads one page of orders.csv, filtered by status when one is chosen.
    loadBacktestOrders() {
        const params = { part: 'orders', limit: BT_PAGE_SIZE, offset: this.btOrdersOffset };
        if (this.btOrdersStatus) params.status = this.btOrdersStatus;
        return this._btFetch(this.btOrders, backtestUrl(this.btDetailId, params));
    },

    // Loads summary.md as plain text (shown preformatted, never rendered).
    loadBacktestSummaryMd() {
        if (!this.btDetailId || this.btSummaryMd.state === 'ready' || this.btSummaryMd.state === 'loading') return;
        this._btFetch(this.btSummaryMd, backtestUrl(this.btDetailId, { part: 'summary_md' }), 'text');
    },

    // ── List ─────────────────────────────────────────────────────────────────

    // The runs that pass the kind and text filters, newest first.
    filteredBacktests() {
        const rows = this.backtestList.data?.backtests || [];
        const text = this.btSearch.trim().toLowerCase();
        return rows
            .filter(b => !this.btKindFilter || b.kind === this.btKindFilter)
            .filter(b => !text || [b.id, b.strategy, b.verdict, ...(b.markets || [])]
                .some(v => String(v ?? '').toLowerCase().includes(text)))
            .sort((a, b) => parseUtc(b.modified) - parseUtc(a.modified));
    },

    // Opens a run's detail through the hash, so the back button returns to where the user was.
    openBacktest(id) {
        if (id) location.hash = backtestHref(id);
    },

    // Shows or hides a multi-run parent's child runs.
    toggleBacktestChildren(id) {
        this.btExpanded = this.btExpanded.includes(id)
            ? this.btExpanded.filter(x => x !== id)
            : [...this.btExpanded, id];
    },

    // The last segment of an id ("split_btc/runs/01_in_sample" → "01_in_sample").
    btLeaf(id) {
        return String(id).split('/').pop();
    },

    // A list row's markets, shortened when there are many.
    btMarketsLabel(markets) {
        const list = markets || [];
        return list.length > 3 ? `${list.slice(0, 3).join(', ')} +${list.length - 3}` : list.join(', ') || '—';
    },

    // ── Detail: shared ───────────────────────────────────────────────────────

    // The shown run's summary.json (an empty object until it is loaded).
    get btSummary() {
        return this.btDetail.data?.summary || {};
    },

    // The shown run's run.json (null when the file is missing).
    get btRun() {
        return this.btDetail.data?.run || null;
    },

    // The shown run's metrics block (single runs).
    get btMetrics() {
        return this.btSummary.metrics || {};
    },

    // Which layout the summary has. A kind whose fields are not the expected
    // ones is 'unknown' and falls back to the honesty block, verdict and raw text.
    get btShape() {
        const s = this.btSummary;
        switch (this.btDetail.data?.kind) {
            case 'run': return s.metrics ? 'run' : 'unknown';
            case 'split': return s.in_sample && s.out_of_sample ? 'split' : 'unknown';
            case 'walkforward': return Array.isArray(s.windows) && s.aggregate ? 'walkforward' : 'unknown';
            case 'regimes': return Array.isArray(s.cells) && s.run ? 'regimes' : 'unknown';
            case 'sensitivity': return Array.isArray(s.rows) && s.baseline ? 'sensitivity' : 'unknown';
            default: return 'unknown';
        }
    },

    // True for the multi-run kinds this view lays out.
    get btIsMultiRun() {
        return ['split', 'walkforward', 'regimes', 'sensitivity'].includes(this.btShape);
    },

    // The quote currency of the run's settings (amounts are shown in it).
    get btCurrency() {
        return this.btRun?.settings?.BASE_CURRENCY || '';
    },

    // The id of the multi-run parent of a child run, or ''.
    get btParentId() {
        const i = this.btDetailId.lastIndexOf('/runs/');
        return i > 0 ? this.btDetailId.slice(0, i) : '';
    },

    // The "Read this first" lines as { text, warn }: a single run's honesty block as
    // written, or for a multi-run summary the warnings of its runs' rows first, then
    // the fixed lines every multi-run summary.md starts with and the summary's notes.
    get btHonestyLines() {
        const s = this.btSummary;
        const notes = Array.isArray(s.notes) ? s.notes : [];
        if (Array.isArray(s.honesty?.lines)) return s.honesty.lines.map(text => ({ text, warn: btIsWarningLine(text) }));
        if (this.btDetail.data?.kind === 'run') return notes.map(text => ({ text, warn: btIsWarningLine(text) }));
        const warnings = this.btMultiRunRows
            .map(row => ({ row, flags: btRowFlags(row) }))
            .filter(x => x.flags.length)
            .map(x => ({ text: `Run ${x.row.label}: ${x.flags.join('; ')}.`, warn: true }));
        return [...warnings, ...[...BT_MULTI_RUN_READ_FIRST, ...notes].map(text => ({ text, warn: false }))];
    },

    // The verdict lines (rule and text), or none.
    get btVerdict() {
        return Array.isArray(this.btSummary.verdict) ? this.btSummary.verdict : [];
    },

    // Every RunRow of a multi-run summary (for its warnings).
    get btMultiRunRows() {
        const s = this.btSummary;
        switch (this.btShape) {
            case 'split': return [s.in_sample, s.out_of_sample];
            case 'walkforward': return s.windows.flatMap(w => [...(w.train || []), w.test]).filter(Boolean);
            case 'regimes': return [s.run];
            case 'sensitivity': return [s.baseline, ...s.rows.map(r => r.result)].filter(Boolean);
            default: return [];
        }
    },

    // The child run written for a row label, or '' when none exists.
    btChildFor(label) {
        const wanted = btRunDirLabel(label);
        const children = this.btDetail.data?.children || [];
        return children.find(c => this.btLeaf(c).replace(/^\d+_/, '') === wanted) || '';
    },

    // ── Detail: single run ───────────────────────────────────────────────────

    // The key figures of a single run, in reading order. A null metric shows
    // 'n/a' and the note the backtester gave for it.
    get btFigures() {
        const m = this.btMetrics;
        const cur = this.btCurrency;
        const dd = m.max_drawdown;
        const ddAmount = m.max_drawdown_amount;
        const figures = [
            { label: 'Starting capital', value: btValue(m.initial_capital, 'money'), meta: cur },
            { label: 'Ending equity', value: btValue(m.final_value, 'money'), meta: cur },
            {
                label: 'Net P&L', value: btValue(m.net_pnl, 'signedMoney'), cls: pnlClass(m.net_pnl),
                meta: `${btValue(m.net_return_pct, 'signedPct')} of starting capital`,
            },
        ];
        if (m.stale_positions > 0) {
            figures.push({
                label: 'Net P&L, stale positions at zero', value: btValue(m.net_pnl_worst_case, 'signedMoney'),
                cls: pnlClass(m.net_pnl_worst_case), warn: true,
                meta: `${btValue(m.net_return_pct_worst_case, 'signedPct')} · ${m.stale_at_end} stale at the end`,
            });
        }
        figures.push(
            { label: 'Fees', value: btValue(m.total_fees, 'money'), meta: cur },
            { label: 'Execution costs', value: btValue(m.total_execution_cost, 'money'), meta: 'drift + spread + impact + rounding' },
            {
                label: 'Round trips', value: btValue(m.round_trips, 'int'),
                meta: `${m.wins} won · ${m.losses} lost · ${m.breakeven} even · ${m.end_of_run_closes} valued at the end`,
            },
            { label: 'Win rate', value: btValue(m.win_rate_pct, 'pct') },
            { label: 'Profit factor', value: btValue(m.profit_factor, 'ratio'), note: m.profit_factor_note },
            {
                // The backtester writes "none" for a run without a drawdown episode.
                label: 'Max drawdown', value: ddAmount ? btValue(ddAmount.amount, 'money') : 'none',
                meta: dd ? `${btValue(dd.pct, 'pct')} (deepest by %)` : '',
            },
            { label: 'Exposure', value: btValue(m.exposure_pct, 'pct'), meta: 'time with a position' },
            { label: 'Annualised return', value: btValue(m.annualised_return_pct, 'signedPct'), note: m.annualised_note },
            { label: 'Sharpe', value: btValue(m.sharpe, 'ratio'), note: m.ratio_note },
        );
        return figures;
    },

    // The benchmarks beside the strategy: return, max drawdown and the strategy's excess over each.
    get btBenchmarks() {
        const set = this.btSummary.benchmarks || {};
        return (set.benchmarks || []).map(b => ({ ...b, primary: b.name === set.primary }));
    },

    // The gross → net bridge as [label, amount, format] rows.
    get btBridgeRows() {
        const b = this.btSummary.bridge || {};
        return [
            ['Gross P&L', b.gross_pnl, 'pnl'],
            ['− latency drift', b.latency_drift, 'money'],
            ['− spread', b.spread_cost, 'money'],
            ['− size impact', b.impact_cost, 'money'],
            ['− price rounding', b.rounding_cost, 'money'],
            ['− unexplained residual', b.residual, 'raw'],
            ['− fees', b.fees, 'money'],
            ['= net P&L (order log)', b.net_pnl, 'pnl'],
            ['Net P&L (engine)', b.engine_net_pnl, 'pnl'],
            ['Engine − order log', b.difference, 'raw'],
        ];
    },

    // The costs file (costs.json), else the summary's copy of it.
    get btCosts() {
        return this.btDetail.data?.costs || this.btSummary.costs || null;
    },

    // The reconciliation's exact-versus-engine comparisons as [label, exact, engine, difference].
    get btReconRows() {
        const r = this.btSummary.reconciliation || {};
        return [
            ['Final cash', r.exact_final_cash, r.engine_final_cash, r.cash_difference],
            ['Final equity', r.exact_final_equity, r.engine_final_equity, r.equity_difference],
            ['Fees', r.exact_fees, r.engine_fees, r.fees_difference],
            ['Net P&L (round trips)', r.exact_net_pnl, r.trades_net_pnl, r.trades_difference],
        ];
    },

    // A count map ({reason: n}) as sorted [reason, n] rows.
    btCounts(map) {
        return Object.entries(map || {}).sort((a, b) => a[0].localeCompare(b[0]));
    },

    // run.json's settings as sorted [key, value] rows.
    get btSettings() {
        return Object.entries(this.btRun?.settings || {}).sort((a, b) => a[0].localeCompare(b[0]));
    },

    // run.json's exchange model as [key, value] rows (pair rules shown by their source).
    get btExchange() {
        return Object.entries(this.btRun?.exchange || {}).map(([key, value]) =>
            key === 'pair_rules' ? [key, value?.source || 'built-in fallback for every pair'] : [key, String(value)]);
    },

    // run.json's command line as one string.
    get btCommandLine() {
        const cmd = this.btRun?.command_line;
        return Array.isArray(cmd) && cmd.length ? cmd.join(' ') : '';
    },

    // ── Detail: trades and orders ────────────────────────────────────────────

    // Shows the Trades or Orders table; orders load the first time they are shown.
    showBacktestRows(tab) {
        this.btRowsTab = tab;
        this.btSort = { key: '', desc: false };
        if (tab === 'orders' && this.btOrders.state === 'idle') this.loadBacktestOrders();
    },

    // Filters the orders by status and reloads from the first page.
    setBacktestOrderStatus(status) {
        this.btOrdersStatus = status;
        this.btOrdersOffset = 0;
        this.loadBacktestOrders();
    },

    // The resource of the table shown (trades or orders).
    get btShownRows() {
        return this.btRowsTab === 'trades' ? this.btTrades : this.btOrders;
    },

    // The page offset of the table shown.
    get _btShownOffset() {
        return this.btRowsTab === 'trades' ? this.btTradesOffset : this.btOrdersOffset;
    },

    // True when the shown table has a page in that direction (-1 back, +1 forward).
    btCanPage(step) {
        const next = this._btShownOffset + step * BT_PAGE_SIZE;
        return next >= 0 && next < (this.btShownRows.data?.total ?? 0);
    },

    // Moves the shown table one page back (-1) or forward (+1).
    pageBacktestRows(step) {
        if (!this.btCanPage(step)) return;
        const next = this._btShownOffset + step * BT_PAGE_SIZE;
        this.btSort = { key: '', desc: false };
        if (this.btRowsTab === 'trades') {
            this.btTradesOffset = next;
            this.loadBacktestTrades();
        } else {
            this.btOrdersOffset = next;
            this.loadBacktestOrders();
        }
    },

    // "1–100 of 1,234" for a loaded page (with a failed page change appended), else the load state.
    btPageLabel(res) {
        const d = res.data;
        if (res.state !== 'ready' || !d) return this.resourceStatus(res, '');
        const failed = res.error ? ` · could not load the page: ${res.error}` : '';
        if (!d.total) return '0 rows' + failed;
        const first = d.offset + 1;
        const last = d.offset + d.rows.length;
        return `${first.toLocaleString('en-GB')}–${last.toLocaleString('en-GB')} of ${d.total.toLocaleString('en-GB')}${failed}`;
    },

    // Sorts the shown page by a column; a second click reverses the order.
    sortBacktestRows(key) {
        this.btSort = { key, desc: this.btSort.key === key ? !this.btSort.desc : false };
    },

    // The rows of the shown page in the chosen order (the page itself comes from the server).
    btSortedRows(res) {
        const rows = [...(res.data?.rows || [])];
        const { key, desc } = this.btSort;
        if (!key) return rows;
        return rows.sort((a, b) => {
            const x = a[key];
            const y = b[key];
            const order = typeof x === 'number' && typeof y === 'number' ? x - y : String(x ?? '').localeCompare(String(y ?? ''));
            return desc ? -order : order;
        });
    },

    // The aria-sort value of a sortable column header.
    btAriaSort(key) {
        if (this.btSort.key !== key) return 'none';
        return this.btSort.desc ? 'descending' : 'ascending';
    },

    // ── Detail: equity chart ─────────────────────────────────────────────────

    // Destroys the equity chart, if one exists.
    _destroyBacktestChart() {
        if (backtestChart) {
            backtestChart.destroy();
            backtestChart = null;
        }
    },

    // Draws equity (top) and drawdown (beneath, subdued) from part=equity, replacing any chart drawn before.
    _renderBacktestChart() {
        this._destroyBacktestChart();
        const data = this.btEquity.data;
        const canvas = document.getElementById('bt-equity-chart');
        if (!data || !canvas || typeof Chart === 'undefined' || this.view !== 'backtests') return;
        const col = name => data.columns.indexOf(name);
        const [timeCol, equityCol, ddCol] = [col('time'), col('equity'), col('drawdown_pct')];
        if (timeCol < 0 || equityCol < 0) return;
        const labels = data.points.map(p => btUtcTime(p[timeCol]));
        const grid = cssVar('--border');
        const muted = cssVar('--text-muted');
        const ticks = { color: muted, font: { size: 11 } };
        backtestChart = new Chart(canvas.getContext('2d'), {
            type: 'line',
            data: {
                labels,
                datasets: [
                    {
                        label: 'Equity', data: data.points.map(p => p[equityCol]), yAxisID: 'y',
                        borderColor: cssVar('--accent'), borderWidth: 1.5, pointRadius: 0, pointHitRadius: 8, tension: 0,
                    },
                    {
                        // Drawdown below the running peak, drawn downwards (0 at the top).
                        label: 'Drawdown', data: ddCol < 0 ? [] : data.points.map(p => (p[ddCol] === null ? null : -p[ddCol])),
                        yAxisID: 'yDrawdown', borderColor: cssVar('--text-muted'), backgroundColor: 'rgba(125, 135, 153, 0.18)',
                        borderWidth: 1, pointRadius: 0, pointHitRadius: 8, tension: 0, fill: 'origin',
                    },
                ],
            },
            options: {
                responsive: true,
                maintainAspectRatio: false,
                animation: false,
                interaction: { mode: 'index', intersect: false },
                plugins: {
                    legend: { display: false },
                    tooltip: {
                        backgroundColor: cssVar('--surface-overlay'),
                        borderColor: cssVar('--border-strong'),
                        borderWidth: 1,
                        titleColor: cssVar('--text'),
                        bodyColor: cssVar('--text-secondary'),
                        callbacks: {
                            title: items => (items.length ? `${items[0].label} UTC` : ''),
                            label: c => (c.dataset.yAxisID === 'y'
                                ? `Equity ${fmtMoney(c.parsed.y)} ${this.btCurrency}`.trim()
                                : `Drawdown ${Math.abs(c.parsed.y).toFixed(2)}%`),
                        },
                    },
                },
                scales: {
                    x: { grid: { color: grid }, ticks: { ...ticks, maxTicksLimit: 6, maxRotation: 0 } },
                    // Two stacked axes in one chart: Chart.js stacks the first-declared axis at
                    // the bottom, so drawdown (a quarter of the height) comes before equity.
                    yDrawdown: {
                        stack: 'bt', stackWeight: 1, offset: true, max: 0,
                        grid: { color: grid }, ticks: { ...ticks, maxTicksLimit: 3, callback: v => `${Math.abs(v).toFixed(1)}%` },
                    },
                    y: { stack: 'bt', stackWeight: 3, offset: true, grid: { color: grid }, ticks: { ...ticks, callback: v => fmtMoney(v) } },
                },
            },
        });
    },

    // ── Detail: multi-run kinds ──────────────────────────────────────────────

    // The split summary side by side: [label, in-sample, out-of-sample, format] rows.
    get btSplitRows() {
        const is = this.btSummary.in_sample || {};
        const oos = this.btSummary.out_of_sample || {};
        const row = (label, key, format) => [label, btValue(is[key], format), btValue(oos[key], format), format];
        return [
            ['Period', btPeriod(is.start, is.end), btPeriod(oos.start, oos.end), 'text'],
            row('Days', 'days', 'int'),
            row('Final value', 'final_value', 'money'),
            row('Net P&L', 'net_pnl', 'pnl'),
            row('Net return', 'net_return_pct', 'pnlPct'),
            row('Gross P&L', 'gross_pnl', 'pnl'),
            row('Fees', 'fees', 'money'),
            row('Execution cost', 'execution_cost', 'money'),
            row('Round trips', 'round_trips', 'int'),
            row('Win rate', 'win_rate_pct', 'pct'),
            row('Profit factor', 'profit_factor', 'ratio'),
            row('Max drawdown', 'max_drawdown_pct', 'pct'),
            ['Benchmark', btValue(is.benchmark, 'text'), btValue(oos.benchmark, 'text'), 'text'],
            row('Benchmark return', 'benchmark_return_pct', 'signedPct'),
            row('Excess over benchmark', 'excess_return_pct', 'pp'),
            ['Warnings', btRowFlags(is).join('; ') || 'none', btRowFlags(oos).join('; ') || 'none', 'text'],
        ];
    },

    // The class of a split cell: P&L rows are coloured by the sign of their value.
    btSplitCellClass(row, side) {
        const key = { 'Net P&L': 'net_pnl', 'Net return': 'net_return_pct', 'Gross P&L': 'gross_pnl' }[row[0]];
        const source = side === 0 ? this.btSummary.in_sample : this.btSummary.out_of_sample;
        return [row[3] === 'text' ? '' : 'num', key ? pnlClass(source?.[key]) : ''].join(' ');
    },

    // The regime table chosen with the chips (cells, by trend or by volatility).
    btRegimeCells() {
        const rows = this.btSummary[this.btRegimeTable];
        return Array.isArray(rows) ? rows : [];
    },

    // The sensitivity variants, baseline first as the backtester lists it in summary.md.
    get btSensitivityRows() {
        const s = this.btSummary;
        const baseline = { dimension: 'baseline', value: '', baseline: true, run: s.baseline?.label, fee_bps: s.baseline_fee_bps, result: s.baseline, sign_survives: null };
        return [baseline, ...(s.rows || [])];
    },
});
