'use strict';
/*
 * Overview: is the bot running, in which mode and environment, what is the
 * account worth, what is open and what needs attention. Key figures, the
 * equity graph, open positions, pending approvals, latest signals and a
 * short activity strip. Approve / reject live here.
 */

Dashboard.register('overview', {
    approvalBusy: {},            // approval id → true while its decision is being sent
    _equityTimestamps: [],       // timestamps of the graph's points, for the tooltip

    // Starts the P&L summary poll (today's realised P&L) while the Overview is shown.
    initOverview() {
        this.pollWhileShown('overview', 30000, () => this.loadPnlSummary());
    },

    // Scrolls to the pending approvals when the header link (#/overview/approvals) was used.
    onOverviewShown(arg) {
        if (arg !== 'approvals') return;
        this.$nextTick(() => document.getElementById('approvals-panel')?.scrollIntoView({ block: 'start' }));
    },

    // Redraws the equity graph after each dashboard response.
    onOverviewData(d) {
        this.renderChart(d.equity_history || []);
    },

    // ── Key figures ──────────────────────────────────────────────────────────

    // Realised P&L of closed trades whose exit falls on today's UTC date (the
    // P&L summary groups by the exit timestamp's date); null until it is loaded.
    realisedToday() {
        const byDay = this.pnlSummary.data?.by_day;
        if (!byDay) return null;
        return Number(byDay[new Date().toISOString().slice(0, 10)] || 0);
    },

    // Sum of the open positions' unrealised P&L.
    unrealisedTotal() {
        return this.positions.reduce((sum, p) => sum + (parseFloat(p.unrealized_pnl) || 0), 0);
    },

    // Equity change across the graph's history: { amount, pct, since } or null with fewer than two points.
    equityChange() {
        const points = this.equityHistory.filter(h => Number.isFinite(Number(h.equity)));
        if (points.length < 2) return null;
        const first = Number(points[0].equity);
        const last = Number(points[points.length - 1].equity);
        return {
            amount: last - first,
            pct: first ? (last - first) / first * 100 : 0,
            since: parseUtc(points[0].timestamp),
        };
    },

    // The one-line state of the bot shown under the Overview heading.
    botStateLabel() {
        if (this.emergencyStop) return 'Stopped';
        if (this.warmupTicksRemaining > 0) return 'Warming up';
        return 'Running';
    },

    // ── Approvals ────────────────────────────────────────────────────────────

    // Approves or rejects a pending trade and tells the operator when no real order was executed.
    async decideApproval(id, action) {
        if (!this._actionAllowedWhileStale()) return;
        this.approvalBusy = { ...this.approvalBusy, [id]: true };
        try {
            const r = await fetch(`/api/approvals/${encodeURIComponent(id)}/${action}`, { method: 'POST' });
            const d = await r.json().catch(() => ({}));
            if (action === 'approve' && r.status === 503) {
                // No live price (or the emergency stop): nothing was sent and the approval stays queued.
                this.notify('error', `Trade NOT executed - the approval is still pending: ${d.detail || 'try again shortly'}`);
            } else if (action === 'approve' && r.status === 409) {
                // The risk checks refused it at approval time (the detail says whether it was removed).
                this.notify('error', `Trade NOT executed: ${d.detail || 'refused by the risk check'}`);
            } else if (!r.ok) {
                this.notify('error', `Could not ${action} the trade: ${d.detail || `HTTP ${r.status}`}`);
            } else if (action === 'approve' && d.order_status === 'rejected') {
                this.notify('error', `Trade approved but the ${d.environment || 'paper'} order was REJECTED (e.g. insufficient funds).`);
            } else {
                this.notify('success', action === 'approve' ? 'Trade approved.' : 'Trade rejected.');
            }
        } catch (e) {
            this.notify('error', `Could not ${action} the trade — the server could not be reached (${e.message}).`);
        } finally {
            const { [id]: _done, ...rest } = this.approvalBusy;
            this.approvalBusy = rest;
        }
        this.loadDashboard();
    },

    // ── Equity graph ─────────────────────────────────────────────────────────

    // Draws the equity graph, or replaces its data in place when it already exists.
    renderChart(history) {
        if (!history.length) return;
        const points = history
            .map(h => ({ timestamp: h.timestamp, equity: Number(h.equity) }))
            .filter(h => Number.isFinite(h.equity));
        if (!points.length) return;

        const labels = points.map(h => this._formatEquityTick(h.timestamp));
        const values = points.map(h => h.equity);
        this._equityTimestamps = points.map(h => h.timestamp);

        const canvas = document.getElementById('equity-chart');
        if (!canvas || typeof Chart === 'undefined') return;   // drawn on the next poll

        const existingChart = Chart.getChart(canvas);
        if (existingChart) {
            // Replace data in-place; the chart has animation:false so this is instant
            existingChart.data.labels = labels;
            existingChart.data.datasets[0].data = values;
            existingChart.update('none');
            return;
        }

        const accent = cssVar('--accent');
        const grid = cssVar('--border');
        const muted = cssVar('--text-muted');
        new Chart(canvas.getContext('2d'), {
            type: 'line',
            data: {
                labels,
                datasets: [{
                    data: values,
                    borderColor: accent,
                    backgroundColor: 'rgba(91, 157, 255, 0.08)',
                    fill: true,
                    tension: 0.3,
                    pointRadius: 0,
                    pointHoverRadius: 4,
                    pointHitRadius: 12,
                    borderWidth: 2,
                }],
            },
            plugins: [this._equityCursorLine()],
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
                        mode: 'index',
                        intersect: false,
                        callbacks: {
                            title: items => items.length ? this._formatEquityTimestamp(items[0].dataIndex) : '',
                            label: c => this.currencySymbol + fmtMoney(c.parsed.y),
                        },
                    },
                },
                scales: {
                    x: { grid: { color: grid }, ticks: { color: muted, maxTicksLimit: 8, font: { size: 11 } } },
                    y: {
                        grace: '35%',
                        grid: { color: grid },
                        ticks: { color: muted, font: { size: 11 }, callback: v => this.currencySymbol + fmtMoney(v) },
                    },
                },
            },
        });

        // Resize the chart whenever its container changes size (window resize, view shown again)
        new ResizeObserver(() => {
            Chart.getChart(canvas)?.resize();
        }).observe(canvas.parentElement);
    },

    // The x-axis label for an equity point (time of day).
    _formatEquityTick(timestamp) {
        const date = parseUtc(timestamp);
        if (Number.isNaN(date.getTime())) return '';
        return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    },

    // The tooltip title for an equity point (date and time with seconds).
    _formatEquityTimestamp(index) {
        const date = parseUtc(this._equityTimestamps[index]);
        if (Number.isNaN(date.getTime())) return '';
        return date.toLocaleString([], { day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit', second: '2-digit' });
    },

    // A Chart.js plugin that draws a dashed vertical line at the hovered point.
    _equityCursorLine() {
        return {
            id: 'equityCursorLine',
            afterDraw: chart => {
                const active = chart.tooltip?.getActiveElements?.() || [];
                if (!active.length) return;
                const x = active[0].element.x;
                const yAxis = chart.scales.y;
                const ctx = chart.ctx;
                ctx.save();
                ctx.beginPath();
                ctx.moveTo(x, yAxis.top);
                ctx.lineTo(x, yAxis.bottom);
                ctx.lineWidth = 1;
                ctx.strokeStyle = 'rgba(170, 178, 192, 0.6)';
                ctx.setLineDash([4, 4]);
                ctx.stroke();
                ctx.restore();
            },
        };
    },
});
