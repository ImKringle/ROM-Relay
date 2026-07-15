param(
    [Parameter(Mandatory = $true)][string]$Executable,
    [Parameter(Mandatory = $true)][string]$Output
)

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.IO.Compression.FileSystem
$root = $PSScriptRoot
$stageRoot = Join-Path $root '.release-staging'
$stage = Join-Path $stageRoot 'ROMRelay'

if (Test-Path -LiteralPath $stageRoot) {
    $resolvedRoot = [IO.Path]::GetFullPath($root).TrimEnd('\')
    $resolvedStage = [IO.Path]::GetFullPath($stageRoot).TrimEnd('\')
    if (-not $resolvedStage.StartsWith($resolvedRoot + '\', [StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to clear staging outside the repository: $resolvedStage"
    }
    Remove-Item -Recurse -Force -LiteralPath $stageRoot
}

New-Item -ItemType Directory -Path $stage | Out-Null
$files = @(
    @{ Source = $Executable; Destination = 'ROMRelay.exe' },
    @{ Source = (Join-Path $root 'config.example.json'); Destination = 'config.example.json' },
    @{ Source = (Join-Path $root 'README.md'); Destination = 'README.md' },
    @{ Source = (Join-Path $root 'docs\CONFIGURATION.md'); Destination = 'CONFIGURATION.md' },
    @{ Source = (Join-Path $root 'docs\SETUP-GUIDE.md'); Destination = 'SETUP-GUIDE.md' },
    @{ Source = (Join-Path $root 'Setup-ROMRelay.bat'); Destination = 'Setup-ROMRelay.bat' },
    @{ Source = (Join-Path $root 'Setup-ROMRelay.ps1'); Destination = 'Setup-ROMRelay.ps1' },
    @{ Source = (Join-Path $root 'Enable-ROMRelayFirewall.ps1'); Destination = 'Enable-ROMRelayFirewall.ps1' }
)
foreach ($file in $files) {
    if (-not (Test-Path -LiteralPath $file.Source -PathType Leaf)) {
        throw "Required release file is missing: $($file.Source)"
    }
    $destination = Join-Path $stage $file.Destination
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $destination) | Out-Null
    Copy-Item -LiteralPath $file.Source -Destination $destination
}

$platforms = @('gb', 'gbc', 'gba', 'nds', 'nes', 'snes', 'n64', 'genesis')
$directories = @('patches', 'metadata\libretro\databases', 'metadata\libretro\thumbnails')
foreach ($platform in $platforms) {
    $directories += "platforms\$platform"
    $directories += "platforms\$platform\patches"
    $directories += "patched\$platform"
}
$directories += @(
    'platforms\3ds\3ds', 'platforms\3ds\cia\encrypted',
    'platforms\3ds\cia\unencrypted', 'platforms\3ds\patches', 'patched\3ds',
    'internal\drivers\boot9-3ds', 'internal\drivers\cia-3ds-decryptor'
)
foreach ($directory in $directories) {
    New-Item -ItemType Directory -Force -Path (Join-Path $stage $directory) | Out-Null
}

New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Output) | Out-Null
if (Test-Path -LiteralPath $Output) { Remove-Item -Force -LiteralPath $Output }
Compress-Archive -Path $stage -DestinationPath $Output -CompressionLevel Optimal

# Only the documented allowlist may be a non-empty file in the ZIP.
$archive = [IO.Compression.ZipFile]::OpenRead($Output)
try {
    $allowedFiles = @(
        'ROMRelay/ROMRelay.exe', 'ROMRelay/config.example.json', 'ROMRelay/README.md',
        'ROMRelay/CONFIGURATION.md', 'ROMRelay/SETUP-GUIDE.md', 'ROMRelay/Setup-ROMRelay.bat',
        'ROMRelay/Setup-ROMRelay.ps1', 'ROMRelay/Enable-ROMRelayFirewall.ps1'
    )
    $unexpected = @($archive.Entries | Where-Object {
        $_.Name -ne '' -and ($_.FullName.Replace('\', '/') -notin $allowedFiles)
    })
    if ($unexpected.Count -gt 0) {
        throw "Release contains unexpected data files: $($unexpected.FullName -join ', ')"
    }
} finally {
    $archive.Dispose()
}

Remove-Item -Recurse -Force -LiteralPath $stageRoot
