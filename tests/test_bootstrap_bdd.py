"""BDD coverage for startup environment bootstrap helpers."""

import unittest
import os
from pathlib import Path

from bdd_helpers import BACKEND_DIR  # noqa: F401
from bootstrap import (
    dependency_error_message,
    ensure_project_venv,
    project_venv_python,
    restart_with_project_venv,
    should_restart_with_project_venv,
    venv_required_error_message,
)


class BootstrapBDDTests(unittest.TestCase):
    # GIVEN the backend main file WHEN project venv Python is resolved
    # THEN it points at the repository virtual environment.
    def test_given_main_file_when_project_venv_resolved_then_repo_venv_python_returned(self) -> None:
        main_file = Path(BACKEND_DIR) / "main.py"

        result = project_venv_python(main_file)

        expected = Path(BACKEND_DIR).parent / ".venv"
        expected = expected / "Scripts" / "python.exe" if os.name == "nt" else expected / "bin" / "python"
        self.assertEqual(expected, result)
        self.assertTrue(result.exists())
        self.assertIn(".venv", result.parts)

    # GIVEN system Python and an existing project venv WHEN restart is checked
    # THEN the bootstrap selects the project venv.
    def test_given_system_python_when_restart_checked_then_project_venv_is_selected(self) -> None:
        main_file = Path(BACKEND_DIR) / "main.py"
        venv_python = project_venv_python(main_file)

        result = should_restart_with_project_venv("C:/Python/python.exe", venv_python)

        self.assertTrue(result)

    # GIVEN a missing dependency under system Python WHEN restart runs
    # THEN exec is invoked with the project venv interpreter.
    def test_given_missing_dependency_when_restart_runs_then_exec_uses_project_venv(self) -> None:
        main_file = Path(BACKEND_DIR) / "main.py"
        calls = []

        def fake_exec(path: str, args: list[str]) -> None:
            calls.append((path, args))

        restarted = restart_with_project_venv(
            main_file,
            current_executable="C:/Python/python.exe",
            argv=["main.py"],
            exec_fn=fake_exec,
        )

        expected = str(project_venv_python(main_file))
        self.assertTrue(restarted)
        self.assertEqual(calls, [(expected, [expected, "main.py"])])

    # GIVEN no explicit argv WHEN restart runs
    # THEN the backend main file is used as the target script.
    def test_given_no_argv_when_restart_runs_then_backend_main_is_target_script(self) -> None:
        main_file = Path(BACKEND_DIR) / "main.py"
        calls = []

        def fake_exec(path: str, args: list[str]) -> None:
            calls.append((path, args))

        restarted = restart_with_project_venv(
            main_file,
            current_executable="C:/Python/python.exe",
            exec_fn=fake_exec,
        )

        self.assertTrue(restarted)
        self.assertEqual(Path(calls[0][1][1]), main_file)

    # GIVEN system Python WHEN project venv enforcement runs
    # THEN the process is restarted through the project virtual environment.
    def test_given_system_python_when_venv_enforced_then_process_restarts_in_project_venv(self) -> None:
        main_file = Path(BACKEND_DIR) / "main.py"
        calls = []

        def fake_exec(path: str, args: list[str]) -> None:
            calls.append((path, args))

        restarted = ensure_project_venv(
            main_file,
            current_executable="C:/Python/python.exe",
            argv=["main.py"],
            exec_fn=fake_exec,
        )

        expected = str(project_venv_python(main_file))
        self.assertTrue(restarted)
        self.assertEqual(calls, [(expected, [expected, "main.py"])])

    # GIVEN no project venv exists WHEN project venv enforcement runs
    # THEN startup fails with setup guidance instead of using system Python.
    def test_given_missing_project_venv_when_venv_enforced_then_setup_guidance_is_returned(self) -> None:
        message = venv_required_error_message(Path(BACKEND_DIR) / "main.py")

        self.assertIn("must run from the project virtual environment", message)
        self.assertIn("setup.bat", message)

    # GIVEN a dependency error WHEN a message is built
    # THEN setup and IDE interpreter guidance is included.
    def test_given_dependency_error_when_message_built_then_setup_guidance_is_included(self) -> None:
        main_file = Path(BACKEND_DIR) / "main.py"
        exc = ModuleNotFoundError("No module named 'httpx'")
        exc.name = "httpx"

        message = dependency_error_message(exc, main_file)

        self.assertIn("httpx", message)
        self.assertIn("setup.bat", message)
        self.assertIn(".venv", message)

    # GIVEN the Windows launcher WHEN its command is inspected
    # THEN it starts main.py with the project venv interpreter explicitly.
    def test_given_windows_launcher_when_inspected_then_project_venv_python_is_used(self) -> None:
        launcher = Path(BACKEND_DIR).parent / "launch.bat"

        content = launcher.read_text(encoding="utf-8")

        self.assertIn("set VENV_PYTHON=", content)
        self.assertIn("%VENV_PYTHON%", content)
        self.assertIn("main.py", content)

    # GIVEN the Windows launcher WHEN inspected
    # THEN it creates or updates the venv from requirements before launching.
    def test_given_windows_launcher_when_inspected_then_venv_is_checked_and_updated(self) -> None:
        launcher = Path(BACKEND_DIR).parent / "launch.bat"

        content = launcher.read_text(encoding="utf-8")

        self.assertIn("set REQUIREMENTS=", content)
        self.assertIn("set VENV_STAMP=", content)
        self.assertIn("-m venv", content)
        self.assertIn("-m pip install --quiet -r", content)
        self.assertIn("Get-FileHash", content)


if __name__ == "__main__":
    unittest.main()
