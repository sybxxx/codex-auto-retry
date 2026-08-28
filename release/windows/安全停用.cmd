@echo off
setlocal
chcp 65001 >nul
title Codex Auto Retry Safe Disable
powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0startup-manager.ps1" -Action safe-disable
set "EXIT_CODE=%ERRORLEVEL%"
echo.
if not "%EXIT_CODE%"=="0" (
  echo Safe disable failed. Review the error above.
) else (
  echo Shared backend disabled and plugin-owned startup state cleaned.
)
pause
exit /b %EXIT_CODE%
