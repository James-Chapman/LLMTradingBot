'use strict';
/*
 * Intelligence: the LLM market briefing and hourly reflection, signals (with
 * the signal detail dialog, also opened from Overview, Markets and Trading),
 * news with source filters, strategy selection, the learner's summary and
 * the LLM shadow reviews (would the LLM's vetoes have helped a strategy?).
 * New signals also raise a desktop notification when the browser allows it.
 */

// How often the news list is refreshed while Intelligence is shown.
const NEWS_REFRESH_MS = 300000;

// How often the LLM shadow reviews are refreshed while Intelligence is shown.
const SHADOW_REFRESH_MS = 60000;

// How many market outlooks the briefing shows before "Show all".
const OUTLOOK_PREVIEW = 9;

// The direction of an outlook's bias; the LLM also writes shades such as 'mildly_bullish'.
function biasDirection(bias) {
    const text = String(bias || '');
    if (text.includes('bear')) return 'bearish';
    if (text.includes('bull')) return 'bullish';
    return 'neutral';
}

// Sort rank of an outlook's bias: bearish, then bullish (including their shades), then neutral.
function outlookRank(bias) {
    const direction = biasDirection(bias);
    if (direction === 'bearish') return 0;
    if (direction === 'bullish') return 1;
    return 2;
}

// A bias as words: 'mildly_bullish' → 'mildly bullish'.
function biasText(bias) {
    return String(bias || 'neutral').replace(/_/g, ' ');
}

// ── Signal dialog chart ──────────────────────────────────────────────────────

// How many candles the signal chart shows at first, and how many of them come after the signal.
const SIGNAL_CHART_CANDLES = 90;
const SIGNAL_CHART_AFTER = 20;

// The chart lives outside Alpine, like the Markets charts, so its objects are not reactive proxies.
let signalChartView = null;   // { chart, series, el, line, signalCandle, range, rangePending, resizer }
let signalChartRequest = 0;   // id of the latest candle request; answers to older ones are dropped

// A candle length as a short label: 5 → '5m', 60 → '1h', 240 → '4h'.
function candleLabel(minutes) {
    return minutes >= 60 ? `${minutes / 60}h` : `${minutes}m`;
}

// Formats a candle time (UTC epoch seconds, unambiguous unlike the stored
// timestamps parseUtc handles) as local "14 Nov 10:42".
function fmtCandleTime(seconds) {
    return new Intl.DateTimeFormat('en-GB', { day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit' })
        .format(seconds * 1000);
}

// Shows the candles around the signal (view.range), or all of them when
// there are few. While the dialog is still opening the pane has no width and
// a range set then would be lost, so the resize observer applies it later.
function showSignalRange(view) {
    if (!view.el.clientWidth) {
        view.rangePending = true;
        return;
    }
    view.rangePending = false;
    const timeScale = view.chart.timeScale();
    if (view.range) timeScale.setVisibleLogicalRange(view.range);
    else timeScale.fitContent();
    placeSignalLine(view);
}

// Moves the vertical line over the signal's candle, or hides it while that
// candle is scrolled out of view. Called after every scroll, zoom and resize.
function placeSignalLine(view) {
    const timeScale = view.chart.timeScale();
    const x = view.signalCandle == null ? null : timeScale.timeToCoordinate(view.signalCandle);
    // timeToCoordinate is null only for a time not in the data; a candle scrolled
    // out of view gets an x outside the plot area, so that hides the line too
    if (x == null || x < 0 || x > timeScale.width()) {
        view.line.style.display = 'none';
        return;
    }
    view.line.style.display = 'block';
    view.line.style.transform = `translateX(${Math.round(x)}px)`;
    view.line.style.bottom = `${timeScale.height() + 1}px`;   // stop at the time axis (+1 px pane border)
}

Dashboard.register('intelligence', {
    news: resource([]),          // /api/news
    shadowReviews: resource({ enabled: false, strategies: [], recent: [] }), // /api/shadow-reviews
    newsSources: [],             // sources picked in the filter; empty = all
    signalModal: null,           // the signal shown in the detail dialog
    signalBreakdown: null,
    signalVoteRows: [],
    signalChartIntervals: [5, 15, 60, 240],                // candle lengths the bot keeps, in minutes
    signalChart: { minutes: 15, state: 'idle', note: '' }, // state: idle | loading | ready | outside | empty | error
    outlooksExpanded: false,     // all market outlooks shown (reset when the view is left)
    _seenSignalIds: new Set(),   // signals already notified, so a poll does not notify twice
    _notifyReady: false,         // false until the first signals were seen (no burst on page load)

    // Asks for desktop-notification permission and loads the news while the view is shown.
    initIntelligence() {
        if ('Notification' in window && Notification.permission === 'default') {
            Notification.requestPermission();
        }
        this.pollWhileShown('intelligence', NEWS_REFRESH_MS, () => this.loadNews());
        this.pollWhileShown('intelligence', SHADOW_REFRESH_MS, () => this.loadShadowReviews());
    },

    // Collapses the outlook list again when the operator leaves the view.
    onIntelligenceHidden() {
        this.outlooksExpanded = false;
    },

    // Raises a desktop notification for each signal that was not seen before.
    onIntelligenceData(d) {
        const signals = d.signals || [];
        // The first response only records what is already there.
        if (!this._notifyReady) {
            signals.forEach(s => { if (s.trade_idea_id) this._seenSignalIds.add(s.trade_idea_id); });
            this._notifyReady = true;
            return;
        }
        if (!('Notification' in window) || Notification.permission !== 'granted') return;
        signals.forEach(s => {
            if (!s.trade_idea_id || this._seenSignalIds.has(s.trade_idea_id)) return;
            this._seenSignalIds.add(s.trade_idea_id);
            const dir = s.direction === 'long' ? '▲ LONG' : '▼ SHORT';
            const pct = Math.round((s.confidence ?? 0) * 100);
            const risk = s.risk_approved ? '✓ Risk approved' : '✗ Risk blocked';
            // Notifications are plain text; the thesis is shortened so the body stays readable
            const thesis = (s.thesis || '').length > 120 ? s.thesis.slice(0, 117) + '…' : (s.thesis || '');
            new Notification(`${s.market} ${dir} — ${pct}% confidence`, {
                body: `${thesis}\n${risk}`,
                tag: s.trade_idea_id,   // the same tag replaces an earlier notification for this signal
            });
        });
    },

    // ── Briefing ─────────────────────────────────────────────────────────────

    // Overall sentiment as { label, cls } (▲ Bullish / ▼ Bearish / — Neutral).
    sentimentBadge(score) {
        const value = score ?? 0;
        if (value >= 0.1) return { label: '▲ Bullish', cls: 'pill-profit' };
        if (value <= -0.1) return { label: '▼ Bearish', cls: 'pill-loss' };
        return { label: '— Neutral', cls: '' };
    },

    // An outlook's bias as { label, cls } for its pill.
    biasBadge(bias) {
        const direction = biasDirection(bias);
        if (direction === 'bullish') return { label: '▲ ' + biasText(bias), cls: 'pill-profit' };
        if (direction === 'bearish') return { label: '▼ ' + biasText(bias), cls: 'pill-loss' };
        return { label: '— ' + biasText(bias), cls: '' };
    },

    // The briefing's market outlooks as [market, outlook] pairs: bearish and bullish
    // before neutral, alphabetical within each group.
    briefingOutlooks() {
        return Object.entries(this.llmBriefing?.market_outlooks ?? {})
            .sort(([a, outA], [b, outB]) => outlookRank(outA.bias) - outlookRank(outB.bias) || a.localeCompare(b));
    },

    // The outlooks on screen: the first OUTLOOK_PREVIEW until the operator asks for all.
    shownOutlooks() {
        const all = this.briefingOutlooks();
        return this.outlooksExpanded ? all : all.slice(0, OUTLOOK_PREVIEW);
    },

    // True when some outlooks are behind the "Show all" button.
    hasMoreOutlooks() {
        return this.briefingOutlooks().length > OUTLOOK_PREVIEW;
    },

    // One line counting the outlooks per direction: "12 bearish · 3 bullish · 40 neutral".
    outlookSummary() {
        const counts = new Map();
        for (const [, out] of this.briefingOutlooks()) {
            const direction = biasDirection(out.bias);
            counts.set(direction, (counts.get(direction) || 0) + 1);
        }
        // briefingOutlooks() is already in bias order, so the counts are too
        return [...counts].map(([direction, n]) => `${n} ${direction}`).join(' · ');
    },

    // ── News ─────────────────────────────────────────────────────────────────

    // Loads the last 12 hours of news.
    loadNews() {
        return this.fetchResource(this.news, '/api/news');
    },

    // The distinct news sources, sorted, for the filter chips.
    newsSourceList() {
        return [...new Set(this.news.data.map(n => n.source).filter(Boolean))].sort();
    },

    // Adds or removes a source from the news filter.
    toggleNewsSource(source) {
        this.newsSources = this.newsSources.includes(source)
            ? this.newsSources.filter(s => s !== source)
            : [...this.newsSources, source];
    },

    // The articles from the selected sources (all when none is selected).
    filteredNews() {
        if (!this.newsSources.length) return this.news.data;
        return this.news.data.filter(n => this.newsSources.includes(n.source));
    },

    // ── Strategy and learning ────────────────────────────────────────────────

    // Learner rows with the most trades first.
    learningRows() {
        return [...this.learning].sort((a, b) => b.trades - a.trades);
    },

    // ── LLM shadow reviews ───────────────────────────────────────────────────

    // Loads the LLM shadow verdicts and their scores.
    loadShadowReviews() {
        return this.fetchResource(this.shadowReviews, '/api/shadow-reviews');
    },

    // A share (0-1) as a whole percentage, or '—' when there is none yet.
    fmtShare(value) {
        return value === null || value === undefined ? '—' : Math.round(value * 100) + '%';
    },

    // The verdict pill for one strategy's shadow score.
    shadowVerdict(row) {
        const needed = this.shadowReviews.data.min_scored_vetoes || 0;
        if (row.verdict === 'ahead') return { label: 'Vetoes would help', cls: 'pill-profit' };
        if (row.verdict === 'behind') return { label: 'Vetoes would hurt', cls: 'pill-loss' };
        return { label: `Too few vetoes (${row.scored_vetoes}/${needed})`, cls: 'pill-outline' };
    },

    // What the LLM said about one entry, in words.
    shadowAnswer(review) {
        if (!review.llm_used) return 'LLM unavailable';
        const scale = '×' + review.confidence_scale.toFixed(2);
        return review.would_veto ? `Would veto (${scale})` : `Would keep (${scale})`;
    },

    // ── Signal detail dialog ─────────────────────────────────────────────────

    // Loads a signal and opens its detail dialog.
    async showSignal(tradeIdeaId) {
        if (!tradeIdeaId) return;
        try {
            const r = await fetch(`/api/signals/${encodeURIComponent(tradeIdeaId)}`);
            if (!r.ok) {
                this.notify('error', 'Signal detail not found.');
                return;
            }
            const s = await r.json();
            this.signalBreakdown = this._computeBreakdown(s);
            this.signalVoteRows = this._computeIndicatorVotes(s);
            this.signalModal = s;
            // The chart opens on the candles the signal's strategy read, once the dialog is in the page
            this.$nextTick(() => this.loadSignalChart(s.candle_minutes || 15));
        } catch (e) {
            console.error('Signal detail:', e);
            this.notify('error', `Could not load the signal — the server could not be reached (${e.message}).`);
        }
    },

    // Closes the signal detail dialog.
    closeSignal() {
        if (!this.signalModal) return;
        this._removeSignalChart();
        this.signalModal = null;
        this.signalBreakdown = null;
        this.signalVoteRows = [];
        this.signalChart = { minutes: 15, state: 'idle', note: '' };
        this.restoreFocus();
    },

    // Loads the open signal's market candles of the given length and draws them.
    async loadSignalChart(minutes) {
        const signal = this.signalModal;
        if (!signal) return;
        const request = ++signalChartRequest;
        this.signalChart = { minutes, state: 'loading', note: '' };
        try {
            const market = signal.market;
            const r = await fetch(`/api/ohlc/${encodeURIComponent(market)}?interval=${minutes}`);
            if (!r.ok) throw new Error(`OHLC HTTP ${r.status}`);
            const candles = toCandleSeries((await r.json()).candles || []);
            // The dialog may have closed, or another signal or length been picked, meanwhile
            if (request !== signalChartRequest || this.signalModal?.id !== signal.id) return;
            this._drawSignalChart(candles, minutes, signal);
        } catch (e) {
            console.error('Signal chart:', e);
            if (request === signalChartRequest) this.signalChart = { minutes, state: 'error', note: e.message };
        }
    },

    // The message shown over the chart while it has nothing to draw.
    signalChartStatus() {
        const { state, minutes, note } = this.signalChart;
        if (state === 'loading') return 'Loading candles…';
        if (state === 'empty') return `No ${candleLabel(minutes)} candles cached for this market yet.`;
        if (state === 'error') return `Could not load the candles (${note}).`;
        return '';
    },

    // Draws candles and marks the candle the signal was generated in with a
    // vertical line and an arrow, showing the candles around it.
    _drawSignalChart(candles, minutes, signal) {
        if (!candles.length) {
            this.signalChart = { minutes, state: 'empty', note: '' };
            return;
        }
        const view = signalChartView || this._createSignalChart();
        if (!view) return; // the dialog closed before the candles arrived

        const last = candles[candles.length - 1];
        const precision = priceDecimals(last.close);
        view.series.applyOptions({ priceFormat: { type: 'price', precision, minMove: 10 ** -precision } });
        view.series.setData(candles);

        // The signal's candle is the last one starting at or before the signal's time,
        // as long as the signal falls inside it (the cache may not reach that far)
        const signalTime = Math.floor(parseUtc(signal.created_at).getTime() / 1000);
        const index = candles.findLastIndex(c => c.time <= signalTime);
        const inside = index >= 0 && signalTime < candles[index].time + minutes * 60;
        view.signalCandle = inside ? candles[index].time : null;

        const isLong = signal.direction === 'long';
        view.series.setMarkers(inside ? [{
            time: view.signalCandle,
            position: isLong ? 'belowBar' : 'aboveBar',
            shape: isLong ? 'arrowUp' : 'arrowDown',
            color: cssVar(isLong ? '--profit' : '--loss'),
            text: 'Signal',
        }] : []);

        // Show the candles leading up to the signal and a few after it (or the newest ones; all when few)
        const to = inside ? Math.min(candles.length - 1, index + SIGNAL_CHART_AFTER) : candles.length - 1;
        view.range = candles.length <= SIGNAL_CHART_CANDLES ? null : { from: to - SIGNAL_CHART_CANDLES + 1, to: to + 1 };
        showSignalRange(view);

        if (inside) {
            this.signalChart = { minutes, state: 'ready', note: '' };
            return;
        }
        // The signal is outside the cached candles: say which way, rather than draw a misplaced line
        const covers = `${fmtCandleTime(candles[0].time)} – ${fmtCandleTime(last.time + minutes * 60)}`;
        const note = signalTime < candles[0].time
            ? `The signal is older than these candles (${covers}). Pick a longer candle length to see it.`
            : `The signal is newer than these candles (${covers}); they refresh shortly.`;
        this.signalChart = { minutes, state: 'outside', note };
    },

    // Creates the dialog's chart, or returns null when its pane is not in the page.
    _createSignalChart() {
        const el = document.querySelector('.signal-chart-pane');
        const line = document.querySelector('.signal-chart-line');
        if (!el || !line) return null;
        const { chart, series } = newCandleChart(el);
        const view = { chart, series, el, line, signalCandle: null, range: null, rangePending: false, resizer: null };
        // Follow the pane's size (window resize, layout switch) and keep the line on its candle;
        // the first size after the dialog opens also applies the candles' starting range
        view.resizer = new ResizeObserver(() => {
            chart.applyOptions({ width: el.clientWidth, height: el.clientHeight });
            if (view.rangePending) showSignalRange(view);
            placeSignalLine(view);
        });
        view.resizer.observe(el);
        chart.timeScale().subscribeVisibleLogicalRangeChange(() => placeSignalLine(view));
        signalChartView = view;
        return view;
    },

    // Removes the dialog's chart and its observer, and drops any candle answer still on its way.
    _removeSignalChart() {
        signalChartRequest++;
        const view = signalChartView;
        if (!view) return;
        view.resizer.disconnect();
        view.chart.remove();
        signalChartView = null;
    },

    // A readable label for a signal's strategy id.
    signalStrategyLabel(strategyId) {
        const labels = {
            basic_strategy_1h: 'Basic · 1h candles', basic_strategy_long_1h: 'Basic · 1h candles, long only',
            basic_strategy_4h: 'Basic · 4h candles', basic_strategy_long_4h: 'Basic · 4h candles, long only',
            basic_and_llm_strategy_1h: 'Basic · 1h candles + LLM', basic_and_llm_strategy_long_1h: 'Basic · 1h candles + LLM, long only',
            basic_and_llm_strategy_4h: 'Basic · 4h candles + LLM', basic_and_llm_strategy_long_4h: 'Basic · 4h candles + LLM, long only',
            basic_and_llm_strategy: 'Basic · ticks + LLM (retired)', combined: 'Basic · ticks + LLM (retired)',
            basic_strategy: 'Basic · ticks (retired)', llm_only_strategy: 'LLM Only', llm: 'LLM Only',
            trend_strategy: 'Trend · 4h EMA', trend_and_llm_strategy: 'Trend · 4h EMA + LLM',
            donchian_strategy: 'Donchian · 4h breakout', donchian_and_llm_strategy: 'Donchian · 4h breakout + LLM',
            rsi_dip_strategy: 'RSI Dip · 4h', rsi_dip_and_llm_strategy: 'RSI Dip · 4h + LLM',
            macd_strategy: 'MACD · 4h momentum', macd_and_llm_strategy: 'MACD · 4h momentum + LLM',
            squeeze_strategy: 'Squeeze · 4h Bollinger', squeeze_and_llm_strategy: 'Squeeze · 4h Bollinger + LLM',
            rotation_strategy: 'Rotation · 14-day strength', rotation_and_llm_strategy: 'Rotation · 14-day strength + LLM',
        };
        return labels[strategyId] || strategyId;
    },

    // The colour class for a confidence (high, medium, low).
    confidenceClass(confidence) {
        return confidence >= 0.7 ? 'pos' : confidence >= 0.5 ? 'warn' : 'neg';
    },

    // A signed percentage of a fraction: 0.054 → "+5.4%".
    signedFractionPct(value) {
        return (value >= 0 ? '+' : '') + (value * 100).toFixed(1) + '%';
    },

    // Rebuilds the confidence calculation (momentum base, bonuses, penalties, LLM, learner) from the stored inputs.
    _computeBreakdown(s) {
        const ss = s.supporting_signals || {};
        const isLong = s.direction === 'long';

        // Momentum base
        const momentum = ss.momentum || 0;
        const base = Math.min(Math.abs(momentum) * 100, 0.50);

        // Indicator consensus bonus
        const votes = ss.indicator_votes ?? 0;
        const available = ss.indicators_available ?? 0;
        const supporting = ss.indicators_supporting ?? Math.max(0, (available + votes) / 2);
        const indicatorBonus = available > 0 ? Math.max(-0.20, Math.min(0.40, votes * 0.05)) : 0;

        // News sentiment bonus
        const newsSent = ss.news_sentiment || 0;
        let newsBonus = 0;
        if ((isLong && newsSent > 0) || (!isLong && newsSent < 0)) newsBonus = 0.10;
        else if ((isLong && newsSent < 0) || (!isLong && newsSent > 0)) newsBonus = -0.10;

        // LLM briefing sentiment bonus, distinct from the later LLM analysis multiplier
        const usesLlmSentiment = ss.uses_llm_sentiment ?? true;
        const llmSentiment = usesLlmSentiment ? (ss.llm_sentiment || 0) : 0;
        let llmSentimentBonus = 0;
        if ((isLong && llmSentiment > 0) || (!isLong && llmSentiment < 0)) llmSentimentBonus = 0.05;
        else if ((isLong && llmSentiment < 0) || (!isLong && llmSentiment > 0)) llmSentimentBonus = -0.05;

        // ATR volatility penalty
        const atrPct = ss.atr_pct || 0;
        const atrPenalty = atrPct > 1.0 ? -Math.min(0.10, (atrPct - 1.0) * 0.05) : 0;

        // Strategy subtotal, rebuilt from the inputs above
        const rebuiltConf = Math.max(0.10, Math.min(0.95,
            base + indicatorBonus + newsBonus + llmSentimentBonus + atrPenalty));

        // The backend scores in this order: strategy → LLM → learner → final.
        const llmUsed = s.llm_used ?? false;
        const llmScale = s.llm_confidence_scale ?? 1.0;         // the LLM's own scale (drives the veto)
        const llmWeight = ss.llm_weight ?? 1.0;                 // LLM_WEIGHT when the signal was made
        const llmApplied = ss.llm_applied_scale ?? llmScale;    // the scale actually applied to the score
        const finalConf = s.confidence;

        // Signals stored before v0.22.0 lack pre_learner_confidence, and their
        // pre_llm_confidence is the final score; rebuild their steps instead.
        const hasSteps = ss.pre_learner_confidence != null;
        const stratConf = hasSteps ? ss.pre_llm_confidence : rebuiltConf;
        const afterLlm = hasSteps ? ss.pre_learner_confidence
            : (llmUsed ? Math.min(0.95, stratConf * llmScale) : stratConf);
        const llmDiff = afterLlm - stratConf;

        // Learner adjustment: what the learner added or removed after the LLM.
        const learnerDiff = finalConf - afterLlm;
        const learnerScale = afterLlm > 0 ? finalConf / afterLlm : 1.0;

        return {
            momentum: momentum * 100, base,
            indicatorBonus, votes, available, supporting,
            newsBonus, newsSent, llmSentiment, llmSentimentBonus,
            atrPct, atrPenalty,
            stratConf, afterLlm,
            llmUsed, llmScale, llmWeight, llmApplied, llmDiff,
            learnerDiff, learnerScale,
            learnerAdjusted: Math.abs(learnerDiff) > 0.005,
            finalConf,
        };
    },

    // One row per indicator: its value, reading and vote for (+1) or against (−1) the signal.
    _computeIndicatorVotes(s) {
        const ss = s.supporting_signals || {};
        const isLong = s.direction === 'long';

        // An indicator has an opinion when either condition holds; it votes +1
        // when it agrees with the signal's direction and −1 when it opposes it.
        const vote = (longCond, shortCond) => {
            if (longCond || shortCond) {
                const agrees = (isLong && longCond) || (!isLong && shortCond);
                return agrees ? 1 : -1;
            }
            return 0;
        };
        const fmt = (v, dp = 2) => (v == null ? '–' : parseFloat(v).toFixed(dp));
        const signed = (v, dp) => (v == null ? '–' : (v >= 0 ? '+' : '') + fmt(v, dp));
        const rows = [];

        const rsi = ss.rsi_14;
        rows.push({
            name: 'RSI (14)', displayValue: fmt(rsi, 1),
            signal: ss.rsi_signal || (rsi != null ? (rsi >= 70 ? 'overbought' : rsi <= 30 ? 'oversold' : 'neutral') : '–'),
            vote: rsi != null ? vote(rsi < 40, rsi > 60) : 0, hasData: rsi != null,
        });

        const emaCross = ss.ema_cross;
        rows.push({
            name: 'EMA 9/21',
            displayValue: ss.ema9 != null && ss.ema21 != null ? `${fmt(ss.ema9, 0)} / ${fmt(ss.ema21, 0)}` : '–',
            signal: emaCross ?? '–',
            vote: emaCross != null ? vote(emaCross === 'bullish', emaCross === 'bearish') : 0, hasData: emaCross != null,
        });

        const bbPos = ss.bb_position;
        rows.push({
            name: 'BB Position', displayValue: bbPos != null ? fmt(bbPos, 1) + '%' : '–',
            signal: bbPos != null
                ? (bbPos >= 95 ? 'upper extreme' : bbPos >= 70 ? 'upper zone'
                    : bbPos <= 5 ? 'lower extreme' : bbPos <= 30 ? 'lower zone' : 'mid-band')
                : '–',
            vote: bbPos != null ? vote(bbPos < 30, bbPos > 70) : 0, hasData: bbPos != null,
        });

        const macd = ss.macd || {};
        const macdBias = ss.macd_bias || macd.bias;
        rows.push({
            name: 'MACD bias', displayValue: macd.line != null ? fmt(macd.line, 4) : '–',
            signal: macdBias ?? '–',
            vote: macdBias != null ? vote(macdBias === 'bullish', macdBias === 'bearish') : 0, hasData: macdBias != null,
        });

        const macdSigBias = ss.macd_signal_bias || macd.signal_bias;
        const macdHist = ss.macd_histogram ?? macd.histogram ?? null;
        rows.push({
            name: 'MACD crossover', displayValue: signed(macdHist, 5),
            signal: macdSigBias ?? '–',
            vote: macdSigBias != null ? vote(macdSigBias === 'bullish', macdSigBias === 'bearish') : 0,
            hasData: macdSigBias != null,
        });

        const stoch = ss.stoch || {};
        const stochK = ss.stoch_k ?? stoch.k ?? null;
        const stochD = ss.stoch_d ?? stoch.d ?? null;
        rows.push({
            name: 'Stochastic %K',
            displayValue: stochK != null ? fmt(stochK, 1) + (stochD != null ? ' / ' + fmt(stochD, 1) : '') : '–',
            signal: stoch.bias ?? (stochK != null ? (stochK < 20 ? 'oversold' : stochK > 80 ? 'overbought' : 'neutral') : '–'),
            vote: stochK != null ? vote(stochK < 20, stochK > 80) : 0, hasData: stochK != null,
        });

        const wr = ss.williams_r;
        rows.push({
            name: 'Williams %R', displayValue: fmt(wr, 1),
            signal: ss.williams_r_signal ?? (wr != null ? (wr <= -80 ? 'oversold' : wr >= -20 ? 'overbought' : 'neutral') : '–'),
            vote: wr != null ? vote(wr <= -80, wr >= -20) : 0, hasData: wr != null,
        });

        for (const [name, value] of [['5m change', ss.price_change_5m], ['15m change', ss.price_change_15m]]) {
            rows.push({
                name, displayValue: value != null ? signed(value, 3) + '%' : '–',
                signal: value != null ? (value > 0 ? 'positive' : value < 0 ? 'negative' : 'flat') : '–',
                vote: value != null ? vote(value > 0, value < 0) : 0, hasData: value != null,
            });
        }
        return rows;
    },

    // A raw supporting-signal value for display.
    rawSignalValue(v) {
        if (v == null) return '–';
        if (typeof v === 'number') return Number.isInteger(v) ? v : parseFloat(v.toFixed(4));
        return typeof v === 'object' ? JSON.stringify(v) : v;
    },
});
