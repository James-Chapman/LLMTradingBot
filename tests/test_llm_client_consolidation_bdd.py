"""BDD tests verifying TransformersClient is the sole LLM backend."""

import unittest

from bdd_helpers import BACKEND_DIR  # noqa: F401
from config.settings import BotSettings


class LLMClientConsolidationBDDTests(unittest.TestCase):

    # GIVEN the settings module WHEN LLM fields are inspected
    # THEN only transformers fields exist — no ollama, lm_studio, or llama_cpp.
    def test_given_settings_when_llm_fields_inspected_then_only_transformers_fields_exist(self) -> None:
        field_aliases = {
            field.validation_alias
            for field in BotSettings.model_fields.values()
            if isinstance(field.validation_alias, str)
        }
        removed = {"OLLAMA_URL", "OLLAMA_MODEL", "OLLAMA_TIMEOUT",
                   "LM_STUDIO_URL", "LM_STUDIO_MODEL",
                   "LLAMA_CPP_URL", "LLAMA_CPP_MODEL", "LLAMA_CPP_TIMEOUT"}
        self.assertFalse(
            removed & field_aliases,
            f"Removed LLM backend fields still present in settings: {removed & field_aliases}",
        )

    # GIVEN the settings module WHEN transformers fields are inspected
    # THEN TRANSFORMERS_LLM_MODEL and TRANSFORMERS_TIMEOUT are defined.
    def test_given_settings_when_transformers_fields_inspected_then_both_fields_exist(self) -> None:
        field_aliases = {
            field.validation_alias
            for field in BotSettings.model_fields.values()
            if isinstance(field.validation_alias, str)
        }
        self.assertIn("TRANSFORMERS_LLM_MODEL", field_aliases)
        self.assertIn("TRANSFORMERS_TIMEOUT", field_aliases)

    # GIVEN default settings WHEN transformers_timeout is read
    # THEN it returns a positive integer.
    def test_given_default_settings_when_transformers_timeout_read_then_positive_int(self) -> None:
        s = BotSettings()
        self.assertIsInstance(s.transformers_timeout, int)
        self.assertGreater(s.transformers_timeout, 0)

    # GIVEN default settings WHEN llm_only_max_concurrency is read
    # THEN it returns a positive integer.
    def test_given_default_settings_when_llm_only_max_concurrency_read_then_positive_int(self) -> None:
        s = BotSettings()
        self.assertIsInstance(s.llm_only_max_concurrency, int)
        self.assertGreater(s.llm_only_max_concurrency, 0)


if __name__ == "__main__":
    unittest.main()
