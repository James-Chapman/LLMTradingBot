# Configuration Reference

Since v0.8.0 the bot has **no `.env` file and reads no environment variables**. Settings come from three places:

1. **Start-up flags**: the database file, listen address, port and log file. These are needed before the database opens.
2. **Dashboard settings**: everything else, edited in the dashboard's **⚙ Settings** dialog and stored in the database's `settings` table.
3. **Trading settings dialog**: trading mode (manual / semi-automated / fully automated) and paper / live, stored in `control_state` (see [10-approval-and-control.md](10-approval-and-control.md)).

A setting never saved from the dashboard uses its built-in default (`config.Defaults()`).

> **Upgrading from 0.7 or earlier:** `.env` (in the repository root or `backend/`) is ignored and nothing is imported from it. The bot starts on the built-in defaults: paper trading, manual mode unless a mode was already chosen in the dashboard. Re-enter your settings, including the Kraken API keys, in the Settings dialog. `.gitignore` still lists `.env`, so an old file is never committed by accident; delete it when you no longer need it.

---

## Start-up Flags

```text
tradingbot.exe [-db FILE] [-host ADDR] [-port N] [-log FILE] [-frontend DIR]
```

| Flag | Default | Description |
|---|---|---|
| `-db` | `./trading_bot.db` | SQLite file. Holds trades, control state and every dashboard setting. Created and migrated on start-up |
| `-host` | `127.0.0.1` | Bind address. Any address other than a loopback one (`127.0.0.1`, `localhost`, `::1`), for example `0.0.0.0` for LAN access, turns on the access token (see **Access from Other Computers** below) |
| `-port` | `8000` | HTTP port (1–65535) |
| `-log` | `trading_bot.log` | Log file. LLM client and analyser records are also written to `llm.log` in the same directory |
| `-frontend` | `frontend` | Directory holding `index.html`, `approvals.html`, `login.html`, `sw.js` and `static/` |

The bot finds its files whatever folder it is started from. Its **home folder** is the first of these that holds `frontend/index.html`: the working directory, the folder holding the executable, and the folder above that (the executable is built into `bin/`). The dashboard is served from the home folder, and the database and log file are kept there when `-db` and `-log` are left at their defaults. So double-clicking `bin	radingbot.exe` uses the same dashboard and database as `launch.bat`. Paths given with `-db`, `-log` or `-frontend` are used exactly as given (relative ones against the working directory). If no dashboard is found the bot refuses to start and lists where it looked. The start-up log line records the `database` and `frontend` paths in use.

`-h` prints the flags. `launch.bat` starts the bot without flags, so the defaults apply. The CLI's `run` command passes its `-db`, `-host`, `-port` and `-log` through (see [12-operations.md](12-operations.md)).

---

## How Settings Load

`cmd/tradingbot` starts in this order:

1. Logging starts at `INFO`, writing to the `-log` file.
2. The database opens.
3. `app.LoadSettings` builds the settings. It starts from `config.Defaults()`, overlays every value saved in the `settings` table, then applies the start-up flags.
   - `CORS_ORIGINS` follows the port (`http://localhost:<port>` and `http://127.0.0.1:<port>`) unless it was saved.
   - A saved value that no longer parses, is outside its allowed range (for example a `STOP_LOSS_PCT` of `0` saved by an older version), or a secret that cannot be decrypted, is skipped. It falls back to the default with a `Saved setting ignored` warning in the log, so the bot still starts.
4. `Settings.Validate()` runs.
5. The log level switches to `LOG_LEVEL` (`DEBUG` when the `DEBUG` switch is on).
6. If no markets were saved, `app.UseLiveDefaultMarkets` replaces the built-in list with Kraken's 50 biggest pairs (see **Default markets** below).

The `settings` table:

| Column | Type | Description |
|---|---|---|
| `key` | VARCHAR (primary key) | Setting key, e.g. `MIN_SIGNAL_CONFIDENCE` |
| `value` | TEXT | Text form of the value. Lists are JSON arrays; secrets are sealed (below) |
| `updated_at` | DATETIME | Last save |

---

## Changing Settings

Open **⚙ Settings** in the dashboard header, edit, and press **Save…**. A confirmation lists every change (`label: old → new`, and whether it applies now or after a restart) before anything is sent. Every value is validated before anything is written, so one bad value saves nothing and the error names the key. Decimal settings must be finite numbers: `NaN` and `Inf` are refused, because `NaN` compares false against every limit and would silently switch off the risk checks that read it.

- **Applies now** settings are copied into the running bot at once. The risk engine receives the new values but keeps today's equity and loss counters, the strategies are rebuilt, and the background loops read the current values.
- **Restart** settings are saved and used after the next restart. Until then the dashboard shows a *Restart the bot to apply* banner listing them, and the field shows *saved — restart to apply*. There is no restart button: stop the bot (Ctrl+C in its window) and start it again.

Every save is logged (`Settings saved`) and appears in the activity log. The API is `GET` / `POST /api/settings` (see [08-api-endpoints.md](08-api-endpoints.md#settings)).

---

## Secrets

`KRAKEN_API_KEY`, `KRAKEN_API_SECRET`, `LMSTUDIO_API_KEY` and `OPENAI_API_KEY` are secrets.

- **Encrypted at rest.** They are encrypted with Windows DPAPI (`CryptProtectData`, bound to the current Windows user, with fixed extra entropy) and stored as `sealed:` + base64.
- **Write-only.** The API and dashboard only say whether a secret is set; a value is never returned. To replace a secret, type a new value; **Clear** removes it. A value of only spaces is refused (`400`) and the stored secret is kept, so a stray space cannot clear a key.
- **Moving the database.** A database copied to another Windows user or machine cannot decrypt them. The bot logs a warning, treats them as not set, and they must be entered again.
- **Non-Windows builds** cannot store secrets: saving one is refused.

Live trading needs the Kraken keys *in use*. Keys saved but not yet loaded (no restart since) are refused when switching to live with *Kraken API keys were saved but are not in use yet: restart the bot, then switch to live*.

---

## Settings Reference

Grouped as in the Settings dialog. "Now" = applies immediately; "Restart" = used after the next restart.

### Risk & sizing

| Key | Default | Applies | Description |
|---|---|---|---|
| `STARTING_CAPITAL` | `500` | Restart | Initial paper cash balance in `BASE_CURRENCY` for a new database |
| `TARGET_TRADE_AMOUNT` | `100` | Now | Preferred order value in `BASE_CURRENCY`; risk can reduce it to available cash |
| `MIN_TRADE_SIZE` | `50` | Now | Minimum final order value; cash-adjusted trades below this are rejected |
| `MAX_LOSS_PER_TRADE_PERCENT` | `5` | Now | Maximum loss one trade may risk, as a percentage of equity (above 0, up to 100) |
| `MAX_DAILY_LOSS_PERCENT` | `5` | Now | New trades stop for the UTC day once this percentage of equity is lost (above 0, up to 100) |
| `STOP_LOSS_PCT` | `0.05` | Now | Fraction loss at which a position is closed (above 0, at most 0.5) |
| `TRAILING_STOP_PCT` | `0.03` | Now | Fraction retracement from a position's peak that closes it (above 0, at most 0.5) |
| `FEE_AND_SLIPPAGE` | `0.0036` | Now | Round-trip cost estimate: Kraken taker fee (0.26%) + slippage (0.1%) |
| `MIN_24H_VOLUME` | `0` | Now | Minimum 24-hour market volume required by the risk engine. `0` disables the gate |

The stop distances must be above 0 because the risk engine estimates a trade's possible loss from `STOP_LOSS_PCT`: at `0` the estimate is 0 and the per-trade loss check never refuses anything. A stop of more than 50% is no stop. The two loss limits must be above 0 too: `0` is not an "off switch" (it would block every trade without saying why). To stop trading, use the emergency stop or manual mode.

### Signals

| Key | Default | Applies | Description |
|---|---|---|---|
| `STRATEGY_INTERVAL_SECONDS` | `60` | Now | Seconds between strategy ticks (30–600), read before each wait, so a change applies from the next tick. Indicators, the warm-up and `MOMENTUM_LOOKBACK_TICKS` count ticks, so a longer interval stretches them over more time. Streamed prices count as stale after two intervals. Backtests always simulate 30-second ticks |
| `MIN_SIGNAL_CONFIDENCE` | `0.65` | Now | Minimum confidence to trade in fully automated mode. No effect in manual or semi-automated mode |
| `LLM_VETO_THRESHOLD` | `0.70` | Now | The combined strategy vetoes signals the LLM rates below this. `0` disables the veto |
| `MOMENTUM_LOOKBACK_TICKS` | `10` | Now | Strategy ticks of price history used for the momentum baseline (1–199: the bot keeps 200 ticks per market and momentum over N ticks needs N+1) |
| `LLM_ONLY_MAX_CONCURRENCY` | `3` | Now | Markets the LLM-only strategy analyses at once per tick (1–16) |

### Markets

| Key | Default | Applies | Description |
|---|---|---|---|
| `BASE_CURRENCY` | `EUR` | Restart | Quote currency for all markets and P&L. `EUR`/`GBP`/`USD` display as `€`/`£`/`$` |
| `FIXED_MARKETS` | The 50 biggest pairs on Kraken (see below) | Restart | Always-active pairs in Kraken's `BASE/QUOTE` format, edited as removable chips in the dialog |
| `DYNAMIC_UNIVERSE_SOURCE` | `coingecko` | Restart | `coingecko` adds the biggest Ethereum-ecosystem coins by market cap from CoinGecko; `none` trades only `FIXED_MARKETS`. The value `coinmarketcap` saved by earlier versions is still accepted and means `coingecko` (CoinMarketCap was never called) |
| `MAX_ETH_ECOSYSTEM_COINS` | `10` | Restart | Cap on pairs the dynamic resolver adds (0–50). `0` adds none and CoinGecko is not asked |

**Dynamic markets.** Each coin becomes `COIN/BASE_CURRENCY` (for example `LINK/USD` with `BASE_CURRENCY=USD`), and CoinGecko is asked for prices in `BASE_CURRENCY`. There is no built-in allow-list: Kraken's symbol validation at start-up drops every pair Kraken does not list, so fewer pairs than the cap may remain. If CoinGecko cannot be reached, a built-in coin list is used.


**Default markets.** Until `FIXED_MARKETS` is saved from the dashboard, the bot asks Kraken at every start for the 50 biggest online pairs quoted in `BASE_CURRENCY`. Two public calls are made, `AssetPairs` and `Ticker` for all pairs (`kraken.MarketAdapter.TopPairsByVolume`), with a 20-second limit.
- **Ranking:** 24h traded value, that is 24h volume × 24h VWAP.
- **Pegged pairs skipped:** pairs whose 24h high–low range is under 0.5% are left out. This drops stablecoins such as USDC/EUR, USDT/EUR and EURC/EUR.
- **Fallback:** if Kraken cannot be reached, the built-in list (`config.Defaults()`, the 50 biggest EUR pairs on 2026-11-14) is used and a warning is logged.
- **Saved lists:** once markets are saved in the dashboard, that list is used as-is and Kraken is not asked.

The OHLC loop fetches candles one market at a time (about 3.5 s per market for Kraken's rate limit), so with 50 markets each candle refresh cycle takes about three minutes.

### Kraken

| Key | Default | Applies | Description |
|---|---|---|---|
| `KRAKEN_API_KEY` | not set | Restart | Secret. Needed for live trading only |
| `KRAKEN_API_SECRET` | not set | Restart | Secret. Needed for live trading only |
| `KRAKEN_REST_URL` | `https://api.kraken.com` | Restart | Kraken spot REST base URL for the market adapter and live engine. Only change for testing |

Market data uses Kraken's public REST and WebSocket v2 endpoints in both paper and live, and never sends API keys.

### LLM

| Key | Default | Applies | Description |
|---|---|---|---|
| `LMSTUDIO_BASE_URL` | `http://localhost:1234/v1` | Restart | LM Studio's OpenAI-compatible server. Empty disables it |
| `LMSTUDIO_MODEL` | empty | Restart | Model id from `GET /v1/models`. Empty uses the first model LM Studio reports |
| `LMSTUDIO_API_KEY` | not set | Restart | Secret. Only if the LM Studio server requires one |
| `LMSTUDIO_TIMEOUT` | `60` | Restart | Seconds to wait for an LM Studio reply (1–600) |
| `OPENAI_BASE_URL` | empty | Restart | Remote OpenAI-compatible fallback, e.g. `https://api.openai.com/v1`. Empty disables it |
| `OPENAI_MODEL` | empty | Restart | Chat model name for the fallback |
| `OPENAI_API_KEY` | not set | Restart | Secret. API key for the fallback |
| `OPENAI_TIMEOUT` | `30` | Restart | Seconds to wait for a fallback reply (1–600) |

The fallback is used only while LM Studio is unreachable; `llm.SwitchingClient` re-probes LM Studio on each news cycle and switches back. With both URLs empty the dashboard reports the LLM as `not_configured`.

### News & alerts

| Key | Default | Applies | Description |
|---|---|---|---|
| `DISABLED_NEWS_SOURCES` | empty (every feed on) | Now | Names of news sources the bot does not read. The dashboard shows it as **News feeds**: one switch per built-in source (20 feeds plus Fear & Greed) and per extra feed. A switched-off source is not fetched from the next news cycle, and its articles leave the news cache. Names match case-insensitively |
| `NEWS_SOURCES` | empty | Restart | **Extra news feeds**: your own RSS or Atom feeds as `Name::URL` (http or https), read as well as the built-in ones. Entries named like a built-in source are dropped (lists saved before 0.13.0 held the old defaults, which are now built in); a malformed entry or two feeds with the same name are refused |
| `ALERT_WEBHOOK_URL` | empty | Now | Webhook for start, emergency stop, daily loss and stop-loss alerts. Empty disables alerts |

### Advanced

| Key | Default | Applies | Description |
|---|---|---|---|
| `CORS_ORIGINS` | `http://localhost:<port>`, `http://127.0.0.1:<port>` | Restart | Other browser origins allowed to call the API. `*` and `null` are refused: `*` would let every website read the API with the user's credentials |
| `ALLOWED_HOSTS` | empty | Restart | Extra host names the dashboard may be opened by when `-host` is not loopback, e.g. `trading-pc.lan`. Bare names only (no scheme, port or path); stored lower-case |
| `LOG_LEVEL` | `INFO` | Restart | `DEBUG`, `INFO`, `WARNING` or `ERROR` |
| `DEBUG` | off | Restart | Log at `DEBUG` whatever `LOG_LEVEL` says |

---

## Access from Other Computers

Two checks guard the HTTP server (`internal/server`):

- **Host check (always on).** A request whose `Host` header names a host the bot is not reached by gets `403 Host not allowed`. This stops DNS rebinding, where a foreign web page points its own name at this computer. With a loopback `-host`, only `localhost`, `127.0.0.1` and `[::1]` (any port) are accepted. With any other `-host`, the bot also accepts that address, this computer's name, any IP address (a rebinding attack always arrives with the attacker's name, never an IP) and every name in `ALLOWED_HOSTS`.
- **Access token (only when `-host` is not loopback).** Every page, `/static/` file and `/api/` call needs the token. The exceptions are `/health`, the `/login` page, its stylesheet `/static/styles.css` and `POST /api/login`. Pages requested without a session redirect to `/login`; API calls get `401 {"detail": "Access token required: ..."}`. With a loopback `-host` nothing changes: no token, no login.

The token:

- **Generated once.** On the first non-loopback start: 32 random bytes from `crypto/rand`, URL-safe base64 (43 characters). It is stored sealed like the API keys (DPAPI, `settings` key `ACCESS_TOKEN`). It is not a dashboard setting: `GET /api/settings` never lists it and `POST /api/settings` refuses it.
- **Printed at every start.** A block marked `DASHBOARD ACCESS TOKEN` is written to the console (stdout). It is not written to the log file. The log gets a warning that the dashboard is reachable from other computers.
- **Used two ways.** Browsers log in at `/login`: `POST /api/login` sets the session cookie `tradingbot_session` (HttpOnly, SameSite=Strict, Path=/, Secure over HTTPS; it ends when the browser closes). Scripts send `Authorization: Bearer <token>`. The CLI sends the token stored in its `-db` database automatically. Tokens are compared in constant time, and a wrong token waits 1 second before the answer.
- **Rotated** with `POST /api/access-token/rotate` (needs the current token). The new token is returned and in use at once; the old one, and every session that used it, stops working. Another way: delete the `ACCESS_TOKEN` row from the `settings` table while the bot is stopped; a new token is generated at the next non-loopback start.

There is no HTTPS: on an untrusted network the token and the traffic can be read. Keep `-host` on a loopback address unless the network is trusted.

---

## Code

| Piece | Where |
|---|---|
| `config.Settings`, `Defaults()`, `Validate()`, `EffectiveLogLevel()`, `Bootstrap` (the start-up flags) | `internal/config/settings.go` |
| Settings registry: key, label, group, help, kind, live, secret, parse/format | `internal/config/fields.go` (`Fields()`, `FieldByKey()`) |
| DPAPI box, `sealed:` encoding | `internal/secret` |
| `settings` table load/save | `internal/storage/settings.go` |
| `LoadSettings`, `SettingsView`, `UpdateSettings`, `LoadAccessToken`, `StoredAccessToken`, `AccessToken.Rotate` | `internal/app/settings.go` |
| `IsLoopbackHost` | `internal/config/settings.go` |
| Host check, access-token check, login | `internal/server/server.go` |

The registry is the single list of dashboard settings. Adding a setting means adding a field to `config.Settings`, a default in `Defaults()`, and one registry entry. The entry's `Live` flag must only be set if every consumer reads the value through the risk engine (`risk.Engine.SetSettings`), the strategy factory, or `App.cfg()` at use time.

`app.App` keeps two copies under a lock: the **running** settings (what components use) and the **saved** settings (what the database holds). A key whose two values differ is "restart pending". Values that are not settings, such as loop intervals and buffer sizes, are constants in `internal/app/app.go` and the `app.Intervals` struct.
