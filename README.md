# MW4W — My Windows 4 Work

[ML4W (My Linux For Work)](https://www.ml4w.com/)-style Hyprland on Windows 11.
**GlazeWM** does the tiling; **Zebar** (widget pack `mw4w`) replaces waybar, rofi, wlogout, waypaper, the ML4W Settings app and the keybinding cheat sheet.

| ML4W (Linux)         | Here                                                                                                   |
| -------------------- | ------------------------------------------------------------------------------------------------------ |
| Hyprland             | GlazeWM: tiling, gaps, borders, rounded corners, 10 workspaces                                         |
| ML4W dock            | `dock`: glass pill at the bottom, open apps (click = focus/next window, middle = close), dot = running, bar = focused; can be turned off in settings (Dock) |
| ML4W Quickshell bar  | `bar`: one floating glass pill — terminal, workspaces 1–10, media capsule (or window title), apps grid, clock + date, notifications, timer, updates (winget), game mode, volume, network, battery, tray, settings, power |
| [MediaControll](https://github.com/OppaiHacker/MediaControll) | media capsule in the bar + `island`: hovering it springs open a liquid-glass dynamic island with cover, seek bar, controls and per-app volume |
| rofi                 | `launcher`: apps with icons, window switcher, Run (Tab)                                                |
| wlogout              | `power`: lock / logout / suspend / hibernate / reboot / shutdown                                       |
| waypaper + matugen   | `wallpaper`: changing the wallpaper generates a color palette for the bar, popups, borders and Alacritty |
| ML4W Settings App    | `settings`: gaps, borders, corners, transparency, apps, bar                                            |
| keybindings.sh       | `keybinds`: cheat sheet generated from `keybinds.json`                                                 |
| gamemode.sh          | SUPER+ALT+G: no gaps, borders or transparency                                                          |

## Media (MediaControll)

The capsule shows the cover, the title and an equalizer (dots when paused); it hides when nothing plays.
Left click = play/pause, right = next, middle = previous. Hovering it opens the island: drag the seek bar,
drag/scroll for volume, click the speaker to mute.

Windows has no MPRIS; players publish to GSMTC (the media flyout), so any app that shows up there works
(browsers, Spotify, VLC, Media Player, ...). Zebar's `media` provider gives the title, state and controls;
`mw4w.ps1 media-loop` (started with GlazeWM, one per session) adds what it lacks — cover art, seekable position and the player's
own volume (its Core Audio session, falling back to the system volume like MediaControll falls back to the default sink) —
in `cache/media.json`. The liquid glass is CSS (blurred cover, specular rim) instead of the GLSL shader: WebView2 widgets can't see the desktop behind them.
Turn it off with *Media player module* in settings.

## Install

Download **MW4W-Setup.exe** from [Releases](https://github.com/OppaiHacker/MW4W/releases/latest) and run it
(it isn't code-signed, so SmartScreen may ask: *More info → Run anyway*). Or from a clone:

```powershell
powershell -ExecutionPolicy Bypass -File install.ps1
```

`MW4W-Setup.exe` takes the same flags, plus `--uninstall`. Build it with `installer/build.sh [version]`
(needs Go; a `v*` tag builds it in CI and attaches it to the release).

The installer:

1. installs GlazeWM and Zebar with winget if they are missing,
2. backs up `~/.glzr/glazewm/config.yaml` and `~/.glzr/zebar/settings.json` (`*.bak-<date>`),
3. copies the pack to `~/.glzr/zebar/mw4w` and the wallpapers to `~/Pictures/Wallpapers`,
4. detects the default browser and terminal (Alacritty > wt > powershell); Alacritty gets an ML4W-style config (`%APPDATA%\alacritty`) with colors from the wallpaper,
5. makes Firefox/Zen open external links in the existing window (see Limitations),
6. sets the MW4W wallpaper, generates colors and the GlazeWM config,
7. adds GlazeWM to autostart. GlazeWM then starts Zebar (bar, dock and hidden popups), `winmask` and `mw4w.ps1 startup`.

Flags: `-NoStart` (don't start anything), `-KeepWallpaper` (keep the current wallpaper).
Uninstall: `uninstall.ps1` (restores the config backups and shows the taskbar again).

Optional: `winget install Alacritty.Alacritty DEVCOM.JetBrainsMonoNerdFont` before installing, for the ML4W terminal look.

## Main keybindings (SUPER = Win)

| Keys                         | Action                              |
| ---------------------------- | ----------------------------------- |
| SUPER + Enter                | terminal                            |
| SUPER + B / E                | browser / files                     |
| SUPER + CTRL/SHIFT + Enter   | launcher (rofi)                     |
| SUPER + Q                    | close window                        |
| SUPER + F / M / T            | fullscreen / maximize / floating    |
| SUPER + J                    | toggle split direction              |
| SUPER + arrows               | focus                               |
| SUPER + ALT + arrows         | move window                         |
| SUPER + SHIFT + arrows       | resize                              |
| SUPER + 1…0                  | workspace 1…10                      |
| SUPER + SHIFT + 1…0          | move window to workspace            |
| SUPER + Tab                  | next workspace                      |
| SUPER + CTRL + W / SHIFT + W | wallpaper picker / random wallpaper |
| SUPER + CTRL + P (or Q)      | power menu                          |
| SUPER + SHIFT + A            | workspace slide on/off              |
| SUPER + CTRL + S             | MW4W settings                       |
| SUPER + CTRL + K             | all keybindings                     |
| SUPER + CTRL + B / SHIFT + B | show/hide bar / reload bar          |
| SUPER + SHIFT + R            | reload config                       |

Win+L, Win+V (clipboard), Win+. (emoji), Win+Shift+S (screenshot) and Win+Space (keyboard layout) stay with Windows.

## Files

```
install.ps1 / uninstall.ps1
pack/                     -> ~/.glzr/zebar/mw4w
  zpack.json              Zebar widgets
  bar.html launcher.html power.html wallpaper.html keybinds.html settings.html
  island.html             MediaControll dynamic island (opened by hovering the bar's media capsule)
  dock.html               nwg-dock: open apps at the bottom of the screen
  keybinds.json           keybindings: the GlazeWM config and the cheat sheet are generated from it
  alacritty.toml          Alacritty config template (colors: colors.toml written by mw4w.ps1)
  defaults.json           default settings (your changes go to settings.json)
  scripts/mw4w.ps1        the brain: colors, GlazeWM config, wallpapers, power, gamemode, bar, media loop, updates
  scripts/Native.cs       Win32: app icons, wallpaper, dominant color, taskbar, per-app volume
  bin/winmask.exe         SUPER doesn't open Start (CTRL+Esc does), SUPER+mouse, workspace slide, hides the taskbar,
                          handles popup keybindings, @mw4w and shell-exec itself (see Speed)
  lib/                    zebar.js (bundled, works offline), shared JS/CSS, icons.js (line icons, media helpers)
installer/                MW4W-Setup.exe: the repo in one exe that runs install.ps1 (build.sh builds it)
helper/                   winmask source (go build -trimpath -ldflags "-H windowsgui -s -w" -o ../pack/bin/winmask.exe)
wallpapers/               7 wallpapers
```

Add your own keybindings in `keybinds.json`, then press SUPER+SHIFT+R. `cmd` accepts GlazeWM commands plus
`@mw4w <command>` (script), `@widget <name>` (Zebar popup) and `{terminal}`, `{browser}`, `{files}`, `{calculator}`.

`mw4w.ps1 selftest` checks the color and config generators.
Script errors go to `~/.glzr/zebar/mw4w/cache/error.log`.

## Speed

On Windows the expensive part is starting a process (0.3–0.5 s here: ShellExecute in GlazeWM + antivirus), and PowerShell adds another ~1 s. So:

- Popups (launcher, power, wallpapers, keybindings, settings) start hidden together with Zebar (`preload` preset) and are only shown/hidden afterwards: ~20 ms instead of ~0.7 s + render.
- `winmask` handles the SUPER keybindings from `keybinds.json` itself: `@widget` shows the ready popup, `shell-exec` starts through CreateProcess (what it can't do — URIs, `msedge`, elevation — goes on to GlazeWM), and `@mw4w` goes to a warm `mw4w.ps1 serve` that `winmask` keeps in the background. Widgets do the same through `winmask --mw4w <command>`.
- Fast workspace switching interrupts the running slide instead of queueing more. The slide draws DWM thumbnails (like Task View), no screenshots.
- SUPER+RMB on a tiled window: when the edge is shared by exactly two containers, `winmask` moves the windows on both sides itself (without waiting for GlazeWM and the apps), and GlazeWM gets a single `resize` on release. Other layouts (e.g. three columns) still use live `resize` commands, because GlazeWM spreads the change over all neighboring windows.
- Don't use quotes in `shell-exec`: GlazeWM misparses them, and a program it can't find won't start and blocks on an error dialog.

## Limitations

- Switching workspaces is an ML4W-like slide (`workspaces, 1, 5, wind`) done by `winmask`: it intercepts the workspace keybindings from `keybinds.json` and slides the windows as snapshots over the wallpaper. Only SUPER keybindings animate (digits, Tab, arrows, letters, `oem_tilde`). Clicking a workspace on the bar switches instantly. Window content (e.g. video) freezes for 0.5 s. No background blur. Turn it off with SUPER+SHIFT+A.
- SUPER+LMB moves, SUPER+RMB resizes a window (done by `winmask`). Like Hyprland, windows can't be dragged by the title bar or edges without SUPER. Windows can't be minimized and ALT+Tab does nothing. A moved tiled window is inserted where you drop it. Resizing a tile moves the split with its neighbor (see Speed) and does nothing on an axis without a neighbor.
- Some apps (e.g. Zen with popups) resize a new window themselves after GlazeWM has placed it. So `winmask` asks GlazeWM to lay out windows again (`wm-redraw`) 0.3 s and 1 s after a new window appears. An app that resizes later still slips through.
- GlazeWM cloaks other workspaces the way Windows hides other virtual desktops, so Firefox/Zen would open an external link in a new window (Zen: plus a tab in the old one). The installer sets `widget.prefer_windows_on_current_virtual_desktop = false` in their profiles; restart the browser afterwards.
- The Windows taskbar is fully hidden by `winmask --hide-taskbar`. It comes back when GlazeWM exits.
- Win+L always locks the screen, because Windows doesn't let anyone take over that shortcut.
- Window borders and corners need Windows 11.

## License

[GPL-3.0](LICENSE), like GlazeWM, Zebar and ML4W. `pack/lib/zebar.js` bundles the Zebar client (GPL-3.0) and zod (MIT).
