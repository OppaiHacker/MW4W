// Hyprland's workspace slide (ML4W: "animation = workspaces, 1, 5, wind").
//
// GlazeWM switches workspaces instantly (hide_method: cloak), so winmask takes
// over the workspace keybinds from keybinds.json: it covers the monitor with an
// overlay showing the wallpaper and live DWM thumbnails of the windows, runs
// the keybind's commands over GlazeWM's IPC, adds thumbnails of the new
// workspace's windows and slides both across the still wallpaper. The real
// windows are already in place underneath, so hiding the overlay is seamless.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	wmApp      = 0x8000
	slideMs    = 500 // speed 5 = 5 ds
	srcCopy    = 0x00CC0020
	swpShow    = 0x0040 | 0x0010 // SWP_SHOWWINDOW|SWP_NOACTIVATE
	swpHide    = 0x0080 | 0x0010 | 0x1 | 0x2
	lwaAlpha   = 0x2
	dwmCloaked = 14
	dwmBounds  = 9 // DWMWA_EXTENDED_FRAME_BOUNDS: without the invisible resize border
)

var (
	gdi32  = syscall.NewLazyDLL("gdi32.dll")
	dwmapi = syscall.NewLazyDLL("dwmapi.dll")

	registerClass = user32.NewProc("RegisterClassExW")
	createWindow  = user32.NewProc("CreateWindowExW")
	defWindowProc = user32.NewProc("DefWindowProcW")
	postMessage   = user32.NewProc("PostMessageW")
	getDC         = user32.NewProc("GetDC")
	releaseDC     = user32.NewProc("ReleaseDC")
	paintDesktop  = user32.NewProc("PaintDesktop")
	setLayered    = user32.NewProc("SetLayeredWindowAttributes")
	enumWindows   = user32.NewProc("EnumWindows")
	isVisible     = user32.NewProc("IsWindowVisible")
	findWindow    = user32.NewProc("FindWindowW")
	keyState      = user32.NewProc("GetAsyncKeyState")
	sysParams     = user32.NewProc("SystemParametersInfoW")
	newDC         = gdi32.NewProc("CreateCompatibleDC")
	newBitmap     = gdi32.NewProc("CreateCompatibleBitmap")
	selectObject  = gdi32.NewProc("SelectObject")
	bitBlt        = gdi32.NewProc("BitBlt")
	deleteObject  = gdi32.NewProc("DeleteObject")
	deleteDC      = gdi32.NewProc("DeleteDC")
	viewportOrg   = gdi32.NewProc("SetViewportOrgEx")
	dwmFlush      = dwmapi.NewProc("DwmFlush")
	dwmAttr       = dwmapi.NewProc("DwmGetWindowAttribute")
	dwmRegister   = dwmapi.NewProc("DwmRegisterThumbnail")
	dwmUpdate     = dwmapi.NewProc("DwmUpdateThumbnailProperties")
	dwmUnregister = dwmapi.NewProc("DwmUnregisterThumbnail")

	peekMessage = user32.NewProc("PeekMessageW")
	setTimer    = user32.NewProc("SetTimer")
	killTimer   = user32.NewProc("KillTimer")
	findWindowX = user32.NewProc("FindWindowExW")

	slideWin uintptr // overlay; its thread runs the animations
	wsKeys   = map[combo]int{}
	wsCmds   [][]string // commands per taken-over keybind
	wsHeld   uint32     // vk of the held taken-over key: auto-repeat is swallowed
	clicked  []string   // commands of the last "slide" message (slider thread only)
	listed   []uintptr  // enumWindows output
	ipc      *glaze     // slider thread only: one GlazeWM connection for all slides
	listCB   = syscall.NewCallback(listWindow)
	bgCache  struct {
		dc, bmp uintptr
		key     string
	}
)

type combo struct {
	vk               uint32
	shift, ctrl, alt bool
}

// packDir is the MW4W pack folder: winmask.exe lives in its bin\.
func packDir() string {
	exe, _ := os.Executable()
	return filepath.Dir(filepath.Dir(exe))
}

func readJSON(name string, v any) {
	b, _ := os.ReadFile(filepath.Join(packDir(), name))
	json.Unmarshal(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), v)
}

// takeOver: keybinds winmask runs itself instead of GlazeWM. Workspace
// switches slide; popups and mw4w.ps1 commands skip a process start, which
// costs 0.3-0.5s here (GlazeWM's shell-exec, plus the AV scanning it).
func takeOver(cmd []string) bool {
	j := strings.Join(cmd, "|")
	return strings.Contains(j, "focus --") && strings.Contains(j, "workspace") ||
		len(cmd) == 1 && (strings.HasPrefix(j, "@widget ") || strings.HasPrefix(j, "@mw4w ") || strings.HasPrefix(j, "shell-exec "))
}

// keyNames: GlazeWM key names whose virtual-key code isn't the letter or digit itself.
var keyNames = map[string]uint32{"tab": 0x09, "enter": 0x0D, "escape": 0x1B, "space": 0x20, "print_screen": 0x2C,
	"left": 0x25, "up": 0x26, "right": 0x27, "down": 0x28, "delete": 0x2E, "insert": 0x2D, "home": 0x24, "end": 0x23,
	"page_up": 0x21, "page_down": 0x22, "backspace": 0x08, "oem_tilde": 0xC0, "oem_period": 0xBE, "oem_comma": 0xBC,
	"oem_minus": 0xBD, "oem_plus": 0xBB, "oem_1": 0xBA, "oem_2": 0xBF, "oem_4": 0xDB, "oem_5": 0xDC, "oem_6": 0xDD, "oem_7": 0xDE}

// parseKey reads "lwin+shift+a". vk 0: a key name we don't know.
func parseKey(k string) (c combo, win bool) {
	parts := strings.Split(strings.ToLower(k), "+")
	for _, p := range parts[:len(parts)-1] {
		switch p {
		case "lwin", "rwin":
			win = true
		case "shift":
			c.shift = true
		case "control", "ctrl":
			c.ctrl = true
		case "alt":
			c.alt = true
		}
	}
	last := parts[len(parts)-1]
	c.vk = keyNames[last]
	if len(last) == 1 && (last[0] >= '0' && last[0] <= '9' || last[0] >= 'a' && last[0] <= 'z') {
		c.vk = uint32(strings.ToUpper(last)[0])
	} else if n, err := strconv.Atoi(strings.TrimPrefix(last, "f")); err == nil && last[0] == 'f' && n >= 1 && n <= 24 {
		c.vk = 0x6F + uint32(n) // F1 = 0x70
	}
	return
}

// loadWorkspaceKeys takes every SUPER keybind takeOver wants, and notes every
// bound SUPER combo for blockedKey.
func loadWorkspaceKeys() {
	var kb []struct {
		Keys []string
		Cmd  []string
	}
	readJSON("keybinds.json", &kb)
	winKeys[combo{vk: 'R'}] = true // GlazeWM's resize mode (mw4w.ps1 Get-GlazeYaml)
	for _, b := range kb {
		for _, k := range b.Keys {
			c, win := parseKey(k)
			if !win || k == "lwin+lmb" || k == "lwin+rmb" {
				continue
			}
			if c.vk == 0 {
				blockUnbound = false // a key we can't read: blocking might eat it
			}
			winKeys[c] = true
			if takeOver(b.Cmd) && c.vk != 0 {
				wsKeys[c] = len(wsCmds)
			}
		}
		if takeOver(b.Cmd) {
			wsCmds = append(wsCmds, b.Cmd)
		}
	}
}

var (
	winKeys      = map[combo]bool{} // every SUPER combo keybinds.json binds
	blockUnbound = true
)

// blockedKey runs in the keyboard hook: SUPER combos nobody bound go nowhere,
// so Windows' own (Win+A quick settings, Win+W widgets, Win+R, Win+X, ...)
// don't break the illusion. Ones to keep: "native": true rows in keybinds.json.
func blockedKey(vk uint32) bool {
	switch {
	case !winDown || !blockUnbound || vk >= 0x10 && vk <= 0x12 || vk >= 0xA0 && vk <= 0xA5 || vk == vkMask:
		return false // not SUPER, or a modifier
	}
	held := func(k uintptr) bool { r, _, _ := keyState.Call(k); return r&0x8000 != 0 }
	return !winKeys[combo{vk, held(0x10), held(0x11), held(0x12)}]
}

// workspaceKey runs in the keyboard hook: true = the key was taken over.
func workspaceKey(vk uint32, down bool) bool {
	if !down {
		if vk == wsHeld {
			wsHeld = 0
		}
		return false
	}
	if !winDown || slideWin == 0 {
		return false
	}
	held := func(k uintptr) bool { r, _, _ := keyState.Call(k); return r&0x8000 != 0 }
	i, ok := wsKeys[combo{vk, held(0x10), held(0x11), held(0x12)}]
	if !ok {
		return false
	}
	cmd := wsCmds[i][0]
	var popup uintptr
	if strings.HasPrefix(cmd, "@widget ") {
		if popup = popupWindow(cmd[8:]); popup == 0 {
			return false // not preloaded: GlazeWM starts it the slow way
		}
	}
	if vk == wsHeld {
		return true
	}
	wsHeld = vk
	switch {
	case popup != 0:
		act(func() { togglePopup(popup) })
	case cmd == "@mw4w kill-active":
		act(killActive)
	case strings.HasPrefix(cmd, "shell-exec "):
		act(func() { launch(cmd[11:]) })
	case strings.HasPrefix(cmd, "@mw4w ") && cmd != "@mw4w empty-workspace": // that one slides
		postMessage.Call(slideWin, wmApp+1, uintptr(i), 0)
	default:
		postMessage.Call(slideWin, wmApp, uintptr(i), 0)
	}
	return true
}

func slider() {
	runtime.LockOSThread()
	name, _ := syscall.UTF16PtrFromString("mw4w-slide")
	wc := struct {
		size, style                  uint32
		proc                         uintptr
		clsExtra, wndExtra           int32
		inst, icon, cursor, bg, menu uintptr
		name                         *uint16
		iconSm                       uintptr
	}{proc: syscall.NewCallback(slideProc), name: name}
	wc.size = uint32(unsafe.Sizeof(wc))
	registerClass.Call(uintptr(unsafe.Pointer(&wc)))
	// topmost, tool window, no activate, click-through (layered + transparent)
	h, _, _ := createWindow.Call(0x08000000|0x80|0x8|0x20|0x80000, uintptr(unsafe.Pointer(name)), 0, 0x80000000, 0, 0, 0, 0, 0, 0, 0, 0)
	setLayered.Call(h, 0, 0, lwaAlpha)
	slideWin = h
	startWorker()
	// warm up so the first slide starts as fast as the rest: IPC connection, wallpaper
	ipcFocus()
	scr, _, _ := getDC.Call(0)
	background(overlayRect(fgMonitor()), scr)
	releaseDC.Call(0, scr)
	pump()
}

func slideProc(h, msg, wp, lp uintptr) uintptr {
	switch msg {
	case wmApp:
		slide(wsCmds[wp])
		return 0
	case wmApp + 1: // an "@mw4w <command>" keybind
		line := strings.TrimPrefix(wsCmds[wp][0], "@mw4w ")
		if toWorker(line) == 0 {
			mw4wArgs(strings.SplitN(line, " ", 2)...).Start()
		}
		return 0
	case wmApp + 2:
		slide(clicked)
		return 0
	case 0x007E: // WM_DISPLAYCHANGE: a monitor came or went, or a game switched modes
		wake(displayWake)            // screens.go puts the workspaces back
		setTimer.Call(h, 1, 3000, 0) // restarted by every change: sync once it settles
		return 0
	case 0x0113: // WM_TIMER
		if wp == 1 {
			killTimer.Call(h, 1)
			bgCache.key = "" // the wallpaper moved with the monitors
			if toWorker("display-sync") == 0 {
				mw4wArgs("display-sync").Start()
			}
			return 0
		}
	case 0x004A: // WM_COPYDATA from "winmask --mw4w"
		cd := (*copyData)(unsafe.Pointer(lp))
		line := syscall.UTF16ToString(unsafe.Slice(cd.ptr, cd.size/2))
		// "slide <base64 of GlazeWM commands, one a line>": a dock/launcher click, slid like the keybinds
		if b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "slide ")); strings.HasPrefix(line, "slide ") && err == nil {
			clicked = strings.Split(string(b), "\n")
			postMessage.Call(h, wmApp+2, 0, 0) // posted: the sender needn't wait out the slide
			return 1
		}
		return toWorker(line)
	}
	r, _, _ := defWindowProc.Call(h, msg, wp, lp)
	return r
}

func animationsOn() bool {
	var s, d struct{ Animations *bool }
	var st struct{ Gamemode *bool }
	readJSON("defaults.json", &d)
	readJSON("settings.json", &s)
	readJSON(filepath.Join("cache", "state.json"), &st)
	if s.Animations == nil {
		s.Animations = d.Animations
	}
	return s.Animations != nil && *s.Animations && (st.Gamemode == nil || !*st.Gamemode)
}

// wsInfo is a workspace from GlazeWM's "query monitors", with its monitor's rect.
type wsInfo struct {
	Name                  string
	HasFocus, IsDisplayed bool
	mon                   rect
}

func workspaces(g *glaze) ([]wsInfo, error) {
	var res struct {
		Data struct {
			Monitors []struct {
				X, Y, Width, Height int32
				Children            []wsInfo
			}
		}
	}
	b, err := g.query("query monitors")
	if err == nil {
		err = json.Unmarshal(b, &res)
	}
	var out []wsInfo
	for _, m := range res.Data.Monitors {
		for _, w := range m.Children {
			w.mon = rect{m.X, m.Y, m.X + m.Width, m.Y + m.Height}
			out = append(out, w)
		}
	}
	return out, err
}

func focused(ws []wsInfo) wsInfo {
	for _, w := range ws {
		if w.HasFocus {
			return w
		}
	}
	return wsInfo{}
}

// ipcFocus returns the GlazeWM connection, dialing again after GlazeWM
// restarted, and the workspaces. Keeping it open saves 10-35ms a slide.
func ipcFocus() (*glaze, []wsInfo) {
	for try := 0; try < 2; try++ {
		if ipc == nil {
			g, err := dialGlaze()
			if err != nil {
				return nil, nil
			}
			ipc = g
		}
		if ws, err := workspaces(ipc); err == nil {
			return ipc, ws
		}
		ipc.c.Close()
		ipc = nil
	}
	return nil, nil
}

func slide(cmds []string) {
	g, ws := ipcFocus()
	if g == nil {
		return
	}
	run := func() {
		for _, c := range cmds {
			if c == "@mw4w empty-workspace" {
				if c = emptyWorkspace(g); c == "" {
					continue
				}
			}
			g.ask("command " + c)
		}
	}
	if !animationsOn() {
		run()
		return
	}
	// the slide runs on the monitor the workspace shows up on: its own when
	// GlazeWM has it on one, else the focused one. One already shown just takes focus.
	from := focused(ws)
	shown := map[string]bool{}
	on := from.mon
	for _, w := range ws {
		shown[w.Name] = w.IsDisplayed
		for _, c := range cmds {
			if c == "focus --workspace "+w.Name {
				on = w.mon
			}
		}
	}
	for _, c := range cmds {
		if n, ok := strings.CutPrefix(c, "focus --workspace "); ok && shown[n] {
			run()
			return
		}
	}
	if from.Name == "" || !overlayMu.TryLock() { // a SUPER+RMB drag has it
		run()
		return
	}
	defer overlayMu.Unlock()
	ov := overlayRect(on)
	w := ov.r - ov.l
	desk := desktop(ov) // registered first: under every window
	defer unregister(desk)
	old := thumbs(ov)
	defer unregister(old)
	place(old, 0)
	showOverlay(ov)
	defer hideOverlay()

	run()
	ws, _ = workspaces(g)
	to := focused(ws)
	// --recent-workspace and friends can land on a shown workspace, or on another monitor
	if to.Name == "" || to.Name == from.Name || shown[to.Name] || to.mon != on {
		return
	}
	cur := thumbs(ov) // the old windows are cloaked now, so these are the new ones
	defer unregister(cur)

	dir := int32(1) // higher workspace comes in from the right
	a, errA := strconv.Atoi(from.Name)
	b, errB := strconv.Atoi(to.Name)
	if errA == nil && errB == nil && b < a || (errA != nil || errB != nil) && to.Name < from.Name {
		dir = -1
	}
	start := time.Now()
	for {
		t := float64(time.Since(start).Milliseconds()) / slideMs
		if t > 1 {
			t = 1
		}
		p := wind(t)
		place(old, int32(-float64(dir*w)*p))
		place(cur, int32(float64(dir*w)*(1-p)))
		dwmFlush.Call() // one frame per vblank
		var m [48]byte
		// t == 1, or the next switch is already waiting: like Hyprland, never queue slides behind each other
		if r, _, _ := peekMessage.Call(uintptr(unsafe.Pointer(&m[0])), slideWin, wmApp, wmApp, 0); t == 1 || r != 0 {
			return
		}
	}
}

// emptyWorkspace is "focus --workspace N" for the first empty workspace 1-10, or "".
func emptyWorkspace(g *glaze) string {
	var res struct {
		Data struct {
			Workspaces []struct {
				Name     string
				Children []json.RawMessage
			}
		}
	}
	b, err := g.query("query workspaces")
	if err != nil || json.Unmarshal(b, &res) != nil {
		return ""
	}
	full := map[string]bool{}
	for _, w := range res.Data.Workspaces {
		full[w.Name] = len(w.Children) > 0
	}
	for i := 1; i <= 10; i++ {
		if n := strconv.Itoa(i); !full[n] {
			return "focus --workspace " + n
		}
	}
	return ""
}

// thumb is a live DWM thumbnail of a window on the overlay (what Task View
// uses): the GPU draws it, so no screenshots and no per-frame blits.
type thumb struct {
	id, h uintptr
	r     rect // window rect, overlay coordinates
}

// overlayMu: slides and SUPER+RMB drags (drag.go) share the overlay.
var overlayMu sync.Mutex

// showOverlay covers ov with the wallpaper, under the thumbnails already
// placed: shown fully transparent, painted, then made opaque, so never a blank frame.
func showOverlay(ov rect) {
	w, ht := uintptr(ov.r-ov.l), uintptr(ov.b-ov.t)
	scr, _, _ := getDC.Call(0)
	bg := background(ov, scr)
	releaseDC.Call(0, scr)
	setPos.Call(slideWin, ^uintptr(0), uintptr(ov.l), uintptr(ov.t), w, ht, swpShow) // HWND_TOPMOST
	win, _, _ := getDC.Call(slideWin)
	bitBlt.Call(win, 0, 0, w, ht, bg, 0, 0, srcCopy)
	releaseDC.Call(slideWin, win)
	setLayered.Call(slideWin, 0, 255, lwaAlpha)
	dwmFlush.Call()
}

func hideOverlay() {
	setLayered.Call(slideWin, 0, 0, lwaAlpha)
	dwmFlush.Call()
	setPos.Call(slideWin, 0, 0, 0, 0, 0, swpHide)
}

type thumbProps struct {
	flags      uint32
	dest, src  rect
	opacity    byte
	visible    int32
	clientOnly int32
}

// thumbs registers the shown app windows over ov, bottom first (the last registered draws on top).
func thumbs(ov rect) []thumb {
	listed = nil
	enumWindows.Call(listCB, 0)
	var out []thumb
	for i := len(listed) - 1; i >= 0; i-- {
		var r rect
		windowRect.Call(listed[i], uintptr(unsafe.Pointer(&r)))
		// by the centre: a window's invisible resize border pokes into the next monitor
		if cx, cy := (r.l+r.r)/2, (r.t+r.b)/2; cx < ov.l || cx >= ov.r || cy < ov.t || cy >= ov.b {
			continue // another monitor
		}
		var id uintptr
		if hr, _, _ := dwmRegister.Call(slideWin, listed[i], uintptr(unsafe.Pointer(&id))); hr == 0 {
			out = append(out, thumb{id, listed[i], rect{r.l - ov.l, r.t - ov.t, r.r - ov.l, r.b - ov.t}})
		}
	}
	return out
}

// desktop is a still thumbnail of the desktop window (wallpaper and icons), cropped to ov;
// none when the shell has no desktop (the overlay's painted wallpaper shows then).
func desktop(ov rect) []thumb {
	cls, _ := syscall.UTF16PtrFromString("Progman")
	h, _, _ := findWindow.Call(uintptr(unsafe.Pointer(cls)), 0)
	var id uintptr
	if h == 0 || !visible(h) {
		return nil
	}
	if hr, _, _ := dwmRegister.Call(slideWin, h, uintptr(unsafe.Pointer(&id))); hr != 0 {
		return nil
	}
	var r rect
	windowRect.Call(h, uintptr(unsafe.Pointer(&r)))
	p := thumbProps{flags: 0x1 | 0x2 | 0x4 | 0x8, dest: rect{0, 0, ov.r - ov.l, ov.b - ov.t}, src: rect{ov.l - r.l, ov.t - r.t, ov.r - r.l, ov.b - r.t}, opacity: 255, visible: 1} // RECTDESTINATION|RECTSOURCE|OPACITY|VISIBLE
	dwmUpdate.Call(id, uintptr(unsafe.Pointer(&p)))
	return []thumb{{id, h, r}}
}

// place shows the thumbnails shifted by dx.
func place(ts []thumb, dx int32) {
	for _, t := range ts {
		p := thumbProps{flags: 0x1 | 0x4 | 0x8, dest: rect{t.r.l + dx, t.r.t, t.r.r + dx, t.r.b}, opacity: 255, visible: 1} // RECTDESTINATION|OPACITY|VISIBLE
		dwmUpdate.Call(t.id, uintptr(unsafe.Pointer(&p)))
	}
}

func unregister(ts []thumb) {
	for _, t := range ts {
		dwmUnregister.Call(t.id)
	}
}

// wind is ML4W's "bezier = wind, 0.05, 0.9, 0.1, 1.05" at time t.
func wind(t float64) float64 {
	bez := func(s, p1, p2 float64) float64 { return 3*(1-s)*(1-s)*s*p1 + 3*(1-s)*s*s*p2 + s*s*s }
	lo, hi := 0.0, 1.0
	for i := 0; i < 30; i++ { // x(s) is monotonic: bisect for s
		if m := (lo + hi) / 2; bez(m, 0.05, 0.1) < t {
			lo = m
		} else {
			hi = m
		}
	}
	return bez((lo+hi)/2, 0.9, 1.05)
}

// background is the wallpaper behind the overlay, cached per wallpaper and rect.
func background(ov rect, scr uintptr) uintptr {
	var wp [260]uint16
	sysParams.Call(0x73, uintptr(len(wp)), uintptr(unsafe.Pointer(&wp[0])), 0) // SPI_GETDESKWALLPAPER
	key := syscall.UTF16ToString(wp[:])
	if fi, err := os.Stat(key); err == nil {
		key += fi.ModTime().String()
	}
	key += strconv.Itoa(int(ov.l)) + "," + strconv.Itoa(int(ov.t)) + "," + strconv.Itoa(int(ov.r)) + "," + strconv.Itoa(int(ov.b))
	if bgCache.key != key {
		if bgCache.dc != 0 {
			deleteDC.Call(bgCache.dc)
			deleteObject.Call(bgCache.bmp)
		}
		dc, _, _ := newDC.Call(scr)
		bmp, _, _ := newBitmap.Call(scr, uintptr(ov.r-ov.l), uintptr(ov.b-ov.t))
		selectObject.Call(dc, bmp)
		viewportOrg.Call(dc, uintptr(-ov.l), uintptr(-ov.t), 0) // PaintDesktop draws in screen coordinates
		paintDesktop.Call(dc)
		viewportOrg.Call(dc, 0, 0, 0)
		bgCache.dc, bgCache.bmp, bgCache.key = dc, bmp, key
	}
	return bgCache.dc
}

// fgMonitor is the foreground window's monitor.
func fgMonitor() rect {
	fg, _, _ := getFg.Call()
	m, _, _ := monitorOf.Call(fg, 2)
	mon, _ := monitorRects(m)
	return mon
}

// overlayRect is monitor ov between the bar and the dock, so both stay put.
func overlayRect(ov rect) rect {
	if r, ok := zebarRect("bar", ov); ok && r.b > ov.t && r.t <= ov.t && r.b < ov.b {
		ov.t = r.b
	}
	if r, ok := zebarRect("dock", ov); ok && r.t < ov.b && r.b >= ov.b && r.t > ov.t {
		ov.b = r.t
	}
	return ov
}

// zebarRect is the widget's window on monitor mon: Zebar opens one per monitor.
func zebarRect(widget string, mon rect) (r rect, ok bool) {
	t, _ := syscall.UTF16PtrFromString("Zebar - mw4w / " + widget)
	for h := uintptr(0); ; {
		if h, _, _ = findWindowX.Call(0, h, 0, uintptr(unsafe.Pointer(t))); h == 0 {
			return r, false
		}
		windowRect.Call(h, uintptr(unsafe.Pointer(&r)))
		if cx, cy := (r.l+r.r)/2, (r.t+r.b)/2; visible(h) && cx >= mon.l && cx < mon.r && cy >= mon.t && cy < mon.b {
			return r, true
		}
	}
}

func visible(h uintptr) bool {
	v, _, _ := isVisible.Call(h)
	var cloaked uint32
	dwmAttr.Call(h, dwmCloaked, uintptr(unsafe.Pointer(&cloaked)), 4)
	return v != 0 && cloaked == 0
}

func listWindow(h, _ uintptr) uintptr {
	ex, _, _ := getLong.Call(h, ^uintptr(19))                      // GWL_EXSTYLE
	if h == slideWin || ex&(0x80|0x08000000) != 0 || !visible(h) { // tool / no-activate windows
		return 1
	}
	switch className(h) {
	case "Progman", "WorkerW", "Shell_TrayWnd", "Shell_SecondaryTrayWnd":
		return 1
	}
	if strings.HasPrefix(title(h), "Zebar") {
		return 1
	}
	listed = append(listed, h)
	return 1
}
