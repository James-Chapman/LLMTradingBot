# Add Endpoint

Add a new FastAPI endpoint to the bot. Usage: `/add-endpoint`

The user will provide (or you will ask for):
- **Method** — `GET` or `POST`
- **Path** — e.g. `/api/positions/{position_id}/history`
- **Purpose** — what the endpoint does and what it returns
- **Auth/guards** — any conditions that should reject the request (e.g. emergency stop active)

## Steps

1. Read `kraken-bot/docs/08-api-endpoints.md` to find the right section and confirm the path does not already exist.

2. Read the area around the nearest existing endpoint in `kraken-bot/backend/main.py` (grep for a nearby path) to match the local code style.

3. Write the endpoint following this pattern:

```python
@app.{method}("{path}")
async def {function_name}({params}):
    """{one-line docstring}"""
    try:
        # implementation
        return {result}
    except Exception as e:
        logger.error("{function_name} error", extra={"error": str(e)})
        raise HTTPException(status_code=500, detail=str(e))
```

   - POST endpoints that mutate state must log to `activity` after success
   - Endpoints that read DB state use `repo.{method}()` — never access SQLAlchemy directly in main.py
   - Place GET endpoints near other GETs for the same resource group; same for POSTs

4. Add the endpoint to `kraken-bot/docs/08-api-endpoints.md` in the correct section:

```
### {METHOD} {path}
{purpose}
**Returns:** `{shape}`
```

## Validation

After making changes confirm:
- Function name is snake_case and descriptive
- Docstring present
- Returns consistent shape (dict or list, not a raw model)
- Doc entry added
