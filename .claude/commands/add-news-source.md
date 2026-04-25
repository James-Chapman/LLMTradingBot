# Add News Source

Add a new RSS news source to the bot. Usage: `/add-news-source`

The user will provide (or you will ask for):
- **Name** — display name shown in the dashboard (e.g. `CryptoSlate`)
- **URL** — RSS feed URL
- **Description** — one sentence on what this source covers

## Steps

1. Read `kraken-bot/backend/ingestion/news_adapter.py` to see existing adapter pattern and confirm the class name does not already exist.

2. Add the new adapter class immediately before `ReutersBusinessAdapter` (keep Reuters and FearGreed last):

```python
class {Name}Adapter(RSSAdapter):
    """{Description}"""
    RSS_URL = "{URL}"

    def __init__(self):
        super().__init__("{Name}")
```

3. Read the import block and `news_adapters` list in `kraken-bot/backend/main.py`.

4. Add `{Name}Adapter` to the import and insert it into `news_adapters` before `ReutersBusinessAdapter()`.

5. Read the news sources table in `kraken-bot/docs/03-background-loops.md` and append a row:
   `| {Name}Adapter | {Name} RSS |`

## Validation

After making changes, confirm:
- Class name follows `{Name}Adapter` convention
- `RSS_URL` is a class attribute, not instance
- `super().__init__("{Name}")` uses the display name (not the class name)
- The adapter appears in both the import and the `news_adapters` list
