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

// The frontend tests read the page sources: index.html holds the markup of
// every view, static/app/*.js the behaviour (core.js plus one file per view).

// readFrontend returns a frontend file's text.
func readFrontend(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(frontendDir, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// appScripts returns every dashboard module in static/app by file name.
func appScripts(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(frontendDir, "static", "app", "*.js"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no dashboard modules found in static/app: %v", err)
	}
	scripts := map[string]string{}
	for _, p := range paths {
		scripts[filepath.Base(p)] = readFrontend(t, filepath.Join("static", "app", filepath.Base(p)))
	}
	return scripts
}

// dashboardSource returns index.html followed by every dashboard module, for
// checks that apply to the whole dashboard wherever the code lives.
func dashboardSource(t *testing.T) string {
	t.Helper()
	scripts := appScripts(t)
	names := make([]string, 0, len(scripts))
	for name := range scripts {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := []string{readFrontend(t, "index.html")}
	for _, name := range names {
		parts = append(parts, scripts[name])
	}
	return strings.Join(parts, "\n")
}

// funcBody returns a JavaScript function or method from its signature to its
// matching closing brace (braces are counted, so nested blocks are included).
func funcBody(t *testing.T, text, signature string) string {
	t.Helper()
	i := strings.Index(text, signature)
	if i < 0 {
		t.Fatalf("function %q not found", signature)
	}
	open := strings.Index(text[i:], "{")
	if open < 0 {
		t.Fatalf("body of %q not found", signature)
	}
	depth := 0
	for j := i + open; j < len(text); j++ {
		switch text[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[i : j+1]
			}
		}
	}
	t.Fatalf("end of %q not found", signature)
	return ""
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

// tableHead returns the normalised <thead> of the table whose <tbody> has the given id.
func tableHead(t *testing.T, index, bodyID string) string {
	t.Helper()
	body := strings.Index(index, `id="`+bodyID+`"`)
	if body < 0 {
		t.Fatalf("tbody %q not found", bodyID)
	}
	start := strings.LastIndex(index[:body], "<table")
	end := start + strings.Index(index[start:], "</thead>")
	return strings.Join(strings.Fields(index[start:end]), " ")
}

// TestFrontendLayout covers the dashboard markup, the design system and the service worker.
func TestFrontendLayout(t *testing.T) {
	index := readFrontend(t, "index.html")
	approvals := readFrontend(t, "approvals.html")
	login := readFrontend(t, "login.html")
	css := readFrontend(t, "static/styles.css")
	sw := readFrontend(t, "sw.js")
	scripts := appScripts(t)

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
		mustContain(t, "index", index, `<body x-data="dashboard()">`)
		h := newHarness(t, harnessConfig{})
		for i := 2; i <= 9; i++ {
			path := "/index" + string(rune('0'+i))
			if rec := h.do(http.MethodGet, path, nil); rec.Code != http.StatusNotFound {
				t.Errorf("%s should not be routed, got %d", path, rec.Code)
			}
		}
		mustNotContain(t, "css", css, "layout-ledger-glass", "layout-radar", "layout-pulse-wall", "layout-incident-rail",
			"layout-holdings-map", "layout-candle-lab", "layout-dispatch-flow", "layout-signal-matrix")
	})

	t.Run("GIVEN the design system WHEN the stylesheet is inspected THEN one dark palette is defined as tokens and used by the page", func(t *testing.T) {
		root := between(t, css, ":root {", "\n}")
		mustContain(t, "tokens", root, "color-scheme: dark;", "--surface-page: #0b0e14;", "--surface-panel:", "--surface-raised:",
			"--border:", "--text:", "--text-secondary:", "--text-muted:", "--accent: #5b9dff;",
			"--profit: #3ccf8e;", "--loss: #ff6b6b;", "--warning:", "--live:", "--danger:", "--page-gutter:")
		mustContain(t, "body", between(t, css, "\nbody {", "}"), "background: var(--surface-page);", "color: var(--text);")
		mustNotContain(t, "css", css, "--surface: #ffffff;", "--bg: #f4f7fb;")
	})

	t.Run("GIVEN every page WHEN loaded THEN it uses the shared stylesheet and no inline or CDN styles", func(t *testing.T) {
		link := `<link rel="stylesheet" href="/static/styles.css">`
		for name, page := range map[string]string{"index": index, "approvals": approvals, "login": login} {
			mustContain(t, name, page, link)
			mustNotContain(t, name, page, "<style>", "cdn.tailwindcss.com", "fonts.googleapis.com")
		}
		h := newHarness(t, harnessConfig{})
		for _, path := range []string{"/static/styles.css", "/static/app/core.js", "/static/app/backtests.js"} {
			if rec := h.do(http.MethodGet, path, nil); rec.Code != http.StatusOK {
				t.Errorf("the server must serve %s from frontend/static, got %d", path, rec.Code)
			}
		}
	})

	t.Run("GIVEN the redesign WHEN markup and CSS are inspected THEN no Tailwind-style utility shim classes are left", func(t *testing.T) {
		shims := []string{"sticky", "top-0", "z-50", "flex", "items-center", "justify-between", "gap-2", "gap-3",
			"grid", "hidden", "text-sm", "font-bold", "px-4", "py-2", "rounded", "w-full"}
		for _, shim := range shims {
			if regexp.MustCompile(`(?m)^\.` + regexp.QuoteMeta(shim) + `\s*[{,]`).MatchString(css) {
				t.Errorf("css: utility class .%s is still defined", shim)
			}
		}
		classAttr := regexp.MustCompile(`\bclass="([^"]*)"`)
		for name, page := range map[string]string{"index": index, "approvals": approvals, "login": login} {
			for _, m := range classAttr.FindAllStringSubmatch(page, -1) {
				for _, cls := range strings.Fields(m[1]) {
					for _, shim := range shims {
						if cls == shim {
							t.Errorf("%s: utility class %q used in %q", name, cls, m[0])
						}
					}
				}
			}
		}
	})

	t.Run("GIVEN wide, medium and phone screens WHEN layout CSS is inspected THEN each breakpoint and reduced motion are handled", func(t *testing.T) {
		mustContain(t, "css", css,
			"@media (max-width: 1439px) {\n    .markets-layout", // the Markets detail goes under the table
			"@media (max-width: 1199px) {",                      // icon-rail navigation
			"@media (max-width: 767px) {",                       // bottom tab bar, 16 px gutter
			"--page-gutter: 16px;",
			"@media (prefers-reduced-motion: reduce) {",
			"minmax(", "repeat(auto-fit, minmax(min(100%, 320px), 1fr))")
		mustContain(t, "css", css, ".news-grid {", ".chart-pair-grid {", ".kpi-grid {", ".approval-details {", ".raw-signal-grid {")
		mustNotContain(t, "index", index, "max-width:1280px")
		mustNotContain(t, "approvals", approvals, "max-width:900px")
	})

	t.Run("GIVEN the equity graph WHEN labels are inspected THEN it is named Equity Graph", func(t *testing.T) {
		mustContain(t, "index", index, `<h2 class="panel-title">Equity Graph</h2>`)
		mustNotContain(t, "index", index, "Portfolio Value")
	})

	t.Run("GIVEN live equity data WHEN chart code is inspected THEN hover and padding are configured", func(t *testing.T) {
		overview := scripts["overview.js"]
		mustContain(t, "overview.js", overview, "_equityCursorLine", "update('none')", "interaction: { mode: 'index', intersect: false }",
			"mode: 'index'", "intersect: false", "pointHoverRadius: 4", "callbacks: {",
			"title: items => items.length ? this._formatEquityTimestamp(items[0].dataIndex) : ''",
			"Chart.getChart(canvas)", "grace: '35%'")
		mustNotContain(t, "overview.js", overview, "chart.config.options", "chart.options.scales", "_applyEquityAxisRange")
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

	t.Run("GIVEN a deploy of new dashboard code WHEN the service worker handles app assets THEN they are network-first, not stale-while-revalidate", func(t *testing.T) {
		mustContain(t, "sw.js", sw, "function isAppAsset(path) {",
			"path.startsWith('/static/app/')", "path === '/static/styles.css'")
		fetchHandler := between(t, sw, "self.addEventListener('fetch'", "// ── Fetch strategies")
		appBranch := between(t, fetchHandler, "if (isAppAsset(path)) {", "}")
		mustContain(t, "app asset branch", appBranch, "event.respondWith(networkFirst(req));")
		// The app branch is decided before the stale-while-revalidate fallback for other static files.
		mustAppearBefore(t, "fetch handler", fetchHandler, "if (isAppAsset(path)) {", "staleWhileRevalidate(req)")
	})

	t.Run("GIVEN trade tables WHEN rendered THEN the four tables share the striped trade-table style", func(t *testing.T) {
		if n := strings.Count(index, `class="table trade-table"`); n != 4 {
			t.Errorf("expected 4 trade tables, got %d", n)
		}
		mustContain(t, "index", index, `<tbody id="positions-body">`, `<tbody id="closed-body">`, `<tbody id="ledger-body">`, `<tbody id="rejected-trades-body">`)
		mustContain(t, "css", css, ".table tbody tr:nth-child(even) td {")
	})

	t.Run("GIVEN trade tables WHEN headers are inspected THEN columns appear in the requested order", func(t *testing.T) {
		expected := map[string][]string{
			"positions-body": {"Date Time", "Opened", "Market", "Direction", "Size", "Avg Price", "Value",
				"Unrealised P&amp;L", "Strategy", "Source", "Status", "Pos ID", "Signal", "Action"},
			"closed-body": {"Date Time Closed", "Date Time Opened", "Duration", "Market", "Direction", "Size",
				"Price", "Value", "P&amp;L", "Strategy", "Source", "Status", "Pos ID", "Open Signal", "Close Signal"},
			"ledger-body": {"Date Time", "Market", "Action", "Direction", "Size", "Price",
				"Value", "P&amp;L", "Strategy", "Source", "Status", "Pos ID", "Signal"},
			"rejected-trades-body": {"Date Time", "Market", "Direction", "Size", "Price", "Value", "Strategy",
				"Source", "Status", "Reason", "Confidence", "Signal"},
		}
		for table, headers := range expected {
			thead := tableHead(t, index, table)
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

	t.Run("GIVEN open positions WHEN rendered THEN rows use the API's position keys and fall back to the opening trade", func(t *testing.T) {
		rows := between(t, index, `<tbody id="positions-body">`, "</tbody>")
		mustContain(t, "positions rows", rows, `x-for="p in filteredRows('positions', positions)"`, "fmtDateTime(positionOpenedDate(p))",
			"positionAge(p)", "_strategyLabel(_positionStrategy(p))", "_positionSource(p)", "positionStatusLabel(p)")
		mustContain(t, "positions filter", index, `id="positions-filter"`, "toggleFilterMarket('positions', m)")
		trading := scripts["trading.js"]
		mustContain(t, "trading.js", trading, "position.opened_at || position.timestamp || position.entry_at",
			"t.trade_type === 'open'", "t.position_id_full === fullId", "t.position_id === shortId",
			"fmtDuration(Date.now() - opened)")

		raw, _ := json.Marshal(app.PositionView{Status: "open"})
		mustContain(t, "position JSON", string(raw), `"opened_at"`, `"strategy"`, `"status":"open"`, `"position_id_full"`)
	})

	t.Run("GIVEN the redesigned layout WHEN hooks are inspected THEN functionality anchors remain", func(t *testing.T) {
		mustContain(t, "index", index, `id="equity-chart"`, `id="markets-table"`, `id="signals-list"`, `id="positions-list"`,
			`id="approvals-queue"`, `id="ledger-body"`, `id="pnl-day-body"`, `id="pnl-market-body"`, `id="closed-body"`,
			`id="candle-detail"`, `id="news-grid"`, "activateStop()", "resumeBot()", "toggleMarket(", "selectStrategy(",
			"resetPositions()", "showSignal(", `href="/api/export/trades.csv"`)
		mustContain(t, "approvals", approvals, `id="count-badge"`, `id="container"`, `data-approval-action="approve"`,
			`data-approval-action="reject"`, "decide(button.dataset.approvalId, button.dataset.approvalAction)",
			"setInterval(load, 3000)")
	})

	t.Run("GIVEN execution rejections WHEN markup is inspected THEN the Rejected Trades register is wired", func(t *testing.T) {
		mustContain(t, "trading.js", scripts["trading.js"], "loadRejectedTrades() {", "'/api/rejected-trades'")
		rows := between(t, index, `<tbody id="rejected-trades-body">`, "</tbody>")
		mustContain(t, "rejected rows", rows, "r.confidence", "_strategyLabel(r.strategy)",
			"r.source || 'system'", "Rejected</span>", "(r.reason || '').replace")
		mustContain(t, "index", index, "Rejected Trades")
		mustContain(t, "sw.js", sw, "'/api/rejected-trades'")
	})

	t.Run("GIVEN strategy context WHEN markup is inspected THEN the key figures and tables show strategy", func(t *testing.T) {
		mustContain(t, "index", index, "Active Strategy", "activeStrategyLabel", "_strategyLabel(t.strategy)")
		if n := strings.Count(tableHead(t, index, "ledger-body"), ">Strategy</th>"); n != 1 {
			t.Errorf("ledger header has %d Strategy columns, want 1", n)
		}
		if n := strings.Count(between(t, index, `<tbody id="ledger-body">`, "</tbody>"), "_strategyLabel("); n != 1 {
			t.Errorf("ledger rows have %d strategy cells, want 1", n)
		}
	})

	t.Run("GIVEN a signal is clicked WHEN its detail modal opens THEN the panel is centred like the other dialogs", func(t *testing.T) {
		modal := between(t, index, "<!-- ── Signal Detail Modal", `<template x-if="signalModal">`)
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
		mustContain(t, "header", header, `<span class="hdr-llm"`, `class="llm-dot"`, "llmStatus", "llmModel || 'Not configured'")
		mustContain(t, "css", between(t, css, ".hdr-llm {", "}"), "display: flex;")
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
