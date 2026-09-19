Add-Type -AssemblyName System.Drawing
$ErrorActionPreference = 'Stop'
$assets = Join-Path $PSScriptRoot '.'
$out = $PSScriptRoot
New-Item -ItemType Directory -Force $out | Out-Null

$BG_TOP = [System.Drawing.Color]::FromArgb(10, 13, 20)
$BG_BOT = [System.Drawing.Color]::FromArgb(4, 5, 9)
$ORANGE = [System.Drawing.Color]::FromArgb(255, 138, 61)
$GREEN  = [System.Drawing.Color]::FromArgb(57, 255, 20)

function Lerp($a, $b, $t) { [int]($a + ($b - $a) * $t) }
function GradColor($t) { [System.Drawing.Color]::FromArgb((Lerp $BG_TOP.R $BG_BOT.R $t), (Lerp $BG_TOP.G $BG_BOT.G $t), (Lerp $BG_TOP.B $BG_BOT.B $t)) }

function NewGraphics($bmp) {
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.SmoothingMode = 'HighQuality'; $g.InterpolationMode = 'HighQualityBicubic'
    $g.PixelOffsetMode = 'HighQuality'; $g.TextRenderingHint = 'AntiAlias'
    $g
}
function FillGradient($g, $w, $h) {
    $rect = New-Object System.Drawing.Rectangle 0, 0, $w, $h
    $br = New-Object System.Drawing.Drawing2D.LinearGradientBrush $rect, $BG_TOP, $BG_BOT, 90
    $g.FillRectangle($br, $rect); $br.Dispose()
}
# Supersampled text: render at $scale, downscale into $dest at (x,y) centered horizontally.
function DrawMonoText($g, $text, $sizeFinal, $color, $centerX, $topY, $scale, $bold = $true, $letterSpacing = 0) {
    $style = if ($bold) { [System.Drawing.FontStyle]::Bold } else { [System.Drawing.FontStyle]::Regular }
    $font = New-Object System.Drawing.Font 'Consolas', ($sizeFinal * $scale), $style, ([System.Drawing.GraphicsUnit]::Pixel)
    $tmpG = [System.Drawing.Graphics]::FromImage((New-Object System.Drawing.Bitmap 1, 1))
    $tmpG.TextRenderingHint = 'AntiAlias'
    $fmt = [System.Drawing.StringFormat]::GenericTypographic
    if ($letterSpacing -gt 0) {
        $w = 0; $chars = @()
        foreach ($c in $text.ToCharArray()) { $cw = $tmpG.MeasureString([string]$c, $font, 10000, $fmt).Width; $chars += ,@($c, $cw); $w += $cw + $letterSpacing * $scale }
        $w -= $letterSpacing * $scale
        $h = $tmpG.MeasureString($text, $font, 10000, $fmt).Height
    } else {
        $sz = $tmpG.MeasureString($text, $font, 10000, $fmt); $w = $sz.Width; $h = $sz.Height
    }
    $bw = [int][math]::Ceiling($w) + 2 * $scale; $bh = [int][math]::Ceiling($h) + 2 * $scale
    $big = New-Object System.Drawing.Bitmap $bw, $bh, ([System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
    $bg = NewGraphics $big
    $brush = New-Object System.Drawing.SolidBrush $color
    if ($letterSpacing -gt 0) {
        $x = [float]$scale
        foreach ($cc in $chars) { $bg.DrawString([string]$cc[0], $font, $brush, $x, [float]$scale, $fmt); $x += $cc[1] + $letterSpacing * $scale }
    } else {
        $bg.DrawString($text, $font, $brush, [float]$scale, [float]$scale, $fmt)
    }
    $bg.Dispose()
    $fw = $bw / $scale; $fh = $bh / $scale
    $dest = New-Object System.Drawing.RectangleF ($centerX - $fw / 2), $topY, $fw, $fh
    $g.DrawImage($big, $dest)
    $big.Dispose(); $brush.Dispose(); $font.Dispose(); $tmpG.Dispose()
    return $fw
}

# ---------- Sidebar 234x392: keep the composited mascot, repaint the wordmark ----------
$src = [System.Drawing.Bitmap]::FromFile("$assets\wix-sidebar.png")
$side = New-Object System.Drawing.Bitmap 234, 392, ([System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
$g = NewGraphics $side
FillGradient $g 234 392
# mascot band from the existing sidebar (rows 80..250 hold the character)
$band = New-Object System.Drawing.Rectangle 0, 80, 234, 170
$g.DrawImage($src, $band, $band, [System.Drawing.GraphicsUnit]::Pixel)
$src.Dispose()
# divider + wordmark (same geometry as make-sidebar.py: mark 95..245, line at +14 above text, text at +28)
$textTop = 245 + 28
$pen = New-Object System.Drawing.Pen ([System.Drawing.Color]::FromArgb(110, 255, 138, 61)), 1
$g.DrawLine($pen, 87, ($textTop - 14), 147, ($textTop - 14)); $pen.Dispose()
$null = DrawMonoText $g 'StonkAgents' 26 $ORANGE 117 $textTop 4
$g.Dispose()
$side.Save("$out\wix-sidebar.png", [System.Drawing.Imaging.ImageFormat]::Png); $side.Dispose()

# ---------- Dialog 493x312 ----------
$dlg = New-Object System.Drawing.Bitmap 493, 312, ([System.Drawing.Imaging.PixelFormat]::Format24bppRgb)
$g = NewGraphics $dlg
FillGradient $g 493 312
# soft green glow behind the wordmark
$path = New-Object System.Drawing.Drawing2D.GraphicsPath
$path.AddEllipse(96, 150, 300, 200)
$pgb = New-Object System.Drawing.Drawing2D.PathGradientBrush $path
$pgb.CenterColor = [System.Drawing.Color]::FromArgb(28, 57, 255, 20)
$pgb.SurroundColors = @([System.Drawing.Color]::FromArgb(0, 57, 255, 20))
$g.FillPath($pgb, $path); $pgb.Dispose(); $path.Dispose()
$null = DrawMonoText $g 'STONKAGENTS' 11 ([System.Drawing.Color]::FromArgb(190, 57, 255, 20)) 246 224 4 $true 4
$null = DrawMonoText $g 'the first p2p network for agents' 8 ([System.Drawing.Color]::FromArgb(120, 150, 165, 160)) 246 240 4 $false 0
$g.Dispose()
$dlg.Save("$out\wix-dialog.bmp", [System.Drawing.Imaging.ImageFormat]::Bmp); $dlg.Dispose()

# ---------- Banner 493x58 ----------
$ban = New-Object System.Drawing.Bitmap 493, 58, ([System.Drawing.Imaging.PixelFormat]::Format24bppRgb)
$g = NewGraphics $ban
FillGradient $g 493 58
$null = DrawMonoText $g 'STONKAGENTS' 8 ([System.Drawing.Color]::FromArgb(110, 57, 255, 20)) 246 38 4 $true 3
$g.Dispose()
$ban.Save("$out\wix-banner.bmp", [System.Drawing.Imaging.ImageFormat]::Bmp); $ban.Dispose()

Get-ChildItem $out | ForEach-Object { "{0}  {1} bytes" -f $_.Name, $_.Length }
