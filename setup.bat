@echo off
setlocal EnableDelayedExpansion
title Kraken Bot — Setup

:: ─────────────────────────────────────────────────────────────────────────────
::  Paths (all relative to this script's location)
:: ─────────────────────────────────────────────────────────────────────────────
set ROOT=%~dp0
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
::  STEP 1 — Check Go
:: ═════════════════════════════════════════════════════════════════════════════
echo %BOLD%[1/3] Checking prerequisites...%RESET%
echo.

where go >nul 2>&1
if errorlevel 1 (
    echo %RED%  ✗  Go not found.%RESET%
    echo.
    echo     Go 1.27 or newer is required.
    echo     Download it from:  https://go.dev/dl/
    echo.
    goto failed
)
for /f "tokens=3" %%v in ('go version') do set GO_VER=%%v
echo %GREEN%  ✓  %GO_VER%%RESET%
echo.

:: ═════════════════════════════════════════════════════════════════════════════
::  STEP 2 — Download modules
:: ═════════════════════════════════════════════════════════════════════════════
echo %BOLD%[2/3] Downloading Go modules...%RESET%
echo.
pushd "%ROOT%"
go mod download
if errorlevel 1 (
    popd
    echo %RED%  ✗  go mod download failed. See error above.%RESET%
    goto failed
)
echo %GREEN%  ✓  Modules downloaded%RESET%
echo.

:: ═════════════════════════════════════════════════════════════════════════════
::  STEP 3 — Build
:: ═════════════════════════════════════════════════════════════════════════════
echo %BOLD%[3/3] Building tradingbot...%RESET%
echo.
go build -o bin\tradingbot.exe ./cmd/tradingbot
if errorlevel 1 (
    popd
    echo %RED%  ✗  Build failed. See error above.%RESET%
    goto failed
)
go build -o bin\tradingbot-cli.exe ./cmd/cli
if errorlevel 1 (
    popd
    echo %RED%  ✗  CLI build failed. See error above.%RESET%
    goto failed
)
popd
echo %GREEN%  ✓  Built bin\tradingbot.exe and bin\tradingbot-cli.exe%RESET%
echo.

echo %BOLD%%GREEN%╔══════════════════════════════════════════════╗%RESET%
echo %BOLD%%GREEN%║              Setup complete!                 ║%RESET%
echo %BOLD%%GREEN%╚══════════════════════════════════════════════╝%RESET%
echo.
echo %BOLD%Configuration%RESET%
echo   Edit settings:   the Settings button on the dashboard
echo   First start:     paper trading, manual mode, built-in defaults
echo   Start-up flags:  tradingbot.exe -db -host -port -log  (see -h)
echo.
echo %BOLD%LLM%RESET%
echo   Start LM Studio's local server (Developer tab) on port 1234 and load a model.
echo.
echo %BOLD%Documentation%RESET%
echo   Overview:        %CYAN%%DOCS_DIR%\00-overview.md%RESET%
echo.
echo %BOLD%Starting the bot%RESET%
echo   Double-click:    %CYAN%%ROOT%launch.bat%RESET%
echo   Dashboard:       http://127.0.0.1:8000
echo.
pause
exit /b 0

:failed
echo.
echo %RED%Setup did not complete successfully. See the error above.%RESET%
echo.
pause
exit /b 1
