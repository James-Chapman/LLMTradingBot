"""BDD coverage for currency display helpers."""
import unittest

from bdd_helpers import BACKEND_DIR  # noqa: F401
from config.currency import currency_symbol


class CurrencyBDDTests(unittest.TestCase):
    # GIVEN a configured base currency WHEN the display symbol is requested
    # THEN common currencies use their expected symbol and unknown values stay explicit.
    def test_given_currency_code_when_symbol_requested_then_display_symbol_is_returned(self) -> None:
        self.assertEqual(currency_symbol("EUR"), "\u20ac")
        self.assertEqual(currency_symbol("GBP"), "\u00a3")
        self.assertEqual(currency_symbol("usd"), "$")
        self.assertEqual(currency_symbol("CHF"), "CHF")

    # GIVEN display symbols are serialized WHEN viewed by the UI THEN mojibake is not returned.
    def test_given_common_currency_when_symbol_requested_then_mojibake_is_not_returned(self) -> None:
        mojibake_fragments = ("â", "Ã", "Â")

        symbols = [currency_symbol("EUR"), currency_symbol("GBP"), currency_symbol("USD")]

        self.assertFalse(any(fragment in symbol for symbol in symbols for fragment in mojibake_fragments))


if __name__ == "__main__":
    unittest.main()
