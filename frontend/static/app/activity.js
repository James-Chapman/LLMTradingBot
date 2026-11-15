'use strict';
/*
 * Activity: the bot's activity log (filterable by level) and system health
 * (LLM backend, model and status, the market data feed, version and control
 * state). Everything here comes from /api/dashboard.
 */

// Activity levels in filter order, with their labels.
const ACTIVITY_LEVELS = [
    { id: '', label: 'All' },
    { id: 'error', label: 'Errors' },
    { id: 'warn', label: 'Warnings' },
    { id: 'success', label: 'Success' },
    { id: 'info', label: 'Info' },
];

Dashboard.register('activity', {
    activityLevel: '',           // '' = all levels
    activityLevels: ACTIVITY_LEVELS,
    activityOpen: {},            // activityKey → true for entries whose full detail is shown

    // Identifies an entry across polls (new entries arrive at the top, so the list index moves).
    activityKey(entry) {
        return `${entry.timestamp}|${entry.message}`;
    },

    // True when an entry's detail is expanded beyond its two-line preview.
    isActivityOpen(entry) {
        return !!this.activityOpen[this.activityKey(entry)];
    },

    // Expands or collapses one entry's detail (Activity and the Overview strip share the state).
    toggleActivityDetail(entry) {
        const key = this.activityKey(entry);
        this.activityOpen = { ...this.activityOpen, [key]: !this.activityOpen[key] };
    },

    // The log entries of the selected level (newest first, as the API sends them).
    filteredActivity() {
        if (!this.activityLevel) return this.activityLog;
        return this.activityLog.filter(e => e.level === this.activityLevel);
    },

    // How many entries have a level ('' counts all).
    activityCount(level) {
        return level ? this.activityLog.filter(e => e.level === level).length : this.activityLog.length;
    },

    // An entry's time of day with seconds.
    activityTime(entry) {
        return fmtTime(parseUtc(entry.timestamp), true);
    },

    // How many listed markets have a price yet (the market data feed is working for them).
    pricedMarketCount() {
        return this.markets.filter(m => m.price_value != null).length;
    },
});
