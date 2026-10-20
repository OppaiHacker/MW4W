# MW4W - control script (GlazeWM + Zebar).
# Usage: mw4w.ps1 <command> [arg]
#   apply | colors | startup | cache | thumbs
#   wallpaper <path|random|next|prev> | wallpaper-auto | wallpaper-loop
#   power <lock|logout|sleep|hibernate|reboot|shutdown>
#   kill-active | empty-workspace | gamemode | bar-restart | bar-toggle
#   set <base64-json> | toggle <setting> | run <base64-cmd> | launch <appid> | winicons
#   media-loop | media-seek <seconds> | media-volume <0-100> | updates | shell <on|off> | autostart
#   quick | app-audio <b64> | audio-default <b64 id> | power-mode <0-2> | brightness <0-100> | radio <wifi|bluetooth> <on|off>
#   wifi-scan | wifi-connect <b64 {ssid, pass}> | wifi-disconnect
#   displays | display-set <b64 {displays, workspaces}> | display-keep | display-undo | display-sync | display-topology <extend|clone|internal|external>
param([string]$Cmd = 'apply', [string]$Arg = '')

$ErrorActionPreference = 'Stop'
$Pack = Split-Path -Parent $PSScriptRoot
$Cache = Join-Path $Pack 'cache'
$Script = $MyInvocation.MyCommand.Path
$Glaze = Join-Path $env:ProgramFiles 'glzr.io\GlazeWM\cli\glazewm.exe'
$Zebar = Join-Path $env:ProgramFiles 'glzr.io\Zebar\zebar.exe'
New-Item -ItemType Directory -Force (Join-Path $Cache 'thumbs'), (Join-Path $Cache 'icons') | Out-Null

function Write-Utf8($path, $text) { [IO.File]::WriteAllText($path, $text, (New-Object Text.UTF8Encoding $false)) }
function Read-Json($path) { if (Test-Path $path) { Get-Content $path -Raw -Encoding UTF8 | ConvertFrom-Json } }
function Save-Json($path, $obj) { Write-Utf8 $path ($obj | ConvertTo-Json -Depth 6) }

function Get-Settings {
  $s = Read-Json (Join-Path $Pack 'defaults.json')
  $u = Read-Json (Join-Path $Pack 'settings.json')
  if ($u) {
    foreach ($p in $u.PSObject.Properties) {
      if ($p.Name -eq 'apps') { foreach ($a in $p.Value.PSObject.Properties) { $s.apps | Add-Member -Force $a.Name $a.Value } }
      else { $s | Add-Member -Force $p.Name $p.Value }
    }
  }
  $s
}

function Get-State {
  $st = Read-Json (Join-Path $Cache 'state.json')
  if (-not $st) { $st = [pscustomobject]@{} }
  foreach ($k in 'wallpaper', 'gamemode', 'barHidden', 'autoPid', 'stamp') {
    if (-not ($st.PSObject.Properties.Name -contains $k)) { $st | Add-Member $k $null }
  }
  $st
}

# Every widget polls state.json; a new stamp makes them reload (colors, settings, bar toggle).
function Save-State($st) { $st.stamp = [DateTime]::UtcNow.Ticks; Save-Json (Join-Path $Cache 'state.json') $st }

function Load-Native {
  Add-Type -AssemblyName System.Drawing
  if ('Mw4w.Native' -as [type]) { return }
  $src = Join-Path $PSScriptRoot 'Native.cs'
  $dll = Join-Path $Cache ("mw4w-native-{0}.dll" -f (Get-Item $src).LastWriteTimeUtc.Ticks)
  if (-not (Test-Path $dll)) {
    Get-ChildItem $Cache -Filter 'mw4w-native-*.dll' | Remove-Item -ErrorAction SilentlyContinue
    Add-Type -Path $src -ReferencedAssemblies System.Drawing -OutputAssembly $dll -OutputType Library
  }
  Add-Type -Path $dll
}

function Test-Glaze { [bool](Get-Process glazewm -ErrorAction SilentlyContinue) }

# GlazeWM IPC (WebSocket on :6123) takes ~15ms; its CLI takes ~450ms.
function Glaze-Ipc([string]$msg) {
  $none = [Threading.CancellationToken]::None
  $ws = New-Object Net.WebSockets.ClientWebSocket
  try {
    # timeouts: a hung GlazeWM must not hang the warm worker (and every command after it)
    if (-not $ws.ConnectAsync([Uri]'ws://127.0.0.1:6123', $none).Wait(2000)) { throw 'GlazeWM IPC: no connection' }
    $b = [Text.Encoding]::UTF8.GetBytes($msg)
    $ws.SendAsync((New-Object ArraySegment[byte] (, $b)), 'Text', $true, $none).Wait()
    $buf = New-Object byte[] 65536; $ms = New-Object IO.MemoryStream
    do {
      $t = $ws.ReceiveAsync((New-Object ArraySegment[byte] (, $buf)), $none)
      if (-not $t.Wait(5000)) { throw "GlazeWM IPC: no reply to '$msg'" }
      $ms.Write($buf, 0, $t.Result.Count)
    } until ($t.Result.EndOfMessage)
    [Text.Encoding]::UTF8.GetString($ms.ToArray()) | ConvertFrom-Json
  } finally { $ws.Dispose() }
}
function Glaze([string[]]$a) { if (Test-Glaze) { Glaze-Ipc ('command ' + ($a -join ' ')) | Out-Null } }

# Launch something outside of Zebar's process tree (GlazeWM spawns it), so
# restarting Zebar never kills it.
# Tokens must not contain spaces/quotes (PS 5.1 mangles those for native exes),
# hence the 8.3 short path of this script.
function Start-Self([string[]]$a) {
  $short = (New-Object -ComObject Scripting.FileSystemObject).GetFile($Script).ShortPath
  $argv = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-WindowStyle', 'Hidden', '-File', $short) + $a
  if (Test-Glaze) { Glaze (@('shell-exec', '--hide-window', 'powershell') + $argv) }
  else { Start-Process powershell $argv -WindowStyle Hidden }
}

# ---------------------------------------------------------------- colors
function Hsl([double]$h, [double]$s, [double]$l) {
  $h = (($h % 360) + 360) % 360 / 360
  $s = [Math]::Max(0.0, [Math]::Min(1.0, $s)); $l = [Math]::Max(0.0, [Math]::Min(1.0, $l))
  $q = if ($l -lt 0.5) { $l * (1 + $s) } else { $l + $s - $l * $s }
  $p = 2 * $l - $q
  $rgb = foreach ($t in ($h + 1 / 3), $h, ($h - 1 / 3)) {
    if ($t -lt 0) { $t += 1 }; if ($t -gt 1) { $t -= 1 }
    $v = if ($t -lt 1 / 6) { $p + ($q - $p) * 6 * $t } elseif ($t -lt 0.5) { $q } elseif ($t -lt 2 / 3) { $p + ($q - $p) * (2 / 3 - $t) * 6 } else { $p }
    [int][Math]::Round($v * 255)
  }
  '#{0:x2}{1:x2}{2:x2}' -f $rgb[0], $rgb[1], $rgb[2]
}

function HexToHsl([string]$hex) {
  $c = [Drawing.ColorTranslator]::FromHtml($hex)
  @($c.GetHue(), $c.GetSaturation(), $c.GetBrightness())
}

# Material-You-ish dark scheme from one hue (what matugen does for ML4W).
function Update-Colors {
  Load-Native
  $s = Get-Settings; $st = Get-State
  $wp = $st.wallpaper
  if (-not $wp) { $wp = (Get-ItemProperty 'HKCU:\Control Panel\Desktop').WallPaper }
  if ($s.dynamicColors -and $wp -and (Test-Path $wp)) {
    $hsl = [Mw4w.Native]::Dominant($wp).Split(',') | ForEach-Object { [double]::Parse($_, [Globalization.CultureInfo]::InvariantCulture) }
  } else { $hsl = HexToHsl $s.accent }
  $h = $hsl[0]; $sat = $hsl[1]
  $chroma = if ($sat -lt 0.1) { 0.12 } else { [Math]::Max(0.45, [Math]::Min(0.85, [double]$sat)) }
  $c = [ordered]@{
    primary     = Hsl $h $chroma 0.74
    onPrimary   = Hsl $h ($chroma * 0.6) 0.14
    secondary   = Hsl ($h + 25) ($chroma * 0.55) 0.72
    tertiary    = Hsl ($h + 70) ($chroma * 0.6) 0.74
    surface     = Hsl $h ($chroma * 0.3) 0.08
    surface2    = Hsl $h ($chroma * 0.25) 0.13
    surface3    = Hsl $h ($chroma * 0.2) 0.19
    outline     = Hsl $h ($chroma * 0.15) 0.36
    text        = Hsl $h ($chroma * 0.3) 0.93
    textDim     = Hsl $h ($chroma * 0.15) 0.70
    error       = '#f2b8b5'
  }
  $css = ":root {`n"
  foreach ($k in $c.Keys) {
    $x = [Drawing.ColorTranslator]::FromHtml($c[$k])
    $css += "  --$k`: $($c[$k]);`n  --$k-rgb: $($x.R), $($x.G), $($x.B);`n"
  }
  Write-Utf8 (Join-Path $Pack 'colors.css') ($css + "}`n")
  # liquid glass backdrop for the bar and dock: a small copy of the wallpaper (same aspect, the CSS crops it like Fill)
  $glass = Join-Path $Cache 'glass.jpg'
  if ($wp -and (Test-Path $wp)) {
    try {
      $img = [Drawing.Image]::FromFile($wp); $gh = [int](960 * $img.Height / $img.Width); $img.Dispose()
      [Mw4w.Native]::Thumb($wp, $glass, 960, $gh)
    } catch { }
  } elseif (Test-Path $glass) { Remove-Item $glass }
  Save-Json (Join-Path $Cache 'colors.json') $c
  # Alacritty imports this; it live-reloads on change.
  $ala = Join-Path $env:APPDATA 'alacritty'
  if (Test-Path $ala) {
    $t = "[colors.primary]`nbackground = '$($c.surface)'`nforeground = '$($c.text)'`n" +
      "`n[colors.cursor]`ncursor = '$($c.primary)'`ntext = '$($c.onPrimary)'`n" +
      "`n[colors.selection]`nbackground = '$($c.surface3)'`ntext = 'CellForeground'`n"
    $hue = @{ red = 0; green = 120; yellow = 45; blue = 215; magenta = 295; cyan = 180 }
    foreach ($kind in 'normal', 'bright') {
      $l = if ($kind -eq 'normal') { 0.70 } else { 0.80 }
      $t += "`n[colors.$kind]`nblack = '$(if ($kind -eq 'normal') { $c.surface3 } else { $c.outline })'`n"
      foreach ($k in 'red', 'green', 'yellow', 'blue', 'magenta', 'cyan') { $t += "$k = '$(Hsl $hue[$k] 0.55 $l)'`n" }
      $t += "white = '$(if ($kind -eq 'normal') { $c.textDim } else { $c.text })'`n"
    }
    Write-Utf8 (Join-Path $ala 'colors.toml') $t
  }
  $c
}

# ---------------------------------------------------------------- glazewm config
function Q([string]$v) { "'" + $v.Replace("'", "''") + "'" }

function Expand-Command([string]$c, $s) {
  foreach ($a in $s.apps.PSObject.Properties) { $c = $c.Replace('{' + $a.Name + '}', [Environment]::ExpandEnvironmentVariables($a.Value)) }
  # winmask hands it to its warm mw4w.ps1 (no quotes: GlazeWM's shell-exec misparses them)
  if ($c.StartsWith('@mw4w ')) { return 'shell-exec ' + (Join-Path $Pack 'bin\winmask.exe') + ' --mw4w ' + $c.Substring(6) }
  if ($c.StartsWith('@widget ')) { return 'shell-exec zebar start-widget-preset --pack mw4w --widget-name ' + $c.Substring(8) + ' --preset default' }
  $c
}

function Update-Glaze {
  $dir = Join-Path $env:USERPROFILE '.glzr\glazewm'
  New-Item -ItemType Directory -Force $dir | Out-Null
  Write-Utf8 (Join-Path $dir 'config.yaml') (Get-GlazeYaml)
  Glaze 'wm-reload-config'
}

function Get-GlazeYaml {
  $s = Get-Settings; $st = Get-State
  $c = Read-Json (Join-Path $Cache 'colors.json')
  if (-not $c) { $c = Update-Colors }
  $game = [bool]$st.gamemode
  $gi = if ($game) { 0 } else { [int]$s.gapsIn }
  $go = if ($game) { 0 } else { [int]$s.gapsOut }
  # Bar widget is 46px tall (8px margin + 38px pill row).
  $top = if ($st.barHidden) { $go } else { 46 + $gi }
  # Dock pill top is 82px above the screen edge (16px margin + 66px pill); it hides itself in game mode / with the bar hidden.
  $bottom = if ($st.barHidden -or $game -or -not $s.dock) { $go } else { 82 + $gi }
  $border = $s.border -and -not $game
  $corners = if ($game) { 'square' } else { $s.corners }
  $opacity = if ($game) { 100 } else { [int]$s.inactiveOpacity }
  $hideTitle = ([bool]$s.hideTitleBars).ToString().ToLower()
  $winmask = Winmask-Cmd $s

  # native and mouse (winmask) rows have no cmd; an empty command makes GlazeWM refuse the whole config
  $kb = (Read-Json (Join-Path $Pack 'keybinds.json')) | Where-Object { @($_.cmd | Where-Object { $_ }).Count }
  $kbYaml = foreach ($k in $kb) {
    "  - commands: [" + (($k.cmd | ForEach-Object { Q (Expand-Command $_ $s) }) -join ', ') + "]`n    bindings: [" + (($k.keys | ForEach-Object { Q $_ }) -join ', ') + "]"
  }
  # workspaces bound to a display (Displays popup): bind_to_monitor is the index in GlazeWM's monitor list
  $bind = @{}
  if ($s.monitorWorkspaces -and (Test-Glaze)) {
    $mons = try { @((Glaze-Ipc 'query monitors').data.monitors.deviceName) } catch { @() }
    foreach ($p in $s.monitorWorkspaces.PSObject.Properties) {
      $i = [array]::IndexOf($mons, $p.Name)
      if ($i -ge 0) { foreach ($n in $p.Value) { $bind["$n"] = $i } }
    }
  }
  # games (FiveM, GTA V, ...) stay out of GlazeWM: tiled, it moves them to whichever monitor or
  # workspace a fullscreen mode switch leaves them on, and cloaks them there
  $ignore = @($s.ignoreApps | Where-Object { $_ } | ForEach-Object { [regex]::Escape(($_ -replace '\.exe$', '')) })
  $ignoreYaml = if ($ignore) { "`n      - window_process: { regex: " + (Q "^($($ignore -join '|'))") + " }" }
  $ws = foreach ($i in 1..10) { "  - name: '$i'" + $(if ($i -le 5) { "`n    keep_alive: true" }) + $(if ($bind.ContainsKey("$i")) { "`n    bind_to_monitor: $($bind["$i"])" }) }

  $yaml = @"
# Generated by MW4W (scripts\mw4w.ps1 apply). Do not edit -
# change settings with SUPER+CTRL+S or edit keybinds.json / settings.json.
general:
  startup_commands:
    - 'shell-exec zebar'
    - $(Q "shell-exec $winmask")
    - $(Q (Expand-Command '@mw4w startup' $s))
  shutdown_commands:
    - 'shell-exec --hide-window taskkill /F /IM zebar.exe'
    - $(Q "shell-exec $(Join-Path $Pack 'bin\winmask.exe') --exit")
  config_reload_commands: []
  focus_follows_cursor: $(([bool]$s.focusFollowsMouse).ToString().ToLower())
  toggle_workspace_on_refocus: false
  cursor_jump:
    enabled: true
    trigger: 'monitor_focus'
  # 'hide' lets DWM animate windows in/out on workspace switch; 'cloak' is instant.
  hide_method: 'cloak'
  show_all_in_taskbar: false

gaps:
  scale_with_dpi: true
  inner_gap: '$($gi)px'
  outer_gap:
    top: '$($top)px'
    right: '$($go)px'
    bottom: '$($bottom)px'
    left: '$($go)px'

window_effects:
  focused_window:
    border:
      enabled: $($border.ToString().ToLower())
      color: '$($c.primary)'
    hide_title_bar:
      enabled: $hideTitle
    corner_style:
      enabled: true
      style: '$corners'
    transparency:
      enabled: false
      opacity: '100%'
  other_windows:
    border:
      enabled: $($border.ToString().ToLower())
      color: '$($c.surface3)'
    hide_title_bar:
      enabled: $hideTitle
    corner_style:
      enabled: true
      style: '$corners'
    transparency:
      enabled: $(($opacity -lt 100).ToString().ToLower())
      opacity: '$($opacity)%'

window_behavior:
  initial_state: 'tiling'
  state_defaults:
    floating:
      centered: true
      shown_on_top: true
    fullscreen:
      maximized: false
      shown_on_top: false

workspaces:
$($ws -join "`n")

window_rules:
  - commands: ['ignore']
    match:
      - window_process: { equals: 'zebar' }
      - window_title: { regex: '[Pp]icture.in.[Pp]icture|Obraz w obrazie' }
        window_class: { regex: 'Chrome_WidgetWin_1|MozillaDialogClass' }
      - window_process: { equals: 'PowerToys' }
        window_class: { regex: 'HwndWrapper\[PowerToys\.PowerAccent.*?\]' }
      - window_process: { equals: 'PowerToys' }
        window_title: { regex: '.*? - Peek' }
      - window_title: { equals: 'Command Palette' }
        window_class: { equals: 'WinUIDesktopWin32WindowClass' }
      - window_process: { regex: 'PowerToys\.PowerLauncher' }
      - window_process: { equals: 'Lively' }
        window_class: { regex: 'HwndWrapper' }
      - window_process: { equals: 'EXCEL' }
        window_class: { not_regex: 'XLMAIN' }
      - window_process: { equals: 'WINWORD' }
        window_class: { not_regex: 'OpusApp' }
      - window_process: { equals: 'POWERPNT' }
        window_class: { not_regex: 'PPTFrameClass' }$ignoreYaml
  # ML4W floats small utility windows.
  - commands: ['set-floating --centered']
    match:
      - window_process: { regex: '^(CalculatorApp|Taskmgr|SndVol|mmsys|charmap|ShareX)$' }
      - window_title: { regex: '^(Calculator|Kalkulator|Task Manager|Mened.er zada.)$' }

binding_modes:
  - name: 'resize'
    keybindings:
      - commands: ['resize --width -2%']
        bindings: ['h', 'left']
      - commands: ['resize --width +2%']
        bindings: ['l', 'right']
      - commands: ['resize --height +2%']
        bindings: ['k', 'up']
      - commands: ['resize --height -2%']
        bindings: ['j', 'down']
      - commands: ['wm-disable-binding-mode --name resize']
        bindings: ['escape', 'enter']

keybindings:
  - commands: ['wm-enable-binding-mode --name resize']
    bindings: ['lwin+r']
$($kbYaml -join "`n")
"@
  $yaml
}

function Apply { Update-Glaze; $st = Get-State; Save-State $st }

# ---------------------------------------------------------------- wallpapers
function Get-WallpaperDir { [Environment]::ExpandEnvironmentVariables((Get-Settings).wallpaperDir) }

function Get-Wallpapers {
  $d = Get-WallpaperDir
  if (-not (Test-Path $d)) { return @() }
  @(Get-ChildItem $d -File | Where-Object { $_.Extension -match '^\.(jpe?g|png|bmp)$' } | Sort-Object Name)
}

function Update-Thumbs {
  Load-Native
  $list = foreach ($f in Get-Wallpapers) {
    $id = '{0:x8}' -f ($f.FullName.ToLower().GetHashCode() -band 0x7fffffff)
    $t = Join-Path $Cache "thumbs\$id.jpg"
    if (-not (Test-Path $t) -or (Get-Item $t).LastWriteTime -lt $f.LastWriteTime) {
      try { [Mw4w.Native]::Thumb($f.FullName, $t, 384, 216) } catch { continue }
    }
    [pscustomobject]@{ name = $f.BaseName; path = $f.FullName; thumb = "cache/thumbs/$id.jpg" }
  }
  Save-Json (Join-Path $Cache 'wallpapers.json') @($list)
}

function Set-Wallpaper([string]$which) {
  Load-Native
  $all = Get-Wallpapers
  $st = Get-State
  if ($which -in 'random', 'next', 'prev', '') {
    if (-not $all.Count) { throw "No wallpapers in $(Get-WallpaperDir)" }
    $i = [Array]::IndexOf(@($all | ForEach-Object FullName), [string]$st.wallpaper)
    $path = switch ($which) {
      'next' { $all[($i + 1) % $all.Count].FullName }
      'prev' { $all[($i - 1 + $all.Count) % $all.Count].FullName }
      default { ($all | Where-Object FullName -ne $st.wallpaper | Get-Random).FullName }
    }
    if (-not $path) { $path = $all[0].FullName }
  } else { $path = (Resolve-Path $which).Path }
  Set-ItemProperty 'HKCU:\Control Panel\Desktop' WallpaperStyle '10'  # fill
  Set-ItemProperty 'HKCU:\Control Panel\Desktop' TileWallpaper '0'
  [Mw4w.Native]::SetWallpaper($path)
  $st.wallpaper = $path
  Save-Json (Join-Path $Cache 'state.json') $st
  Update-Colors | Out-Null
  Apply
}

function Toggle-WallpaperAuto {
  $st = Get-State
  $p = if ($st.autoPid) { Get-Process -Id $st.autoPid -ErrorAction SilentlyContinue | Where-Object ProcessName -eq 'powershell' }
  if ($p) { Stop-Process -Id $p.Id -Force; $st.autoPid = $null; Save-State $st }
  else { Start-Self 'wallpaper-loop' }
}

function Wallpaper-Loop {
  $st = Get-State; $st.autoPid = $PID; Save-State $st
  while ($true) {
    Start-Sleep -Seconds ([Math]::Max(1, [int](Get-Settings).wallpaperAutoMinutes) * 60)
    try { Set-Wallpaper 'random' } catch { }
  }
}

# ---------------------------------------------------------------- apps (launcher)
function Update-Apps {
  Load-Native
  $skip = '(?i)(uninstall|odinstaluj|readme|help|pomoc|documentation|license|licencja|website|\.(chm|txt|url|html?|pdf|ini|log)$)'
  $apps = Get-StartApps | Where-Object { $_.Name -notmatch $skip -and $_.AppID -notmatch $skip } | Sort-Object Name -Unique
  $todo = @(); $out = @()
  $list = foreach ($a in $apps) {
    $id = '{0:x8}' -f ($a.AppID.ToLower().GetHashCode() -band 0x7fffffff)
    $icon = Join-Path $Cache "icons\$id.png"
    if (-not (Test-Path $icon)) { $todo += $a.AppID; $out += $icon }
    [pscustomobject]@{ name = $a.Name; id = $a.AppID; icon = "cache/icons/$id.png" }
  }
  Save-Json (Join-Path $Cache 'apps.json') @($list)
  if ($todo.Count) { [Mw4w.Native]::Icons([string[]]$todo, [string[]]$out, 64) | Out-Null }
}

# ---------------------------------------------------------------- media (MediaControll island)
# Windows' MPRIS is GSMTC. Zebar's media provider has title, state and controls but no cover
# art, seek or per-app volume, so media-loop keeps cache\media.json up to date with those.
# await for WinRT calls (PowerShell 5.1 has no await): IAsyncOperation<T> / IAsyncAction -> Task
function Init-WinRt {
  if ($script:AsTask) { return }
  Add-Type -AssemblyName System.Runtime.WindowsRuntime
  $one = [WindowsRuntimeSystemExtensions].GetMethods() | Where-Object { $_.Name -eq 'AsTask' -and $_.GetParameters().Count -eq 1 }
  $script:AsTaskAction = $one | Where-Object { $_.GetParameters()[0].ParameterType.Name -eq 'IAsyncAction' } | Select-Object -First 1
  $script:AsTask = $one | Where-Object { $_.GetParameters()[0].ParameterType.Name -eq 'IAsyncOperation`1' } | Select-Object -First 1
}

function Await-Action($op, [int]$ms = 3000) {
  $task = $script:AsTaskAction.Invoke($null, @($op))
  if (-not $task.Wait($ms)) { throw 'WinRT call timed out' }
}

function Init-Media {
  if ($script:MediaMgr) { return }
  Init-WinRt
  $null = [Windows.Media.Control.GlobalSystemMediaTransportControlsSessionManager, Windows.Media.Control, ContentType = WindowsRuntime]
  $null = [Windows.Storage.Streams.IRandomAccessStreamWithContentType, Windows.Storage.Streams, ContentType = WindowsRuntime]
  $script:MediaMgr = Await-WinRt ([Windows.Media.Control.GlobalSystemMediaTransportControlsSessionManager]::RequestAsync()) ([Windows.Media.Control.GlobalSystemMediaTransportControlsSessionManager])
}

function Await-WinRt($op, [type]$type, [int]$ms = 3000) {
  $task = $script:AsTask.MakeGenericMethod($type).Invoke($null, @($op))
  if (-not $task.Wait($ms)) { throw 'WinRT call timed out' }
  $task.Result
}

# GSMTC app id -> processes (for the audio session and the icon) and a label. Ids look like
# 'chrome.exe', 'SpotifyAB.SpotifyMusic_zpnnbk0ga4vpm!Spotify', or a hash for Firefox-based browsers.
function Get-MediaApp([string]$id) {
  if ($script:AppCache -and $script:AppCache.id -eq $id -and (Get-Process -Id $script:AppCache.pid -ErrorAction SilentlyContinue)) { return $script:AppCache.app }
  $procs = @(
    if ($id -match '^(.+)\.exe$') { $Matches[1] }
    elseif ($id -match '^([^_!]+)_[^!]*!(.+)$') { $Matches[2]; $Matches[1] -replace '^.*\.', '' }
    elseif ($id -match '^[0-9A-Fa-f]{16}$') { 'firefox', 'zen', 'librewolf', 'floorp', 'waterfox' }
    else { $id }
  )
  $run = Get-Process -Name $procs -ErrorAction SilentlyContinue | Select-Object -First 1
  if ($run) { $procs = @($run.ProcessName) }
  $script:AppCache = $null
  $names = @{ chrome = 'Chrome'; msedge = 'Edge'; firefox = 'Firefox'; zen = 'Zen'; brave = 'Brave'; opera = 'Opera'; spotify = 'Spotify'; vlc = 'VLC' }
  $p = $procs[0].ToLower()
  $label = if ($names[$p]) { $names[$p] } else { (Get-Culture).TextInfo.ToTitleCase($procs[0]) }
  $app = @{ procs = [string[]]$procs; name = $label; icon = "cache/icons/proc-$p.png" }
  if ($run) { $script:AppCache = @{ id = $id; pid = $run.Id; app = $app } }
  $app
}

# The cover goes to cache\media\<hash>.img; none yet = try again next time (browsers send it late).
function Save-Cover($thumb, [string]$key) {
  if (-not $thumb) { return $null }
  $dir = Join-Path $Cache 'media'
  New-Item -ItemType Directory -Force $dir | Out-Null
  $name = '{0:x8}.img' -f ($key.GetHashCode() -band 0x7fffffff)
  $ras = Await-WinRt ($thumb.OpenReadAsync()) ([Windows.Storage.Streams.IRandomAccessStreamWithContentType])
  $in = [IO.WindowsRuntimeStreamExtensions]::AsStreamForRead($ras)
  try {
    $fs = [IO.File]::Create((Join-Path $dir $name))
    try { $in.CopyTo($fs) } finally { $fs.Dispose() }
  } finally { $in.Dispose() }
  Get-ChildItem $dir -Filter '*.img' | Where-Object Name -ne $name | Remove-Item -ErrorAction SilentlyContinue
  "cache/media/$name"
}

function Get-MediaState {
  Init-Media
  $s = $script:MediaMgr.GetCurrentSession()
  if (-not $s) { return [ordered]@{ title = $null } }
  $p = Await-WinRt ($s.TryGetMediaPropertiesAsync()) ([Windows.Media.Control.GlobalSystemMediaTransportControlsSessionMediaProperties])
  $t = $s.GetTimelineProperties()
  $pb = $s.GetPlaybackInfo()
  $app = Get-MediaApp $s.SourceAppUserModelId
  $key = "$($s.SourceAppUserModelId)|$($p.Title)|$($p.Artist)"
  if ($key -ne $script:CoverKey) {
    try { $script:Cover = Save-Cover $p.Thumbnail $key } catch { $script:Cover = $null }
    if ($script:Cover) { $script:CoverKey = $key }
  }
  $at = $t.LastUpdatedTime.ToUnixTimeMilliseconds()
  if ($at -le 0) { $at = [DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds() }
  $vol = [Mw4w.Audio]::AppVolume($app.procs, -1)
  [ordered]@{
    title   = $p.Title
    artist  = $p.Artist
    album   = $p.AlbumTitle
    app     = $app.name
    icon    = $app.icon
    cover   = $(if ($script:CoverKey -eq $key) { $script:Cover })
    playing = ("$($pb.PlaybackStatus)" -eq 'Playing')
    canSeek = [bool]$pb.Controls.IsPlaybackPositionEnabled
    pos     = [Math]::Round(($t.Position - $t.StartTime).TotalSeconds, 1)
    len     = [Math]::Round(($t.EndTime - $t.StartTime).TotalSeconds, 1)
    at      = $at
    vol     = $(if ($vol -ge 0) { $vol })
  }
}

function Media-Loop {
  $mutex = New-Object Threading.Mutex($false, 'Local\mw4w-media')
  if (-not $mutex.WaitOne(0)) { return }  # one loop is enough
  Load-Native; Init-Media
  $path = Join-Path $Cache 'media.json'
  $last = $null; $gone = 0; $i = 0
  # ends ~10s after Zebar does (the bar restart only takes it away for a moment)
  while ($gone -lt 3) {
    # every ~3.5s is enough to notice Zebar is gone
    if (($i++ % 5) -eq 0) { $gone = if (Get-Process zebar -ErrorAction SilentlyContinue) { 0 } else { $gone + 1 } }
    $idle = $true
    try {
      $m = Get-MediaState
      $idle = -not $m.playing
      $j = $m | ConvertTo-Json -Compress
      if ($j -ne $last) { Write-Utf8 $path $j; $last = $j }
    } catch { }
    # paused or nothing playing: the bar extrapolates nothing, a slower poll is fine
    Start-Sleep -Milliseconds $(if ($idle) { 1400 } else { 700 })
  }
}

function Media-Seek([double]$sec) {
  Init-Media
  $s = $script:MediaMgr.GetCurrentSession()
  if (-not $s) { return }
  $ticks = $s.GetTimelineProperties().StartTime.Ticks + [long]($sec * 1e7)
  Await-WinRt ($s.TryChangePlaybackPositionAsync($ticks)) ([bool]) | Out-Null
}

function Media-Volume([int]$v) {
  Load-Native; Init-Media
  $s = $script:MediaMgr.GetCurrentSession()
  if ($s) { [Mw4w.Audio]::AppVolume((Get-MediaApp $s.SourceAppUserModelId).procs, [Math]::Max(0, [Math]::Min(100, $v))) | Out-Null }
}

# ---------------------------------------------------------------- quick panels (quick.html)
# What Zebar's providers can't do: the volume mixer, the default device, Wi-Fi networks, radios,
# power mode and brightness. Results go to cache\quick.json and cache\wifi.json; the panel polls them.
function Init-Radios {
  if ($script:RadioType) { return }
  Init-WinRt
  $script:RadioType = [Windows.Devices.Radios.Radio, Windows.System.Devices, ContentType = WindowsRuntime]
  $null = [Windows.Devices.Radios.RadioAccessStatus, Windows.System.Devices, ContentType = WindowsRuntime]
  $null = Await-WinRt ($script:RadioType::RequestAccessAsync()) ([Windows.Devices.Radios.RadioAccessStatus])
}

function Get-Radios {
  Init-Radios
  Await-WinRt ($script:RadioType::GetRadiosAsync()) ([Collections.Generic.IReadOnlyList[Windows.Devices.Radios.Radio]])
}

function Set-Radio([string]$a) {
  $kind, $on = $a.Split(' ')
  Init-Radios
  $want = if ($on -eq 'on') { [Windows.Devices.Radios.RadioState]::On } else { [Windows.Devices.Radios.RadioState]::Off }
  foreach ($r in Get-Radios) {
    if ("$($r.Kind)" -eq $kind) { $null = Await-WinRt ($r.SetStateAsync($want)) ([Windows.Devices.Radios.RadioAccessStatus]) 5000 }
  }
  Update-Quick
}

function Get-Brightness {
  try { [int](Get-CimInstance -Namespace root/WMI -ClassName WmiMonitorBrightness -ErrorAction Stop | Select-Object -First 1).CurrentBrightness } catch { $null }
}

function Update-Quick {
  Load-Native
  $todo = @(); $out = @()
  $mixer = foreach ($row in [Mw4w.Audio]::Mixer()) {
    $name, $vol, $muted, $exe = $row.Split("`t")
    $icon = Join-Path $Cache ("icons\proc-{0}.png" -f $name.ToLower())
    if ($exe -and -not (Test-Path $icon)) { $todo += $exe; $out += $icon }
    [ordered]@{ name = $name; vol = [int]$vol; muted = $muted -eq '1'; icon = "cache/icons/proc-$($name.ToLower()).png" }
  }
  if ($todo.Count) { [Mw4w.Native]::Icons([string[]]$todo, [string[]]$out, 64) | Out-Null }
  $radios = [ordered]@{}
  try { foreach ($r in Get-Radios) { $radios["$($r.Kind)".ToLower()] = "$($r.State)" -eq 'On' } } catch { }
  Save-Json (Join-Path $Cache 'quick.json') ([ordered]@{
      at         = [DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds()
      mixer      = @($mixer)
      radios     = $radios
      powerMode  = [Mw4w.Native]::PowerMode()
      brightness = Get-Brightness
    })
}

function Set-AppAudio([string]$b64) {
  $p = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($b64)) | ConvertFrom-Json
  Load-Native
  $vol = if ($null -ne $p.vol) { [int]$p.vol } else { -1 }
  $mute = if ($null -ne $p.muted) { [int][bool]$p.muted } else { -1 }
  [Mw4w.Audio]::SetApp($p.name, $vol, $mute)
  Update-Quick
}

function Get-WifiAdapter {
  Init-WinRt
  $t = [Windows.Devices.WiFi.WiFiAdapter, Windows.System.Devices, ContentType = WindowsRuntime]
  $null = [Windows.Devices.WiFi.WiFiAccessStatus, Windows.System.Devices, ContentType = WindowsRuntime]
  $script:WifiAccess = "$(Await-WinRt ($t::RequestAccessAsync()) ([Windows.Devices.WiFi.WiFiAccessStatus]))"
  @(Await-WinRt ($t::FindAllAdaptersAsync()) ([Collections.Generic.IReadOnlyList[Windows.Devices.WiFi.WiFiAdapter]]))[0]
}

function Get-ConnectedSsid($ad) {
  $null = [Windows.Networking.Connectivity.ConnectionProfile, Windows.Networking.Connectivity, ContentType = WindowsRuntime]
  try {
    $cp = Await-WinRt ($ad.NetworkAdapter.GetConnectedProfileAsync()) ([Windows.Networking.Connectivity.ConnectionProfile])
    if ($cp -and $cp.IsWlanConnectionProfile) { $cp.WlanConnectionProfileDetails.GetConnectedSsid() }
  } catch { }
}

# Saved profiles: netsh's labels are translated, but every profile line is "<label> : <name>"
function Get-WifiProfiles {
  @(netsh wlan show profiles 2>$null | Where-Object { $_ -match '^\s+\S.*\s:\s(.+)$' } | ForEach-Object { $Matches[1].Trim() })
}

function Save-Wifi($ad, [string]$status = '') {
  $known = Get-WifiProfiles
  $cur = Get-ConnectedSsid $ad
  $best = @{}
  foreach ($n in $ad.NetworkReport.AvailableNetworks) {
    if (-not $n.Ssid) { continue }  # hidden
    if ($best[$n.Ssid] -and $best[$n.Ssid].SignalBars -ge $n.SignalBars) { continue }
    $best[$n.Ssid] = $n
  }
  $list = foreach ($n in $best.Values) {
    $auth = "$($n.SecuritySettings.NetworkAuthenticationType)"
    [ordered]@{ ssid = $n.Ssid; bars = [int]$n.SignalBars; secure = $auth -notin 'Open80211', 'None'; known = $known -contains $n.Ssid; connected = $n.Ssid -eq $cur }
  }
  $list = @($list | Sort-Object { -not $_.connected }, { -not $_.known }, { - $_.bars })
  Save-Json (Join-Path $Cache 'wifi.json') ([ordered]@{
      at        = [DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds()
      adapter   = [bool]$ad
      access    = $script:WifiAccess
      connected = $cur
      status    = $status
      networks  = $list
    })
}

function Wifi-Scan {
  $ad = Get-WifiAdapter
  if (-not $ad) { Save-Json (Join-Path $Cache 'wifi.json') @{ at = [DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds(); adapter = $false; networks = @() }; return }
  try { Await-Action ($ad.ScanAsync()) 10000 } catch { }
  Save-Wifi $ad
}

# arg: base64 JSON {ssid, pass?}. No password: the saved profile (or an open network).
function Wifi-Connect([string]$b64) {
  $p = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($b64)) | ConvertFrom-Json
  $ad = Get-WifiAdapter
  if (-not $ad) { return }
  Save-Wifi $ad 'connecting'
  $net = $ad.NetworkReport.AvailableNetworks | Where-Object Ssid -eq $p.ssid | Sort-Object SignalBars -Descending | Select-Object -First 1
  if (-not $net) { try { Await-Action ($ad.ScanAsync()) 10000 } catch { }; $net = $ad.NetworkReport.AvailableNetworks | Where-Object Ssid -eq $p.ssid | Select-Object -First 1 }
  if (-not $net) { return Save-Wifi $ad 'notfound' }
  $null = [Windows.Devices.WiFi.WiFiConnectionResult, Windows.System.Devices, ContentType = WindowsRuntime]
  $auto = [Windows.Devices.WiFi.WiFiReconnectionKind]::Automatic
  $op = if ($p.pass) {
    $null = [Windows.Security.Credentials.PasswordCredential, Windows.Security.Credentials, ContentType = WindowsRuntime]
    $cred = New-Object Windows.Security.Credentials.PasswordCredential
    $cred.Password = $p.pass
    $ad.ConnectAsync($net, $auto, $cred)
  } else { $ad.ConnectAsync($net, $auto) }
  $r = "$((Await-WinRt $op ([Windows.Devices.WiFi.WiFiConnectionResult]) 20000).ConnectionStatus)"
  Save-Wifi $ad $(if ($r -eq 'Success') { '' } elseif ($r -eq 'InvalidCredential') { 'password' } else { 'failed' })
}

function Wifi-Disconnect { $ad = Get-WifiAdapter; if ($ad) { $ad.Disconnect(); Start-Sleep -Milliseconds 500; Save-Wifi $ad } }

# Package updates for the bar (ML4W's updates module): `winget upgrade` rows -> cache\updates.json.
function Update-Updates {
  if (-not (Get-Command winget -ErrorAction SilentlyContinue)) { return }
  [Console]::OutputEncoding = [Text.Encoding]::UTF8
  $rows = 0; $table = $false
  foreach ($l in (& winget upgrade --accept-source-agreements --disable-interactivity 2>$null)) {
    if ($l -match '-{20,}') { $table = $true; continue }
    if (-not $table) { continue }
    # a row has (at least) the installed and the available version; the footer has one number at most
    if ($l -notmatch '\s\S*\d+(\.\d+)+\S*\s+\S*\d+(\.\d+)+') { break }
    $rows++
  }
  Save-Json (Join-Path $Cache 'updates.json') ([ordered]@{ count = $rows; at = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds() })
}

# ---------------------------------------------------------------- misc
function Kill-Active([long]$hwnd = 0) {
  Load-Native
  $p = Get-Process -Id ([Mw4w.Native]::PidOf($hwnd)) -ErrorAction SilentlyContinue
  if ($p -and $p.ProcessName -notin 'explorer', 'glazewm', 'zebar', 'powershell', 'csrss', 'dwm', 'winlogon') {
    # Elevated apps can't be killed from here: fall back to a normal close.
    try { Stop-Process -Id $p.Id -Force } catch { Glaze 'close' }
  }
}

function Empty-Workspace {
  $ws = (Glaze-Ipc 'query workspaces').data.workspaces
  foreach ($i in 1..10) {
    $w = $ws | Where-Object name -eq "$i"
    if (-not $w -or -not $w.children.Count) { Glaze 'focus', '--workspace', "$i"; return }
  }
}

function Power([string]$a) {
  switch ($a) {
    'lock' { rundll32.exe user32.dll, LockWorkStation }
    'logout' { shutdown.exe /l }
    'sleep' { Load-Native; [Mw4w.Native]::Suspend() }
    'hibernate' { shutdown.exe /h }
    'reboot' { shutdown.exe /r /t 0 }
    'shutdown' { shutdown.exe /s /t 0 }
    default { throw "Unknown power action: $a" }
  }
}

function Bar-Restart {
  Get-Process zebar -ErrorAction SilentlyContinue | Stop-Process -Force
  Start-Sleep -Milliseconds 600
  $st = Get-State; $st.barHidden = $false; Save-State $st
  Start-Process $Zebar
  Update-Glaze
}

function Bar-Toggle {
  $st = Get-State
  $st.barHidden = -not $st.barHidden
  Save-State $st  # the bar sees barHidden and closes itself
  if (-not $st.barHidden) { & $Zebar start-widget-preset --pack mw4w --widget-name bar --preset default }
  Update-Glaze
}

function Set-Settings([string]$b64) {
  $patch = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($b64)) | ConvertFrom-Json
  $path = Join-Path $Pack 'settings.json'
  $u = Read-Json $path
  if (-not $u) { $u = [pscustomobject]@{} }
  foreach ($p in $patch.PSObject.Properties) { $u | Add-Member -Force $p.Name $p.Value }
  Save-Json $path $u
  # the dock closes itself when off; turning it on has to start it again
  if ($patch.dock -eq $true) { & $Zebar start-widget-preset --pack mw4w --widget-name dock --preset default }
  if ($patch.showMedia -eq $true) { Start-Self 'media-loop' }
  # only the slow steps this patch needs (a toggle shouldn't recolor or restart winmask)
  $keys = @($patch.PSObject.Properties.Name)
  if ($keys -contains 'hideTaskbar') { Sync-Taskbar }
  if ($keys -contains 'replaceExplorer') { Sync-Shell ([bool]$patch.replaceExplorer) }
  if ($keys -contains 'filesForFolders') { Sync-Folders ([bool]$patch.filesForFolders) }
  if ($keys -contains 'dynamicColors' -or $keys -contains 'accent') { Update-Colors | Out-Null }
  Apply
}

# winmask also hides the taskbar windows completely (--hide-taskbar) and shows them again on --exit.
function Winmask-Cmd($s) { (Join-Path $Pack 'bin\winmask.exe') + $(if ($s.hideTaskbar) { ' --hide-taskbar' }) }

function Sync-Taskbar {
  $s = Get-Settings
  $want = [bool]$s.hideTaskbar
  Load-Native
  if ([Mw4w.Native]::TaskbarIsAutoHide() -ne $want) { [Mw4w.Native]::TaskbarAutoHide($want) }
  # restart winmask through GlazeWM so it keeps GlazeWM's integrity level (it kills the old one)
  Glaze 'shell-exec', (Winmask-Cmd $s)
}

# MW4W instead of Explorer: Winlogon starts bin\mw4w-shell.exe at sign-in (it runs the startup
# apps and GlazeWM, and falls back to Explorer when GlazeWM can't run). Takes effect at the next sign-in.
$WinlogonKey = 'HKCU:\Software\Microsoft\Windows NT\CurrentVersion\Winlogon'
function Sync-Shell([bool]$on) {
  $exe = Join-Path $Pack 'bin\mw4w-shell.exe'
  if ($on -and (Test-Path $exe)) { Set-ItemProperty $WinlogonKey Shell $exe }
  elseif ((Get-ItemProperty $WinlogonKey -ErrorAction SilentlyContinue).Shell -like '*mw4w-shell*') { Remove-ItemProperty $WinlogonKey Shell }
  Sync-Autostart
}

# GlazeWM at sign-in: a logon task, not the Run key. Explorer runs Run entries one after another,
# only after RunOnce and an idle wait: GlazeWM came ~100 s after the desktop. GlazeWM is uiAccess,
# which a task can't CreateProcess (error 740), so rundll32 ShellExecutes it. As the shell,
# mw4w-shell starts GlazeWM itself: the task is off then.
function Sync-Autostart {
  $run = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
  $exe = Join-Path $env:ProgramFiles 'glzr.io\GlazeWM\glazewm.exe'
  $asShell = (Get-ItemProperty $WinlogonKey -ErrorAction SilentlyContinue).Shell -like '*mw4w-shell*'
  try {
    $set = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit 0 -Priority 4 -Disable:$asShell
    Register-ScheduledTask 'MW4W-GlazeWM' -Force -Settings $set `
      -Action (New-ScheduledTaskAction -Execute 'rundll32.exe' -Argument "shell32.dll,ShellExec_RunDLL `"$exe`"") `
      -Trigger (New-ScheduledTaskTrigger -AtLogOn -User "$env:USERDOMAIN\$env:USERNAME") | Out-Null
    Remove-ItemProperty $run 'MW4W-GlazeWM' -ErrorAction SilentlyContinue
  } catch { Set-ItemProperty $run 'MW4W-GlazeWM' "`"$exe`"" }  # no Task Scheduler: the slow way
}

# MW4W Files opens folders everywhere (double-click on a folder, "Open folder" in apps, shortcuts):
# the per-user Directory and Drive verbs. Off gives them back to Explorer.
function Sync-Folders([bool]$on) {
  $exe = Join-Path $Pack 'bin\mw4w-files.exe'
  foreach ($cls in 'Directory', 'Drive') {
    $shell = "HKCU:\Software\Classes\$cls\shell"
    if ($on -and (Test-Path $exe)) {
      New-Item -Force "$shell\mw4wfiles\command" | Out-Null
      Set-ItemProperty "$shell\mw4wfiles" '(default)' 'Open in MW4W Files'
      Set-ItemProperty "$shell\mw4wfiles\command" '(default)' "`"$exe`" `"%1`""
      Set-ItemProperty $shell '(default)' 'mw4wfiles'
    } elseif (Test-Path "$shell\mw4wfiles") {
      Remove-Item -Recurse -Force "$shell\mw4wfiles"
      if ((Get-ItemProperty $shell -ErrorAction SilentlyContinue).'(default)' -eq 'mw4wfiles') { Remove-ItemProperty $shell '(default)' }
    }
  }
}

# ---------------------------------------------------------------- displays
# cache\displays.json for the Displays popup: active displays, workspace bindings, a pending revert.
function Update-Displays {
  Load-Native
  $rv = Read-Json (Join-Path $Cache 'displays-revert.json')
  Save-Json (Join-Path $Cache 'displays.json') ([ordered]@{
      at         = [DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds()
      displays   = @([Mw4w.Displays]::List())
      workspaces = (Get-Settings).monitorWorkspaces
      revertAt   = $rv.at
    })
}

# $list: [{device, mode 'WxH@Hz', x, y, orientation, primary, scale}]; the primary display goes to 0,0
function Set-DisplayPlan($list) {
  Load-Native
  $pri = @($list | Where-Object primary)[0]
  if (-not $pri) { $pri = $list[0] }
  foreach ($d in $list) {
    $w, $h, $hz = [int[]]($d.mode -split '[x@]')
    $r = [Mw4w.Displays]::Stage($d.device, $w, $h, $hz, [int]$d.x - $pri.x, [int]$d.y - $pri.y, [int]$d.orientation, $d -eq $pri)
    if ($r -ne 0) { throw "display $($d.device): ChangeDisplaySettingsEx $r" }
  }
  [Mw4w.Displays]::Commit() | Out-Null
  $now = @([Mw4w.Displays]::List())
  foreach ($d in $list) {
    if ($d.scale -and ($now | Where-Object Device -eq $d.device).Scale -ne $d.scale) { [Mw4w.Displays]::SetScale($d.device, [int]$d.scale) | Out-Null }
  }
}

function Get-DisplayPlan {
  foreach ($d in [Mw4w.Displays]::List()) { [ordered]@{ device = $d.Device; mode = $d.Mode; x = $d.X; y = $d.Y; orientation = $d.Orientation; primary = $d.Primary; scale = $d.Scale } }
}

# GlazeWM applies bind_to_monitor only to workspaces it creates: move the existing ones over
function Move-BoundWorkspaces {
  $binds = (Get-Settings).monitorWorkspaces
  if (-not $binds -or -not (Test-Glaze)) { return }
  $back = @((Glaze-Ipc 'query workspaces').data.workspaces | Where-Object hasFocus)[0].name
  $moved = $false
  foreach ($p in $binds.PSObject.Properties) {
    foreach ($n in $p.Value) {
      for ($try = 0; $try -lt 4; $try++) {  # ponytail: one monitor per step, 4 steps covers 5 side by side
        $mons = @((Glaze-Ipc 'query monitors').data.monitors)
        $to = @($mons | Where-Object deviceName -eq $p.Name)[0]
        $from = @($mons | Where-Object { $_.children.name -contains "$n" })[0]
        if (-not $to -or -not $from -or $from.deviceName -eq $to.deviceName) { break }
        $dx = ($to.x + $to.width / 2) - ($from.x + $from.width / 2)
        $dy = ($to.y + $to.height / 2) - ($from.y + $from.height / 2)
        $dir = if ([Math]::Abs($dx) -ge [Math]::Abs($dy)) { if ($dx -gt 0) { 'right' } else { 'left' } } elseif ($dy -gt 0) { 'down' } else { 'up' }
        Glaze 'focus', '--workspace', "$n"
        Glaze 'move-workspace', '--direction', $dir
        $moved = $true
      }
    }
  }
  if ($moved -and $back) { Glaze 'focus', '--workspace', $back }
}

# winmask (WM_DISPLAYCHANGE, settled) and startup: a monitor came back, maybe in another order, so
# bind_to_monitor indexes are re-read and the workspaces GlazeWM parked elsewhere go back
function Sync-Displays {
  if (-not (Get-Settings).monitorWorkspaces -or -not (Test-Glaze)) { return }
  $path = Join-Path $env:USERPROFILE '.glzr\glazewm\config.yaml'
  $yaml = Get-GlazeYaml
  if (-not (Test-Path $path) -or [IO.File]::ReadAllText($path) -ne $yaml) { Write-Utf8 $path $yaml; Glaze 'wm-reload-config' }
  Move-BoundWorkspaces
}

# arg: base64 {displays?: plan, workspaces?: {device: [names]}}. New display settings revert
# after 15s unless kept (display-keep), like Windows Settings does.
function Set-Displays([string]$b64) {
  $p = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($b64)) | ConvertFrom-Json
  Load-Native
  if ($null -ne $p.workspaces) {
    $path = Join-Path $Pack 'settings.json'
    $u = Read-Json $path
    if (-not $u) { $u = [pscustomobject]@{} }
    $u | Add-Member -Force monitorWorkspaces $p.workspaces
    Save-Json $path $u
  }
  if ($p.displays) {
    $id = [DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds()
    $prev = @(Get-DisplayPlan)
    Set-DisplayPlan $p.displays
    Save-Json (Join-Path $Cache 'displays-revert.json') ([ordered]@{ id = "$id"; at = $id + 15000; prev = $prev })
    Start-Self 'display-revert', "$id"
  }
  Update-Glaze; Move-BoundWorkspaces; Update-Displays
}

# $id: only the change that started this timer ("" = now)
function Undo-Displays([string]$id) {
  $path = Join-Path $Cache 'displays-revert.json'
  if ($id) { Start-Sleep 15 }
  $rv = Read-Json $path
  if (-not $rv -or ($id -and $rv.id -ne $id)) { return }
  Remove-Item $path
  Set-DisplayPlan $rv.prev
  Update-Glaze; Move-BoundWorkspaces; Update-Displays
}

# Icons for the bar's taskbar module: cache\icons\proc-<process>.png
function Update-WinIcons {
  Load-Native
  $todo = @(); $out = @()
  foreach ($w in (Glaze-Ipc 'query windows').data.windows) {
    $png = Join-Path $Cache ("icons\proc-{0}.png" -f $w.processName.ToLower())
    if ((Test-Path $png) -or $out -contains $png) { continue }
    $exe = (Get-Process -Id ([Mw4w.Native]::PidOf($w.handle)) -ErrorAction SilentlyContinue).Path
    if ($exe) { $todo += $exe; $out += $png }
  }
  if ($todo.Count) { [Mw4w.Native]::Icons([string[]]$todo, [string[]]$out, 64) | Out-Null }
}

function Startup {
  Sync-Taskbar
  $st = Get-State; $st.barHidden = $false; $st.autoPid = $null; Save-State $st
  if (-not (Test-Path (Join-Path $Pack 'colors.css')) -or -not (Test-Path (Join-Path $Cache 'glass.jpg'))) { Update-Colors | Out-Null; Update-Glaze }
  Sync-Displays
  if ((Get-Settings).showMedia) { Start-Self 'media-loop' }
  # MW4W Files stays resident (index kept live, a window ready): opening one is instant
  $files = Join-Path $Pack 'bin\mw4w-files.exe'
  if (Test-Path $files) { Start-Process $files '--background' }
  Start-Self 'updates-now'
  Update-Thumbs
  Update-Apps
}

function Invoke-Mw4w([string]$Cmd, [string]$Arg) {
  switch ($Cmd) {
    'apply' { Sync-Taskbar; Apply }  # Sync-Taskbar restarts winmask: it reads keybinds.json too
    'colors' { Update-Colors | Out-Null; Apply }
    'startup' { Startup }
    'autostart' { Sync-Autostart }
    'cache' { Update-Apps }
    'thumbs' { Update-Thumbs }
    'wallpaper' { Set-Wallpaper $Arg }
    'wallpaper-auto' { Toggle-WallpaperAuto }
    'wallpaper-loop' { Wallpaper-Loop }
    'power' { Power $Arg }
    'kill-active' { if ($Arg) { Kill-Active ([long]$Arg) } else { Kill-Active } }  # arg: hwnd (default foreground)
    'empty-workspace' { Empty-Workspace }
    'gamemode' { $st = Get-State; $st.gamemode = -not $st.gamemode; Save-State $st; Update-Glaze }
    'bar-restart' { Bar-Restart }
    'bar-toggle' { Bar-Toggle }
    'set' { Set-Settings $Arg }
    'toggle' { $v = -not (Get-Settings).$Arg; Set-Settings ([Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes((@{ $Arg = $v } | ConvertTo-Json)))) }
    'winicons' { Update-WinIcons }
    'media-loop' { Media-Loop }
    'media-seek' { Media-Seek ([double]::Parse($Arg, [Globalization.CultureInfo]::InvariantCulture)) }
    'media-volume' { Media-Volume ([int]$Arg) }
    'updates' { Start-Self 'updates-now' }  # winget takes seconds: not in the warm worker
    'updates-now' { Update-Updates }
    'quick' { Update-Quick }
    'app-audio' { Set-AppAudio $Arg }
    'audio-default' { Load-Native; [Mw4w.Audio]::SetDefault([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($Arg))) }
    'power-mode' { Load-Native; [Mw4w.Native]::SetPowerMode([int]$Arg); Update-Quick }
    'brightness' { $m = Get-CimInstance -Namespace root/WMI -ClassName WmiMonitorBrightnessMethods | Select-Object -First 1; if ($m) { Invoke-CimMethod -InputObject $m -MethodName WmiSetBrightness -Arguments @{ Timeout = 0; Brightness = [byte][Math]::Max(0, [Math]::Min(100, [int]$Arg)) } | Out-Null } }
    'radio' { Set-Radio $Arg }
    # Wi-Fi takes seconds: not in the warm worker
    'wifi-scan' { Start-Self 'wifi-scan-now' }
    'wifi-scan-now' { Wifi-Scan }
    'wifi-connect' { Start-Self 'wifi-connect-now', $Arg }
    'wifi-connect-now' { Wifi-Connect $Arg }
    'wifi-disconnect' { Start-Self 'wifi-disconnect-now' }
    'wifi-disconnect-now' { Wifi-Disconnect }
    'displays' { Update-Displays }
    'display-set' { Set-Displays $Arg }
    'display-keep' { Remove-Item (Join-Path $Cache 'displays-revert.json') -ErrorAction SilentlyContinue; Update-Displays }
    'display-undo' { Undo-Displays '' }
    'display-sync' { Sync-Displays }
    'display-revert' { Undo-Displays $Arg }  # Start-Self'd: sleeps 15s
    'display-topology' { if ($Arg -in 'extend', 'clone', 'internal', 'external') { Start-Process DisplaySwitch.exe "/$Arg"; Start-Sleep 3; Update-Displays } }
    'run' { Start-Self 'run-now', $Arg }
    'run-now' { Start-Process cmd.exe ('/c start "" ' + [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($Arg))) -WindowStyle Hidden }
    'launch' { Start-Process "shell:AppsFolder\$Arg" }  # ShellExecute, no explorer.exe needed (MW4W may be the shell)
    'files' { Set-Settings ([Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes((@{ filesForFolders = ($Arg -eq 'on') } | ConvertTo-Json)))) }
    'shell' { Set-Settings ([Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes((@{ replaceExplorer = ($Arg -eq 'on') } | ConvertTo-Json)))) }
    'selftest' {
      $s = Get-Settings
      $checks = @(
        @((Hsl 0 1 0.5), '#ff0000'), @((Hsl 120 1 0.25), '#008000'), @((Hsl 240 0.5 0.5), '#4040bf'), @((Hsl -120 1 0.5), '#0000ff'),
        @((Expand-Command '@widget power' $s), 'shell-exec zebar start-widget-preset --pack mw4w --widget-name power --preset default'),
        @((Expand-Command '@mw4w toggle animations' $s), "shell-exec $Pack\bin\winmask.exe --mw4w toggle animations"),
        @((Q "it's"), "'it''s'")
      )
      foreach ($c in $checks) { if ($c[0] -ne $c[1]) { throw "selftest: got '$($c[0])' expected '$($c[1])'" } }
      'selftest ok'
    }
    default { throw "Unknown command: $Cmd" }
  }
}

function Run-Mw4w([string]$Cmd, [string]$Arg) {
  try { Invoke-Mw4w $Cmd $Arg }
  catch { Add-Content (Join-Path $Cache 'error.log') ("[{0}] {1} {2}: {3}" -f (Get-Date -Format s), $Cmd, $Arg, $_); throw }
}

if ($Cmd -eq 'serve') {
  # winmask keeps this one running and pipes in "<command> [arg]" lines, so
  # keybinds skip PowerShell's ~1s start. Ends when winmask does (stdin closes).
  # warm up: JSON cmdlets, System.Drawing, Native.cs, the WebSocket client
  Get-Settings | ConvertTo-Json | Out-Null; Load-Native
  try { Glaze-Ipc 'query focused' | Out-Null; Invoke-Mw4w selftest | Out-Null; Get-GlazeYaml | Out-Null; Get-State | ConvertTo-Json | Out-Null } catch { }
  while ($null -ne ($line = [Console]::In.ReadLine())) {
    $c, $a = $line.Split(' ', 2)
    try { Run-Mw4w $c "$a" | Out-Null } catch { }
  }
  exit
}
try { Run-Mw4w $Cmd $Arg } catch { Write-Error $_; exit 1 }
