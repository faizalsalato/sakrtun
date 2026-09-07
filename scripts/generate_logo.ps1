# Generates the SAKR TUN logo:
#   internal/nativeui/assets/logo.png  (embedded in the app window)
#   cmd/socksrevivepc/logo.ico         (embedded in the EXE via the .rc file)
Add-Type -AssemblyName System.Drawing

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot

function New-LogoBitmap([int]$size) {
    $bmp = New-Object System.Drawing.Bitmap($size, $size)
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
    $g.TextRenderingHint = [System.Drawing.Text.TextRenderingHint]::AntiAliasGridFit
    $g.Clear([System.Drawing.Color]::Transparent)

    # Rounded dark background.
    $radius = $size * 0.21
    $rect = New-Object System.Drawing.RectangleF(0, 0, $size, $size)
    $path = New-Object System.Drawing.Drawing2D.GraphicsPath
    $d = $radius * 2
    $path.AddArc($rect.X, $rect.Y, $d, $d, 180, 90)
    $path.AddArc($rect.Right - $d, $rect.Y, $d, $d, 270, 90)
    $path.AddArc($rect.Right - $d, $rect.Bottom - $d, $d, $d, 0, 90)
    $path.AddArc($rect.X, $rect.Bottom - $d, $d, $d, 90, 90)
    $path.CloseFigure()

    $grad = New-Object System.Drawing.Drawing2D.LinearGradientBrush(
        $rect,
        [System.Drawing.Color]::FromArgb(255, 12, 24, 44),
        [System.Drawing.Color]::FromArgb(255, 20, 38, 70), 90)
    $g.FillPath($grad, $path)

    # Tunnel rings (top half): three concentric ellipses, like a tunnel mouth.
    $cyan = [System.Drawing.Color]::FromArgb(255, 0, 229, 255)
    $w1 = [Math]::Max(2.0, $size * 0.045)
    $pen1 = New-Object System.Drawing.Pen([System.Drawing.Color]::FromArgb(80, 0, 229, 255), $w1)
    $pen2 = New-Object System.Drawing.Pen([System.Drawing.Color]::FromArgb(150, 0, 229, 255), $w1)
    $pen3 = New-Object System.Drawing.Pen($cyan, $w1)
    $cy = $size * 0.30
    $g.DrawEllipse($pen1, $size * 0.15, $cy - $size * 0.115, $size * 0.70, $size * 0.23)
    $g.DrawEllipse($pen2, $size * 0.235, $cy - $size * 0.07, $size * 0.53, $size * 0.14)
    $g.DrawEllipse($pen3, $size * 0.32, $cy - $size * 0.032, $size * 0.36, $size * 0.065)

    # Wordmark "SAKR TUN": SAKR in white, TUN in cyan.
    $font = New-Object System.Drawing.Font('Arial', [Math]::Max(8, $size * 0.16),
        [System.Drawing.FontStyle]::Bold, [System.Drawing.GraphicsUnit]::Pixel)
    $sakrW = $g.MeasureString('SAKR ', $font).Width
    $tunW = $g.MeasureString('TUN', $font).Width
    $total = $sakrW + $tunW
    $x0 = ($size - $total) / 2
    $y = $size * 0.60
    $cyanBrush = New-Object System.Drawing.SolidBrush($cyan)
    $g.DrawString('SAKR ', $font, [System.Drawing.Brushes]::White, $x0, $y)
    $g.DrawString('TUN', $font, $cyanBrush, $x0 + $sakrW, $y)

    $g.Dispose()
    return $bmp
}

# --- PNG for the app window icon ---
$assetsDir = Join-Path $root "internal\nativeui\assets"
New-Item -ItemType Directory -Force -Path $assetsDir | Out-Null
$pngPath = Join-Path $assetsDir "logo.png"
$bmp = New-LogoBitmap 256
$bmp.Save($pngPath, [System.Drawing.Imaging.ImageFormat]::Png)
$bmp.Dispose()
Write-Host "logo written: $pngPath"

# --- ICO for the EXE (PNG-compressed entries: 256/64/48/32/16) ---
$sizes = @(256, 64, 48, 32, 16)
$pngs = @()
foreach ($s in $sizes) {
    $b = New-LogoBitmap $s
    $ms = New-Object System.IO.MemoryStream
    $b.Save($ms, [System.Drawing.Imaging.ImageFormat]::Png)
    $pngs += , $ms.ToArray()
    $b.Dispose()
    $ms.Dispose()
}

$out = New-Object System.IO.MemoryStream
$bw = New-Object System.IO.BinaryWriter($out)
$bw.Write([uint16]0)     # reserved
$bw.Write([uint16]1)     # type: icon
$bw.Write([uint16]$sizes.Count)
$offset = 6 + 16 * $sizes.Count
for ($i = 0; $i -lt $sizes.Count; $i++) {
    $s = $sizes[$i]
    $data = $pngs[$i]
    $bw.Write([byte]($(if ($s -ge 256) { 0 } else { $s })))  # width
    $bw.Write([byte]($(if ($s -ge 256) { 0 } else { $s })))  # height
    $bw.Write([byte]0)    # palette
    $bw.Write([byte]0)    # reserved
    $bw.Write([uint16]1)  # planes
    $bw.Write([uint16]32) # bpp
    $bw.Write([uint32]$data.Length)
    $bw.Write([uint32]$offset)
    $offset += $data.Length
}
foreach ($data in $pngs) {
    $bw.Write([byte[]]$data)
}
$bw.Flush()
$icoPath = Join-Path $root "cmd\socksrevivepc\logo.ico"
[System.IO.File]::WriteAllBytes($icoPath, $out.ToArray())
$out.Dispose()
Write-Host "ico written: $icoPath"
