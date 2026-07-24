@echo off
rem SeDoc test-server installer (Windows). Thin wrapper around
rem scripts\install\install.ps1 - see that file or docs/deploy/windows-test-server.md.
rem Options pass through, e.g.:  install.bat -Prebuilt
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\install\install.ps1" %*
exit /b %ERRORLEVEL%
