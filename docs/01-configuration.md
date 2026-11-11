# Configuration Reference

All runtime configuration is managed by `internal/config/settings.go`. Values are layered in this order (highest precedence first):

1. Real process environment variables.
2. The `.env` file in the repository root (path set by the server's `-env` flag, default `.env`; the CLI uses `--config`).
3. The built-in defaults returned by `config.Defaults()`.

Unknown keys are silently ignored. Keys are case-sensitive, so use the uppercase names shown here. An empty value (for example `OPENAI_MODEL=`) keeps the default rather than overriding it with an empty string. A missing `.env` file is not an error; the bot runs on defaults plus environment variables.

---

## Settings Struct: `config.Settings`

```go
// internal/config/settings.go
type Settings struct {
	Debug        bool
	Host         string
	Port         int
	CORSOrigins  []string
	DatabasePath string
	// ... trading, modes, Kraken, news, universe, LLM and logging fields
}

func Defaults() Settings
func Load(envFile string) (Settings, error)
func FromLookup(lookup func(string) (string, bool)) (Settings, error)
func (s Settings) Validate() error
```

`Load` reads the `.env` file with `ReadEnvFile` and overlays the process environment; `FromLookup` is the pure core that tests exercise with a map-backed lookup. `.env` parsing accepts `KEY=VALUE` lines, ignores blank lines and `#` comments, strips an optional `export ` prefix, and removes one pair of matching surrounding quotes.

Typed parsing rules:

| Type | Accepted values |
|---|---|
| `int` / `float64` | `strconv.Atoi` / `strconv.ParseFloat`. An invalid value fails start-up with `invalid value for KEY`. |
| `bool` | `1`, `true`, `yes`, `on` / `0`, `false`, `no`, `off` (case-insensitive) |
| `[]string` | JSON array (`["BTC/EUR","ETH/EUR"]`), or a comma-separated list when the value does not start with `[` |

---

## Full Settings Reference

### Application

The application name and version are constants in `internal/app/app.go` (`app.Name = "Kraken Trading Bot"`, `app.Version = "0.6.0"`) and are not configurable via `.env`.

| Key | Type | Default | Description |
|---|---|---|---|
| `DEBUG` | bool | `false` | Parsed into `Settings.Debug` but not currently used by the Go server (the Python version used it for Uvicorn hot-reload). Use `LOG_LEVEL=DEBUG` for verbose output. |

### Server

| Key | Type | Default | Description |
|---|---|---|---|
| `HOST` | string | `"127.0.0.1"` | Bind address. Use `0.0.0.0` for LAN access |
| `PORT` | int | `8000` | HTTP port |
| `CORS_ORIGINS` | []string | `["http://localhost:8000","http://127.0.0.1:8000"]` | Origins allowed by the CORS middleware in `internal/server` |

### Database

| Key | Type | Default | Description |
|---|---|---|---|
| `DATABASE_PATH` | string | `"./trading_bot.db"` | SQLite file path. Created and migrated on start-up |
| `DATABASE_URL` | string | — | Legacy SQLAlchemy URL. Used only when `DATABASE_PATH` is unset; a leading `sqlite:///` is stripped, so `sqlite:///./trading_bot.db` becomes `./trading_bot.db` |

Databases created by the Python version are opened as-is; the schema is compatible.

### Trading Behaviour

| Key | Type | Default | Description |
|---|---|---|---|
| `BASE_CURRENCY` | string | `"EUR"` | Quote currency for all markets and P&L display. `config.CurrencySymbol` maps `EUR`/`GBP`/`USD` to `€`/`£`/`$` and falls back to the upper-cased code |
| `STARTING_CAPITAL` | float64 | `500.0` | Initial paper cash balance in `BASE_CURRENCY` |
| `TARGET_TRADE_AMOUNT` | float64 | `100.0` | Preferred notional order value in `BASE_CURRENCY`; risk can reduce it to available cash |
| `MAX_LOSS_PER_TRADE_PERCENT` | float64 | `5.0` | Maximum allowable loss per trade as a percentage of current equity |
| `MAX_DAILY_LOSS_PERCENT` | float64 | `5.0` | Maximum allowable cumulative loss in a calendar day (UTC) |
| `MIN_TRADE_SIZE` | float64 | `50.0` | Minimum final order value in `BASE_CURRENCY`; cash-adjusted trades below this are rejected |
| `STOP_LOSS_PCT` | float64 | `0.05` | Fraction loss at which a position is automatically closed (5%) |
| `TRAILING_STOP_PCT` | float64 | `0.03` | Fraction retracement from a position's peak watermark that closes it (3%) |
| `MOMENTUM_LOOKBACK_TICKS` | int | `10` | Number of 30-second ticks of price history used for the momentum baseline |
| `FEE_AND_SLIPPAGE` | float64 | `0.0036` | Combined cost estimate: Kraken taker fee (0.26%) + one-way slippage (0.1%) |
| `MIN_SIGNAL_CONFIDENCE` | float64 | `0.65` | Minimum signal confidence required to execute a trade in fully-automated mode. Signals below this threshold are skipped and logged. Has no effect in manual or semi-automated modes. |
| `LLM_VETO_THRESHOLD` | float64 | `0.70` | If the LLM returns a `confidence_scale` below this value the signal is vetoed. Set to `0.0` to disable the veto. |
| `MIN_24H_VOLUME` | float64 | `0.0` | Minimum 24-hour market volume required by the risk engine. `0.0` disables the liquidity gate. |
| `ALERT_WEBHOOK_URL` | string | `""` | Optional webhook URL for emergency stop, daily loss, stop-loss, and restart alerts. Blank disables alerts. |

`.env.example` ships wider-than-default stops (`STOP_LOSS_PCT=0.07`, `TRAILING_STOP_PCT=0.05`); the table lists the code defaults that apply when a key is absent.

### Trading Modes

| Key | Type | Default | Allowed Values |
|---|---|---|---|
| `TRADING_MODE` | string | `"manual"` | `manual`, `semi_automated`, `fully_automated` |
| `TRADING_ENVIRONMENT` | string | `"paper"` | `paper`, `live` |

Both fields are checked by `Settings.Validate()`. Invalid values cause a start-up error (`TRADING_MODE must be manual, semi_automated or fully_automated, got "..."`).

### Exchange

| Key | Type | Default | Description |
|---|---|---|---|
| `KRAKEN_API_KEY` | string | `""` | Kraken REST API key. Required for live trading only |
| `KRAKEN_API_SECRET` | string | `""` | Kraken REST API secret. Required for live trading only |
| `KRAKEN_REST_URL` | string | `"https://api.kraken.com"` | Kraken spot REST base URL used by both the market adapter and the live engine. Only change this for testing |

In paper mode the credentials are ignored. The Kraken market adapter uses public endpoints (REST and the WebSocket v2 ticker at `wss://ws.kraken.com/v2`) for market data regardless of environment and never sends API keys.

### News Sources

| Key | Type | Default | Description |
|---|---|---|---|
| `NEWS_SOURCES` | []string | 9 built-in feeds | RSS sources to ingest. Each entry must be `"Name::URL"`. Use JSON array syntax in `.env`. The Fear & Greed adapter is always included and does not need a `NEWS_SOURCES` entry. |

### Market Universe

| Key | Type | Default | Description |
|---|---|---|---|
| `FIXED_MARKETS` | []string | `["BTC/EUR","ETH/EUR"]` | Always-active trading pairs |
| `DYNAMIC_UNIVERSE_SOURCE` | string | `"coinmarketcap"` | Any value other than `none` enables the CoinGecko-backed dynamic resolver (the value is only recorded as the snapshot's source label). `none` restricts the universe to `FIXED_MARKETS` |
| `MAX_ETH_ECOSYSTEM_COINS` | int | `10` | Cap on how many dynamic ETH-ecosystem pairs the universe resolver adds (passed to `universe.Options.MaxCoins`) |

Market symbols use Kraken's public format (`BASE/QUOTE`). The Kraken package maps to Kraken's altname convention internally (`kraken.ToKrakenPair("BTC/EUR")` → `XBTEUR`; `DOGE` → `XDG`).

### Primary LLM (LM Studio)

| Key | Type | Default | Description |
|---|---|---|---|
| `LMSTUDIO_BASE_URL` | string | `"http://localhost:1234/v1"` | LM Studio's OpenAI-compatible server (Developer tab → Start Server) |
| `LMSTUDIO_MODEL` | string | `""` | Model identifier as listed by `GET /v1/models`. Blank selects the first model LM Studio reports |
| `LMSTUDIO_API_KEY` | string | `""` | Bearer token, only needed if authentication is enabled on the LM Studio server |
| `LMSTUDIO_TIMEOUT` | int | `60` | Per-request timeout in seconds. Increase for large local models |

### Fallback LLM (Remote OpenAI-compatible)

| Key | Type | Default | Description |
|---|---|---|---|
| `OPENAI_BASE_URL` | string | `""` | Base URL for a remote OpenAI-compatible API, for example `https://api.openai.com/v1`. Blank disables the fallback |
| `OPENAI_API_KEY` | string | `""` | Bearer token for the remote API |
| `OPENAI_MODEL` | string | `""` | Chat model name exposed by the remote API |
| `OPENAI_TIMEOUT` | int | `30` | Per-request timeout in seconds |
| `LLM_ONLY_MAX_CONCURRENCY` | int | `3` | Maximum number of concurrent per-market LLM-only recommendations per strategy tick (values below 1 are treated as 1) |

The remote endpoint is used only while LM Studio is unreachable; `llm.SwitchingClient` re-probes LM Studio on each news cycle and switches back when it returns. This reverses the Python version, which preferred the remote endpoint and fell back to a local Transformers model. The `TRANSFORMERS_*` keys have been removed and are ignored if present. If both `LMSTUDIO_BASE_URL` and `OPENAI_BASE_URL` are blank the dashboard reports the LLM as `not_configured`.

### Logging

| Key | Type | Default | Description |
|---|---|---|---|
| `LOG_LEVEL` | string | `"INFO"` | `slog` level: `DEBUG`, `INFO`, `WARNING`, `ERROR` |
| `LOG_FILE` | string | `"trading_bot.log"` | Log file path relative to the working directory. LLM client and analyser records are also written to `llm.log` in the same directory |

---

## Example `.env` File

The complete commented template lives at `.env.example` in the repository root. It contains every setting, uses parseable defaults, and documents operational impact for each setting. Copy it to `.env` in the repository root (`launch.bat` does this automatically on first run), then replace local values such as capital, markets, webhook URL, LM Studio model, and Kraken credentials.

---

## Runtime Constants

The Python version exposed module-level aliases such as `risk.engine.STOP_LOSS_ASSUMPTION`. The Go code has no such aliases: components receive the `config.Settings` value (or individual fields) at construction, for example:

```go
// internal/app/app.go
a.risk = risk.NewEngine(settings, nil)
a.paper = paper.NewEngine(settings.StartingCapital, paperStore{repo: repo}, nil)
```

Values that are not settings, such as loop intervals and buffer sizes, are constants in `internal/app/app.go` (`signalBufferMax`, `priceHistoryMax`, `equityHistoryMax`, …) and the `app.Intervals` struct.

---

## Settings Access Pattern

There is no global singleton. `cmd/tradingbot` loads settings once and passes them down:

```go
// cmd/tradingbot/main.go
settings, err := config.Load(envFile)
if err != nil {
	return fmt.Errorf("load settings: %w", err)
}
bot, err := app.New(ctx, settings, repo)
```

`app.App` keeps its copy and exposes it read-only via `App.Settings()`. Changing a `.env` value requires a process restart to take effect.
