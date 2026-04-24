# Configuration Reference

All runtime configuration is managed by `backend/config/settings.py` using Pydantic v2's `BaseSettings`. Values are read from environment variables or from a `.env` file in the backend working directory. Unknown keys are silently ignored (`extra="ignore"`). Keys are case-insensitive.

---

## Settings Class: `BotSettings`

```python
# backend/config/settings.py
from pydantic_settings import BaseSettings, SettingsConfigDict

class BotSettings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", case_sensitive=False, extra="ignore")
```

A singleton instance `settings` is created at module level and imported everywhere.

---

## Full Settings Reference

### Application

| Key | Type | Default | Description |
|---|---|---|---|
| `APP_NAME` | str | `"Kraken Trading Bot"` | Application name (cosmetic) |
| `VERSION` | str | `"0.1.0"` | Version string |
| `DEBUG` | bool | `False` | Enables Uvicorn hot-reload and verbose output |

### Server

| Key | Type | Default | Description |
|---|---|---|---|
| `HOST` | str | `"127.0.0.1"` | Bind address. Use `0.0.0.0` for LAN access |
| `PORT` | int | `8000` | HTTP port |

### Database

| Key | Type | Default | Description |
|---|---|---|---|
| `DATABASE_URL` | str | `"sqlite:///./kraken_bot.db"` | SQLAlchemy connection string. Only SQLite is used in practice |

### Trading Behaviour

| Key | Type | Default | Description |
|---|---|---|---|
| `BASE_CURRENCY` | str | `"EUR"` | Quote currency for all markets and P&L display |
| `STARTING_CAPITAL` | float | `500.0` | Initial paper cash balance in `BASE_CURRENCY` |
| `MAX_LOSS_PER_TRADE_PERCENT` | float | `5.0` | Maximum allowable loss per trade as a percentage of current equity |
| `MAX_DAILY_LOSS_PERCENT` | float | `5.0` | Maximum allowable cumulative loss in a calendar day (UTC) |
| `MIN_TRADE_SIZE` | float | `50.0` | Minimum order value in `BASE_CURRENCY`. Orders below this are rejected by the risk engine |
| `STOP_LOSS_PCT` | float | `0.05` | Fraction loss at which a position is automatically closed (5%) |
| `FEE_AND_SLIPPAGE` | float | `0.0036` | Combined cost estimate: Kraken taker fee (0.26%) + one-way slippage (0.1%) |
| `MIN_SIGNAL_CONFIDENCE` | float | `0.65` | Minimum signal confidence required to execute a trade in fully-automated mode. Signals below this threshold are skipped and logged. Has no effect in manual or semi-automated modes. |

### Trading Modes

| Key | Type | Default | Allowed Values |
|---|---|---|---|
| `TRADING_MODE` | str | `"manual"` | `manual`, `semi_automated`, `fully_automated` |
| `TRADING_ENVIRONMENT` | str | `"paper"` | `paper`, `live` |

Both fields are validated against a regex pattern in the Pydantic model. Invalid values cause a startup error.

### Exchange

| Key | Type | Default | Description |
|---|---|---|---|
| `KRAKEN_API_KEY` | str \| None | `None` | Kraken REST API key. Required for live trading only |
| `KRAKEN_API_SECRET` | str \| None | `None` | Kraken REST API secret. Required for live trading only |

In paper mode these fields are ignored. The Kraken adapter is used for read-only market data regardless of environment.

### Market Universe

| Key | Type | Default | Description |
|---|---|---|---|
| `FIXED_MARKETS` | list[str] | `["BTC/EUR", "ETH/EUR"]` | Always-active trading pairs |
| `DYNAMIC_UNIVERSE_SOURCE` | str | `"coinmarketcap"` | Source for dynamic market resolution (stub — not yet implemented) |
| `MAX_ETH_ECOSYSTEM_COINS` | int | `10` | Maximum dynamic pairs from the ETH ecosystem |

Market symbols use Kraken's public format (`BASE/QUOTE`). The adapter internally maps to Kraken's altname convention (e.g. `XBT/EUR` for `BTC/EUR`).

### Local LLM (Ollama)

| Key | Type | Default | Description |
|---|---|---|---|
| `OLLAMA_URL` | str | `"http://localhost:11434"` | Base URL of the Ollama REST API |
| `OLLAMA_MODEL` | str | `"phi3:mini"` | Model name as registered in Ollama |
| `OLLAMA_TIMEOUT` | int | `60` | Per-request timeout in seconds. Needs to be high (≥30s) on first call as the model loads into VRAM |
| `LLM_VETO_THRESHOLD` | float | `0.70` | If the LLM returns a `confidence_scale` below this value the signal is skipped entirely (vetoed). Set to `0` to disable. Only fires when the LLM is available — if Ollama is unreachable the veto never triggers. |

### Logging

| Key | Type | Default | Description |
|---|---|---|---|
| `LOG_LEVEL` | str | `"INFO"` | Python logging level: `DEBUG`, `INFO`, `WARNING`, `ERROR` |
| `LOG_FILE` | str | `"kraken_bot.log"` | Log file path relative to the backend working directory |

---

## Example `.env` File

```dotenv
# ── Trading behaviour ──────────────────────────────────
TRADING_MODE=semi_automated
TRADING_ENVIRONMENT=paper
STARTING_CAPITAL=1000.0
BASE_CURRENCY=EUR

# ── Risk limits ────────────────────────────────────────
MAX_LOSS_PER_TRADE_PERCENT=3.0
MAX_DAILY_LOSS_PERCENT=5.0
MIN_TRADE_SIZE=50.0
STOP_LOSS_PCT=0.05
FEE_AND_SLIPPAGE=0.0036
MIN_SIGNAL_CONFIDENCE=0.65

# ── Markets ────────────────────────────────────────────
FIXED_MARKETS=["BTC/EUR","ETH/EUR","SOL/EUR","ADA/EUR"]

# ── Kraken API (required for live mode only) ───────────
KRAKEN_API_KEY=
KRAKEN_API_SECRET=

# ── Local LLM ──────────────────────────────────────────
OLLAMA_URL=http://localhost:11434
OLLAMA_MODEL=phi3:mini
OLLAMA_TIMEOUT=60
LLM_VETO_THRESHOLD=0.70

# ── Server ─────────────────────────────────────────────
HOST=127.0.0.1
PORT=8000
DEBUG=false

# ── Logging ────────────────────────────────────────────
LOG_LEVEL=INFO
LOG_FILE=kraken_bot.log
```

---

## Runtime Constants

Some values are derived from settings at module import time. They are exposed as module-level names so other modules can import them directly:

```python
# backend/risk/engine.py
MIN_TRADE_SIZE_EUR   = settings.min_trade_size    # alias for backward compat
STOP_LOSS_ASSUMPTION = settings.stop_loss_pct
_FEE_AND_SLIPPAGE    = settings.fee_and_slippage
```

These module-level names exist so existing `from risk.engine import STOP_LOSS_ASSUMPTION` imports continue to work while the actual value is driven by the settings file.

---

## Settings Access Pattern

The `settings` singleton is imported directly:

```python
from config.settings import settings

# Use anywhere
max_loss = settings.max_loss_per_trade_percent
```

There is no dependency injection. All modules read from the same singleton at startup. Changing a `.env` value requires a process restart to take effect.
