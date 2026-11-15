'use strict';
/*
 * The two dialogs opened from the header:
 *   Trading settings — trading mode, strategy and paper / live. Every change
 *     is confirmed; going live needs "LIVE" typed.
 *   Settings — every stored bot setting, one group at a time. Saving lists the
 *     changes in a confirmation first. Secrets are write-only: the API only
 *     says whether one is set, and the form never shows a value. The
 *     DISABLED_NEWS_SOURCES list is shown as one on/off switch per news feed.
 * selectStrategy is also used by the strategy panel of the Intelligence view.
 */

Dashboard.register('settings', {
    // Trading settings dialog
    tradingSettingsOpen: false,
    showLiveConfirm: false,
    liveConfirmText: '',
    tradingSettingsError: '',
    // Settings dialog
    settingsOpen: false,
    settingsFields: [],
    settingsDraft: {},      // key -> edited value (lists: arrays; secrets: '' = unchanged)
    settingsGroup: '',      // group shown in the dialog
    settingsClear: {},      // secret key -> true when it should be cleared on save
    settingsRestartRequired: [],
    settingsNewsFeeds: [],  // built-in news sources: {name, url, category, kind}
    settingsError: '',
    settingsNotice: '',
    settingsSaving: false,

    // ── Trading settings (mode, strategy and paper / live) ──────────────────

    // Opens the trading settings dialog with a clean form.
    openTradingSettings() {
        this.tradingSettingsError = '';
        this.showLiveConfirm = false;
        this.liveConfirmText = '';
        this.tradingSettingsOpen = true;
    },

    // Closes the trading settings dialog and forgets a half-typed LIVE confirmation.
    closeTradingSettings() {
        if (!this.tradingSettingsOpen) return;
        this.tradingSettingsOpen = false;
        this.showLiveConfirm = false;
        this.liveConfirmText = '';
        this.restoreFocus();
    },

    // Changes the trading mode after confirmation.
    async setTradingMode(mode) {
        if (mode === this.mode) return;
        if (!this._actionAllowedWhileStale()) return;
        const option = this.tradingModes.find(m => m.id === mode);
        // Every mode change is confirmed so a stray click changes nothing.
        if (!confirm(`Change trading mode to ${option ? option.label : mode}?\n\n${option ? option.description : ''}`)) return;
        this.tradingSettingsError = '';
        try {
            const response = await fetch('/api/control/trading-mode', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ mode }),
            });
            if (!response.ok) {
                this.tradingSettingsError = await this._refusalDetail(response);
                return;
            }
        } catch (e) {
            this.tradingSettingsError = `Trading mode not changed — the server could not be reached (${e.message}).`;
            return;
        }
        this.mode = mode;
        this.loadDashboard();
    },

    // Switches between paper and live. Back to paper is a plain confirm; to live the typed LIVE is the confirmation.
    async setTradingEnvironment(environment) {
        if (environment === this.environment) return;
        if (!this._actionAllowedWhileStale()) return;
        if (environment === 'paper' && !confirm('Switch to PAPER trading?\n\nOrders will be simulated and nothing will be sent to Kraken.')) return;
        if (environment === 'live' && this.liveConfirmText !== 'LIVE') return;
        this.tradingSettingsError = '';
        try {
            const response = await fetch('/api/control/trading-environment', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ environment, confirmation: this.liveConfirmText }),
            });
            if (!response.ok) {
                this.tradingSettingsError = await this._refusalDetail(response);
                return;
            }
        } catch (e) {
            this.tradingSettingsError = `Trading environment not changed — the server could not be reached (${e.message}).`;
            return;
        }
        this.environment = environment;
        this.showLiveConfirm = false;
        this.liveConfirmText = '';
        this.loadDashboard();
    },

    // Selects the active strategy after confirmation.
    async selectStrategy(id) {
        const selected = this.strategies.find(s => s.id === id);
        if (!selected || selected.selected) return;
        if (!this._actionAllowedWhileStale()) return;
        // Every strategy change is confirmed so a stray click changes nothing.
        const parts = this._strategyLabelParts(selected);
        if (!confirm(`Change strategy to ${parts.title}?\n\n${parts.text}`)) return;
        this.tradingSettingsError = '';
        try {
            const response = await fetch(`/api/control/strategies/${encodeURIComponent(id)}/select`, { method: 'POST' });
            if (!response.ok) {
                this.tradingSettingsError = await this._refusalDetail(response);
                return;
            }
        } catch (e) {
            this.tradingSettingsError = `Strategy not changed — the server could not be reached (${e.message}).`;
            return;
        }
        this.strategies = this.strategies.map(s => ({ ...s, selected: s.id === id }));
        this.activeStrategyId = id;
        this.activeStrategyLabel = selected.label || this._strategyLabel(id);
        this.loadDashboard();
    },

    // Splits a strategy label "Name (what it does)" into a title and a description.
    _strategyLabelParts(strategy) {
        const label = strategy.label || strategy.id;
        const match = label.match(/^(.*?)\s*\((.*)\)$/);
        return match ? { title: match[1], text: match[2] } : { title: label, text: '' };
    },

    // ── Settings dialog ─────────────────────────────────────────────────────

    // Fields grouped in the order the API lists them.
    get settingsGroups() {
        const groups = [];
        for (const field of this.settingsFields) {
            let group = groups.find(g => g.name === field.group);
            if (!group) groups.push(group = { name: field.group, fields: [] });
            group.fields.push(field);
        }
        return groups;
    },

    // Fetches the settings fields; throws with the server's detail when the request fails.
    async loadSettings() {
        const response = await fetch('/api/settings');
        if (!response.ok) throw new Error(await this._refusalDetail(response));
        const data = await response.json();
        // Fill the draft first: the dialog's list templates read it as soon as the fields arrive.
        this.resetSettingsDraft(data.fields);
        this.settingsFields = data.fields;
        this.settingsRestartRequired = data.restart_required;
        this.settingsNewsFeeds = data.news_feeds || [];
    },

    // Opens the settings dialog with freshly loaded values (or the reason they could not load).
    async openSettings() {
        this.settingsError = '';
        this.settingsNotice = '';
        try {
            await this.loadSettings();
        } catch (e) {
            // Still open the dialog so the operator sees why the settings are missing or stale.
            this.settingsError = `Could not load settings: ${e.message}`;
        }
        this.resetSettingsDraft();
        if (!this.settingsGroups.some(g => g.name === this.settingsGroup)) {
            this.settingsGroup = this.settingsGroups[0]?.name || '';
        }
        this.settingsOpen = true;
    },

    // Closes the settings dialog; unsaved changes are only dropped after confirmation.
    closeSettings() {
        if (!this.settingsOpen) return;
        if (this.settingsChangeCount() > 0 && !confirm('Discard your unsaved settings changes?')) return;
        this.settingsOpen = false;
        this.restoreFocus();
    },

    // Fills the form with the saved values (lists as arrays, secrets blank).
    resetSettingsDraft(fields = this.settingsFields) {
        const draft = {};
        const clear = {};
        for (const field of fields) {
            if (field.secret) {
                draft[field.key] = '';
                clear[field.key] = false;
            } else if (field.kind === 'list') {
                draft[field.key] = JSON.parse(field.value || '[]');
            } else {
                draft[field.key] = field.value;
            }
        }
        this.settingsDraft = draft;
        this.settingsClear = clear;
    },

    // Adds the typed entry to a list setting (market symbols are upper-cased); duplicates are ignored.
    addListItem(field, input) {
        let value = input.value.trim();
        if (field.key === 'FIXED_MARKETS') value = value.toUpperCase();
        if (value && !this.settingsDraft[field.key].includes(value)) {
            this.settingsDraft[field.key] = [...this.settingsDraft[field.key], value];
        }
        input.value = '';
        input.focus();
    },

    // Removes one entry from a list setting. A removed extra news feed also loses its off switch.
    removeListItem(key, index) {
        const removed = this.settingsDraft[key][index];
        this.settingsDraft[key] = this.settingsDraft[key].filter((_, i) => i !== index);
        if (key === 'NEWS_SOURCES' && removed) {
            const name = removed.split('::')[0].trim();
            this.settingsDraft.DISABLED_NEWS_SOURCES = this.settingsDraft.DISABLED_NEWS_SOURCES.filter(n => n !== name);
        }
    },

    // ── News feed switches (the DISABLED_NEWS_SOURCES setting) ──────────────

    // Every news feed the switches cover: built-in ones by category, then the extra feeds being edited.
    get newsFeedGroups() {
        const groups = [];
        for (const feed of this.settingsNewsFeeds) {
            let group = groups.find(g => g.name === feed.category);
            if (!group) groups.push(group = { name: feed.category, feeds: [] });
            group.feeds.push(feed);
        }
        const extra = (this.settingsDraft.NEWS_SOURCES || []).map(spec => {
            const [name, ...url] = spec.split('::');
            return { name: name.trim(), url: url.join('::').trim() };
        });
        if (extra.length) groups.push({ name: 'Your feeds', feeds: extra });
        return groups;
    },

    // True unless the feed is switched off in the form.
    newsFeedOn(name) {
        return !(this.settingsDraft.DISABLED_NEWS_SOURCES || []).includes(name);
    },

    // Flips one feed's switch in the form (saved with the other settings).
    toggleNewsFeed(name) {
        const off = this.settingsDraft.DISABLED_NEWS_SOURCES;
        this.settingsDraft.DISABLED_NEWS_SOURCES = off.includes(name) ? off.filter(n => n !== name) : [...off, name];
    },

    // Switches every feed on, or every feed off.
    setAllNewsFeeds(on) {
        this.settingsDraft.DISABLED_NEWS_SOURCES = on ? [] : this.newsFeedGroups.flatMap(g => g.feeds.map(f => f.name));
    },

    // "12 of 21 on" for the switch panel.
    newsFeedSummary() {
        const names = this.newsFeedGroups.flatMap(g => g.feeds.map(f => f.name));
        return `${names.filter(n => this.newsFeedOn(n)).length} of ${names.length} on`;
    },

    // The feed's host name for display, e.g. coindesk.com.
    feedHost(url) {
        try {
            return new URL(url).host.replace(/^www\./, '');
        } catch {
            return url;
        }
    },

    // The edited settings as the API expects them: {KEY: text}. Lists become JSON arrays.
    settingsChanges() {
        const changes = {};
        for (const field of this.settingsFields) {
            const draft = this.settingsDraft[field.key];
            if (draft === undefined) continue;
            if (field.secret) {
                if (this.settingsClear[field.key]) changes[field.key] = '';
                else if (draft !== '') changes[field.key] = draft;
            } else if (field.kind === 'list') {
                const edited = JSON.stringify(draft);
                if (edited !== JSON.stringify(JSON.parse(field.value || '[]'))) changes[field.key] = edited;
            } else if (draft.trim() !== field.value) {
                changes[field.key] = draft.trim();
            }
        }
        return changes;
    },

    // How many settings differ from their saved values.
    settingsChangeCount() {
        return Object.keys(this.settingsChanges()).length;
    },

    // True when a group has unsaved changes (its navigation entry shows a dot).
    settingsGroupChanged(group) {
        const changes = this.settingsChanges();
        return group.fields.some(f => f.key in changes);
    },

    // One confirmation line per change; secret values are never shown and lists show what was added/removed.
    _describeSettingChange(field, value) {
        const when = field.live ? 'applies now' : 'after restart';
        if (field.secret) return `• ${field.label}: ${value === '' ? 'cleared' : 'new value'} (${when})`;
        if (field.key === 'DISABLED_NEWS_SOURCES') {
            const before = JSON.parse(field.value || '[]');
            const after = JSON.parse(value);
            const parts = [];
            const off = after.filter(x => !before.includes(x));
            const on = before.filter(x => !after.includes(x));
            if (off.length) parts.push(`switched off ${off.join(', ')}`);
            if (on.length) parts.push(`switched on ${on.join(', ')}`);
            return `• ${field.label}: ${parts.join('; ') || 'reordered'} (${when})`;
        }
        if (field.kind === 'list') {
            const before = JSON.parse(field.value || '[]');
            const after = JSON.parse(value);
            const added = after.filter(x => !before.includes(x));
            const removed = before.filter(x => !after.includes(x));
            const parts = [];
            if (added.length) parts.push(`+ ${added.join(', ')}`);
            if (removed.length) parts.push(`− ${removed.join(', ')}`);
            if (!parts.length) parts.push('reordered');
            return `• ${field.label}: ${parts.join('; ')} (${when})`;
        }
        const show = text => (text === '' ? '(empty)' : text);
        return `• ${field.label}: ${show(field.value)} → ${show(value)} (${when})`;
    },

    // Saves the edited settings after a confirmation that lists every change.
    async saveSettings() {
        const changes = this.settingsChanges();
        const fields = this.settingsFields.filter(f => f.key in changes);
        if (fields.length === 0) return;
        if (!this._actionAllowedWhileStale()) return;
        // Every save is confirmed with the full list of changes so a stray click changes nothing.
        const summary = fields.map(f => this._describeSettingChange(f, changes[f.key])).join('\n');
        if (!confirm(`Save ${fields.length} setting change${fields.length === 1 ? '' : 's'}?\n\n${summary}`)) return;
        this.settingsError = '';
        this.settingsNotice = '';
        this.settingsSaving = true;
        try {
            const response = await fetch('/api/settings', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ changes }),
            });
            if (!response.ok) {
                this.settingsError = await this._refusalDetail(response);
                return;
            }
            const result = await response.json();
            this.settingsNotice = result.applied_live.length > 0
                ? `Saved. In use now: ${result.applied_live.join(', ')}.`
                : 'Saved.';
            await this.loadSettings();
            this.resetSettingsDraft();
            this.loadDashboard();
        } catch (e) {
            this.settingsError = this.settingsNotice
                ? `Saved, but reloading the settings failed: ${e.message}`
                : `Settings not saved: ${e.message}`;
        } finally {
            this.settingsSaving = false;
        }
    },
});
