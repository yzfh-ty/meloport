@echo off
setlocal EnableExtensions

pushd "%~dp0"
if errorlevel 1 exit /b 1

powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\dev.ps1" -Action dev %*
set "testExitCode=%ERRORLEVEL%"

popd
exit /b %testExitCode%
