@echo off
setlocal
title Meloport - Manual Test

pushd "%~dp0"
if errorlevel 1 exit /b 1

echo Starting Meloport for manual testing...
echo Default Web UI: http://127.0.0.1:8080/
echo If server.listen was changed, use the address shown in the server log.
echo.
echo Frontend changes: refresh the browser.
echo Go or YAML changes: press Ctrl+C, then start this script again.
echo Press Ctrl+C to stop the server when finished.
echo.

powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\dev.ps1" -Action dev %*
set "testExitCode=%ERRORLEVEL%"

popd
if not "%testExitCode%"=="0" (
    echo.
    echo Meloport stopped with exit code %testExitCode%. Review the messages above.
    pause
)
exit /b %testExitCode%
