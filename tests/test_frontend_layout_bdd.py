"""BDD coverage for responsive frontend layout and shared CSS."""

import unittest
from pathlib import Path


ROOT_DIR = Path(__file__).resolve().parents[1]
FRONTEND_DIR = ROOT_DIR / "frontend"
STATIC_CSS = FRONTEND_DIR / "static" / "styles.css"


class FrontendLayoutBDDTests(unittest.TestCase):
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
            "grid-template-columns: repeat(2, minmax(0, 1fr));",
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

        self.assertIn("const CACHE  = 'trading-bot-v0.4.0';", sw)
        self.assertIn("if (req.mode === 'navigate' || acceptsHtml) {", sw)
        self.assertIn("event.respondWith(networkFirst(req));", sw)
        self.assertNotIn("c.addAll(['/'])", sw)
        self.assertIn('"Cache-Control": "no-store, max-age=0"', backend_main)

    # GIVEN dense status panels WHEN dashboard structure is inspected
    # THEN markets and signals share the core-grid row, while open and closed positions
    # occupy their own positions-row (1/3 + 2/3 split) directly below.
    def test_given_status_panels_when_markup_inspected_then_core_panels_share_row(self) -> None:
        index = (FRONTEND_DIR / "index.html").read_text(encoding="utf-8")

        # core-grid contains only Markets and Signals (2 panels)
        core_start = index.index('<div class="core-grid area-core">')
        core_end = index.index("<!-- Open Positions", core_start)
        core_markup = index[core_start:core_end]
        self.assertIn("<!-- Markets -->", core_markup)
        self.assertIn("<!-- Signals -->", core_markup)
        self.assertNotIn("<!-- Open Positions", core_markup)
        self.assertEqual(core_markup.count('class="card panel core-panel'), 2)

        # positions-row holds Open Positions (1/3) and Closed Positions (2/3)
        pos_start = index.index('<div class="positions-row">')
        pos_end = index.index("<!-- P&L Summary", pos_start)
        pos_markup = index[pos_start:pos_end]
        self.assertIn("<!-- Open Positions (1/3) -->", pos_markup)
        self.assertIn("<!-- Closed Positions (2/3)", pos_markup)
        self.assertIn('id="positions-list"', pos_markup)
        self.assertIn('id="closed-body"', pos_markup)

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
        self.assertIn("r.confidence", index)
        self.assertIn("r.strategy", index)
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

        # The dot must exist and its colour binding must use llmAvailable
        self.assertIn('class="llm-dot"', header_markup)
        self.assertIn("llmAvailable", header_markup)

        # The metric-block containing the dot must opt into flex layout so the
        # 8×8 span dimensions actually render (inline elements ignore width/height)
        self.assertIn('display:flex', header_markup)

        # Model text must be bound with a "Not configured" fallback
        self.assertIn("llmModel || 'Not configured'", header_markup)


if __name__ == "__main__":
    unittest.main()
