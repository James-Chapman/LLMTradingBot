"""BDD coverage for project linting configuration."""
import unittest
from pathlib import Path

PROJECT_ROOT = Path(__file__).resolve().parents[1]


class LintingConfigurationBDDTests(unittest.TestCase):
    # GIVEN project linting is required WHEN tooling is inspected THEN Ruff is configured.
    def test_given_project_linting_when_config_checked_then_ruff_is_configured(self) -> None:
        pyproject = PROJECT_ROOT / "pyproject.toml"
        content = pyproject.read_text(encoding="utf-8")

        self.assertIn("[tool.ruff]", content)
        self.assertIn("[tool.ruff.lint]", content)
        self.assertIn("select", content)

    # GIVEN contributors need lint commands WHEN docs are inspected THEN lint commands are documented.
    def test_given_linting_tooling_when_docs_checked_then_lint_command_is_documented(self) -> None:
        docs = PROJECT_ROOT / "docs" / "13-testing.md"
        content = docs.read_text(encoding="utf-8")

        self.assertIn("ruff check", content)
        self.assertIn("requirements-dev.txt", content)


if __name__ == "__main__":
    unittest.main()
