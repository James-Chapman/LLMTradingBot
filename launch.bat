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
