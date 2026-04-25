# Check Docs

Detect documentation drift — symbols, columns, endpoints, or behaviours that exist in code but are missing or outdated in the docs folder.

## Steps

### 1. Collect changed symbols

Search the key backend files for patterns that should be reflected in docs:

- `kraken-bot/backend/storage/models.py` — every `Column(` definition per table
- `kraken-bot/backend/storage/repository.py` — every `def ` (public methods only, no leading `_`)
- `kraken-bot/backend/main.py` — every `@app.get` and `@app.post` decorator
- `kraken-bot/backend/ingestion/news_adapter.py` — every class ending in `Adapter`
- `kraken-bot/frontend/index.html` — every `panels.` key in the state block

### 2. Check each doc file

| Doc file | Must contain |
|----------|-------------|
| `docs/08-api-endpoints.md` | Every `@app.get`/`@app.post` path from main.py |
| `docs/03-background-loops.md` | Every `Adapter` class name from news_adapter.py |
| `docs/11-frontend.md` | Every `panels.` key; column counts for Trade Ledger and Closed Positions tables |
| `docs/02-domain-models.md` | Every Pydantic class from domain/models.py |
| `docs/05-execution-engine.md` | Key public methods of `PaperExecutionEngine` |

### 3. Report

Output a table:

| File | Item | Status |
|------|------|--------|
| 08-api-endpoints.md | GET /api/ohlc/{market} | ✓ present |
| 03-background-loops.md | NewsBTCAdapter | ✗ missing |
| ... | ... | ... |

Use ✓ for present, ✗ for missing, ⚠ for present but likely stale (e.g. column count mismatch).

### 4. Fix

For each ✗ or ⚠ item, make the minimal doc edit to bring it in sync. Do not rewrite sections — add or correct the specific missing detail only.

After fixing, re-state the table with all items showing ✓.
