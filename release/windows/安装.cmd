@echo off
setlocal
chcp 65001 >nul
title Codex Auto Retry Installer
echo.
echo Codex Auto Retry - one-click installer
echo.
powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0deploy.ps1" -WaitForCodexExit
set "EXIT_CODE=%ERRORLEVEL%"
echo.
if "%EXIT_CODE%"=="2" (
  echo Installation cancelled. No plugin or runtime changes were made.
) else if not "%EXIT_CODE%"=="0" (
  echo Installation failed. Review the error above.
) else (
  echo Installation succeeded.
)
echo.
pause
exit /b %EXIT_CODE%
