# MW4W — My Windows 4 Work

[ML4W (My Linux For Work)](https://www.ml4w.com/)-style Hyprland on Windows 11.
**GlazeWM** does the tiling; **Zebar** (widget pack `mw4w`) replaces waybar, rofi, wlogout, waypaper, the ML4W Settings app and the keybinding cheat sheet;
**MW4W Files** replaces Explorer's file manager (Nautilus look, instant indexed search, previews).

![Desktop](docs/screenshots/mw4w-desktop.webp)

<details>
<summary><b>Screenshots</b>: bar, tray, MediaControll, quick panels, popups, MW4W Files</summary>

Bar (tray drawer open)

![Bar](docs/screenshots/mw4w-bar-tray-open.webp)

Popups open over a window: MediaControll island and the Sound panel

![Popups](docs/screenshots/mw4w-desktop-popups.webp)

| MediaControll | Sound | Network |
| --- | --- | --- |
| ![Island](docs/screenshots/mw4w-island.webp) | ![Sound](docs/screenshots/mw4w-quick-audio.webp) | ![Network](docs/screenshots/mw4w-quick-network.webp) |
| **Power** | **Calendar** | **Launcher** |
| ![Power](docs/screenshots/mw4w-quick-battery.webp) | ![Calendar](docs/screenshots/mw4w-quick-calendar.webp) | ![Launcher](docs/screenshots/mw4w-launcher.webp) |

| Wallpapers | Keybindings | Settings |
| --- | --- | --- |
| ![Wallpapers](docs/screenshots/mw4w-wallpaper.webp) | ![Keybindings](docs/screenshots/mw4w-keybinds.webp) | ![Settings](docs/screenshots/mw4w-settings.webp) |

Dock

![Dock](docs/screenshots/mw4w-dock.webp)

Power menu

![Power menu](docs/screenshots/mw4w-power.webp)

MW4W Files

| | | |
| --- | --- | --- |
| ![Home](docs/screenshots/files-home.webp) | ![Code](docs/screenshots/files-code-preview.webp) | ![Images](docs/screenshots/files-grid-images.webp) |
| ![Search](docs/screenshots/files-search-names.webp) | ![Contents](docs/screenshots/files-search-contents.webp) | ![Quick Look](docs/screenshots/files-quick-look.webp) |
| ![Markdown](docs/screenshots/files-markdown-preview.webp) | ![CSV](docs/screenshots/files-csv-preview.webp) | ![Video](docs/screenshots/files-video-preview.webp) |
| ![Font](docs/screenshots/files-font-preview.webp) | ![Archive](docs/screenshots/files-archive-preview.webp) | ![Menu](docs/screenshots/files-context-menu.webp) |
| ![Trash](docs/screenshots/files-trash.webp) | ![Computer](docs/screenshots/files-computer.webp) | ![Path](docs/screenshots/files-path-autocomplete.webp) |

The screenshots are rendered from the repo's own pages with demo data (made on Linux, so glyphs of the
Windows-only *Segoe Fluent Icons* font are missing, e.g. in the power menu).
</details>

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

## Quick panels

The bar's icons drop down panels (`quick.html`) instead of opening Windows Settings:

| Click | Panel |
| --- | --- |
| volume | **Sound**: output and microphone volume/mute, pick the output/input device, per-app mixer. Scroll = volume, right click = mute |
| network | **Network**: Wi-Fi and Bluetooth switches, connection and traffic, Wi-Fi list — click to connect (password field for new networks), click the connected one to disconnect |
| battery | **Power**: charge, time left, health, draw, brightness (laptop screens) and power mode (efficiency / balanced / performance) |
| clock | **Calendar** |

Zebar's providers give volume, devices, network and battery; `mw4w.ps1` adds the rest (Core Audio sessions, `IPolicyConfig`
for the default device, WinRT `WiFiAdapter` and `Radio`, the power mode overlay, WMI brightness) through `cache/quick.json`
and `cache/wifi.json`. Since Windows 11 24H2, listing Wi-Fi networks needs location access for desktop apps; the panel links it.

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

## Multiple monitors

Every monitor gets its own bar and dock. A bar marks the workspace shown on its monitor (filled while that monitor
has the focus) and shows the title of that monitor's window. Clicking a workspace on a bar opens a new one on that
bar's monitor, and clicking a monitor's empty desktop focuses it, so SUPER+Enter or SUPER+6 land there, like Hyprland.
Popups (launcher, settings, power menu, ...) open on the monitor under the cursor, quick panels and the island under their
icon, at that monitor's scale. *Displays* (SUPER+CTRL+M) binds workspaces to monitors. Tested with 2 and 3 monitors at
mixed scaling (100% + 175%), including plugging one in and out.

## MW4W instead of Explorer

*Settings → System → MW4W as the Windows shell* (or `MW4W-Setup.exe -ReplaceExplorer`, or `mw4w.ps1 shell on`)
makes Winlogon start `bin/mw4w-shell.exe` instead of `explorer.exe` from the next sign-in. Then nothing of Explorer runs:
no taskbar, Start menu, desktop icons, Win+X menu or Explorer's ~100 MB; the bar, dock and launcher are the shell.
`mw4w-shell` does Explorer's sign-in chores — tells Winlogon the shell is ready, runs the `Run`/`RunOnce` keys and the
Startup folders (skipping what Task Manager's Startup tab turned off) — and keeps GlazeWM running (restarted after a crash).

Folders open in MW4W Files with *Settings → System → MW4W Files opens folders* (else in Explorer windows: Explorer only becomes the shell when it is the registered one).
Toasts, the volume/brightness OSD, Win+A/Win+N panels and the Windows tray come from Explorer and are gone;
the bar has its own tray (Zebar). If GlazeWM is missing or keeps crashing, `mw4w-shell` starts Explorer, so a broken
install never ends at a black screen; CTRL+SHIFT+ESC → *Run new task* → `explorer` works too. `mw4w.ps1 shell off`
or the uninstaller switch back. Some antiviruses (F-Secure DeepGuard) block `mw4w-shell` as a shell replacement:
Windows then comes up with Explorer and the `MW4W-GlazeWM` logon task starts GlazeWM anyway. Allow `bin\mw4w-shell.exe`
in the antivirus to keep MW4W as the shell.

This is a user-mode shell like Explorer itself — nothing of a window manager or shell belongs in the kernel
(a driver could only crash the machine and needs a Microsoft signature).

## MW4W Files

`bin/mw4w-files.exe` (SUPER+E) is a file manager in the style of GNOME Files (Nautilus), built for speed:

- **Instant search** from its own index (SQLite, FTS5 trigram): any part of a name in milliseconds, or the **contents**
  of text and code files. `ext:pdf`, `kind:image|video|audio|doc|code|archive|dir` filter. Just start typing in a folder.
- **Previews** in a side pane (Ctrl+I) or Quick Look (Space): code with syntax highlighting and line numbers (chroma),
  Markdown rendered, CSV as a table, images, video, audio, PDF, fonts, archive contents (zip, tar), hex for binaries,
  folder size from the index.
- **Thumbnails** from Windows (photos, videos, PDFs, Office files, app icons; the system thumbnail cache), kept in the index.
- Recent, Starred, Trash (the real Recycle Bin: restore, delete, empty; Undo after Delete), This PC with drive usage,
  bookmarks (drag a folder onto the sidebar), path bar with Tab completion (Ctrl+L), list and grid, zoom, hidden files,
  rename in place (F2), new folder/file, copy/cut/paste through the Windows clipboard (works with every app),
  drag & drop onto folders, compress/extract zip, Open With, Properties, open in terminal (Ctrl+Alt+T).
- Copy, move and delete go through the Windows copy engine (its progress, conflict and undo).

How it is fast:

- One resident process (`--background`, started with MW4W) keeps the index live (ReadDirectoryChangesW) and a hidden
  window already loaded, so SUPER+E only shows it: no process or WebView start. Closing a window keeps it as the spare.
- The index scans with low-priority threads (background mode: no CPU/disk competition), and the first scan fills the
  name index in one rebuild (10× faster than row by row). Measured here: 87,000 files indexed in 1.4 s; a name search
  over them takes 0.4–10 ms (a one-letter query ~60 ms), a content search ~1–2 ms, opening a folder ~13 ms; a folder of
  50,000 files opens in ~150 ms, sorts in ~20 ms and scrolls at 60 fps (its files are stat'ed on all cores).
- Folders render virtually (only visible rows exist), hovering a folder prefetches it, item counts load for visible rows only,
  and a listing is one hand-written JSON array.

The index covers your profile and other fixed drives (not `C:\Windows` and the like; `node_modules`, `.git` and hidden folders
are skipped). Edit `%LOCALAPPDATA%\mw4w-files\config.json` (`roots`, `exclude`, `content`, `contentMaxKB`) and rescan from the menu.

The UI talks to the resident process over HTTP on `127.0.0.1` only, with a random port and a random token per run
(HttpOnly SameSite cookie), a pinned Host header against DNS rebinding, Origin checks and a strict Content Security Policy;
nothing is reachable from the network or from web pages. `mw4w-files.exe --quit` stops it.

## Install

Download **MW4W-Setup.exe** from [Releases](https://github.com/OppaiHacker/MW4W/releases/latest) and run it
(it isn't code-signed, so SmartScreen may ask: *More info → Run anyway*). Or from a clone:

```powershell
powershell -ExecutionPolicy Bypass -File install.ps1
```

`MW4W-Setup.exe` takes the same flags, plus `--uninstall`. Build it with `installer/build.sh [version]`
(needs Go). CI builds it on every push and publishes it as release `v<VERSION>`; bump `VERSION` for a new release.

The installer:

1. installs GlazeWM, Zebar, PowerShell 7 and JetBrainsMono Nerd Font with winget if they are missing,
2. backs up `~/.glzr/glazewm/config.yaml` and `~/.glzr/zebar/settings.json` (`*.bak-<date>`),
3. copies the pack to `~/.glzr/zebar/mw4w` and the wallpapers to `~/Pictures/Wallpapers`,
4. detects the default browser and terminal (Alacritty > wt > powershell); Alacritty gets an ML4W-style config (`%APPDATA%\alacritty`) with colors from the wallpaper,
5. installs fastfetch (winget), copies its config to `~/.config/fastfetch` and runs it from the pwsh profile on every new terminal,
6. makes Firefox/Zen open external links in the existing window (see Limitations),
7. sets the MW4W wallpaper, generates colors and the GlazeWM config,
8. adds GlazeWM to autostart (a logon task: Explorer runs Run-key entries one by one after an idle wait, ~100 s late on a busy sign-in). GlazeWM then starts Zebar (bar, dock and hidden popups), `winmask` and `mw4w.ps1 startup`.

Flags: `-NoStart` (don't start anything), `-KeepWallpaper` (keep the current wallpaper).
Uninstall: `uninstall.ps1` (restores the config backups and shows the taskbar again).

Optional: `winget install Alacritty.Alacritty` before installing, for the ML4W terminal look.

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
| SUPER + CTRL + M             | displays, workspaces per display    |
| SUPER + CTRL + K             | all keybindings                     |
| SUPER + CTRL + B / SHIFT + B | show/hide bar / reload bar          |
| SUPER + SHIFT + R            | reload config                       |

Win+L, Win+V (clipboard), Win+. (emoji), Win+N (notifications), Win+Shift+S (screenshot) and Win+Space (keyboard layout) stay with Windows.
Every other SUPER shortcut of Windows (Win+A, Win+W, Win+R, Win+X, Win+I, ...) is blocked by `winmask`, so nothing breaks the illusion;
to keep one, add it to `keybinds.json` as a `"native": true` row and press SUPER+SHIFT+R.

## Files

```
install.ps1 / uninstall.ps1
pack/                     -> ~/.glzr/zebar/mw4w
  zpack.json              Zebar widgets
  bar.html launcher.html power.html wallpaper.html keybinds.html settings.html
  quick.html              sound / network / power / calendar panels under the bar's icons
  island.html             MediaControll dynamic island (opened by hovering the bar's media capsule)
  dock.html               nwg-dock: open apps at the bottom of the screen
  keybinds.json           keybindings: the GlazeWM config and the cheat sheet are generated from it
  alacritty.toml          Alacritty config template (colors: colors.toml written by mw4w.ps1)
  fastfetch.jsonc         fastfetch config (-> ~/.config/fastfetch, Nerd Font icons)
  defaults.json           default settings (your changes go to settings.json)
  scripts/mw4w.ps1        the brain: colors, GlazeWM config, wallpapers, power, gamemode, bar, media loop, updates
  scripts/Native.cs       Win32: app icons, wallpaper, dominant color, taskbar, per-app volume
  bin/mw4w-shell.exe      the shell when MW4W replaces Explorer (source: shell/)
  bin/mw4w-files.exe      MW4W Files, the file manager (source: files/)
  bin/winmask.exe         SUPER doesn't open Start (CTRL+Esc does), SUPER+mouse, workspace slide, hides the taskbar,
                          handles popup keybindings, @mw4w and shell-exec itself (see Speed)
  lib/                    zebar.js (bundled, works offline), shared JS/CSS, icons.js (line icons, media helpers)
installer/                MW4W-Setup.exe: the repo in one exe that runs install.ps1 (build.sh builds it)
shell/                    mw4w-shell source (GOOS=windows go build -trimpath -ldflags "-H windowsgui -s -w" -o ../pack/bin/mw4w-shell.exe)
files/                    MW4W Files source: Go (index, API, previews, WebView2 windows) + ui/ (GOOS=windows go build -trimpath -ldflags "-H windowsgui -s -w" -o ../pack/bin/mw4w-files.exe;
                          `go run . --serve` runs the UI in a browser on any OS)
docs/screenshots/         README screenshots
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
- The liquid glass bakes its displacement and light maps only for the bezel band and copies the straight edges (3–6× faster than per pixel), and hidden popups check `state.json` every 6 s instead of 1.5 s.
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
