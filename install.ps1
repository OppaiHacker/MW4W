# MW4W - installer.
#   powershell -ExecutionPolicy Bypass -File install.ps1 [-NoStart] [-KeepWallpaper]
param([switch]$NoStart, [switch]$KeepWallpaper)

$ErrorActionPreference = 'Stop'
$Repo = $PSScriptRoot
$Glzr = Join-Path $env:USERPROFILE '.glzr'
$Pack = Join-Path $Glzr 'zebar\mw4w'
$GlazeExe = Join-Path $env:ProgramFiles 'glzr.io\GlazeWM\glazewm.exe'
$ZebarExe = Join-Path $env:ProgramFiles 'glzr.io\Zebar\zebar.exe'
$Walls = Join-Path $env:USERPROFILE 'Pictures\Wallpapers'
$AlacrittyExe = Join-Path $env:ProgramFiles 'Alacritty\alacritty.exe'
function Step($m) { Write-Host "==> $m" -ForegroundColor Cyan }
function Write-Utf8($path, $text) { [IO.File]::WriteAllText($path, $text, (New-Object Text.UTF8Encoding $false)) }

if ([Environment]::OSVersion.Version.Build -lt 22000) { Write-Warning 'Windows 10 detected: window borders and corners need Windows 11.' }

Step 'Dependencies (GlazeWM, Zebar)'
foreach ($d in @(@{ id = 'glzr-io.glazewm'; exe = $GlazeExe }, @{ id = 'glzr-io.zebar'; exe = $ZebarExe })) {
  if (Test-Path $d.exe) { Write-Host "    $($d.id) ok" }
  else { winget install --id $d.id -e --silent --accept-package-agreements --accept-source-agreements; if (-not (Test-Path $d.exe)) { throw "Failed to install $($d.id)" } }
}

Step 'Backup of existing configs'
$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
foreach ($f in "$Glzr\glazewm\config.yaml", "$Glzr\zebar\settings.json") {
  if ((Test-Path $f) -and -not (Select-String -Path $f -Pattern 'MW4W|"mw4w"' -Quiet)) { Copy-Item $f "$f.bak-$stamp"; Write-Host "    $f -> .bak-$stamp" }
}

Step "Installing widget pack -> $Pack"
New-Item -ItemType Directory -Force $Pack | Out-Null
# settings.json (user) and cache/ survive reinstalls.
robocopy (Join-Path $Repo 'pack') $Pack /E /NFL /NDL /NJH /NJS /NP /XF settings.json /XD cache | Out-Null
if ($LASTEXITCODE -ge 8) { throw "robocopy failed ($LASTEXITCODE)" }

$settingsPath = Join-Path $Pack 'settings.json'
if (-not (Test-Path $settingsPath)) {
  Step 'Detecting default apps'
  $browser = 'msedge'
  $prog = (Get-ItemProperty 'HKCU:\Software\Microsoft\Windows\Shell\Associations\UrlAssociations\https\UserChoice' -ErrorAction SilentlyContinue).ProgId
  if ($prog) {
    $cmd = (Get-ItemProperty "Registry::HKEY_CLASSES_ROOT\$prog\shell\open\command" -ErrorAction SilentlyContinue).'(default)'
    if ($cmd -match '^"([^"]+\.exe)"' -or $cmd -match '^(\S+\.exe)') { $browser = $Matches[1] }
  }
  $terminal = if (Test-Path $AlacrittyExe) { $AlacrittyExe } elseif (Get-Command wt -ErrorAction SilentlyContinue) { 'wt' } else { 'powershell' }
  Write-Host "    terminal=$terminal browser=$browser"
  Write-Utf8 $settingsPath (@{ apps = @{ terminal = $terminal; browser = $browser; files = 'explorer'; calculator = 'calc' } } | ConvertTo-Json)
}

Step "Wallpapers -> $Walls"
New-Item -ItemType Directory -Force $Walls | Out-Null
Get-ChildItem (Join-Path $Repo 'wallpapers') | Where-Object { -not (Test-Path (Join-Path $Walls $_.Name)) } | Copy-Item -Destination $Walls
# Keep the user's current wallpaper selectable in the picker.
$cur = (Get-ItemProperty 'HKCU:\Control Panel\Desktop').WallPaper
if ($cur -and (Test-Path $cur) -and -not $cur.StartsWith($Walls, 'OrdinalIgnoreCase')) {
  $dst = Join-Path $Walls ('previous-' + (Split-Path $cur -Leaf))
  if (-not (Test-Path $dst)) { Copy-Item $cur $dst }
}

Step 'Zebar: start the MW4W bar on launch'
$zs = Join-Path $Glzr 'zebar\settings.json'
# Popups start hidden ("preload") so they open instantly later.
$popups = foreach ($w in 'launcher', 'power', 'wallpaper', 'keybinds', 'settings', 'island') { @{ pack = 'mw4w'; widget = $w; preset = 'preload' } }
Write-Utf8 $zs (@{
    '$schema'      = 'https://github.com/glzr-io/zebar/raw/v3.3.1/resources/settings-schema.json'
    startupConfigs = @(@{ pack = 'mw4w'; widget = 'bar'; preset = 'default' }, @{ pack = 'mw4w'; widget = 'dock'; preset = 'default' }) + $popups
  } | ConvertTo-Json -Depth 4)

$alaCfg = Join-Path $env:APPDATA 'alacritty\alacritty.toml'
if ((Test-Path $AlacrittyExe) -and -not (Test-Path $alaCfg)) {
  Step "Alacritty config -> $alaCfg"
  New-Item -ItemType Directory -Force (Split-Path $alaCfg) | Out-Null
  $t = Get-Content (Join-Path $Repo 'pack\alacritty.toml') -Raw
  if (Get-Command pwsh -ErrorAction SilentlyContinue) { $t += "`n[terminal.shell]`nprogram = `"pwsh`"`n" }
  Write-Utf8 $alaCfg $t
}

# GlazeWM cloaks other workspaces like virtual desktops, so Firefox/Zen open an
# external link in a new window (Zen: plus a tab in the old one). Keep one window.
$pref = 'user_pref("widget.prefer_windows_on_current_virtual_desktop", false);'
foreach ($p in Get-ChildItem "$env:APPDATA\zen\Profiles", "$env:APPDATA\Mozilla\Firefox\Profiles" -Directory -ErrorAction SilentlyContinue) {
  $uj = Join-Path $p.FullName 'user.js'
  if (-not ((Test-Path $uj) -and (Select-String -Path $uj -Pattern 'prefer_windows_on_current_virtual_desktop' -Quiet))) { Add-Content $uj $pref }
}

Step 'Theme + GlazeWM config'
$mw4w = Join-Path $Pack 'scripts\mw4w.ps1'
$state = Join-Path $Pack 'cache\state.json'
$hasWallpaper = (Test-Path $state) -and (Get-Content $state -Raw | ConvertFrom-Json).wallpaper
if ($KeepWallpaper -or $hasWallpaper) { & powershell -NoProfile -ExecutionPolicy Bypass -File $mw4w colors }
else { & powershell -NoProfile -ExecutionPolicy Bypass -File $mw4w wallpaper (Join-Path $Walls 'mw4w-aurora.jpg') }
if ($LASTEXITCODE) { throw 'mw4w.ps1 failed, see cache\error.log' }
& powershell -NoProfile -ExecutionPolicy Bypass -File $mw4w thumbs

Step 'Autostart (GlazeWM starts Zebar + helpers)'
Set-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' 'MW4W-GlazeWM' "`"$GlazeExe`""

if (-not $NoStart) {
  Step 'Starting'
  if (Get-Process glazewm -ErrorAction SilentlyContinue) {
    & powershell -NoProfile -ExecutionPolicy Bypass -File $mw4w bar-restart
    & (Join-Path $env:ProgramFiles 'glzr.io\GlazeWM\cli\glazewm.exe') command wm-reload-config | Out-Null
  } else {
    Get-Process zebar -ErrorAction SilentlyContinue | Stop-Process -Force
    Start-Process $GlazeExe
  }
}

Write-Host ''
Write-Host 'MW4W installed.' -ForegroundColor Green
Write-Host '  SUPER+CTRL+K  keybindings      SUPER+CTRL+Enter  app launcher'
Write-Host '  SUPER+CTRL+S  settings         SUPER+CTRL+W      wallpapers'
Write-Host '  SUPER+Enter   terminal         SUPER+CTRL+Q      power menu'
