"""BDD coverage for configuration loading."""
import os
import tempfile
import unittest
from pathlib import Path

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

    # GIVEN the example env file WHEN settings aliases are inspected
    # THEN every supported configuration option is documented and parseable.
    def test_given_env_example_when_settings_aliases_inspected_then_all_options_are_present(self) -> None:
        example_path = Path(BACKEND_DIR).parent / "backend" / ".env.example"
        content = example_path.read_text(encoding="utf-8")
        example_keys = {
            line.split("=", 1)[0].strip()
            for line in content.splitlines()
            if line.strip() and not line.lstrip().startswith("#") and "=" in line
        }
        expected_keys = {
            field.validation_alias
            for field in BotSettings.model_fields.values()
            if isinstance(field.validation_alias, str)
        }

        self.assertEqual(example_keys, expected_keys)
        previous_values = {
            key: os.environ.pop(key)
            for key in expected_keys
            if key in os.environ
        }
        try:
            BotSettings(_env_file=example_path)
        finally:
            os.environ.update(previous_values)


if __name__ == "__main__":
    unittest.main()
