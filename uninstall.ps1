# MW4W - uninstaller. Leaves GlazeWM/Zebar installed and wallpapers in place.
#   powershell -ExecutionPolicy Bypass -File uninstall.ps1
$ErrorActionPreference = 'Continue'
$Glzr = Join-Path $env:USERPROFILE '.glzr'
$Pack = Join-Path $Glzr 'zebar\mw4w'

# GlazeWM runs elevated (Stop-Process gets access denied), so ask it to exit.
$cli = Join-Path $env:ProgramFiles 'glzr.io\GlazeWM\cli\glazewm.exe'
if (Get-Process glazewm -ErrorAction SilentlyContinue) { & $cli command wm-exit | Out-Null; Start-Sleep -Seconds 2 }
# winmask --exit shows the hidden taskbar again (GlazeWM's shutdown_commands do the same).
$wm = Join-Path $Pack 'bin\winmask.exe'
if (Test-Path $wm) { & $wm --exit; Start-Sleep -Milliseconds 500 }
Get-Process zebar, winmask -ErrorAction SilentlyContinue | Stop-Process -Force
Remove-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' 'MW4W-GlazeWM' -ErrorAction SilentlyContinue

# Show the taskbar again.
$dll = Get-ChildItem (Join-Path $Pack 'cache') -Filter 'mw4w-native-*.dll' -ErrorAction SilentlyContinue | Select-Object -First 1
if ($dll) {
  # load a temp copy so the pack folder can be deleted below
  $tmp = Join-Path $env:TEMP $dll.Name; Copy-Item $dll.FullName $tmp -Force
  Add-Type -AssemblyName System.Drawing; Add-Type -Path $tmp; [Mw4w.Native]::TaskbarAutoHide($false)
}

# Restore the newest backups made by install.ps1.
foreach ($f in "$Glzr\glazewm\config.yaml", "$Glzr\zebar\settings.json") {
  $bak = Get-ChildItem "$f.bak-*" -ErrorAction SilentlyContinue | Sort-Object Name | Select-Object -Last 1
  if ($bak) { Copy-Item $bak.FullName $f -Force } else { Remove-Item $f -ErrorAction SilentlyContinue }
}

Remove-Item $Pack -Recurse -Force -ErrorAction SilentlyContinue
Write-Host 'MW4W removed.' -ForegroundColor Green
