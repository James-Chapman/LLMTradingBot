# Operations Guide

---

## Prerequisites

| Requirement | Version | Notes |
|-------------|---------|-------|
| Python | 3.11+ | Earlier versions untested |
| OpenAI-compatible LLM server or Transformers model | Optional | Required for LLM features; bot runs without it |
| SQLite | Built-in | No separate install needed |
| Kraken account | Optional | Paper mode works without API credentials |

---

## First-Time Setup

### 1. Clone and enter the project

```powershell
cd C:\dev\LLMTradingBot
```

### 2. Create a virtual environment

```powershell
python -m venv .venv
.\.venv\Scripts\activate        # Windows
# source .venv/bin/activate     # macOS/Linux
```

### 3. Install dependencies

```powershell
pip install -r requirements.txt
```

### 4. Create `.env` file

Copy from the template (or create from scratch). Minimum viable configuration for paper trading:

```ini
TRADING_MODE=semi_automated
TRADING_ENVIRONMENT=paper
STARTING_CAPITAL=500.0
FIXED_MARKETS=BTC/EUR,ETH/EUR

OPENAI_BASE_URL=http://127.0.0.1:1234/v1
OPENAI_MODEL=google/gemma-4-e4b
TRANSFORMERS_LLM_MODEL=

DATABASE_URL=sqlite:///./trading_bot.db
```

Full configuration reference: `docs/01-configuration.md`.

### 5. Configure an LLM backend

For LM Studio or another OpenAI-compatible local server, start the server and set `OPENAI_BASE_URL` plus `OPENAI_MODEL`. The bot posts standard `/v1/chat/completions` requests with `model` and `messages`.

For in-process Transformers fallback, set `TRANSFORMERS_LLM_MODEL` to a Hugging Face model ID. The bot starts without an LLM backend; LLM features are disabled until one is reachable.

### 6. Start the configured LLM server

If using an OpenAI-compatible local server, start it before the bot and confirm the base URL matches `OPENAI_BASE_URL`. If using only Transformers fallback, no separate server is required.

---

## Starting the Bot

On Windows, the preferred path is to run the repository launcher from `C:\dev\LLMTradingBot`:

```powershell
.\launch.bat
```

The launcher creates `.venv` if it is missing, hashes `requirements.txt`, updates the virtual environment when dependencies are stale or key imports fail, and runs `backend\main.py` with `.venv\Scripts\python.exe` explicitly. This avoids accidentally starting the bot with the system Python installation.

From the project root, start the app with the project virtual environment:

```powershell
cd backend
..\.venv\Scripts\python.exe main.py
```

Or through Uvicorn directly:

```powershell
cd backend
..\.venv\Scripts\python.exe -m uvicorn main:app --host 127.0.0.1 --port 8000
```

If `main.py` is started with the system Python, startup attempts to re-execute itself with the repository `.venv` interpreter before importing application dependencies. If `.venv` is missing or incomplete, run `launch.bat` or `setup.bat` from the repository root.

**What happens on startup:**

1. Database is initialised (SQLite file created if absent; migrations applied).
2. Open positions are restored from the database.
3. Cash balance is restored from the latest equity snapshot.
4. The learner is seeded with historical signal outcomes.
5. News cache is seeded from the database.
6. Four background tasks start: strategy loop, news loop, OHLC loop, reflection loop.
7. The configured OpenAI-compatible and/or Transformers LLM backend is probed. LLM availability is logged.
8. FastAPI begins serving requests.

The dashboard is available at: `http://127.0.0.1:8000`

---

## Running Tests

The test suite uses Python's standard `unittest` runner and BDD-style test names.

From the project root:

```bash
.\.venv\Scripts\python.exe -m unittest discover -s tests
```

With an activated virtual environment:

```bash
python -m unittest discover -s tests
```

See `docs/13-testing.md` for the coverage map and test-writing rules.

---

## Trading Modes

Set via `TRADING_MODE` in `.env` or toggled at runtime via the dashboard (if a toggle UI is added — currently mode is read-only on the dashboard, set only via config).

| Mode | Behaviour |
|------|-----------|
| `manual` | Signals appear in dashboard only. No orders placed. |
| `semi_automated` | Approved signals are queued. User clicks Approve/Reject within 30 minutes. |
| `fully_automated` | Signals execute immediately after passing risk checks. Signals below `MIN_SIGNAL_CONFIDENCE` (default 65%) are skipped and logged. No human step. |

Start in `manual` mode to observe signal quality before enabling execution.

---

## Monitoring

### Dashboard

`http://127.0.0.1:8000` — Refreshes every 5 seconds. Key panels to watch:

- **Equity curve** — Overall account health. A flat or declining curve warrants attention.
- **Positions** — Any open position showing large negative unrealised P&L may be approaching stop-loss.
- **Activity log** — Real-time feed of bot events. `STOP-LOSS` entries indicate positions that were auto-closed.
- **Risk rejections** — Repeated rejections for the same market indicate the risk engine is blocking signals. Check `daily_loss` limit or position sizing.
- **LLM status** — Market briefing updates after each new news batch. Reflection updates hourly once ≥5 closed trades exist.

### Log File

The bot writes structured JSON logs to `trading_bot.log` (configurable via `LOG_FILE`). Useful for forensic analysis after unexpected behaviour.

```bash
# Tail the log file
Get-Content trading_bot.log -Wait -Tail 50
```

Log level is configurable via `LOG_LEVEL` (default `INFO`). Set to `DEBUG` for verbose output including all Kraken API responses.

---

## Managing Markets

Markets are defined in `FIXED_MARKETS` in `.env`. To add a new market:

1. Add the symbol to `FIXED_MARKETS` (e.g., `FIXED_MARKETS=BTC/EUR,ETH/EUR,SOL/EUR`).
2. Restart the bot.
3. On startup, `validate_symbols()` checks Kraken recognises the symbol. Invalid symbols are dropped with a warning.

To temporarily disable a market without restarting: use the **Disable** button on the dashboard Markets panel. Re-enable without restart using the **Enable** button.

---

## Managing Positions

### View open positions

Dashboard → Positions panel (live, updates every 5 seconds).

### View closed trades

Dashboard → Closed Trades panel (updates every 30 seconds).

### Manually close a position

Use the **✕ Close** button on the Positions panel in the dashboard. This calls `POST /api/positions/{position_id_full}/close`, closes the position at the current market price, records the P&L outcome, and saves an equity snapshot immediately.

Alternatively, via the API:

```bash
curl -X POST http://127.0.0.1:8000/api/positions/3fa85f64-5717-4562-b3fc-2c963f66afa6/close
```

The 8-character position ID prefix is also accepted:

```bash
curl -X POST http://127.0.0.1:8000/api/positions/3fa85f64/close
```

If you need to discard all positions without recording P&L (e.g., after a testing session), use `POST /api/positions/reset` (paper mode only) — see below.

### Reset stale positions

If positions remain open after a crash or during testing:

```bash
curl -X POST http://127.0.0.1:8000/api/positions/reset
```

Or use the **Reset Positions** button (paper mode only) on the dashboard. This removes all open_positions rows from the database and clears in-memory state. Cash balance is unaffected.

---

## Database Management

The SQLite database file is `trading_bot.db` in the `backend/` directory (or as configured in `DATABASE_URL`).

### Schema migrations

Migrations are applied automatically on startup in `backend/storage/database.py`. The migration strategy is:
- **Additive changes** (new nullable columns): `ALTER TABLE ADD COLUMN` with `IF NOT EXISTS`.
- **Structural changes** (changing primary key type, etc.): DROP the table and recreate it (losing historical data for that table).

No manual migration step is required.

### Inspect the database

```bash
sqlite3 trading_bot.db

# Useful queries:
.tables
SELECT * FROM order_records ORDER BY timestamp DESC LIMIT 20;
SELECT * FROM signal_outcomes ORDER BY exit_at DESC LIMIT 20;
SELECT * FROM open_positions;
SELECT equity, timestamp FROM equity_snapshots ORDER BY timestamp DESC LIMIT 10;
```

### Backup

Simply copy `trading_bot.db`. The file is self-contained.

```bash
Copy-Item trading_bot.db trading_bot_backup_$(Get-Date -Format 'yyyyMMdd').db
```

### Reset for fresh start

Stop the bot, delete the database file, and restart. The bot will create a new empty database and start with `STARTING_CAPITAL` as the initial cash balance.

```bash
Remove-Item backend\trading_bot.db
```

---

## Emergency Stop

To immediately halt all trading:

1. Dashboard → click **Emergency Stop** button (top of page), or:
2. API: `curl -X POST http://127.0.0.1:8000/api/control/emergency-stop`

**Effect:**
- All strategy evaluation and execution is paused at the next tick (within 30 seconds).
- All pending approvals are cleared.
- Existing open positions are **not** automatically closed — they remain subject to the stop-loss monitor.

To resume: click **Resume** on the dashboard or:

```bash
curl -X POST http://127.0.0.1:8000/api/control/resume
```

---

## Switching to Live Trading

**Warning:** Live trading sends real orders to Kraken with real funds. Ensure thorough paper trading validation before switching.

1. Set Kraken API credentials in `.env`:

```ini
KRAKEN_API_KEY=your_key_here
KRAKEN_API_SECRET=your_secret_here
TRADING_ENVIRONMENT=live
```

2. Start with `TRADING_MODE=manual` to observe signals without execution.
3. Promote to `semi_automated` for human-gated execution.
4. Promote to `fully_automated` only after extended validation.

The risk engine still runs before live execution. Per-market live toggles route that market to `KrakenExecutionEngine`, which submits Kraken `AddOrder` requests and records the returned transaction ID in the trade ledger. Markets without the live flag continue to use paper execution, so you can promote one market at a time.

Live orders are submitted as Kraken spot market orders unless the `ExecutionIntent` carries a limit price. Live fills and balances remain exchange-owned; local paper positions are not mutated by live orders.

---

## Performance Tuning

### Signal frequency

The strategy loop runs every 30 seconds. This is hardcoded in `_strategy_loop` (`await asyncio.sleep(30)`). Reducing this increases Kraken API call frequency — stay above 10 seconds to avoid rate limiting.

### LLM response time

If LLM calls are too slow, either:
- Increase `OPENAI_TIMEOUT` or `TRANSFORMERS_TIMEOUT` in `.env`.
- Switch to a smaller/faster model on the configured backend.
- Disable LLM features by clearing `OPENAI_MODEL` and `TRANSFORMERS_LLM_MODEL`.

After a timeout or transport failure, the LLM client opens a circuit breaker before retrying. The retry delay starts at 30 seconds, doubles on repeated failures, and caps at 5 minutes. A malformed JSON response from the model does not mark the backend unavailable; it only skips that one LLM decision so the next prompt can still run.

### Stop-loss sensitivity

`STOP_LOSS_PCT=0.05` (5%). Reduce to `0.03` for tighter stop-losses (more frequent closures, smaller losses). Increase to `0.08` for wider stops (less frequent, larger losses).

Stop-losses are measured against the entry price and only close positions that are losing by at least this percentage. They do not close profitable positions that have merely retraced from a previous high or low.

This setting also affects the risk engine's per-trade loss estimate — changing it will affect how many signals pass the per-trade loss limit check.

---

## Adding a New Strategy

1. Create `backend/strategy/my_strategy.py` implementing the same interface as `BasicStrategy`:

```python
class MyStrategy:
    strategy_id = "my_strategy"

    async def evaluate(
        self,
        market_data: Dict[str, Any],
        news_signals: List[Dict],
        learner=None,
    ) -> List[TradeIdea]:
        ...
```

2. In `main.py`, instantiate the strategy and add it to the strategy evaluation loop:

```python
my_strategy = MyStrategy()
# In _strategy_loop():
for strategy in [basic_strategy, my_strategy]:
    if not control.is_strategy_enabled(strategy.strategy_id):
        continue
    ideas = await strategy.evaluate(market_data, [], learner)
```

3. The learner will automatically track outcomes for the new `strategy_id`. No changes to `PerformanceLearner` are needed.

---

## Troubleshooting

### "Warm-up: only N ticks collected"

Normal at startup. The strategy loop requires 10 price ticks before generating signals. Wait approximately 5 minutes. If price ticks were restored from the database, this message may not appear at all.

### "LLM unavailable — using neutral defaults"

The configured OpenAI-compatible endpoint is not reachable, the Transformers model could not load, or no LLM backend is configured. The bot will continue without LLM confidence adjustment — signals use the base strategy confidence only.

### `ModuleNotFoundError: No module named 'httpx'`

The bot was started with a Python interpreter that does not have the project dependencies installed. Use `launch.bat`, or run:

```powershell
cd C:\dev\LLMTradingBot\backend
C:\dev\LLMTradingBot\.venv\Scripts\python.exe main.py
```

If `.venv` is missing or still lacks dependencies, run `launch.bat` from `C:\dev\LLMTradingBot`; it will create or update the virtual environment before starting the bot. `setup.bat` remains available for a full first-time setup.

### "Daily loss limit reached"

The bot has lost more than `MAX_DAILY_LOSS_PERCENT` (default 5%) of equity in the current UTC day. All new trades are blocked until midnight UTC when the limit resets. The limit can be raised in `.env` if appropriate.

### "Position already open for X — one position per pair"

A signal was generated for a market that already has an open position in the same direction. This is expected behaviour — the risk engine enforces one position per pair. The signal will be retried on the next tick once the position is closed.

### Positions not appearing after restart

Positions are restored from the database on startup. If `open_positions` table is empty (e.g., after a reset or fresh DB), no positions will appear. Check `SELECT * FROM open_positions;` in SQLite.

### LLM reflection not appearing

Requires ≥5 closed trades in the database. Check `SELECT COUNT(*) FROM signal_outcomes;`. If zero, no trades have been recorded as closed. Verify that `fully_automated` mode is producing fills and that stop-loss closures are being recorded.
