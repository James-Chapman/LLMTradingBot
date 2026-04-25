"""Startup helpers for selecting the project Python environment."""

import os
import sys
from pathlib import Path
from typing import Callable, Sequence


# Return the Python executable inside the repository-level virtual environment.
def project_venv_python(main_file: str | Path) -> Path:
    """Return the expected project virtual-environment Python executable."""
    backend_dir = Path(main_file).resolve().parent
    repo_root = backend_dir.parent
    if os.name == "nt":
        return repo_root / ".venv" / "Scripts" / "python.exe"
    return repo_root / ".venv" / "bin" / "python"


# Return True when a missing dependency can be handled by restarting in .venv.
def should_restart_with_project_venv(
    current_executable: str | Path,
    venv_python: str | Path,
) -> bool:
    """Return whether the current process should restart under project .venv."""
    current = Path(current_executable).resolve()
    target = Path(venv_python).resolve()
    return target.exists() and current != target


# Re-execute the current process using the project .venv Python when available.
def restart_with_project_venv(
    main_file: str | Path,
    *,
    current_executable: str | Path | None = None,
    argv: Sequence[str] | None = None,
    exec_fn: Callable[[str, list[str]], object] = os.execv,
) -> bool:
    """Restart this process under project .venv and return True if attempted."""
    venv_python = project_venv_python(main_file)
    executable = current_executable or sys.executable
    if not should_restart_with_project_venv(executable, venv_python):
        return False
    target_args = list(argv) if argv is not None else [str(Path(main_file).resolve()), *sys.argv[1:]]
    args = [str(venv_python), *target_args]
    exec_fn(str(venv_python), args)
    return True


# Build a clear error message when the project virtual environment is absent.
def venv_required_error_message(main_file: str | Path) -> str:
    """Return guidance for startup attempts outside the project virtual environment."""
    venv_python = project_venv_python(main_file)
    return (
        "The bot must run from the project virtual environment.\n"
        f"Expected interpreter: {venv_python}\n"
        "Run setup.bat from the repository root, then launch with launch.bat.\n"
        "If you are starting from an IDE, select the repository .venv interpreter."
    )


# Enforce that backend startup runs from the repository-level virtual environment.
def ensure_project_venv(
    main_file: str | Path,
    *,
    current_executable: str | Path | None = None,
    argv: Sequence[str] | None = None,
    exec_fn: Callable[[str, list[str]], object] = os.execv,
) -> bool:
    """Restart under project .venv or stop startup when .venv is unavailable."""
    venv_python = project_venv_python(main_file)
    executable = current_executable or sys.executable
    if Path(executable).resolve() == venv_python.resolve():
        return False
    if should_restart_with_project_venv(executable, venv_python):
        return restart_with_project_venv(
            main_file,
            current_executable=executable,
            argv=argv,
            exec_fn=exec_fn,
        )
    raise SystemExit(venv_required_error_message(main_file))


# Build a clear setup message for missing Python packages.
def dependency_error_message(exc: ModuleNotFoundError, main_file: str | Path) -> str:
    """Return a user-facing dependency installation error message."""
    venv_python = project_venv_python(main_file)
    package_name = exc.name or str(exc)
    return (
        f"Missing Python dependency: {package_name}\n"
        f"Expected project interpreter: {venv_python}\n"
        "Run setup.bat from the repository root, or install dependencies with:\n"
        f"  {venv_python} -m pip install -r {venv_python.parents[2] / 'requirements.txt'}\n"
        "If you are starting from an IDE, select the repository .venv interpreter."
    )
