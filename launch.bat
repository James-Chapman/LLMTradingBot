@echo off
setlocal

set SCRIPT_DIR=%~dp0
set VENV_ACTIVATE=%SCRIPT_DIR%.venv\Scripts\activate.bat
set BACKEND_DIR=%SCRIPT_DIR%kraken-bot\backend
set URL=http://127.0.0.1:8000

echo Starting Ollama...
where ollama >nul 2>&1
if %ERRORLEVEL% equ 0 (
    start "Ollama" cmd /k "ollama serve"
    timeout /t 3 /nobreak > nul
) else (
    echo WARNING: ollama not found on PATH - LLM features will be unavailable.
)

echo Starting Kraken Trading Bot...
start "Kraken Bot" cmd /k "call %VENV_ACTIVATE% && cd /d %BACKEND_DIR% && python main.py"

echo Waiting for server to start...
timeout /t 4 /nobreak > nul

echo Opening %URL%...
if exist "C:\Program Files\Mozilla Firefox\firefox.exe" (
    start "" "C:\Program Files\Mozilla Firefox\firefox.exe" %URL%
) else if exist "C:\Program Files (x86)\Mozilla Firefox\firefox.exe" (
    start "" "C:\Program Files (x86)\Mozilla Firefox\firefox.exe" %URL%
) else (
    start %URL%
)
