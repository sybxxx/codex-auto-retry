@echo off
setlocal
chcp 65001 >nul
title Codex Auto Retry Installer
echo.
echo Codex Auto Retry - one-click installer
echo.
powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0deploy.ps1"
set "EXIT_CODE=%ERRORLEVEL%"
echo.
if not "%EXIT_CODE%"=="0" (
  echo Installation failed. Review the error above.
) else (
  echo Installation succeeded.
)
echo.
pause
exit /b %EXIT_CODE%
