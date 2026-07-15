@echo off
setlocal
title ROM Relay LAN Setup

if /I "%~1"=="elevated" goto elevated

net session >nul 2>&1
if %errorlevel%==0 goto elevated

echo ROM Relay needs Administrator permission to configure Windows Firewall.
echo A Windows UAC prompt will open now.
powershell.exe -NoProfile -Command "Start-Process -FilePath '%ComSpec%' -Verb RunAs -ArgumentList '/d /c ""%~f0" elevated"'"
exit /b

:elevated
cd /d "%~dp0"
powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0Setup-ROMRelay.ps1"
set "code=%errorlevel%"
echo.
if not "%code%"=="0" echo Setup stopped with exit code %code%.
pause
exit /b %code%
