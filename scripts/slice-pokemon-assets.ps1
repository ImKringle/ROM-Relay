# One-off slicer for assets/PokemonTools sprite sheets into individual PNGs
# consumed by internal/web/ui/dist. Not part of the npm build; rerun by hand
# if the source sheets change.
Add-Type -AssemblyName System.Drawing

function Crop-Save($src, $x, $y, $w, $h, $outPath) {
    $bmp = New-Object System.Drawing.Bitmap $w, $h
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.DrawImage($src, (New-Object System.Drawing.Rectangle 0, 0, $w, $h), (New-Object System.Drawing.Rectangle $x, $y, $w, $h), [System.Drawing.GraphicsUnit]::Pixel)
    $g.Dispose()
    New-Item -ItemType Directory -Force -Path (Split-Path $outPath) | Out-Null
    $bmp.Save($outPath, [System.Drawing.Imaging.ImageFormat]::Png)
    $bmp.Dispose()
}

$root = "C:\Users\dylan\Desktop\ROM-Relay"

# --- Type badges: 64x532 sheet, 19 rows @ 28px, mainline type-chart order ---
$typesSheet = [System.Drawing.Image]::FromFile("$root\assets\PokemonTools\pkmn-types.png")
$typeOrder = @(
    "normal","fighting","flying","poison","ground","rock","bug","ghost","steel",
    "unknown",
    "fire","water","grass","electric","psychic","ice","dragon","dark","fairy"
)
$rowH = 28
for ($i = 0; $i -lt $typeOrder.Length; $i++) {
    $name = $typeOrder[$i]
    if ($name -eq "unknown") { continue } # the old Gen2-5 ??? placeholder type; nothing in this catalog uses it
    Crop-Save $typesSheet 0 ($i * $rowH) 64 $rowH "$root\internal\web\ui\dist\typebadges\$name.png"
}
$typesSheet.Dispose()

# --- Gender icons: 64x64 sheet, 2x2 grid of 32x32 cells ---
# row0: male idle, male active(highlighted) | row1: female idle, female active(highlighted)
$genderSheet = [System.Drawing.Image]::FromFile("$root\assets\PokemonTools\pkmn-genders.png")
Crop-Save $genderSheet 0  0  32 32 "$root\internal\web\ui\dist\gender\male-idle.png"
Crop-Save $genderSheet 32 0  32 32 "$root\internal\web\ui\dist\gender\male.png"
Crop-Save $genderSheet 0  32 32 32 "$root\internal\web\ui\dist\gender\female-idle.png"
Crop-Save $genderSheet 32 32 32 32 "$root\internal\web\ui\dist\gender\female.png"
$genderSheet.Dispose()

Write-Output "Sliced $($typeOrder.Length - 1) type badges and 4 gender icons."
