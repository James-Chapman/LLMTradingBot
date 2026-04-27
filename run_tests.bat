@echo off
REM ==================================================
REM LLM Trading Bot — Test Runner
REM Activates the virtual environment and runs the full
REM test suite via pytest with verbose output.
REM ==================================================

echo.
echo Activating virtual environment...
call .venv\Scripts\activate.bat

echo.
echo ==================================================
echo Running all tests...
echo ==================================================
echo.
python -m pytest -v --tb=short

if %errorlevel% neq 0 (
    echo.
    echo !!! TESTS FAILED — review the output above. !!!
) else (
    echo.
    echo *** All tests passed. ***
)

echo.
echo ==================================================
echo Done.
echo ==================================================
