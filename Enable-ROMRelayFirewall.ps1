#Requires -RunAsAdministrator
$ErrorActionPreference = 'Stop'
$name = 'ROM Relay (Private LAN)'
$program = Join-Path $PSScriptRoot 'ROMRelay.exe'

if (-not (Test-Path -LiteralPath $program)) {
    throw "ROMRelay.exe was not found beside this script."
}

$existing = Get-NetFirewallRule -DisplayName $name -ErrorAction SilentlyContinue
if ($existing) {
    $existing | Remove-NetFirewallRule
}

New-NetFirewallRule `
    -DisplayName $name `
    -Description 'Allow ROM Relay HTTP access from private LAN networks only.' `
    -Direction Inbound `
    -Action Allow `
    -Profile Private `
    -Program $program `
    -Protocol TCP `
    -LocalPort 8080 | Out-Null

Write-Host 'ROM Relay is allowed on Private networks at TCP port 8080.' -ForegroundColor Green
