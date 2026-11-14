@echo off
setlocal EnableDelayedExpansion

REM ==================================================
REM Kraken Trading Bot — build and launch
REM Builds the Go binary, starts the server in its own
REM window and opens the dashboard in a browser.
REM ==================================================

set ROOT=%~dp0
REM The folder without its trailing backslash, for start /D.
set ROOT_DIR=%ROOT:~0,-1%
set BIN_DIR=%ROOT%bin
set BIN=%BIN_DIR%\tradingbot.exe
set URL=http://127.0.0.1:8000

where go >nul 2>&1
if errorlevel 1 (
    echo ERROR: Go was not found on PATH. Install Go 1.27+ or run setup.bat.
    pause
    exit /b 1
)

echo Building tradingbot...
pushd "%ROOT%"
go build -o "%BIN%" ./cmd/tradingbot
if errorlevel 1 (
    popd
    echo ERROR: build failed. See the output above.
    pause
    exit /b 1
)
popd

echo.
echo Starting Kraken Trading Bot...
REM /D sets the working folder; cmd /k strips the outer quotes of ""%BIN%"",
REM so a path with spaces stays quoted and the window stays open on exit.
start "Kraken Bot" /D "%ROOT_DIR%" cmd /k ""%BIN%""

echo Waiting for server to start...
timeout /t 3 /nobreak > nul

echo Opening %URL%...
if exist "C:\Program Files\Mozilla Firefox\firefox.exe" (
    start "" "C:\Program Files\Mozilla Firefox\firefox.exe" %URL%
) else if exist "C:\Program Files (x86)\Mozilla Firefox\firefox.exe" (
    start "" "C:\Program Files (x86)\Mozilla Firefox\firefox.exe" %URL%
) else (
    start %URL%
)
