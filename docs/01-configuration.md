# Configuration Reference

All runtime configuration is managed by `backend/config/settings.py` using Pydantic v2's `BaseSettings`. Values are read from environment variables or from a `.env` file in the backend working directory. Unknown keys are silently ignored (`extra="ignore"`). Keys are case-sensitive, so use the uppercase names shown here.

---

## Settings Class: `BotSettings`

```python
# backend/config/settings.py
from pydantic_settings import BaseSettings, SettingsConfigDict

class BotSettings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", case_sensitive=True, extra="ignore")
```

A singleton instance `settings` is created at module level and imported everywhere.

---

## Full Settings Reference

### Application

`APP_NAME` and `APP_VERSION` are hard-coded constants in `backend/main.py` and are not configurable via `.env`. To change the app name or version, edit those constants directly.

| Key | Type | Default | Description |
|---|---|---|---|
| `DEBUG` | bool | `False` | Enables Uvicorn hot-reload and verbose output |

### Server

| Key | Type | Default | Description |
|---|---|---|---|
| `HOST` | str | `"127.0.0.1"` | Bind address. Use `0.0.0.0` for LAN access |
| `PORT` | int | `8000` | HTTP port |

### Database

| Key | Type | Default | Description |
|---|---|---|---|
| `DATABASE_URL` | str | `"sqlite:///./trading_bot.db"` | SQLAlchemy connection string. Only SQLite is used in practice |

### Trading Behaviour

| Key | Type | Default | Description |
|---|---|---|---|
| `BASE_CURRENCY` | str | `"EUR"` | Quote currency for all markets and P&L display |
| `STARTING_CAPITAL` | float | `500.0` | Initial paper cash balance in `BASE_CURRENCY` |
| `TARGET_TRADE_AMOUNT` | float | `100.0` | Preferred notional order value in `BASE_CURRENCY`; risk can reduce it to available cash |
| `MAX_LOSS_PER_TRADE_PERCENT` | float | `5.0` | Maximum allowable loss per trade as a percentage of current equity |
| `MAX_DAILY_LOSS_PERCENT` | float | `5.0` | Maximum allowable cumulative loss in a calendar day (UTC) |
| `MIN_TRADE_SIZE` | float | `50.0` | Minimum final order value in `BASE_CURRENCY`; cash-adjusted trades below this are rejected |
| `STOP_LOSS_PCT` | float | `0.05` | Fraction loss at which a position is automatically closed (5%) |
| `FEE_AND_SLIPPAGE` | float | `0.0036` | Combined cost estimate: Kraken taker fee (0.26%) + one-way slippage (0.1%) |
| `MIN_SIGNAL_CONFIDENCE` | float | `0.65` | Minimum signal confidence required to execute a trade in fully-automated mode. Signals below this threshold are skipped and logged. Has no effect in manual or semi-automated modes. |
| `LLM_VETO_THRESHOLD` | float | `0.70` | If the LLM returns a `confidence_scale` below this value the signal is vetoed. Set to `0.0` to disable the veto. |
| `MIN_24H_VOLUME` | float | `0.0` | Minimum 24-hour market volume required by the risk engine. `0.0` disables the liquidity gate. |
| `ALERT_WEBHOOK_URL` | str \| None | `None` | Optional webhook URL for emergency stop, daily loss, stop-loss, and restart alerts. Blank disables alerts. |

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

### News Sources

| Key | Type | Default | Description |
|---|---|---|---|
| `NEWS_SOURCES` | list[str] | 9 built-in feeds | RSS sources to ingest. Each entry must be `"Name::URL"`. Use JSON array syntax in `.env`. `FearGreedAdapter` is always included and does not need a `NEWS_SOURCES` entry. |

### Market Universe

| Key | Type | Default | Description |
|---|---|---|---|
| `FIXED_MARKETS` | list[str] | `["BTC/EUR", "ETH/EUR"]` | Always-active trading pairs |
| `DYNAMIC_UNIVERSE_SOURCE` | str | `"coinmarketcap"` | Source for dynamic market resolution (stub — not yet implemented) |
| `MAX_ETH_ECOSYSTEM_COINS` | int | `10` | Maximum dynamic pairs from the ETH ecosystem |

Market symbols use Kraken's public format (`BASE/QUOTE`). The adapter internally maps to Kraken's altname convention (e.g. `XBT/EUR` for `BTC/EUR`).

### External LLM (OpenAI-compatible)

| Key | Type | Default | Description |
|---|---|---|---|
| `OPENAI_BASE_URL` | str | `""` | Base URL for an OpenAI-compatible API, for example `http://127.0.0.1:1234/v1` |
| `OPENAI_API_KEY` | str | `""` | Optional bearer token for hosted APIs. Leave blank for local servers that do not require auth |
| `OPENAI_MODEL` | str | `""` | Chat model name exposed by the configured OpenAI-compatible server |
| `OPENAI_TIMEOUT` | int | `30` | Per-request timeout in seconds |

`OpenAiClient.chat()` posts a standard chat-completions JSON object with `model` and `messages`. The plain prompt in debug logs is only a readable transcript for troubleshooting.

### Local LLM (Transformers)

| Key | Type | Default | Description |
|---|---|---|---|
| `TRANSFORMERS_LLM_MODEL` | str | `""` | Hugging Face model ID used when the OpenAI-compatible backend is not configured or unavailable |
| `TRANSFORMERS_TIMEOUT` | int | `60` | Per-request timeout in seconds. Needs to be high (>=30s) on first call as the model loads into memory |
| `LLM_ONLY_MAX_CONCURRENCY` | int | `3` | Maximum number of concurrent per-market LLM-only recommendations per strategy tick. |

### Logging

| Key | Type | Default | Description |
|---|---|---|---|
| `LOG_LEVEL` | str | `"INFO"` | Python logging level: `DEBUG`, `INFO`, `WARNING`, `ERROR` |
| `LOG_FILE` | str | `"trading_bot.log"` | Log file path relative to the backend working directory |

---

## Example `.env` File

The complete commented template lives at `backend/.env.example`. It contains every `BotSettings` option, uses parseable defaults, and documents operational impact for each setting. Copy it to `backend/.env`, then replace local values such as capital, markets, webhook URL, and Kraken credentials.

---

## Runtime Constants

Some values are derived from settings at module import time. They are exposed as module-level names so other modules can import them directly:

```python
# backend/risk/engine.py
MIN_TRADE_SIZE_EUR   = settings.min_trade_size    # alias for backward compat
TARGET_TRADE_AMOUNT_EUR = settings.target_trade_amount
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
