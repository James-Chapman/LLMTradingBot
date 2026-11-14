@echo off
REM ==================================================
REM LLM Trading Bot — Test Runner
REM Runs go vet and the full Go test suite with
REM verbose GIVEN/WHEN/THEN output.
REM ==================================================

pushd "%~dp0"

echo.
echo ==================================================
echo Running go vet...
echo ==================================================
go vet ./...
if %errorlevel% neq 0 goto failed

echo.
echo ==================================================
echo Running all tests...
echo ==================================================
echo.
go test -v ./...
if %errorlevel% neq 0 goto failed

echo.
echo *** All tests passed. ***
goto done

:failed
echo.
echo !!! TESTS FAILED — review the output above. !!!

:done
popd
echo.
echo ==================================================
echo Done.
echo ==================================================
