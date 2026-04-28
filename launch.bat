@echo off
setlocal EnableDelayedExpansion

set SCRIPT_DIR=%~dp0
set VENV_DIR=%SCRIPT_DIR%.venv
set VENV_ACTIVATE=%VENV_DIR%\Scripts\activate.bat
set VENV_PYTHON=%VENV_DIR%\Scripts\python.exe
set REQUIREMENTS=%SCRIPT_DIR%requirements.txt
set VENV_STAMP=%VENV_DIR%\.requirements.sha256
set BACKEND_DIR=%SCRIPT_DIR%backend
set URL=http://127.0.0.1:8000

if not exist "%REQUIREMENTS%" (
    echo ERROR: requirements.txt not found at "%REQUIREMENTS%".
    pause
    exit /b 1
)

if not exist "%VENV_PYTHON%" (
    echo Project virtual environment not found. Creating "%VENV_DIR%"...
    set PYTHON_CMD=
    py -3.14 --version >nul 2>&1
    if not errorlevel 1 (
        set PYTHON_CMD=py -3.14
    ) else (
        python --version >nul 2>&1
        if errorlevel 1 (
            echo ERROR: Python was not found. Install Python 3.14+ or run setup.bat.
            pause
            exit /b 1
        )
        set PYTHON_CMD=python
    )
    !PYTHON_CMD! -m venv "%VENV_DIR%"
    if errorlevel 1 (
        echo ERROR: failed to create project virtual environment.
        pause
        exit /b 1
    )
)

for /f "usebackq delims=" %%H in (`powershell -NoProfile -ExecutionPolicy Bypass -Command "(Get-FileHash -Algorithm SHA256 -LiteralPath '%REQUIREMENTS%').Hash"`) do set REQUIREMENTS_HASH=%%H
if "%REQUIREMENTS_HASH%"=="" (
    echo ERROR: failed to hash requirements.txt.
    pause
    exit /b 1
)

set NEEDS_VENV_UPDATE=0
if not exist "%VENV_STAMP%" set NEEDS_VENV_UPDATE=1
if exist "%VENV_STAMP%" (
    set /p INSTALLED_REQUIREMENTS_HASH=<"%VENV_STAMP%"
    if /I not "!INSTALLED_REQUIREMENTS_HASH!"=="!REQUIREMENTS_HASH!" set NEEDS_VENV_UPDATE=1
)

"%VENV_PYTHON%" -c "import fastapi, httpx, numpy, pydantic_settings, sqlalchemy, uvicorn" >nul 2>&1
if errorlevel 1 set NEEDS_VENV_UPDATE=1

if "%NEEDS_VENV_UPDATE%"=="1" (
    echo Updating project virtual environment from requirements.txt...
    "%VENV_PYTHON%" -m pip install --quiet --upgrade pip
    if errorlevel 1 (
        echo ERROR: failed to upgrade pip in the project virtual environment.
        pause
        exit /b 1
    )
    "%VENV_PYTHON%" -m pip install --quiet -r "%REQUIREMENTS%"
    if errorlevel 1 (
        echo ERROR: failed to install project dependencies.
        pause
        exit /b 1
    )
    > "%VENV_STAMP%" echo %REQUIREMENTS_HASH%
    echo Project virtual environment is up to date.
) else (
    echo Project virtual environment is up to date.
)

echo.
echo =========================================
echo  Checking LLM backends...
echo =========================================

:: -- LM Studio ------------------------------------------------------------------
:: Priority 1: try LM Studio OpenAI-compatible server on port 1234.
set LM_STUDIO_READY=0

curl -s --max-time 3 http://localhost:1234/v1/models >nul 2>&1
if not errorlevel 1 (
    echo [OK] LM Studio server is already running.
    set LM_STUDIO_READY=1
)

if "!LM_STUDIO_READY!"=="1" goto :after_lm_studio_check

:: LM Studio not running ? try to start it.
:: Prefer the headless CLI (lms) over the GUI.
where lms >nul 2>&1
if not errorlevel 1 (
    echo LM Studio not running. Starting via lms CLI...
    start "LM Studio Server" /min cmd /c "lms server start"
    goto :wait_lm_studio
)

:: Fall back to launching the GUI application.
set LMS_EXE=
if exist "%LOCALAPPDATA%\Programs\LM Studio\LM Studio.exe" set LMS_EXE=%LOCALAPPDATA%\Programs\LM Studio\LM Studio.exe
if "%LMS_EXE%"=="" if exist "%PROGRAMFILES%\LM Studio\LM Studio.exe" set LMS_EXE=%PROGRAMFILES%\LM Studio\LM Studio.exe

if not "%LMS_EXE%"=="" (
    echo LM Studio not running. Starting from "%LMS_EXE%"...
    echo NOTE: Enable "Start server on launch" in LM Studio settings for reliable auto-start.
    start "" "%LMS_EXE%"
    goto :wait_lm_studio
)

echo LM Studio not found. Skipping.
goto :try_ollama

:wait_lm_studio
echo Waiting for LM Studio server (up to 20s)...
for /l %%i in (1,1,10) do (
    if "!LM_STUDIO_READY!"=="0" (
        timeout /t 2 /nobreak >nul
        curl -s --max-time 3 http://localhost:1234/v1/models >nul 2>&1
        if not errorlevel 1 set LM_STUDIO_READY=1
    )
)
if "!LM_STUDIO_READY!"=="1" (
    echo [OK] LM Studio server is running.
) else (
    echo [WARN] LM Studio did not respond in time. Trying Ollama...
    goto :try_ollama
)

:after_lm_studio_check
if "!LM_STUDIO_READY!"=="1" goto :start_app

:: -- Ollama ----------------------------------------------------------------------
:: Priority 2: try Ollama on port 11434.
:try_ollama
set OLLAMA_READY=0

curl -s --max-time 3 http://localhost:11434 >nul 2>&1
if not errorlevel 1 (
    echo [OK] Ollama is already running.
    set OLLAMA_READY=1
)

if "!OLLAMA_READY!"=="0" (
    where ollama >nul 2>&1
    if not errorlevel 1 (
        echo Ollama not running. Starting ollama serve...
        start "Ollama" /min cmd /c "ollama serve"
        echo Waiting for Ollama (up to 15s)...
        for /l %%i in (1,1,5) do (
            if "!OLLAMA_READY!"=="0" (
                timeout /t 3 /nobreak >nul
                curl -s --max-time 3 http://localhost:11434 >nul 2>&1
                if not errorlevel 1 set OLLAMA_READY=1
            )
        )
        if "!OLLAMA_READY!"=="1" (
            echo [OK] Ollama is running.
        ) else (
            echo [WARN] Ollama did not respond in time.
        )
    ) else (
        echo Ollama not installed. Skipping.
    )
)

if "!OLLAMA_READY!"=="0" (
    echo [INFO] No LLM server available. Application will use local Transformers model.
)

:: -- Start the bot ---------------------------------------------------------------
:start_app
echo.
echo Starting Kraken Trading Bot...
start "Kraken Bot" cmd /k "call ""%VENV_ACTIVATE%"" && cd /d ""%BACKEND_DIR%"" && ""%VENV_PYTHON%"" main.py"

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
