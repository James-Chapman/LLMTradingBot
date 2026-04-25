# Testing

The project now has a lightweight `unittest` suite under `tests/`. Tests are written in a BDD style, using explicit GIVEN WHEN THEN wording in test names and short comments above each test.

## Run Tests

From the project root:

```powershell
.\.venv\Scripts\python.exe -m unittest discover -s tests
```

If you use an activated virtual environment instead:

```powershell
python -m unittest discover -s tests
```

## Run Linters

Install developer tooling from `requirements-dev.txt`:

```powershell
.\.venv\Scripts\python.exe -m pip install -r requirements-dev.txt
```

Run Ruff from the project root:

```powershell
.\.venv\Scripts\python.exe -m ruff check .
```

To apply safe automatic fixes, such as import ordering:

```powershell
.\.venv\Scripts\python.exe -m ruff check . --fix
```

## Coverage Focus

The suite intentionally checks stable behavior rather than internal implementation shape:

- Approval queue lifecycle: submit, duplicate suppression, approve, expiry purge.
- Risk decisions: valid approval, same-direction position rejection, closing signal allowance, cash insufficiency, daily loss block.
- Paper execution: fills, cash accounting with fee/slippage, duplicate-position guard, FIFO close behavior, mark-to-market equity.
- Kraken execution: live AddOrder payload mapping, Kraken error handling, and fail-closed missing-credential behavior.
- Strategy behavior: public strategy IDs, momentum threshold, hard RSI filter, minimum six-indicator gate, bullish consensus signal generation, opposing consensus block, and indicator-only ignoring non-indicator sentiment.
- Control state behavior: the default selected strategy and single selected-strategy switching.
- LLM strategy behavior: LLM long recommendations emit trade ideas, hold recommendations do not, and indicators are passed through to the LLM.
- LLM analyser prompt behavior: LLM-only recommendation prompts include the technical indicator context.
- Indicators: insufficient-history behavior, RSI edge case, named price-change windows, numpy scalar reductions, and JSON-safe output.
- Backtest replay: deterministic 48-hour replay behavior plus numpy-backed drawdown, trade stats, and candle resampling metrics.
- Learner: no adjustment before enough samples, confidence boost after wins, confidence reduction after losses, rolling win rate, and P&L percentiles.

## BDD Style

Use this naming pattern for new tests:

```python
def test_given_state_when_action_then_expected_outcome(self) -> None:
    ...
```

Keep assertions specific where the contract matters, such as order status, trade direction, or risk approval. Keep assertions general where exact values would make the suite brittle, such as confidence ranges, partial reason text, or collection membership.

## Test Design Rules

- Prefer public methods and domain models over private implementation details.
- Use exact numeric assertions only for accounting or indicator formulas where precision is the point of the behavior.
- Use `assertIn()` or range checks when wording or scoring can evolve without breaking the user-facing contract.
- Keep tests independent and avoid the real SQLite database unless the behavior under test is persistence.
- Add or update the relevant GIVEN WHEN THEN test before changing behavior.
