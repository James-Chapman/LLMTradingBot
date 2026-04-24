"""BDD coverage for currency display helpers."""
import unittest

from bdd_helpers import BACKEND_DIR  # noqa: F401
from config.currency import currency_symbol


class CurrencyBDDTests(unittest.TestCase):

    # GIVEN a configured base currency WHEN the display symbol is requested
    # THEN common currencies use their expected symbol and unknown values stay explicit.
    def test_given_currency_code_when_symbol_requested_then_display_symbol_is_returned(self) -> None:
        self.assertEqual(currency_symbol("EUR"), "€")
        self.assertEqual(currency_symbol("GBP"), "£")
        self.assertEqual(currency_symbol("usd"), "$")
        self.assertEqual(currency_symbol("CHF"), "CHF")


if __name__ == "__main__":
    unittest.main()
