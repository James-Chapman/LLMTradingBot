"""Currency display helpers."""

CURRENCY_SYMBOLS = {
    "EUR": "€",
    "GBP": "£",
    "USD": "$",
}


# Return a display symbol for an ISO currency code.
def currency_symbol(code: str) -> str:
    return CURRENCY_SYMBOLS.get((code or "").upper(), (code or "").upper())
