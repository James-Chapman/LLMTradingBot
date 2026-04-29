"""BDD coverage for responsive frontend layout and shared CSS."""

import unittest
from pathlib import Path


ROOT_DIR = Path(__file__).resolve().parents[1]
FRONTEND_DIR = ROOT_DIR / "frontend"
STATIC_CSS = FRONTEND_DIR / "static" / "styles.css"


class FrontendLayoutBDDTests(unittest.TestCase):
    # GIVEN the trial dashboard layouts are removed WHEN frontend files and routes are inspected
    # THEN only the promoted primary index dashboard remains.
    def test_given_trial_dashboard_layouts_removed_when_inspected_then_only_primary_index_remains(self) -> None:
        backend_main = (ROOT_DIR / "backend" / "main.py").read_text(encoding="utf-8")
        css = STATIC_CSS.read_text(encoding="utf-8")
        index = (FRONTEND_DIR / "index.html").read_text(encoding="utf-8")

        self.assertEqual(["index.html"], sorted(path.name for path in FRONTEND_DIR.glob("index*.html")))
        self.assertIn('<body class="layout-ledger-glass" x-data="dashboard()">', index)

        for token in [
            '@app.get("/index2")',
            '@app.get("/index3")',
            '@app.get("/index4")',
            '@app.get("/index5")',
            '@app.get("/index6")',
            '@app.get("/index7")',
            '@app.get("/index8")',
            '@app.get("/index9")',
        ]:
            self.assertNotIn(token, backend_main)

        for token in [
            "layout-radar",
            "layout-pulse-wall",
            "layout-incident-rail",
            "layout-holdings-map",
            "layout-candle-lab",
            "layout-dispatch-flow",
            "layout-signal-matrix",
        ]:
            self.assertNotIn(token, css)

    # GIVEN the operator uses the primary dashboard WHEN the theme is inspected
    # THEN index uses the cool dark ledger glass palette.
    def test_given_primary_index_when_theme_inspected_then_default_is_ledger_glass(self) -> None:
        css = STATIC_CSS.read_text(encoding="utf-8")
        index = (FRONTEND_DIR / "index.html").read_text(encoding="utf-8")

        theme_start = css.index("body.layout-ledger-glass {")
        theme_end = css.index(".layout-ledger-glass .app-header {", theme_start)
        theme_block = css[theme_start:theme_end]

        self.assertIn('<body class="layout-ledger-glass" x-data="dashboard()">', index)
        self.assertIn("--bg: #070b12;", theme_block)
        self.assertIn("--surface: #101826;", theme_block)
        self.assertIn("--blue: #5cc8ff;", theme_block)
        self.assertIn("--green: #2dd4bf;", theme_block)
        self.assertIn("background: linear-gradient(180deg, #070b12 0%, #0b1220 48%, #080d16 100%);", theme_block)
        self.assertNotIn("--surface: #ffffff;", theme_block)
        self.assertNotIn("--bg: #f4f7fb;", theme_block)

    # GIVEN price monitoring is a primary workflow WHEN dashboard shells load
    # THEN Price Charts start expanded and share the same fixed height as Crypto News.
    def test_given_price_charts_when_dashboard_loads_then_panel_is_expanded_and_matches_news_height(self) -> None:
        css = STATIC_CSS.read_text(encoding="utf-8")

        for page_path in [FRONTEND_DIR / "index.html"]:
            page = page_path.read_text(encoding="utf-8")
            self.assertIn("priceCharts: true, news: true", page)
            self.assertIn("<!-- Price Charts (5-min & 15-min candles, expanded by default) -->", page)

        for token in [
            "--news-chart-panel-height",
            "--news-chart-panel-height: clamp(1140px, 126vh, 1680px);",
            ".area-charts,\n.area-news {\n    height: var(--news-chart-panel-height);",
            ".area-charts > .panel-body,\n.area-news > .panel-body {",
            "overflow-y: auto;",
            ".area-charts .chart-grid,\n.area-news .news-grid {",
            ".area-charts .chart-pair-grid {\n    grid-template-columns: repeat(2, minmax(0, 1fr));",
        ]:
            self.assertIn(token, css)

    # GIVEN the dashboard pages WHEN they are loaded by the browser
    # THEN shared CSS is loaded from the static stylesheet instead of embedded style blocks.
    def test_given_dashboard_pages_when_loaded_then_shared_static_css_is_used(self) -> None:
        index = (FRONTEND_DIR / "index.html").read_text(encoding="utf-8")
        approvals = (FRONTEND_DIR / "approvals.html").read_text(encoding="utf-8")
        backend_main = (ROOT_DIR / "backend" / "main.py").read_text(encoding="utf-8")
        css = STATIC_CSS.read_text(encoding="utf-8")

        self.assertTrue(STATIC_CSS.exists())
        self.assertIn('<link rel="stylesheet" href="/static/styles.css">', index)
        self.assertIn('<link rel="stylesheet" href="/static/styles.css">', approvals)
        self.assertIn('app.mount("/static"', backend_main)
        self.assertIn('StaticFiles(directory=str(FRONTEND_DIR / "static"))', backend_main)
        self.assertNotIn("<style>", index)
        self.assertNotIn("<style>", approvals)
        self.assertNotIn("cdn.tailwindcss.com", index)
        self.assertNotIn("cdn.tailwindcss.com", approvals)
        for token in [".sticky", ".top-0", ".z-50", ".flex", ".items-center", ".justify-between", ".gap-2", ".gap-3"]:
            self.assertIn(token, css)

    # GIVEN widescreen monitors and mobile devices WHEN layout CSS is inspected
    # THEN the dashboard keeps full-width sections while dense status panels adapt responsively.
    def test_given_responsive_layout_when_css_inspected_then_adaptive_primitives_exist(self) -> None:
        css = STATIC_CSS.read_text(encoding="utf-8")
        index = (FRONTEND_DIR / "index.html").read_text(encoding="utf-8")
        approvals = (FRONTEND_DIR / "approvals.html").read_text(encoding="utf-8")

        expected_tokens = [
            "--page-gutter",
            ".app-shell",
            ".dashboard-grid",
            ".panel",
            ".panel-header",
            ".metric-strip",
            ".responsive-table",
            ".chart-grid",
            ".chart-pair-grid",
            ".core-grid",
            ".core-panel",
            ".core-scroll",
            ".llm-status",
            ".llm-row",
            ".approval-card",
            "clamp(",
            "minmax(",
            "@media",
            "width: calc(100% - (var(--page-gutter) * 2));",
            ".dashboard-grid {\n    display: flex;\n    flex-direction: column;",
            ".core-grid {\n    display: grid;",
            "grid-template-columns: repeat(3, minmax(0, 1fr));",
            "@media (max-width: 1100px) {\n    .activity-llm-row",
            "@media (max-width: 700px) {\n    :root",
            "height: var(--core-panel-height);",
            "overflow-y: auto;",
            ".approval-details,\n.chart-grid,\n.raw-signal-grid {\n    display: flex;\n    flex-direction: column;",
            ".news-grid {\n    display: grid;",
            "grid-template-columns: repeat(auto-fit, minmax(min(100%, 280px), 1fr));",
        ]
        for token in expected_tokens:
            self.assertIn(token, css)

        self.assertNotIn("grid-template-columns: repeat(12", css)
        self.assertNotIn("grid-column: span", css)
        self.assertNotIn("@media (max-width: 1100px) {\n    .core-grid", css)
        self.assertNotIn("max-width:1280px", index)
        self.assertNotIn("max-width:900px", approvals)

    # GIVEN the operator monitors account value WHEN dashboard labels are inspected
    # THEN the graph is named Equity Graph and the old Portfolio Value label is gone.
    def test_given_equity_graph_when_labels_inspected_then_portfolio_value_label_is_renamed(self) -> None:
        index = (FRONTEND_DIR / "index.html").read_text(encoding="utf-8")

        self.assertIn('<span class="label">Equity Graph</span>', index)
        self.assertNotIn('<span class="label">Portfolio Value</span>', index)

    # GIVEN the equity graph receives live dashboard data WHEN chart code is inspected
    # THEN it updates without reload, uses Chart.js scale grace, and avoids live option proxy mutation.
    def test_given_equity_graph_when_chart_code_inspected_then_live_hover_and_padding_are_configured(self) -> None:
        index = (FRONTEND_DIR / "index.html").read_text(encoding="utf-8")

        expected_tokens = [
            "_equityCursorLine",
            "update('none')",
            "interaction: { mode: 'index', intersect: false }",
            "mode: 'index'",
            "intersect: false",
            "pointHoverRadius: 4",
            "callbacks: {",
            "title: items => items.length ? this._formatEquityTimestamp(items[0].dataIndex) : ''",
            "Chart.getChart(canvas)",
            "grace: '35%'",
        ]
        for token in expected_tokens:
            self.assertIn(token, index)

        self.assertNotIn("chart.config.options", index)
        self.assertNotIn("chart.options.scales", index)
        self.assertNotIn("_applyEquityAxisRange", index)

    # GIVEN the dashboard shell changes WHEN the browser asks for the page or service worker
    # THEN stale cached HTML is not served before the fresh network copy.
    def test_given_dashboard_shell_changes_when_service_worker_runs_then_html_is_network_first(self) -> None:
        sw = (FRONTEND_DIR / "sw.js").read_text(encoding="utf-8")
        backend_main = (ROOT_DIR / "backend" / "main.py").read_text(encoding="utf-8")

        self.assertIn("const CACHE  = 'trading-bot-v0.5.25';", sw)
        self.assertIn("if (req.mode === 'navigate' || acceptsHtml) {", sw)
        self.assertIn("event.respondWith(networkFirst(req));", sw)
        self.assertNotIn("c.addAll(['/'])", sw)
        self.assertIn('"Cache-Control": "no-store, max-age=0"', backend_main)

    # GIVEN dense status panels WHEN dashboard structure is inspected
    # THEN markets, P&L, and signals share the core-grid row, while open positions,
    # closed positions, and trade ledger are full-width panels below.
    def test_given_status_panels_when_markup_inspected_then_core_panels_share_row(self) -> None:
        index = (FRONTEND_DIR / "index.html").read_text(encoding="utf-8")

        # core-grid contains Markets, P&L Summary, and Signals (3 panels)
        core_start = index.index('<div class="core-grid area-core">')
        core_end = index.index("<!-- Open Positions", core_start)
        core_markup = index[core_start:core_end]
        self.assertIn("<!-- Markets -->", core_markup)
        self.assertIn("<!-- P&L Summary -->", core_markup)
        self.assertIn("<!-- Signals -->", core_markup)
        self.assertNotIn("<!-- Open Positions", core_markup)
        self.assertEqual(core_markup.count('class="card panel core-panel'), 3)

        open_start = index.index("<!-- Open Positions -->")
        closed_start = index.index("<!-- Closed Positions", open_start)
        ledger_start = index.index("<!-- Trade Ledger -->", closed_start)
        self.assertLess(open_start, closed_start)
        self.assertLess(closed_start, ledger_start)
        self.assertNotIn('<div class="positions-row">', index)
        self.assertNotIn('<div class="pnl-ledger-row">', index)
        self.assertIn('class="card panel area-positions"', index[open_start:closed_start])
        self.assertIn('id="positions-filter"', index[open_start:closed_start])
        self.assertIn('id="positions-body"', index[open_start:closed_start])
        self.assertIn('class="card panel area-closed"', index[closed_start:ledger_start])
        self.assertIn('id="closed-body"', index[closed_start:ledger_start])
        self.assertIn('class="card panel area-ledger"', index[ledger_start:])
        self.assertIn('id="ledger-body"', index[ledger_start:])

    # GIVEN open positions are rendered WHEN dashboard code is inspected
    # THEN open positions use single-line table rows like closed positions.
    def test_given_open_positions_when_markup_inspected_then_single_line_table_rows_are_used(self) -> None:
        index = (FRONTEND_DIR / "index.html").read_text(encoding="utf-8")
        open_start = index.index("<!-- Open Positions -->")
        closed_start = index.index("<!-- Closed Positions", open_start)
        open_markup = index[open_start:closed_start]
        render_start = index.index("renderPositions(positions)")
        render_end = index.index("renderApprovals(approvals)", render_start)
        render_markup = index[render_start:render_end]
        backend_main = (ROOT_DIR / "backend" / "main.py").read_text(encoding="utf-8")

        self.assertIn("<table class=\"responsive-table trade-table\">", open_markup)
        self.assertIn("Date Time</th>", open_markup)
        self.assertIn("Opened</th>", open_markup)
        self.assertIn("Strategy</th>", open_markup)
        self.assertIn("Source</th>", open_markup)
        self.assertIn("Status</th>", open_markup)
        self.assertIn('id="positions-body"', open_markup)
        self.assertIn("_renderTradeFilter('positions')", render_markup)
        self.assertIn("_applyPositionsFilter()", render_markup)
        self.assertIn("_positionOpenedAt(p)", render_markup)
        self.assertIn("position.opened_at || position.timestamp || position.entry_at", render_markup)
        self.assertIn("t.trade_type === 'open'", render_markup)
        self.assertIn("t.position_id_full === fullId", render_markup)
        self.assertIn("t.position_id === shortId", render_markup)
        self.assertIn("_applyPositionsFilter();", index[index.index("renderLedger(trades)") :])
        self.assertIn("tbody.innerHTML = positions.map", render_markup)
        self.assertIn("return `<tr", render_markup)
        self.assertIn("fmtTime(openedDt)", render_markup)
        self.assertIn("fmtDuration(Date.now() - openedDt)", render_markup)
        self.assertIn("this._positionStrategy(p)", render_markup)
        self.assertIn("this._positionSource(p)", render_markup)
        self.assertIn("statusBadge", render_markup)
        self.assertIn('"opened_at": pos.timestamp.isoformat()', backend_main)
        self.assertIn('"strategy": meta.get("strategy_id", "")', backend_main)
        self.assertIn('"status": "open"', backend_main)
        self.assertNotIn('<div class="row-sep"', render_markup)

    # GIVEN trade-heavy tables WHEN dashboard styles are inspected
    # THEN open, closed, and ledger rows use subtle alternate-row contrast.
    def test_given_trade_tables_when_styles_loaded_then_rows_are_zebra_striped(self) -> None:
        index = (FRONTEND_DIR / "index.html").read_text(encoding="utf-8")
        css = (FRONTEND_DIR / "static" / "styles.css").read_text(encoding="utf-8")

        self.assertEqual(index.count('class="responsive-table trade-table"'), 4)
        self.assertIn('<tbody id="positions-body">', index)
        self.assertIn('<tbody id="closed-body">', index)
        self.assertIn('<tbody id="ledger-body">', index)
        self.assertIn('<tbody id="rejected-trades-body">', index)
        self.assertIn(".trade-table tbody tr:nth-child(even)", css)
        self.assertIn("background: rgba(139, 148, 158, 0.055);", css)

    # GIVEN dashboard trade tables WHEN their headers are inspected
    # THEN they expose the requested operational columns in order.
    def test_given_trade_tables_when_markup_inspected_then_columns_match_requested_order(self) -> None:
        index = (FRONTEND_DIR / "index.html").read_text(encoding="utf-8")

        expected_headers = {
            "positions": [
                "Date Time", "Opened", "Market", "Direction", "Size", "Avg Price", "Value",
                "Unrealised P&amp;L", "Strategy", "Source", "Status", "Pos ID", "Signal", "Action",
            ],
            "closed": [
                "Date Time Closed", "Date Time Opened", "Duration", "Market", "Direction", "Size",
                "Price", "Value", "P&amp;L", "Strategy", "Source", "Status", "Pos ID",
                "Open Signal", "Close Signal",
            ],
            "ledger": [
                "Date Time", "Market", "Strategy", "Action", "Direction", "Size", "Price",
                "Value", "P&amp;L", "Strategy", "Source", "Status", "Pos ID", "Signal",
            ],
            "rejected-trades": [
                "Date Time", "Market", "Direction", "Size", "Price", "Value", "Strategy",
                "Source", "Status", "Reason", "Confidence", "Signal",
            ],
        }

        for table_id, headers in expected_headers.items():
            table_start = index.rfind("<table", 0, index.index(f'id="{table_id}-body"'))
            thead = " ".join(index[table_start:index.index("</thead>", table_start)].split())
            cursor = 0
            for header in headers:
                cursor = thead.index(header, cursor) + len(header)

    # GIVEN Local LLM status data WHEN dashboard markup is inspected
    # THEN model, availability, briefing, and reflection rows each render on their own line.
    # Bot Activity and Local LLM now share an activity-llm-row (Activity left, LLM right).
    def test_given_local_llm_panel_when_markup_inspected_then_items_render_as_rows(self) -> None:
        index = (FRONTEND_DIR / "index.html").read_text(encoding="utf-8")
        llm_start = index.index("<!-- Local LLM (50%) -->")
        llm_end = index.index("<!-- Risk Rejections", llm_start)
        llm_markup = index[llm_start:llm_end]

        self.assertIn('class="llm-status"', llm_markup)
        self.assertIn('class="llm-row"', llm_markup)
        self.assertIn('class="llm-card"', llm_markup)
        self.assertNotIn('class="flex items-center gap-3"', llm_markup)

    # GIVEN the layout is refactored WHEN important dashboard hooks are inspected
    # THEN existing frontend behavior still has the DOM anchors it depends on.
    def test_given_refactored_layout_when_hooks_inspected_then_functionality_anchors_remain(self) -> None:
        index = (FRONTEND_DIR / "index.html").read_text(encoding="utf-8")
        approvals = (FRONTEND_DIR / "approvals.html").read_text(encoding="utf-8")

        for hook in [
            'id="equity-chart"',
            'id="markets-list"',
            'id="signals-list"',
            'id="positions-list"',
            'id="approvals-queue"',
            'id="ledger-body"',
            'id="pnl-day-body"',
            'id="pnl-market-body"',
            'id="closed-body"',
            'id="candle-grid"',
            'id="news-grid"',
            "activateStop()",
            "resumeBot()",
            "toggleMarket(",
            "selectStrategy(",
            "resetPositions()",
            "showSignal(",
        ]:
            self.assertIn(hook, index)

        for hook in [
            'id="count-badge"',
            'id="container"',
            "decide('${a.id}','approve')",
            "decide('${a.id}','reject')",
            "setInterval(load, 3000)",
        ]:
            self.assertIn(hook, approvals)

    # E13: GIVEN execution rejections exist WHEN dashboard markup is inspected
    # THEN the Rejected Trades register has its endpoint, table body, and confidence field.
    def test_given_rejected_trades_register_when_markup_inspected_then_required_fields_are_rendered(self) -> None:
        index = (FRONTEND_DIR / "index.html").read_text(encoding="utf-8")
        sw = (FRONTEND_DIR / "sw.js").read_text(encoding="utf-8")

        self.assertIn("Rejected Trades", index)
        self.assertIn("loadRejectedTrades()", index)
        self.assertIn("fetch('/api/rejected-trades')", index)
        self.assertIn("'/api/rejected-trades'", sw)
        self.assertIn('id="rejected-trades-body"', index)
        self.assertIn('class="responsive-table trade-table"', index)
        self.assertIn("r.confidence", index)
        self.assertIn("r.strategy", index)
        self.assertIn("r.source || 'system'", index)
        self.assertIn("Rejected</span>", index)
        self.assertIn("(r.reason || '').replace", index)

    # GIVEN strategy context is available WHEN dashboard markup is inspected
    # THEN the header and trade tables expose the active/row strategy.
    def test_given_strategy_context_when_markup_inspected_then_header_and_trade_tables_show_strategy(self) -> None:
        index = (FRONTEND_DIR / "index.html").read_text(encoding="utf-8")

        self.assertIn("Active Strategy", index)
        self.assertIn("activeStrategyLabel", index)
        self.assertIn("_strategyLabel(", index)
        self.assertIn('<th style="padding:6px 10px;color:var(--muted);font-weight:600;">Strategy</th>', index)
        self.assertIn("this._strategyLabel(t.strategy)", index)


    # GIVEN the header LLM status indicator WHEN markup is inspected
    # THEN the dot is inside a flex metric-block so width/height apply,
    # and the model label is bound to llmModel with a fallback.
    def test_given_header_llm_indicator_when_markup_inspected_then_dot_is_in_flex_container(self) -> None:
        index = (FRONTEND_DIR / "index.html").read_text(encoding="utf-8")

        # Locate the header section
        header_start = index.index('<header class="app-header')
        header_end = index.index("</header>", header_start) + len("</header>")
        header_markup = index[header_start:header_end]

        # The dot must exist and its colour binding must use llmStatus
        self.assertIn('class="llm-dot"', header_markup)
        self.assertIn("llmStatus", header_markup)

        # The metric-block containing the dot must opt into flex layout so the
        # 8×8 span dimensions actually render (inline elements ignore width/height)
        self.assertIn('display:flex', header_markup)

        # Model text must be bound with a "Not configured" fallback
        self.assertIn("llmModel || 'Not configured'", header_markup)


if __name__ == "__main__":
    unittest.main()
