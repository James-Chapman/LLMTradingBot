# Frontend Context

Load the targeted parts of `index.html` needed for frontend work without reading the entire 1600+ line file.

## Steps

1. Read `kraken-bot/docs/11-frontend.md` — full doc (panel inventory, state shape, rendering patterns)
2. Read lines 774–840 of `kraken-bot/frontend/index.html` — Alpine.js state initialisation (`dashboard()` factory + `init()`)
3. Read lines 800–815 of `kraken-bot/frontend/index.html` — panel flags and their default collapsed/expanded state

Then, based on what the user is about to work on, read only the relevant section:
- **Signals panel** → search for `renderSignals` and read ±30 lines
- **Trade Ledger** → search for `renderLedger` and read ±80 lines
- **Closed Positions** → search for `renderClosedTrades` and read ±80 lines
- **Charts** → search for `_fetchAndRender` and read ±100 lines
- **HTML structure for a panel** → grep for the panel's label text and read ±40 lines around it

After reading, output:
- Current panel list with collapsed/expanded defaults
- The state properties relevant to the area being changed
- Any existing helper functions that can be reused (e.g. `fmt2`, `fmtDuration`, `timeAgo`, `pinToWindow`)

Do not output raw HTML. Summarise structure and patterns only.
