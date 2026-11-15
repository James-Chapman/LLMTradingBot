'use strict';
/*
 * Markets: the market table (search, filters, enable and live toggles) and,
 * beside or below it, the selected market's 5-minute and 15-minute candle
 * charts. The selected market is part of the hash (#/markets/BTC-EUR).
 *
 * Phones (below 768 px) use a list → detail flow instead: #/markets shows the
 * list alone and nothing is selected; #/markets/BTC-EUR shows only that
 * market's detail with a back control. From 768 px the table and the detail
 * sit side by side (or stacked) and the first market is selected.
 *
 * Chart lifecycle: only the selected market has charts. They are created
 * when its candles first arrive, removed when another market is selected,
 * when the market leaves the list, when a phone returns to the list and when
 * the view is hidden. Each market has at most one OHLC request in flight.
 */

// How often the selected market's candles are refreshed while Markets is shown.
const CHART_REFRESH_MS = 60000;

// Matches the stylesheet's phone breakpoint: below it Markets is a list → detail flow.
const NARROW_SCREEN = window.matchMedia('(max-width: 767px)');

// Chart state lives outside Alpine so the chart objects are not wrapped in reactive proxies.
const candleCharts = new Map();   // market → { chart5, series5, chart15, series15, resizers }
const chartRequests = new Map();  // market → in-flight _fetchAndRender promise

// Converts API candles to Lightweight Charts data. The library rejects unsorted or
// repeated timestamps, so the last candle per time wins and the result is sorted.
function toCandleSeries(candles) {
    const byTime = new Map();
    for (const c of candles) byTime.set(c.t, { time: c.t, open: c.o, high: c.h, low: c.l, close: c.c });
    return [...byTime.values()].sort((a, b) => a.time - b.time);
}

// The detail pane's quote before (or without) candles; span5 / span15 describe each chart's time range.
function emptyChartQuote() {
    return { state: 'idle', error: '', close: null, change: null, span5: '', span15: '' };
}

// How much time a candle series covers, for the chart caption: "8h 15m" ('' without candles).
function candleSpan(series, intervalMinutes) {
    if (!series.length) return '';
    const seconds = series[series.length - 1].time - series[0].time + intervalMinutes * 60;
    return fmtDuration(seconds * 1000);
}

// The hash form of a market symbol: 'BTC/EUR' → 'BTC-EUR'.
function marketSlug(symbol) {
    return String(symbol || '').replace(/\//g, '-');
}

Dashboard.register('markets', {
    marketSearch: '',
    marketFilter: 'all',         // all | enabled | disabled | positions | signals | live
    selectedMarket: '',          // symbol whose charts are shown
    _requestedMarketSlug: '',    // market named in the hash, resolved once the market list is known
    chartQuote: emptyChartQuote(),
    marketsNarrow: NARROW_SCREEN.matches,  // phone layout: list → detail instead of side by side
    _marketDetailShown: false,   // a phone is showing one market's detail page
    _marketOpenedFromList: false, // that detail was opened from the list (so Back returns to it)
    _marketListScrollY: 0,       // where the phone list was scrolled when a market was opened

    // Refreshes the selected market's charts while Markets is shown and follows the phone breakpoint.
    initMarkets() {
        this.pollWhileShown('markets', CHART_REFRESH_MS, () => this._loadSelectedChart());
        NARROW_SCREEN.addEventListener('change', event => {
            this.marketsNarrow = event.matches;
            if (this.view === 'markets') this._resolveSelectedMarket();
        });
    },

    // Picks the market named in the hash (or keeps the current one). On a phone,
    // coming back from a detail page puts the list where the user left it.
    onMarketsShown(arg) {
        const returningToList = this.marketsNarrow && !arg && this._marketDetailShown;
        this._requestedMarketSlug = arg || '';
        if (!arg) this._marketOpenedFromList = false;
        this._resolveSelectedMarket();
        this._marketDetailShown = this.marketsNarrow && !!arg;
        if (returningToList) {
            this.$nextTick(() => window.scrollTo(0, this._marketListScrollY));
        } else if (this._marketDetailShown) {
            window.scrollTo(0, 0);
        }
    },

    // The charts exist only while Markets is shown.
    onMarketsHidden() {
        for (const market of [...candleCharts.keys()]) this._removeCharts(market);
        this.chartQuote = emptyChartQuote();
        this._marketDetailShown = false;
    },

    // Keeps the selection valid when the market list changes.
    onMarketsData() {
        this._resolveSelectedMarket();
    },

    // ── Table ────────────────────────────────────────────────────────────────

    // The filter chips above the table (Live only matters in the live environment).
    marketFilterOptions() {
        const options = [
            { id: 'all', label: 'All' },
            { id: 'enabled', label: 'Trading on' },
            { id: 'disabled', label: 'Trading off' },
            { id: 'positions', label: 'With position' },
            { id: 'signals', label: 'With signal' },
        ];
        if (this.environment === 'live') options.push({ id: 'live', label: 'Live' });
        return options;
    },

    // The markets matching the search box and the selected filter chip.
    filteredMarkets() {
        const query = this.marketSearch.trim().toUpperCase();
        return this.markets.filter(m => {
            if (query && !m.symbol.toUpperCase().includes(query)) return false;
            switch (this.marketFilter) {
                case 'enabled': return m.enabled;
                case 'disabled': return !m.enabled;
                case 'positions': return !!this.marketPosition(m.symbol);
                case 'signals': return !!this.marketSignal(m.symbol);
                case 'live': return m.live;
                default: return true;
            }
        });
    },

    // The dashboard row for a symbol.
    marketBySymbol(symbol) {
        return this.markets.find(m => m.symbol === symbol);
    },

    // The latest signal for a market, if any.
    marketSignal(symbol) {
        return this.signals.find(s => s.market === symbol);
    },

    // The open position on a market, if any.
    marketPosition(symbol) {
        return this.positions.find(p => p.market === symbol);
    },

    // The market's price as text (price_value is null until the market is priced).
    marketPriceText(m) {
        return m.price_value != null ? this.currencySymbol + fmtPrice(m.price_value) : m.price;
    },

    // The LLM briefing's outlook for a market, if it has one.
    marketOutlook(symbol) {
        return this.llmBriefing?.market_outlooks?.[symbol] || null;
    },

    // Opens a market's charts (through the hash, so it can be bookmarked). A phone
    // remembers where its list was scrolled so Back can return there.
    selectMarket(symbol) {
        if (this.marketsNarrow && this.view === 'markets' && !this.viewArg) {
            this._marketListScrollY = window.scrollY;
            this._marketOpenedFromList = true;
        }
        this.navigate('markets', marketSlug(symbol));
        if (!this.marketsNarrow) this._revealMarketDetail();
    },

    // When the detail sits below the table (768–1439 px) and is mostly off screen,
    // scrolls it into view so choosing a market visibly shows it.
    _revealMarketDetail() {
        const detail = document.getElementById('candle-detail');
        if (detail && detail.getBoundingClientRect().top > window.innerHeight * 0.6) {
            detail.scrollIntoView({ block: 'start' });
        }
    },

    // Leaves a phone's market detail for the list: a history step back when the
    // list opened it (so the browser's Back and this control agree), otherwise #/markets.
    marketsBack() {
        if (this._marketOpenedFromList) {
            history.back();
        } else {
            this.navigate('markets');
        }
    },

    // ── Selection ────────────────────────────────────────────────────────────

    // Chooses the market to show: the one in the hash, else the current one, else the first.
    // A phone shows the list until a market is picked, so it never selects one by itself.
    _resolveSelectedMarket() {
        if (!this.markets.length) return;
        const requested = this._requestedMarketSlug
            ? this.markets.find(m => marketSlug(m.symbol) === this._requestedMarketSlug)
            : null;
        if (this.marketsNarrow && !requested) {
            this._setSelectedMarket('');
            return;
        }
        const current = this.marketBySymbol(this.selectedMarket);
        const next = requested || current || this.markets[0];
        if (this.view === 'markets' && marketSlug(next.symbol) !== this._requestedMarketSlug) {
            // Record the shown market in the hash without adding a history entry.
            history.replaceState(null, '', '#/markets/' + marketSlug(next.symbol));
            this._requestedMarketSlug = marketSlug(next.symbol);
        }
        this._setSelectedMarket(next.symbol);
    },

    // Switches the detail pane to another market: the old charts go, the new ones load.
    _setSelectedMarket(symbol) {
        if (symbol === this.selectedMarket) return;
        if (this.selectedMarket) this._removeCharts(this.selectedMarket);
        this.selectedMarket = symbol;
        this.chartQuote = emptyChartQuote();
        if (symbol && this._ready && this.view === 'markets') this.$nextTick(() => this._loadSelectedChart());
    },

    // ── Market controls ──────────────────────────────────────────────────────

    // Enables or disables trading on one market; a refusal or network error is reported.
    async toggleMarket(market) {
        if (!this._actionAllowedWhileStale()) return;
        try {
            const response = await fetch(`/api/control/markets/${encodeURIComponent(market)}/toggle`, { method: 'POST' });
            if (!response.ok) this.notify('error', `Could not toggle ${market}: ${await this._refusalDetail(response)}`);
        } catch (e) {
            this.notify('error', `Could not toggle ${market} — the server could not be reached (${e.message}).`);
        }
        this.loadDashboard();
    },

    // Switches one market between live (real Kraken orders) and paper execution, after confirmation.
    async toggleMarketLive(market) {
        if (!this._actionAllowedWhileStale()) return;
        const goingLive = !this.marketBySymbol(market)?.live;
        const question = goingLive
            ? `Turn LIVE trading on for ${market}?\n\nThe bot will send real orders with real money to Kraken for ${market}.`
            : `Turn live trading off for ${market}?\n\nOrders for ${market} will be simulated on paper again.`;
        if (!confirm(question)) return;
        try {
            const response = await fetch(`/api/control/markets/${encodeURIComponent(market)}/live-toggle`, { method: 'POST' });
            if (!response.ok) {
                this.notify('error', `Could not change live trading for ${market}: ${await this._refusalDetail(response)}`);
            } else {
                this.notify(goingLive ? 'warning' : 'success', goingLive
                    ? `${market} is LIVE: its orders now go to Kraken with real money.`
                    : `${market} is back on paper.`);
            }
        } catch (e) {
            this.notify('error', `Could not change live trading for ${market} — the server could not be reached (${e.message}).`);
        }
        this.loadDashboard();
    },

    // ── Candle charts ────────────────────────────────────────────────────────

    // Loads (or refreshes) the selected market's charts.
    _loadSelectedChart() {
        if (this.selectedMarket) return this._loadChart(this.selectedMarket);
    },

    // Fetches and renders one market, sharing the request already in flight for it (if any).
    _loadChart(market) {
        if (chartRequests.has(market)) return chartRequests.get(market);
        const request = this._fetchAndRender(market).finally(() => {
            if (chartRequests.get(market) === request) chartRequests.delete(market);
        });
        chartRequests.set(market, request);
        return request;
    },

    // Fetches both timeframes and draws them, unless the market is no longer shown.
    async _fetchAndRender(market) {
        if (this.chartQuote.state !== 'ready') this.chartQuote = { ...this.chartQuote, state: 'loading' };
        try {
            const enc = encodeURIComponent(market);
            const [r5, r15] = await Promise.all([
                fetch(`/api/ohlc/${enc}?interval=5`),
                fetch(`/api/ohlc/${enc}?interval=15`),
            ]);
            if (!r5.ok || !r15.ok) throw new Error(`OHLC HTTP ${r5.status} / ${r15.status}`);
            const [d5, d15] = await Promise.all([r5.json(), r15.json()]);
            // The selection or the view may have changed while the request was running
            if (market !== this.selectedMarket || this.view !== 'markets') return;
            const c5 = toCandleSeries(d5.candles || []);
            const c15 = toCandleSeries(d15.candles || []);
            if (!c5.length && !c15.length) {
                this.chartQuote = { ...emptyChartQuote(), state: 'empty' };
                return;
            }

            // Last close and its change against the previous candle (5-min, else 15-min)
            const ref = c5.length ? c5 : c15;
            const last = ref[ref.length - 1];
            const prev = ref[ref.length - 2];
            const change = prev ? (last.close - prev.close) / prev.close * 100 : null;

            const entry = candleCharts.get(market) || this._createCharts(market);

            // The price scale's precision follows the price, so sub-cent candles are not flattened
            const precision = priceDecimals(last.close);
            const minMove = 10 ** -precision;
            for (const series of [entry.series5, entry.series15]) {
                series.applyOptions({ priceFormat: { type: 'price', precision, minMove } });
            }

            // Each chart spreads the candles it has across its full width
            if (c5.length) {
                entry.series5.setData(c5);
                entry.chart5.timeScale().fitContent();
            }
            if (c15.length) {
                entry.series15.setData(c15);
                entry.chart15.timeScale().fitContent();
            }

            this.chartQuote = {
                state: 'ready', error: '', close: fmtPrice(last.close), change,
                span5: candleSpan(c5, 5), span15: candleSpan(c15, 15),
            };
        } catch (e) {
            console.error('Chart error', market, e);
            if (market === this.selectedMarket) this.chartQuote = { ...this.chartQuote, state: 'error', error: e.message };
        }
    },

    // Creates the two Lightweight Charts instances in the detail pane for market.
    _createCharts(market) {
        const entry = { chart5: null, series5: null, chart15: null, series15: null, resizers: [] };
        const border = cssVar('--border');
        const makeChart = el => {
            const chart = LightweightCharts.createChart(el, {
                width: el.clientWidth,
                height: el.clientHeight,
                layout: { background: { color: cssVar('--surface-panel') }, textColor: cssVar('--text-muted') },
                grid: { vertLines: { color: border }, horzLines: { color: border } },
                crosshair: { mode: LightweightCharts.CrosshairMode.Normal },
                rightPriceScale: { borderColor: border },
                timeScale: { borderColor: border, timeVisible: true, secondsVisible: false },
            });
            const up = cssVar('--profit');
            const down = cssVar('--loss');
            const series = chart.addCandlestickSeries({
                upColor: up, downColor: down, borderUpColor: up, borderDownColor: down, wickUpColor: up, wickDownColor: down,
            });
            // Follow the pane's size (window resize, sidebar collapse, layout change) and refit the candles
            const resizer = new ResizeObserver(() => {
                chart.applyOptions({ width: el.clientWidth, height: el.clientHeight });
                chart.timeScale().fitContent();
            });
            resizer.observe(el);
            entry.resizers.push(resizer);
            return { chart, series };
        };
        const detail = document.getElementById('candle-detail');
        ({ chart: entry.chart5, series: entry.series5 } = makeChart(detail.querySelector('.chart-pane-5')));
        ({ chart: entry.chart15, series: entry.series15 } = makeChart(detail.querySelector('.chart-pane-15')));
        candleCharts.set(market, entry);
        return entry;
    },

    // Destroys a market's charts. A request still in flight finds no charts and is
    // dropped by _fetchAndRender, so a market never has two requests at once.
    _removeCharts(market) {
        const entry = candleCharts.get(market);
        if (!entry) return;
        entry.resizers.forEach(observer => observer.disconnect());
        entry.chart5.remove();
        entry.chart15.remove();
        candleCharts.delete(market);
    },
});
