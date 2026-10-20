// Multi-monitor and fullscreen rules GlazeWM lacks, the same for every app
// and window (nothing here knows an app by name):
//
//   - A fullscreen window (a game, a video) belongs to the monitor it is
//     fullscreen on. GlazeWM keeps it on its workspace's monitor: one that
//     goes fullscreen on another monitor is pulled back there (and pushes
//     back), and a switch of that workspace hides it, though it's on screen
//     elsewhere. followFullscreen moves it to the workspace shown where it is.
//   - Focus follows the mouse (GlazeWM's own is off), but never off a
//     fullscreen window: a game lost the focus, and minimized, as soon as the
//     cursor touched the next monitor. A click still moves the focus.
//   - Minimize (onMinimize, onForeground): only the active window minimized
//     from its title bar comes back at once. A window minimizing itself in
//     the background, like a fullscreen game that lost the focus, stays so
//     until it gets the focus again, like on the taskbar.
//   - Workspaces go back to a monitor that comes back (homes.go).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

var (
	isIconic   = user32.NewProc("IsIconic")
	isZoomed   = user32.NewProc("IsZoomed")
	monitorAt  = user32.NewProc("MonitorFromPoint")
	cursorPos  = user32.NewProc("GetCursorPos")
	setCursor  = user32.NewProc("SetCursorPos")
	desktopWin = user32.NewProc("GetDesktopWindow")

	lastFg uintptr // window events thread only: the foreground window, in event order

	claims      = make(chan claim, 64)   // windows that went fullscreen
	fgWake      = make(chan struct{}, 1) // the foreground window changed
	displayWake = make(chan struct{}, 1) // WM_DISPLAYCHANGE
	glazeWake   = make(chan struct{}, 1) // a GlazeWM workspace / monitor / focus event
	forcedNext  atomic.Bool              // monitors came or went, or GlazeWM (re)started
	addedMu     sync.Mutex
	added       = map[string]bool{}      // monitor keys GlazeWM added since the last look; "*": all (GlazeWM started)
	ffmWake     = make(chan struct{}, 1) // the mouse moved
	cursorAt    atomic.Uint64            // x<<32 | y, from the mouse hook
	ffmSetting  = struct{ on, read atomic.Int64 }{}
)

type claim struct {
	h   uintptr
	mon rect
}

func wake(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

func iconic(h uintptr) bool { r, _, _ := isIconic.Call(h); return r != 0 }
func zoomed(h uintptr) bool { r, _, _ := isZoomed.Call(h); return r != 0 }

func rootOf(h uintptr) uintptr { r, _, _ := ancestor.Call(h, 2); return r } // GA_ROOT

func shellWindow(h uintptr) bool {
	switch className(h) {
	case "Progman", "WorkerW", "Shell_TrayWnd", "Shell_SecondaryTrayWnd":
		return true
	}
	return false
}

// fullscreen: top-level window h covers its whole monitor (mon), like a game
// or a video, and isn't just maximized: GlazeWM's own test.
func fullscreen(h uintptr) (mon rect, ok bool) {
	if h == 0 || h == slideWin || iconic(h) || zoomed(h) || shellWindow(h) {
		return
	}
	var r rect
	windowRect.Call(h, uintptr(unsafe.Pointer(&r)))
	m, _, _ := monitorOf.Call(h, 2) // MONITOR_DEFAULTTONEAREST
	mon, _ = monitorRects(m)
	return mon, r.l <= mon.l+1 && r.t <= mon.t+1 && r.r >= mon.r-1 && r.b >= mon.b-1
}

func isFullscreen(h uintptr) bool { _, ok := fullscreen(h); return ok }

// ---------- window events (their own thread, see main) ----------

// onForeground: a minimized window that gets the focus (the dock, a switch
// to its workspace) is restored, like a click on its taskbar button does.
func onForeground(_, _, hwnd, idObject, _, _, _ uintptr) uintptr {
	if idObject != 0 {
		return 0
	}
	lastFg = hwnd
	if iconic(hwnd) && rootOf(hwnd) == hwnd {
		showAsync.Call(hwnd, 9) // SW_RESTORE
	}
	wake(fgWake)
	return 0
}

// onMinimize undoes minimizing the active window (its title bar button or
// menu): Hyprland has no minimize. A window that minimizes itself while
// another one has the focus is left alone until it is focused again
// (onForeground): games in exclusive fullscreen do that when they lose the
// focus, and restoring them right away fought them for the screen.
func onMinimize(_, _, hwnd, idObject, _, _, _ uintptr) uintptr {
	if idObject == 0 && hwnd == lastFg && !strings.HasPrefix(title(hwnd), "Zebar") {
		showAsync.Call(hwnd, 9) // SW_RESTORE, without waiting on the app
	}
	return 0
}

// onLocation notes top-level windows that are fullscreen after a move, for follow().
func onLocation(_, _, hwnd, idObject, idChild, _, _ uintptr) uintptr {
	if idObject != 0 || idChild != 0 || rootOf(hwnd) != hwnd { // the cursor, carets, child windows
		return 0
	}
	if mon, ok := fullscreen(hwnd); ok {
		select {
		case claims <- claim{hwnd, mon}:
		default:
		}
	}
	return 0
}

// ---------- GlazeWM layout ----------

type gMonitor struct {
	X, Y, Width, Height int32
	HasFocus            bool
	DeviceName          string
	DevicePath          *string
	HardwareId          *string
	Children            []node // workspaces
}

func (m *gMonitor) rect() rect { return rect{m.X, m.Y, m.X + m.Width, m.Y + m.Height} }

// key names the physical monitor, the same after it was unplugged and plugged in again.
func (m *gMonitor) key() string {
	switch {
	case m.DevicePath != nil && *m.DevicePath != "":
		return *m.DevicePath
	case m.HardwareId != nil && *m.HardwareId != "":
		return *m.HardwareId
	}
	return m.DeviceName
}

func (m *gMonitor) shown() *node {
	for i := range m.Children {
		if m.Children[i].IsDisplayed {
			return &m.Children[i]
		}
	}
	return nil
}

func inside(r rect, x, y int32) bool { return x >= r.l && x < r.r && y >= r.t && y < r.b }

func center(r rect) (int32, int32) { return (r.l + r.r) / 2, (r.t + r.b) / 2 }

// layout returns GlazeWM's monitors (with their workspaces and windows), dialing again after a GlazeWM restart.
func layout(g *glaze) (*glaze, []gMonitor) {
	for try := 0; try < 2; try++ {
		if g == nil {
			var err error
			if g, err = dialGlaze(); err != nil {
				return nil, nil
			}
		}
		var res struct{ Data struct{ Monitors []gMonitor } }
		b, err := g.query("query monitors")
		if err == nil && json.Unmarshal(b, &res) == nil && len(res.Data.Monitors) > 0 {
			return g, res.Data.Monitors
		}
		g.c.Close()
		g = nil
	}
	return nil, nil
}

// managedWindow returns GlazeWM's window h and its monitor.
func managedWindow(mons []gMonitor, h uintptr) (*node, *gMonitor) {
	for i := range mons {
		for k := range mons[i].Children {
			if p := mons[i].Children[k].path(h); p != nil {
				return p[len(p)-1], &mons[i]
			}
		}
	}
	return nil, nil
}

// focusedIn is GlazeWM's focused container, a window or an (empty) workspace,
// and its monitor. hasFocus marks the whole path down to it.
func focusedIn(mons []gMonitor) (*node, *gMonitor) {
	var find func(n *node) *node
	find = func(n *node) *node {
		for i := range n.Children {
			if f := find(&n.Children[i]); f != nil {
				return f
			}
		}
		if n.HasFocus {
			return n
		}
		return nil
	}
	for i := range mons {
		for k := range mons[i].Children {
			if f := find(&mons[i].Children[k]); f != nil {
				return f, &mons[i]
			}
		}
	}
	return nil, nil
}

// ---------- screens: fullscreen windows and workspace homes ----------

func screens() {
	var (
		g      *glaze
		h      = loadHomes()
		settle <-chan time.Time // a display change is still going on
		look   <-chan time.Time
		burst  = map[uintptr]rect{} // each window's first fullscreen monitor in a burst of moves
		follow <-chan time.Time
		wait   bool // a restore waits for the fullscreen window to lose the focus
		moved  = map[uintptr][]time.Time{}
		tries  = map[string]int{}
	)
	go subscribe()
	look = time.After(time.Second)
	for {
		select {
		case <-glazeWake:
			if look == nil {
				look = time.After(400 * time.Millisecond)
			}
		case <-fgWake:
			if wait && look == nil {
				look = time.After(400 * time.Millisecond)
			}
		case <-displayWake:
			settle = time.After(3 * time.Second) // restarted by every change: monitors come up one by one
		case <-settle:
			settle, look = nil, time.After(0)
		case c := <-claims:
			if _, ok := burst[c.h]; !ok {
				burst[c.h] = c.mon
			}
			if follow == nil {
				follow = time.After(150 * time.Millisecond)
			}
		case <-follow:
			follow = nil
			if settle == nil && !dragging.Load() {
				g = followFullscreen(g, burst, moved)
			}
			burst = map[uintptr]rect{}
		case <-look:
			look = nil
			if settle == nil { // it looks once the monitors settled
				g, wait = arrange(g, h, tries)
			}
		}
	}
}

// subscribe wakes screens on GlazeWM's workspace, monitor and focus events.
func subscribe() {
	for {
		if g, err := dialGlaze(); err == nil {
			// GlazeWM (re)started, or winmask did: it may have laid the workspaces out anew
			addMonitor("*")
			wake(glazeWake)
			if g.ask("sub -e workspace_activated workspace_deactivated workspace_updated focus_changed "+
				"monitor_added monitor_removed user_config_changed") == nil {
				for {
					g.c.SetDeadline(time.Time{})
					b, err := g.read()
					if err != nil {
						break
					}
					var ev struct {
						Data struct {
							EventType    string
							AddedMonitor gMonitor
						}
					}
					json.Unmarshal(b, &ev)
					switch ev.Data.EventType {
					case "monitor_added":
						addMonitor(ev.Data.AddedMonitor.key())
					case "monitor_removed":
						forcedNext.Store(true)
					}
					wake(glazeWake)
				}
			}
			g.c.Close()
		}
		time.Sleep(time.Second)
	}
}

func addMonitor(key string) {
	addedMu.Lock()
	added[key] = true
	addedMu.Unlock()
	forcedNext.Store(true)
}

// followFullscreen moves each window that went fullscreen on another monitor
// than its workspace's to the workspace shown there, keeping its focus.
func followFullscreen(g *glaze, burst map[uintptr]rect, moved map[uintptr][]time.Time) *glaze {
	for hwnd, mon := range burst {
		var mons []gMonitor
		if g, mons = layout(g); g == nil {
			return nil
		}
		w, at := managedWindow(mons, hwnd)
		if w == nil || w.State.Type == "minimized" || !visible(hwnd) {
			continue // not GlazeWM's, or on a hidden workspace: GlazeWM moved it along with that one
		}
		x, y := center(mon)
		if inside(at.rect(), x, y) {
			continue // fullscreen where GlazeWM has it
		}
		var to *node
		for i := range mons {
			if inside(mons[i].rect(), x, y) {
				to = mons[i].shown()
			}
		}
		// never fight an app that keeps jumping: twice in 10s at most
		recent := moved[hwnd][:0]
		for _, t := range moved[hwnd] {
			if time.Since(t) < 10*time.Second {
				recent = append(recent, t)
			}
		}
		moved[hwnd] = recent
		if to == nil || len(recent) >= 2 {
			continue
		}
		moved[hwnd] = append(recent, time.Now())
		fg, _, _ := getFg.Call()
		g.ask("command --id " + w.ID + " move --workspace " + to.Name)
		if fg == hwnd { // GlazeWM gives the focus to the old workspace
			g.ask("command focus --container-id " + w.ID)
		}
	}
	for h := range moved {
		if len(moved[h]) > 0 && time.Since(moved[h][len(moved[h])-1]) > 10*time.Second {
			delete(moved, h)
		}
	}
	return g
}

// arrange moves workspaces back to their monitors (homes.go). wait: it would
// have to take the focus from a fullscreen window, so it waits until that one
// loses it.
func arrange(g *glaze, h *homes, tries map[string]int) (*glaze, bool) {
	var mons []gMonitor
	if g, mons = layout(g); g == nil {
		return nil, false
	}
	forced := forcedNext.Swap(false)
	addedMu.Lock()
	back := added
	added = map[string]bool{}
	addedMu.Unlock()
	if forced {
		clear(tries)
	}
	present := map[string]bool{}
	byDevice := map[string]string{}
	var ws []wsAt
	for i := range mons {
		k := mons[i].key()
		present[k] = true
		byDevice[mons[i].DeviceName] = k
		for _, w := range mons[i].Children {
			ws = append(ws, wsAt{w.Name, k, w.IsDisplayed})
		}
	}
	bound := map[string]string{}
	var s struct{ MonitorWorkspaces map[string][]any }
	readJSON("settings.json", &s)
	for dev, names := range s.MonitorWorkspaces {
		for _, n := range names {
			bound[fmt.Sprint(n)] = byDevice[dev]
		}
	}
	if back["*"] {
		back = present
	}
	before := h.save()
	moves, show := h.plan(ws, present, bound, forced, back)
	if len(moves)+len(show) > 0 {
		if fg, _, _ := getFg.Call(); isFullscreen(fg) {
			if forced { // the next look still knows GlazeWM moved them
				for k := range back {
					addMonitor(k)
				}
				forcedNext.Store(true)
			}
			return g, true
		}
		var pt [2]int32
		cursorPos.Call(uintptr(unsafe.Pointer(&pt)))
		prev, prevMon := focusedIn(mons)
		for n, k := range moves {
			if tries[n]++; tries[n] <= 3 && g != nil { // GlazeWM can't get it there: don't keep trying
				g = moveWorkspace(g, n, k)
			}
		}
		if g, mons = layout(g); g == nil {
			return nil, false
		}
		showOn := map[string]bool{}
		for _, n := range show {
			for i := range mons { // only once it got home: elsewhere it would hide what's shown there
				for _, w := range mons[i].Children {
					if w.Name == n && mons[i].key() == h.Home[n] && !w.IsDisplayed {
						g.ask("command focus --workspace " + n)
						showOn[h.Home[n]] = true
					}
				}
			}
		}
		// the focus goes back where it was, unless that is a workspace just hidden by one shown
		if prev != nil && len(show) > 0 && !showOn[prevMon.key()] {
			if prev.Type == "window" {
				g.ask("command focus --container-id " + prev.ID)
			} else {
				g.ask("command focus --workspace " + prev.Name)
			}
		}
		setCursor.Call(uintptr(pt[0]), uintptr(pt[1])) // GlazeWM's cursor jump follows the focus around
	}
	if after := h.save(); !bytes.Equal(before, after) {
		os.MkdirAll(filepath.Join(packDir(), "cache"), 0o755)
		os.WriteFile(homesPath(), after, 0o644)
	}
	return g, false
}

// moveWorkspace takes workspace name to monitor key, a neighbour at a time
// (GlazeWM only moves workspaces by direction). It stays hidden there.
func moveWorkspace(g *glaze, name, key string) *glaze {
	for step := 0; step < 4; step++ {
		var mons []gMonitor
		if g, mons = layout(g); g == nil {
			return nil
		}
		var from, to *gMonitor
		var id string
		for i := range mons {
			if mons[i].key() == key {
				to = &mons[i]
			}
			for _, w := range mons[i].Children {
				if w.Name == name {
					from, id = &mons[i], w.ID
				}
			}
		}
		if from == nil || to == nil || from == to {
			return g
		}
		fx, fy := center(from.rect())
		tx, ty := center(to.rect())
		dx, dy := tx-fx, ty-fy
		dir := "left"
		switch {
		case abs(dy) > abs(dx) && dy > 0:
			dir = "down"
		case abs(dy) > abs(dx):
			dir = "up"
		case dx > 0:
			dir = "right"
		}
		if g.ask("command --id "+id+" move-workspace --direction "+dir) != nil {
			return g
		}
	}
	return g
}

func homesPath() string { return filepath.Join(packDir(), "cache", "workspaces.json") }

func loadHomes() *homes {
	h := newHomes()
	if b, err := os.ReadFile(homesPath()); err == nil {
		json.Unmarshal(b, h)
	}
	for _, m := range []*map[string]string{&h.Home, &h.Shown} {
		if *m == nil {
			*m = map[string]string{}
		}
	}
	if h.Displaced == nil {
		h.Displaced = map[string]bool{}
	}
	return h
}

func (h *homes) save() []byte { b, _ := json.Marshal(h); return b }

// ---------- focus follows the mouse ----------

// ffmOn is the focusFollowsMouse setting, read again at most every 2s.
func ffmOn() bool {
	if now := time.Now().Unix(); now-ffmSetting.read.Load() >= 2 {
		var s, d struct{ FocusFollowsMouse *bool }
		readJSON("defaults.json", &d)
		readJSON("settings.json", &s)
		if s.FocusFollowsMouse == nil {
			s.FocusFollowsMouse = d.FocusFollowsMouse
		}
		on := int64(0)
		if s.FocusFollowsMouse != nil && *s.FocusFollowsMouse {
			on = 1
		}
		ffmSetting.on.Store(on)
		ffmSetting.read.Store(now)
	}
	return ffmSetting.on.Load() == 1
}

func buttonsDown() bool {
	for _, vk := range []uintptr{0x01, 0x02, 0x04} { // VK_LBUTTON, VK_RBUTTON, VK_MBUTTON
		if r, _, _ := keyState.Call(vk); r&0x8000 != 0 {
			return true
		}
	}
	return false
}

// follower focuses the window under the cursor, or its monitor over the
// desktop, like GlazeWM's focus_follows_cursor (turned off in its config).
// Not while a button is held (a drag), while a popup or other window
// GlazeWM doesn't manage has the focus, or off a fullscreen window.
func follower() {
	var (
		g    *glaze
		last [3]uintptr // window and monitor under the cursor, foreground window: done already
	)
	for range ffmWake {
		time.Sleep(30 * time.Millisecond) // the newest position only, like GlazeWM's 50ms
		select {
		case <-ffmWake:
		default:
		}
		if !ffmOn() || dragging.Load() || buttonsDown() {
			continue
		}
		v := cursorAt.Load()
		x, y := int32(v>>32), int32(v)
		h := dragTarget(x, y) // 0 over the desktop, taskbar and Zebar
		m, _, _ := monitorAt.Call(uintptr(uint32(x))|uintptr(uint32(y))<<32, 2)
		fg, _, _ := getFg.Call()
		if h == fg || [3]uintptr{h, m, fg} == last {
			continue
		}
		last = [3]uintptr{h, m, fg}
		if isFullscreen(fg) {
			continue // a fullscreen window keeps the focus until a click
		}
		g = focusUnder(g, h, x, y, fg)
	}
}

func focusUnder(g *glaze, h uintptr, x, y int32, fg uintptr) *glaze {
	var mons []gMonitor
	if g, mons = layout(g); g == nil {
		return nil
	}
	f, _ := focusedIn(mons)
	if f == nil {
		return g
	}
	desk, _, _ := desktopWin.Call()
	if f.Type == "window" && f.Handle != fg || f.Type != "window" && fg != 0 && fg != desk && !shellWindow(fg) {
		return g // GlazeWM's focus isn't the foreground: a popup or an unmanaged window has it
	}
	if w, _ := managedWindow(mons, h); h != 0 && w != nil {
		if w.ID != f.ID && w.State.Type != "minimized" {
			g.ask("command focus --container-id " + w.ID)
		}
		return g
	}
	for i := range mons {
		if inside(mons[i].rect(), x, y) && !mons[i].HasFocus {
			g.ask(fmt.Sprintf("command focus --monitor %d", i))
		}
	}
	return g
}
