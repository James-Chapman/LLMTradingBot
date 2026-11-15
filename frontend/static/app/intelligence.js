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

Dashboard.register('intelligence', {
    news: resource([]),          // /api/news
    shadowReviews: resource({ enabled: false, strategies: [], recent: [] }), // /api/shadow-reviews
    newsSources: [],             // sources picked in the filter; empty = all
    signalModal: null,           // the signal shown in the detail dialog
    signalBreakdown: null,
    signalVoteRows: [],
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
        } catch (e) {
            console.error('Signal detail:', e);
            this.notify('error', `Could not load the signal — the server could not be reached (${e.message}).`);
        }
    },

    // Closes the signal detail dialog.
    closeSignal() {
        if (!this.signalModal) return;
        this.signalModal = null;
        this.signalBreakdown = null;
        this.signalVoteRows = [];
        this.restoreFocus();
    },

    // A readable label for a signal's strategy id.
    signalStrategyLabel(strategyId) {
        const labels = {
            basic_and_llm_strategy: 'Combined · Indicator + LLM', combined: 'Combined · Indicator + LLM',
            basic_strategy: 'Indicator Only', llm_only_strategy: 'LLM Only', llm: 'LLM Only',
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

    // Rebuilds the confidence calculation (momentum base, bonuses, penalties, learner, LLM) from the stored inputs.
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

        // Strategy subtotal
        const stratConf = Math.max(0.10, Math.min(0.95,
            base + indicatorBonus + newsBonus + llmSentimentBonus + atrPenalty));

        // Learner adjustment: pre_llm_confidence is after the learner, so the
        // difference to the strategy subtotal is what the learner added or removed.
        const preLlm = ss.pre_llm_confidence != null ? ss.pre_llm_confidence : stratConf;
        const learnerDiff = preLlm - stratConf;
        const learnerScale = stratConf > 0 ? preLlm / stratConf : 1.0;

        // LLM multiplier
        const llmScale = s.llm_confidence_scale ?? 1.0;
        const llmUsed = s.llm_used ?? false;
        const finalConf = s.confidence;

        return {
            momentum: momentum * 100, base,
            indicatorBonus, votes, available, supporting,
            newsBonus, newsSent, llmSentiment, llmSentimentBonus,
            atrPct, atrPenalty,
            stratConf, preLlm,
            learnerDiff, learnerScale,
            learnerAdjusted: Math.abs(learnerDiff) > 0.005,
            llmScale, llmUsed, finalConf,
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
