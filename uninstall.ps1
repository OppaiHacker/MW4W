# MW4W - uninstaller. Leaves GlazeWM/Zebar installed and wallpapers in place.
#   powershell -ExecutionPolicy Bypass -File uninstall.ps1
$ErrorActionPreference = 'Continue'
$Glzr = Join-Path $env:USERPROFILE '.glzr'
$Pack = Join-Path $Glzr 'zebar\mw4w'

# Explorer is the shell again from the next sign-in (and mw4w-shell starts it when GlazeWM exits below).
$wl = 'HKCU:\Software\Microsoft\Windows NT\CurrentVersion\Winlogon'
if ((Get-ItemProperty $wl -ErrorAction SilentlyContinue).Shell -like '*mw4w-shell*') { Remove-ItemProperty $wl Shell }

# GlazeWM runs elevated (Stop-Process gets access denied), so ask it to exit.
$cli = Join-Path $env:ProgramFiles 'glzr.io\GlazeWM\cli\glazewm.exe'
# mw4w.ps1 wm-exit first brings windows of hidden workspaces back (GlazeWM leaves them cloaked)
$mw4w = Join-Path $Pack 'scripts\mw4w.ps1'
if ((Get-Process glazewm -ErrorAction SilentlyContinue) -and (Test-Path $mw4w)) { & powershell -NoProfile -ExecutionPolicy Bypass -File $mw4w wm-exit; Start-Sleep -Seconds 2 }
if (Get-Process glazewm -ErrorAction SilentlyContinue) { & $cli command wm-exit | Out-Null; Start-Sleep -Seconds 2 }
# winmask --exit shows the hidden taskbar again (GlazeWM's shutdown_commands do the same).
$wm = Join-Path $Pack 'bin\winmask.exe'
if (Test-Path $wm) { & $wm --exit; Start-Sleep -Milliseconds 500 }
Get-Process zebar, winmask, mw4w-shell, mw4w-files -ErrorAction SilentlyContinue | Stop-Process -Force
# folders open in Explorer again; the file index goes
foreach ($cls in 'Directory', 'Drive') {
  $shell = "HKCU:\Software\Classes\$cls\shell"
  if (Test-Path "$shell\mw4wfiles") { Remove-Item -Recurse -Force "$shell\mw4wfiles" }
  if ((Get-ItemProperty $shell -ErrorAction SilentlyContinue).'(default)' -eq 'mw4wfiles') { Remove-ItemProperty $shell '(default)' }
}
Remove-Item -Recurse -Force (Join-Path $env:LOCALAPPDATA 'mw4w-files') -ErrorAction SilentlyContinue
Remove-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' 'MW4W-GlazeWM' -ErrorAction SilentlyContinue
Unregister-ScheduledTask 'MW4W-GlazeWM' -Confirm:$false -ErrorAction SilentlyContinue

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

# fastfetch greeting out of the pwsh profile (fastfetch itself and its config stay)
$pwshProfile = Join-Path ([Environment]::GetFolderPath('MyDocuments')) 'PowerShell\Microsoft.PowerShell_profile.ps1'
if (Test-Path $pwshProfile) {
  # IO.File: Windows PowerShell's Get/Set-Content would read and write ANSI
  $p = [IO.File]::ReadAllText($pwshProfile)
  $q = $p -replace '(?s)\r?\n?# >>> MW4W fastfetch.*?# <<< MW4W fastfetch\r?\n?', ''
  if ($q -ne $p) { [IO.File]::WriteAllText($pwshProfile, $q, (New-Object Text.UTF8Encoding $false)) }
}

Remove-Item $Pack -Recurse -Force -ErrorAction SilentlyContinue
Write-Host 'MW4W removed.' -ForegroundColor Green
