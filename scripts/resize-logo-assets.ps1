# One-off resizer: produces the header brand icon and browser favicons from
# assets/core. Not part of the npm build; rerun by hand if the source art changes.
Add-Type -AssemblyName System.Drawing

function Resize-Save($src, $size, $outPath) {
    $bmp = New-Object System.Drawing.Bitmap $size, $size
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
    $g.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::HighQuality
    $g.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
    $g.DrawImage($src, 0, 0, $size, $size)
    $g.Dispose()
    New-Item -ItemType Directory -Force -Path (Split-Path $outPath) | Out-Null
    $bmp.Save($outPath, [System.Drawing.Imaging.ImageFormat]::Png)
    $bmp.Dispose()
}

# Like Resize-Save, but for non-square source art: scales to fit inside the
# square canvas instead of stretching, centered on a transparent background.
function Resize-Contain($src, $size, $outPath) {
    $bmp = New-Object System.Drawing.Bitmap $size, $size
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
    $g.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::HighQuality
    $ratio = [Math]::Min($size / $src.Width, $size / $src.Height)
    $w = [int]($src.Width * $ratio)
    $h = [int]($src.Height * $ratio)
    $g.DrawImage($src, [int](($size - $w) / 2), [int](($size - $h) / 2), $w, $h)
    $g.Dispose()
    New-Item -ItemType Directory -Force -Path (Split-Path $outPath) | Out-Null
    $bmp.Save($outPath, [System.Drawing.Imaging.ImageFormat]::Png)
    $bmp.Dispose()
}

$root = "C:\Users\dylan\Desktop\ROM-Relay"
$transparent = [System.Drawing.Image]::FromFile("$root\assets\core\transparent-logo.png")
$filled = [System.Drawing.Image]::FromFile("$root\assets\core\filled-background-logo.png")

# Header brand emblem: transparent bg so it sits on the existing emblem tile.
Resize-Save $transparent 96  "$root\internal\web\ui\dist\logo\brand.png"

# Favicons: filled bg reads better than transparent at tiny sizes / on light
# browser chrome. PNG favicons are supported everywhere modern; no .ico needed.
Resize-Save $filled 32  "$root\internal\web\ui\dist\logo\favicon-32.png"
Resize-Save $filled 180 "$root\internal\web\ui\dist\logo\favicon-180.png"

$transparent.Dispose()
$filled.Dispose()

# Shiny sparkle badge (replaces the ✨ emoji on dex/box tiles). Source art is
# not square, so it's scaled to fit rather than stretched.
$shiny = [System.Drawing.Image]::FromFile("$root\assets\PokemonTools\shiny-icon.png")
Resize-Contain $shiny 64 "$root\internal\web\ui\dist\shiny\shiny.png"
$shiny.Dispose()

Write-Output "Logo assets resized."
