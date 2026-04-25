# Project Context

Load the minimal set of files needed to understand the current state of the Kraken Bot codebase. Run this at the start of any session before making changes.

## Steps

1. Read `kraken-bot/docs/00-overview.md` — architecture, file layout, operating modes
2. Read `kraken-bot/backend/domain/models.py` — all Pydantic domain models (source of truth for data shapes)
3. Read `kraken-bot/backend/storage/models.py` — all SQLAlchemy table definitions (columns, types, nullability)
4. Read `kraken-bot/backend/config/settings.py` — all configurable settings and their defaults

After reading, output a single compact summary:
- **Mode/environment** options
- **Key domain models** (one line each: name + purpose)
- **Database tables** (one line each: name + primary key + notable columns)
- **Settings** that affect behaviour (exclude boilerplate like DB path)

Keep the summary under 300 words. Do not repeat content already visible in the files — synthesise it.
