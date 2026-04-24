"""BDD coverage for configuration loading."""
import os
import tempfile
import unittest

from bdd_helpers import BACKEND_DIR  # noqa: F401
from config.settings import BotSettings


class SettingsBDDTests(unittest.TestCase):

    # GIVEN uppercase and lowercase environment variables WHEN settings load
    # THEN only the exact uppercase variable is accepted.
    def test_given_mixed_case_env_when_settings_load_then_uppercase_value_wins(self) -> None:
        previous_upper = os.environ.pop("BASE_CURRENCY", None)
        previous_lower = os.environ.pop("base_currency", None)
        env_file = tempfile.NamedTemporaryFile("w", delete=False)
        env_file.write("base_currency=GBP\nBASE_CURRENCY=USD\n")
        env_file.close()
        try:
            settings = BotSettings(_env_file=env_file.name)
        finally:
            os.unlink(env_file.name)
            if previous_upper is None:
                os.environ.pop("BASE_CURRENCY", None)
            else:
                os.environ["BASE_CURRENCY"] = previous_upper
            if previous_lower is None:
                os.environ.pop("base_currency", None)
            else:
                os.environ["base_currency"] = previous_lower

        self.assertEqual(settings.base_currency, "USD")


if __name__ == "__main__":
    unittest.main()
