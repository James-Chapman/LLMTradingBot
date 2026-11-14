package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/James-Chapman/LLMTradingBot/internal/app"
)

// Port of tests/test_frontend_layout_bdd.py. Assertions that inspected
// backend/main.py source text now check the Go server's behaviour instead.

// readFrontend returns a frontend file's text.
func readFrontend(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(frontendDir, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// mustContain fails for every token missing from text.
func mustContain(t *testing.T, what, text string, tokens ...string) {
	t.Helper()
	for _, tok := range tokens {
		if !strings.Contains(text, tok) {
			t.Errorf("%s: missing %q", what, tok)
		}
	}
}

// mustNotContain fails for every token present in text.
func mustNotContain(t *testing.T, what, text string, tokens ...string) {
	t.Helper()
	for _, tok := range tokens {
		if strings.Contains(text, tok) {
			t.Errorf("%s: unexpected %q", what, tok)
		}
	}
}

// between returns text from the first start marker up to the next end marker.
func between(t *testing.T, text, start, end string) string {
	t.Helper()
	i := strings.Index(text, start)
	if i < 0 {
		t.Fatalf("marker %q not found", start)
	}
	j := strings.Index(text[i:], end)
	if j < 0 {
		t.Fatalf("marker %q not found after %q", end, start)
	}
	return text[i : i+j]
}

// TestFrontendLayout covers the dashboard markup, CSS and service worker.
func TestFrontendLayout(t *testing.T) {
	index := readFrontend(t, "index.html")
	approvals := readFrontend(t, "approvals.html")
	css := readFrontend(t, "static/styles.css")
	sw := readFrontend(t, "sw.js")

	t.Run("GIVEN trial layouts were removed WHEN frontend files and routes are inspected THEN only the primary index remains", func(t *testing.T) {
		matches, _ := filepath.Glob(filepath.Join(frontendDir, "index*.html"))
		names := []string{}
		for _, m := range matches {
			names = append(names, filepath.Base(m))
		}
		sort.Strings(names)
		if len(names) != 1 || names[0] != "index.html" {
			t.Errorf("expected only index.html, got %v", names)
		}
		mustContain(t, "index", index, `<body class="layout-ledger-glass" x-data="dashboard()">`)
		h := newHarness(t, harnessConfig{})
		for i := 2; i <= 9; i++ {
			path := "/index" + string(rune('0'+i))
			if rec := h.do(http.MethodGet, path, nil); rec.Code != http.StatusNotFound {
				t.Errorf("%s should not be routed, got %d", path, rec.Code)
			}
		}
		mustNotContain(t, "css", css, "layout-radar", "layout-pulse-wall", "layout-incident-rail",
			"layout-holdings-map", "layout-candle-lab", "layout-dispatch-flow", "layout-signal-matrix")
	})

	t.Run("GIVEN the primary index WHEN the theme is inspected THEN it uses the ledger glass palette", func(t *testing.T) {
		theme := between(t, css, "body.layout-ledger-glass {", ".layout-ledger-glass .app-header {")
		mustContain(t, "theme", theme, "--bg: #070b12;", "--surface: #101826;", "--blue: #5cc8ff;", "--green: #2dd4bf;",
			"background: linear-gradient(180deg, #070b12 0%, #0b1220 48%, #080d16 100%);")
		mustNotContain(t, "theme", theme, "--surface: #ffffff;", "--bg: #f4f7fb;")
	})

	t.Run("GIVEN price monitoring WHEN the dashboard loads THEN price charts start expanded and match the news height", func(t *testing.T) {
		mustContain(t, "index", index, "priceCharts: true, news: true", "<!-- Price Charts (5-min & 15-min candles, expanded by default) -->")
		mustContain(t, "css", css,
			"--news-chart-panel-height",
			"--news-chart-panel-height: clamp(1140px, 126vh, 1680px);",
			".area-charts,\n.area-news {\n    height: var(--news-chart-panel-height);",
			".area-charts > .panel-body,\n.area-news > .panel-body {",
			"overflow-y: auto;",
			".area-charts .chart-grid,\n.area-news .news-grid {",
			".area-charts .chart-pair-grid {\n    grid-template-columns: repeat(2, minmax(0, 1fr));",
		)
	})

	t.Run("GIVEN the dashboard pages WHEN loaded THEN shared CSS comes from the static stylesheet", func(t *testing.T) {
		link := `<link rel="stylesheet" href="/static/styles.css">`
		mustContain(t, "index", index, link)
		mustContain(t, "approvals", approvals, link)
		mustNotContain(t, "index", index, "<style>", "cdn.tailwindcss.com")
		mustNotContain(t, "approvals", approvals, "<style>", "cdn.tailwindcss.com")
		mustContain(t, "css", css, ".sticky", ".top-0", ".z-50", ".flex", ".items-center", ".justify-between", ".gap-2", ".gap-3")
		h := newHarness(t, harnessConfig{})
		if rec := h.do(http.MethodGet, "/static/styles.css", nil); rec.Code != http.StatusOK {
			t.Errorf("the server must mount frontend/static at /static, got %d", rec.Code)
		}
	})

	t.Run("GIVEN wide and narrow screens WHEN layout CSS is inspected THEN adaptive primitives exist", func(t *testing.T) {
		mustContain(t, "css", css,
			"--page-gutter", ".app-shell", ".dashboard-grid", ".panel", ".panel-header", ".metric-strip",
			".responsive-table", ".chart-grid", ".chart-pair-grid", ".core-grid", ".core-panel", ".core-scroll",
			".llm-status", ".llm-row", ".approval-card", "clamp(", "minmax(", "@media",
			"width: calc(100% - (var(--page-gutter) * 2));",
			".dashboard-grid {\n    display: flex;\n    flex-direction: column;",
			".core-grid {\n    display: grid;",
			"grid-template-columns: repeat(3, minmax(0, 1fr));",
			"@media (max-width: 1100px) {\n    .activity-llm-row",
			"@media (max-width: 700px) {\n    :root",
			"height: var(--core-panel-height);",
			".approval-details,\n.chart-grid,\n.raw-signal-grid {\n    display: flex;\n    flex-direction: column;",
			".news-grid {\n    display: grid;",
			"grid-template-columns: repeat(auto-fit, minmax(min(100%, 280px), 1fr));",
		)
		mustNotContain(t, "css", css, "grid-template-columns: repeat(12", "grid-column: span", "@media (max-width: 1100px) {\n    .core-grid")
		mustNotContain(t, "index", index, "max-width:1280px")
		mustNotContain(t, "approvals", approvals, "max-width:900px")
	})

	t.Run("GIVEN the equity graph WHEN labels are inspected THEN it is named Equity Graph", func(t *testing.T) {
		mustContain(t, "index", index, `<span class="label">Equity Graph</span>`)
		mustNotContain(t, "index", index, `<span class="label">Portfolio Value</span>`)
	})

	t.Run("GIVEN live equity data WHEN chart code is inspected THEN hover and padding are configured", func(t *testing.T) {
		mustContain(t, "index", index, "_equityCursorLine", "update('none')", "interaction: { mode: 'index', intersect: false }",
			"mode: 'index'", "intersect: false", "pointHoverRadius: 4", "callbacks: {",
			"title: items => items.length ? this._formatEquityTimestamp(items[0].dataIndex) : ''",
			"Chart.getChart(canvas)", "grace: '35%'")
		mustNotContain(t, "index", index, "chart.config.options", "chart.options.scales", "_applyEquityAxisRange")
	})

	t.Run("GIVEN the dashboard shell changes WHEN the service worker runs THEN HTML is network-first and versioned", func(t *testing.T) {
		mustContain(t, "sw.js", sw, "const CACHE  = 'trading-bot-v"+app.Version+"';",
			"if (req.mode === 'navigate' || acceptsHtml) {", "event.respondWith(networkFirst(req));")
		mustNotContain(t, "sw.js", sw, "c.addAll(['/'])")
		h := newHarness(t, harnessConfig{})
		for _, path := range []string{"/", "/approvals", "/sw.js"} {
			if got := h.do(http.MethodGet, path, nil).Header().Get("Cache-Control"); got != "no-store, max-age=0" {
				t.Errorf("%s: Cache-Control = %q", path, got)
			}
		}
	})

	t.Run("GIVEN dense status panels WHEN markup is inspected THEN core panels share a row and trade panels follow", func(t *testing.T) {
		core := between(t, index, `<div class="core-grid area-core">`, "<!-- Open Positions")
		mustContain(t, "core", core, "<!-- Markets -->", "<!-- P&L Summary -->", "<!-- Signals -->")
		if n := strings.Count(core, `class="card panel core-panel`); n != 3 {
			t.Errorf("expected 3 core panels, got %d", n)
		}
		open := between(t, index, "<!-- Open Positions -->", "<!-- Closed Positions")
		closed := between(t, index, "<!-- Closed Positions", "<!-- Trade Ledger -->")
		ledger := index[strings.Index(index, "<!-- Trade Ledger -->"):]
		mustNotContain(t, "index", index, `<div class="positions-row">`, `<div class="pnl-ledger-row">`)
		mustContain(t, "open", open, `class="card panel area-positions"`, `id="positions-filter"`, `id="positions-body"`)
		mustContain(t, "closed", closed, `class="card panel area-closed"`, `id="closed-body"`)
		mustContain(t, "ledger", ledger, `class="card panel area-ledger"`, `id="ledger-body"`)
	})

	t.Run("GIVEN open positions WHEN rendered THEN single-line rows use the API's position keys", func(t *testing.T) {
		open := between(t, index, "<!-- Open Positions -->", "<!-- Closed Positions")
		render := between(t, index, "renderPositions(positions)", "renderApprovals(approvals)")
		mustContain(t, "open", open, `<table class="responsive-table trade-table">`, "Date Time</th>", "Opened</th>",
			"Strategy</th>", "Source</th>", "Status</th>", `id="positions-body"`)
		mustContain(t, "render", render, "_renderTradeFilter('positions')", "_applyPositionsFilter()", "_positionOpenedAt(p)",
			"position.opened_at || position.timestamp || position.entry_at", "t.trade_type === 'open'",
			"t.position_id_full === fullId", "t.position_id === shortId", "tbody.innerHTML = positions.map",
			"return `<tr", "fmtTime(openedDt)", "fmtDuration(Date.now() - openedDt)", "this._positionStrategy(p)",
			"this._positionSource(p)", "statusBadge")
		mustNotContain(t, "render", render, `<div class="row-sep"`)
		mustContain(t, "ledger render", index[strings.Index(index, "renderLedger(trades)"):], "_applyPositionsFilter();")

		raw, _ := json.Marshal(app.PositionView{Status: "open"})
		mustContain(t, "position JSON", string(raw), `"opened_at"`, `"strategy"`, `"status":"open"`, `"position_id_full"`)
	})

	t.Run("GIVEN trade tables WHEN styles load THEN rows are zebra striped", func(t *testing.T) {
		if n := strings.Count(index, `class="responsive-table trade-table"`); n != 4 {
			t.Errorf("expected 4 trade tables, got %d", n)
		}
		mustContain(t, "index", index, `<tbody id="positions-body">`, `<tbody id="closed-body">`, `<tbody id="ledger-body">`, `<tbody id="rejected-trades-body">`)
		mustContain(t, "css", css, ".trade-table tbody tr:nth-child(even)", "background: rgba(139, 148, 158, 0.055);")
	})

	t.Run("GIVEN trade tables WHEN headers are inspected THEN columns appear in the requested order", func(t *testing.T) {
		expected := map[string][]string{
			"positions": {"Date Time", "Opened", "Market", "Direction", "Size", "Avg Price", "Value",
				"Unrealised P&amp;L", "Strategy", "Source", "Status", "Pos ID", "Signal", "Action"},
			"closed": {"Date Time Closed", "Date Time Opened", "Duration", "Market", "Direction", "Size",
				"Price", "Value", "P&amp;L", "Strategy", "Source", "Status", "Pos ID", "Open Signal", "Close Signal"},
			"ledger": {"Date Time", "Market", "Strategy", "Action", "Direction", "Size", "Price",
				"Value", "P&amp;L", "Strategy", "Source", "Status", "Pos ID", "Signal"},
			"rejected-trades": {"Date Time", "Market", "Direction", "Size", "Price", "Value", "Strategy",
				"Source", "Status", "Reason", "Confidence", "Signal"},
		}
		for table, headers := range expected {
			body := strings.Index(index, `id="`+table+`-body"`)
			start := strings.LastIndex(index[:body], "<table")
			end := start + strings.Index(index[start:], "</thead>")
			thead := strings.Join(strings.Fields(index[start:end]), " ")
			cursor := 0
			for _, header := range headers {
				i := strings.Index(thead[cursor:], header)
				if i < 0 {
					t.Errorf("%s: header %q missing or out of order", table, header)
					break
				}
				cursor += i + len(header)
			}
		}
	})

	t.Run("GIVEN Local LLM status WHEN markup is inspected THEN items render as rows", func(t *testing.T) {
		llm := between(t, index, "<!-- Local LLM (50%) -->", "<!-- Risk Rejections")
		mustContain(t, "llm", llm, `class="llm-status"`, `class="llm-row"`, `class="llm-card"`)
		mustNotContain(t, "llm", llm, `class="flex items-center gap-3"`)
	})

	t.Run("GIVEN the refactored layout WHEN hooks are inspected THEN functionality anchors remain", func(t *testing.T) {
		mustContain(t, "index", index, `id="equity-chart"`, `id="markets-list"`, `id="signals-list"`, `id="positions-list"`,
			`id="approvals-queue"`, `id="ledger-body"`, `id="pnl-day-body"`, `id="pnl-market-body"`, `id="closed-body"`,
			`id="candle-grid"`, `id="news-grid"`, "activateStop()", "resumeBot()", "toggleMarket(", "selectStrategy(",
			"resetPositions()", "showSignal(")
		mustContain(t, "approvals", approvals, `id="count-badge"`, `id="container"`, "decide('${a.id}','approve')",
			"decide('${a.id}','reject')", "setInterval(load, 3000)")
	})

	t.Run("GIVEN execution rejections WHEN markup is inspected THEN the Rejected Trades register is wired", func(t *testing.T) {
		mustContain(t, "index", index, "Rejected Trades", "loadRejectedTrades()", "fetch('/api/rejected-trades')",
			`id="rejected-trades-body"`, `class="responsive-table trade-table"`, "r.confidence", "r.strategy",
			"r.source || 'system'", "Rejected</span>", "(r.reason || '').replace")
		mustContain(t, "sw.js", sw, "'/api/rejected-trades'")
	})

	t.Run("GIVEN strategy context WHEN markup is inspected THEN the header and tables show strategy", func(t *testing.T) {
		mustContain(t, "index", index, "Active Strategy", "activeStrategyLabel", "_strategyLabel(",
			`<th style="padding:6px 10px;color:var(--muted);font-weight:600;">Strategy</th>`, "this._strategyLabel(t.strategy)")
	})

	t.Run("GIVEN a signal is clicked WHEN its detail modal opens THEN the panel is centred like the other dialogs", func(t *testing.T) {
		modal := between(t, index, "<!-- Signal Detail Modal", `<template x-if="signalModal">`)
		mustContain(t, "signal modal", modal, `x-show="signalModal" x-cloak class="modal-backdrop"`, `class="modal-panel signal-panel"`)
		mustContain(t, "css", css, ".modal-backdrop {\n    position: fixed;\n    inset: 0;\n    z-index: 1000;\n    display: flex;\n    align-items: center;\n    justify-content: center;",
			".signal-panel {")
	})

	t.Run("GIVEN elements toggled by x-show WHEN shown THEN their layout does not rely on an inline display style", func(t *testing.T) {
		// Alpine's x-show removes the inline display property when it shows an
		// element, so an inline display:flex would silently become display:block.
		tag := regexp.MustCompile(`<[a-z]+\b[^>]*>`)
		inlineDisplay := regexp.MustCompile(`style="[^"]*display\s*:`)
		for _, el := range tag.FindAllString(index, -1) {
			if strings.Contains(el, " x-show=") && inlineDisplay.MatchString(el) {
				t.Errorf("x-show element with an inline display style: %s", strings.Join(strings.Fields(el), " "))
			}
		}
	})

	t.Run("GIVEN the header LLM indicator WHEN markup is inspected THEN the dot sits in a flex container", func(t *testing.T) {
		header := between(t, index, `<header class="app-header`, "</header>")
		mustContain(t, "header", header, `class="llm-dot"`, "llmStatus", "display:flex", "llmModel || 'Not configured'")
	})
}

// TestPyFloat covers the Python-compatible float formatting used by the CSV export.
func TestPyFloat(t *testing.T) {
	t.Run("GIVEN floats WHEN formatted THEN they match Python's str(float)", func(t *testing.T) {
		cases := map[float64]string{65000: "65000.0", 0.1: "0.1", -2.5: "-2.5", 0: "0.0", 1e-05: "1e-05", 1e16: "1e+16", 0.0001: "0.0001"}
		for in, want := range cases {
			if got := pyFloat(in); got != want {
				t.Errorf("pyFloat(%v) = %q, want %q", in, got, want)
			}
		}
	})
}
