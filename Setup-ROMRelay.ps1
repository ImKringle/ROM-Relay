$ErrorActionPreference = 'Stop'

function Read-YesNo([string]$Prompt, [bool]$DefaultYes = $true) {
    $suffix = if ($DefaultYes) { '[Y/n]' } else { '[y/N]' }
    while ($true) {
        $answer = (Read-Host "$Prompt $suffix").Trim().ToLowerInvariant()
        if ($answer -eq '') { return $DefaultYes }
        if ($answer -in @('y', 'yes')) { return $true }
        if ($answer -in @('n', 'no')) { return $false }
        Write-Host 'Please answer Y or N.' -ForegroundColor Yellow
    }
}

function Test-PrivateIPv4([string]$Address) {
    $parsed = $null
    if (-not [Net.IPAddress]::TryParse($Address, [ref]$parsed)) { return $false }
    $bytes = $parsed.GetAddressBytes()
    return $bytes.Length -eq 4 -and (
        $bytes[0] -eq 10 -or
        ($bytes[0] -eq 172 -and $bytes[1] -ge 16 -and $bytes[1] -le 31) -or
        ($bytes[0] -eq 192 -and $bytes[1] -eq 168)
    )
}

Write-Host ''
Write-Host 'ROM Relay LAN Setup' -ForegroundColor Cyan
Write-Host 'Let us find the useful network adapter and avoid advertising WSL to your phone.'
Write-Host ''

$root = $PSScriptRoot
$exe = Join-Path $root 'ROMRelay.exe'
$configPath = Join-Path $root 'config.json'
$examplePath = Join-Path $root 'config.example.json'

if (-not (Test-Path -LiteralPath $exe)) {
    throw 'ROMRelay.exe must be beside Setup-ROMRelay.bat.'
}
if (-not (Test-Path -LiteralPath $configPath)) {
    if (-not (Test-Path -LiteralPath $examplePath)) {
        throw 'Neither config.json nor config.example.json was found.'
    }
    Copy-Item -LiteralPath $examplePath -Destination $configPath
    Write-Host 'Created config.json from config.example.json.' -ForegroundColor Green
}

$config = Get-Content -Raw -LiteralPath $configPath | ConvertFrom-Json
$profiles = @(Get-NetConnectionProfile -ErrorAction SilentlyContinue)
$candidates = @(
    Get-NetIPConfiguration |
        Where-Object {
            $_.NetAdapter.Status -eq 'Up' -and
            $_.IPv4DefaultGateway -and
            $_.IPv4Address -and
            (Test-PrivateIPv4 $_.IPv4Address.IPAddress)
        } |
        ForEach-Object {
            $profile = $profiles | Where-Object InterfaceAlias -eq $_.InterfaceAlias | Select-Object -First 1
            [pscustomobject]@{
                Alias = $_.InterfaceAlias
                Description = $_.InterfaceDescription
                IP = $_.IPv4Address.IPAddress
                Gateway = $_.IPv4DefaultGateway.NextHop
                Category = if ($profile) { [string]$profile.NetworkCategory } else { 'Unknown' }
                PrivateScore = if ($profile.NetworkCategory -eq 'Private') { 0 } else { 1 }
            }
        } |
        Sort-Object PrivateScore, Alias
)

if ($candidates.Count -eq 0) {
    Write-Host 'No private IPv4 adapter with a default gateway was detected.' -ForegroundColor Yellow
    $ip = Read-Host 'Enter the LAN IPv4 address this PC uses'
    if (-not (Test-PrivateIPv4 $ip)) { throw 'That is not a private IPv4 address.' }
    $adapter = $null
} else {
    $adapter = $candidates[0]
    Write-Host "Detected adapter : $($adapter.Alias)"
    Write-Host "Description      : $($adapter.Description)"
    Write-Host "Internal IP      : $($adapter.IP)"
    Write-Host "Gateway          : $($adapter.Gateway)"
    Write-Host "Windows profile  : $($adapter.Category)"
    $ip = $adapter.IP

    if (-not (Read-YesNo 'Is this the LAN connection your phone/handheld uses?')) {
        Write-Host ''
        for ($i = 0; $i -lt $candidates.Count; $i++) {
            Write-Host "[$($i + 1)] $($candidates[$i].Alias) - $($candidates[$i].IP) - gateway $($candidates[$i].Gateway) - $($candidates[$i].Category)"
        }
        $choice = Read-Host 'Choose an adapter number, or type M for a manual IP'
        if ($choice -match '^\d+$' -and [int]$choice -ge 1 -and [int]$choice -le $candidates.Count) {
            $adapter = $candidates[[int]$choice - 1]
            $ip = $adapter.IP
        } else {
            $ip = Read-Host 'Enter the LAN IPv4 address this PC uses'
            if (-not (Test-PrivateIPv4 $ip)) { throw 'That is not a private IPv4 address.' }
            $adapter = $null
        }
    }
}

$defaultPort = 8080
if ($config.server.port -as [int]) { $defaultPort = [int]$config.server.port }
$portText = Read-Host "HTTP port [$defaultPort]"
$port = if ($portText.Trim() -eq '') { $defaultPort } else { [int]$portText }
if ($port -lt 1 -or $port -gt 65535) { throw 'Port must be between 1 and 65535.' }

$listeners = @(Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue)
if ($listeners.Count -gt 0) {
    $owners = ($listeners.OwningProcess | Sort-Object -Unique) -join ', '
    Write-Host "Port $port is currently in use by process ID(s): $owners" -ForegroundColor Yellow
    if (-not (Read-YesNo 'Continue with this port anyway?' $false)) { throw 'Choose another port and run setup again.' }
}

$url = "http://${ip}:$port/"
Write-Host ''
Write-Host 'Proposed ROM Relay setup' -ForegroundColor Cyan
Write-Host "Listen address : 0.0.0.0 (all local adapters)"
Write-Host "Advertise IP   : $ip"
Write-Host "Port           : $port"
Write-Host "Catalog URL    : $url"
Write-Host 'Firewall       : Allow this EXE on TCP port above, Private profile only'
Write-Host ''
if (-not (Read-YesNo 'Write this configuration and firewall rule?')) {
    Write-Host 'Nothing changed.'
    exit 0
}

$backup = "$configPath.backup-$(Get-Date -Format 'yyyyMMdd-HHmmss')"
Copy-Item -LiteralPath $configPath -Destination $backup

$config.server.listen_address = '0.0.0.0'
$config.server.port = $port
$config.server.advertise_ip = $ip
$config.server.advertise_port = $port
$config.server.public_base_url = ''
$json = $config | ConvertTo-Json -Depth 20
$temp = "$configPath.tmp"
[IO.File]::WriteAllText($temp, $json + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
Move-Item -LiteralPath $temp -Destination $configPath -Force

$ruleName = 'ROM Relay (Private LAN)'
Get-NetFirewallRule -DisplayName $ruleName -ErrorAction SilentlyContinue | Remove-NetFirewallRule
New-NetFirewallRule `
    -DisplayName $ruleName `
    -Description "Allow ROM Relay on $url from private networks only." `
    -Direction Inbound `
    -Action Allow `
    -Profile Private `
    -Program $exe `
    -Protocol TCP `
    -LocalPort $port | Out-Null

Write-Host ''
Write-Host 'Setup complete.' -ForegroundColor Green
Write-Host "Catalog: $url"
Write-Host "Backup:  $backup"
if ($adapter -and $adapter.Category -ne 'Private') {
    Write-Host "Warning: Windows currently marks $($adapter.Alias) as $($adapter.Category). The Private-only firewall rule will not apply until that network profile is Private." -ForegroundColor Yellow
}
Write-Host 'Tip: reserve this IP in your Spectrum router so QR URLs stay put.' -ForegroundColor Yellow

if (Read-YesNo 'Start ROM Relay now?') {
    Start-Process -FilePath $exe -WorkingDirectory $root
    Write-Host 'ROM Relay started. Leave its window open while downloading.' -ForegroundColor Green
}
