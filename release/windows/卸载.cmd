@echo off
setlocal
chcp 65001 >nul
title Codex Auto Retry Uninstaller
echo.
echo Codex Auto Retry - one-click uninstaller
echo Existing retry settings and state will be preserved.
echo.
powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0uninstall-release.ps1"
set "EXIT_CODE=%ERRORLEVEL%"
echo.
if not "%EXIT_CODE%"=="0" (
  echo Uninstall failed. Review the error above.
) else (
  echo Uninstall succeeded.
)
echo.
pause
exit /b %EXIT_CODE%
