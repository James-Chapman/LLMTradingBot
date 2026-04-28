@echo off
setlocal EnableDelayedExpansion
title Kraken Bot — Setup

:: ─────────────────────────────────────────────────────────────────────────────
::  Paths (all relative to this script's location)
:: ─────────────────────────────────────────────────────────────────────────────
set ROOT=%~dp0
set VENV_DIR=%ROOT%.venv
set BACKEND_DIR=%ROOT%backend
set REQUIREMENTS=%ROOT%requirements.txt
set ENV_FILE=%BACKEND_DIR%\.env
set ENV_TEMPLATE=%BACKEND_DIR%\.env.example
set DOCS_DIR=%ROOT%docs

:: ─────────────────────────────────────────────────────────────────────────────
::  Colour helpers (via ANSI — works on Windows 10 1511+)
:: ─────────────────────────────────────────────────────────────────────────────
for /f %%a in ('echo prompt $E ^| cmd') do set ESC=%%a
set GREEN=%ESC%[32m
set RED=%ESC%[31m
set YELLOW=%ESC%[33m
set CYAN=%ESC%[36m
set BOLD=%ESC%[1m
set RESET=%ESC%[0m

echo.
echo %BOLD%%CYAN%╔══════════════════════════════════════════════╗%RESET%
echo %BOLD%%CYAN%║         Kraken Trading Bot  —  Setup         ║%RESET%
echo %BOLD%%CYAN%╚══════════════════════════════════════════════╝%RESET%
echo.


:: ═════════════════════════════════════════════════════════════════════════════
::  STEP 1 — Check Python 3.14+
:: ═════════════════════════════════════════════════════════════════════════════
echo %BOLD%[1/4] Checking prerequisites...%RESET%
echo.

:: Try the Python Launcher first (most reliable on Windows)
set PYTHON_CMD=
py -3.14 --version >nul 2>&1
if not errorlevel 1 (
    set PYTHON_CMD=py -3.14
    goto python_ok
)

:: Fall back to bare python — check version with a one-liner
python --version >nul 2>&1
if errorlevel 1 goto no_python

python -c "import sys; exit(0 if sys.version_info >= (3,14) else 1)" >nul 2>&1
if errorlevel 1 goto wrong_python

set PYTHON_CMD=python
goto python_ok

:no_python
echo %RED%  ✗  Python not found.%RESET%
echo.
echo     Python 3.14 or newer is required.
echo     Download it from:  https://www.python.org/downloads/
echo     Make sure to check "Add Python to PATH" during installation.
echo.
goto prerequisites_failed

:wrong_python
for /f "tokens=2" %%v in ('python --version 2^>^&1') do set PY_VER=%%v
echo %RED%  ✗  Python %PY_VER% found, but 3.14+ is required.%RESET%
echo.
echo     Download Python 3.14 from:  https://www.python.org/downloads/
echo     The Python Launcher (py.exe) can manage multiple versions side-by-side.
echo.
goto prerequisites_failed

:python_ok
for /f "tokens=2" %%v in ('!PYTHON_CMD! --version 2^>^&1') do set PY_VER=%%v
echo %GREEN%  ✓  Python %PY_VER%%RESET%
goto prerequisites_ok

:prerequisites_failed
echo %RED%Please install the missing prerequisite(s) above, then re-run setup.bat.%RESET%
echo.
pause
exit /b 1

:prerequisites_ok
echo.


:: ═════════════════════════════════════════════════════════════════════════════
::  STEP 2 — Create virtual environment
:: ═════════════════════════════════════════════════════════════════════════════
echo %BOLD%[2/4] Setting up Python virtual environment...%RESET%
echo.

if exist "%VENV_DIR%\Scripts\activate.bat" (
    echo %GREEN%  ✓  Virtual environment already exists — skipping creation%RESET%
) else (
    echo       Creating .venv at %VENV_DIR%
    !PYTHON_CMD! -m venv "%VENV_DIR%"
    if errorlevel 1 (
        echo %RED%  ✗  Failed to create virtual environment.%RESET%
        echo     Check that python has the venv module available.
        goto failed
    )
    echo %GREEN%  ✓  Virtual environment created%RESET%
)
echo.


:: ═════════════════════════════════════════════════════════════════════════════
::  STEP 3 — Install Python dependencies
:: ═════════════════════════════════════════════════════════════════════════════
echo %BOLD%[3/4] Installing Python dependencies...%RESET%
echo.

if not exist "%REQUIREMENTS%" (
    echo %RED%  ✗  requirements.txt not found at %REQUIREMENTS%%RESET%
    goto failed
)

echo       This may take a minute on first run.
echo.
call "%VENV_DIR%\Scripts\activate.bat"
pip install --quiet --upgrade pip
pip install --quiet -r "%REQUIREMENTS%"
if errorlevel 1 (
    echo %RED%  ✗  pip install failed. See error above.%RESET%
    goto failed
)
echo %GREEN%  ✓  All packages installed%RESET%
echo.


:: ═════════════════════════════════════════════════════════════════════════════
::  STEP 4 — Create .env if missing
:: ═════════════════════════════════════════════════════════════════════════════
echo %BOLD%[4/4] Configuring environment...%RESET%
echo.

if exist "%ENV_FILE%" (
    echo %GREEN%  ✓  .env already exists — leaving it untouched%RESET%
) else (
    :: Write a safe default .env — paper trading, manual mode, no API keys
    (
        echo # ── Trading mode ────────────────────────────────────────────────────────────
        echo # manual          — signals visible in dashboard only, no trades placed
        echo # semi_automated  — signals submitted to approval queue for your review
        echo # fully_automated — executes immediately when risk engine approves
        echo TRADING_MODE=manual
        echo.
        echo # paper — simulation only, no real money at risk
        echo # live  — real Kraken orders ^(requires API keys below^)
        echo TRADING_ENVIRONMENT=paper
        echo.
        echo # ── Capital ^& risk ──────────────────────────────────────────────────────────
        echo STARTING_CAPITAL=500.0
        echo STOP_LOSS_PCT=0.07
        echo MIN_TRADE_SIZE=50.0
        echo MAX_LOSS_PER_TRADE_PERCENT=7.0
        echo MAX_DAILY_LOSS_PERCENT=7.0
        echo FEE_AND_SLIPPAGE=0.0036
        echo.
        echo # ── Signal quality gates ────────────────────────────────────────────────────
        echo MIN_SIGNAL_CONFIDENCE=0.65
        echo LLM_VETO_THRESHOLD=0.70
        echo.
        echo # ── Markets ^(EUR-quoted Kraken pairs^) ──────────────────────────────────────
        echo FIXED_MARKETS=["BTC/EUR", "ETH/EUR", "SOL/EUR", "XRP/EUR", "ADA/EUR"]
        echo.
        echo # ── Local LLM ^(Transformers^) ────────────────────────────────────────────────
        echo # Set to a HuggingFace model ID, e.g. google/gemma-4-E4B-it
        echo TRANSFORMERS_LLM_MODEL=
        echo TRANSFORMERS_TIMEOUT=60
        echo LLM_ONLY_MAX_CONCURRENCY=3
        echo.
        echo # ── Kraken API ^(live trading only — leave blank for paper^) ─────────────────
        echo KRAKEN_API_KEY=
        echo KRAKEN_API_SECRET=
        echo.
        echo # ── Server ──────────────────────────────────────────────────────────────────
        echo HOST=127.0.0.1
        echo PORT=8000
        echo DEBUG=false
        echo.
        echo # ── Logging ─────────────────────────────────────────────────────────────────
        echo LOG_LEVEL=INFO
    ) > "%ENV_FILE%"
    echo %GREEN%  ✓  Created %ENV_FILE%%RESET%
    echo %YELLOW%  ℹ  Default config: paper trading, manual mode, £500 starting capital%RESET%
    echo %YELLOW%     Edit %ENV_FILE% to adjust settings before starting.%RESET%
)
echo.


:: ═════════════════════════════════════════════════════════════════════════════
::  Done — print summary
:: ═════════════════════════════════════════════════════════════════════════════
echo %BOLD%%GREEN%╔══════════════════════════════════════════════╗%RESET%
echo %BOLD%%GREEN%║              Setup complete!                 ║%RESET%
echo %BOLD%%GREEN%╚══════════════════════════════════════════════╝%RESET%
echo.
echo %BOLD%Configuration%RESET%
echo   Edit settings:   %CYAN%%ENV_FILE%%RESET%
echo   Key options:     TRADING_MODE, STARTING_CAPITAL, FIXED_MARKETS
echo.
echo %BOLD%Documentation%RESET%
echo   Overview:        %CYAN%%DOCS_DIR%\00-overview.md%RESET%
echo   All docs:        %CYAN%%DOCS_DIR%%RESET%
echo.
echo %BOLD%Starting the bot%RESET%
echo   Double-click:    %CYAN%%ROOT%launch.bat%RESET%
echo   This starts the bot server and opens the dashboard
echo   in your browser at  http://127.0.0.1:8000
echo.
echo %YELLOW%  ℹ  First run tip:%RESET%
echo %YELLOW%     The bot starts in manual mode — signals are shown but no trades%RESET%
echo %YELLOW%     are placed. Switch to semi_automated in .env when you're ready.%RESET%
echo.
pause
exit /b 0


:: ─────────────────────────────────────────────────────────────────────────────
:failed
echo.
echo %RED%Setup did not complete successfully. See the error above.%RESET%
echo.
pause
exit /b 1
