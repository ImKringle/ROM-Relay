@echo off
setlocal
cd /d "%~dp0"

where go.exe >nul 2>&1
if errorlevel 1 (
  echo Go was not found on PATH. Install Go before building a release.
  exit /b 1
)

set "VERSION=%~1"
if "%VERSION%"=="" set "VERSION=local"
set "OUTPUT=%~dp0dist\ROMRelay-%VERSION%-windows-amd64.zip"

echo Building ROMRelay.exe...
go build -trimpath -ldflags="-s -w" -o "%~dp0dist\ROMRelay.exe" .\cmd\romrelay
if errorlevel 1 exit /b %errorlevel%

powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0Build-Release.ps1" -Executable "%~dp0dist\ROMRelay.exe" -Output "%OUTPUT%"
if errorlevel 1 exit /b %errorlevel%

echo.
echo Release ready: %OUTPUT%
exit /b 0
