// winmask: SUPER never opens the Start menu, like on Hyprland (CTRL+Esc still
// does).
//
// GlazeWM swallows the keydown of a bound key, so Windows only sees
// "Win down, Win up" and opens Start. Like AutoHotkey, we inject the unused
// vkE8 "mask" key right before Win is released, which Windows reads as a
// combo. Must be started AFTER GlazeWM so this hook runs first in the
// low-level hook chain (GlazeWM startup_commands does it).
//
// It also gives MW4W popups (launcher, power menu, ...) keyboard focus: Zebar
// opens them from a background process, which Windows' foreground lock keeps
// from activating its own window.
//
// SUPER+left/right drag moves/resizes windows, see drag.go. Windows can't be
// minimized and ALT+Tab is off. Workspace keybinds slide, see slide.go.
//
// --hide-taskbar hides the Windows taskbar completely (not just auto-hide) and
// keeps it hidden when Explorer shows it again. --exit stops the running
// instance and shows the taskbar.
package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	whKeyboardLL   = 13
	wmKeyDown      = 0x0100
	wmKeyUp        = 0x0101
	wmSysKeyDown   = 0x0104
	wmSysKeyUp     = 0x0105
	vkLWin         = 0x5B
	vkRWin         = 0x5C
	vkMask         = 0xE8
	vkTab          = 0x09
	llkhfAltDown   = 0x20
	inputKeyboard  = 1
	keyeventfKeyUp = 0x2
)

type kbdllhookstruct struct {
	vkCode, scanCode, flags, time uint32
	extra                         uintptr
}

// INPUT for keyboard, padded to the size of the union on amd64 (40 bytes).
type input struct {
	typ    uint32
	_      uint32
	vk, sc uint16
	flags  uint32
	time   uint32
	_      uint32
	extra  uintptr
	_      [8]byte
}

var (
	user32       = syscall.NewLazyDLL("user32.dll")
	setHook      = user32.NewProc("SetWindowsHookExW")
	callNext     = user32.NewProc("CallNextHookEx")
	getMessage   = user32.NewProc("GetMessageW")
	dispatch     = user32.NewProc("DispatchMessageW")
	sendInput    = user32.NewProc("SendInput")
	winEventHook = user32.NewProc("SetWinEventHook")
	getText      = user32.NewProc("InternalGetWindowText") // never sends messages, so a hung app can't stall us
	getFg        = user32.NewProc("GetForegroundWindow")
	threadOf     = user32.NewProc("GetWindowThreadProcessId")
	attachInput  = user32.NewProc("AttachThreadInput")
	setFg        = user32.NewProc("SetForegroundWindow")
	toTop        = user32.NewProc("BringWindowToTop")
	getClass     = user32.NewProc("GetClassNameW")
	showWindow   = user32.NewProc("ShowWindow")
	findWindowEx = user32.NewProc("FindWindowExW")
	showAsync    = user32.NewProc("ShowWindowAsync")
	dpiAware     = user32.NewProc("SetProcessDpiAwarenessContext")
	getLong      = user32.NewProc("GetWindowLongPtrW")
	redrawWake   = make(chan struct{}, 1)
	curThread    = syscall.NewLazyDLL("kernel32.dll").NewProc("GetCurrentThreadId")
	winDown      bool
	hideTaskbar  bool
)

func className(h uintptr) string {
	var b [64]uint16
	n, _, _ := getClass.Call(h, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
	return syscall.UTF16ToString(b[:n])
}

func isTaskbar(h uintptr) bool {
	c := className(h)
	return c == "Shell_TrayWnd" || c == "Shell_SecondaryTrayWnd"
}

// setTaskbars shows (SW_SHOWNA) or hides (SW_HIDE) the taskbar on every monitor.
func setTaskbars(show bool) {
	cmd := uintptr(0)
	if show {
		cmd = 8
	}
	for _, c := range []string{"Shell_TrayWnd", "Shell_SecondaryTrayWnd"} {
		cls, _ := syscall.UTF16PtrFromString(c)
		for h := uintptr(0); ; {
			h, _, _ = findWindowEx.Call(0, h, uintptr(unsafe.Pointer(cls)), 0)
			if h == 0 {
				break
			}
			showWindow.Call(h, cmd)
		}
	}
}

func mask() {
	in := [2]input{
		{typ: inputKeyboard, vk: vkMask},
		{typ: inputKeyboard, vk: vkMask, flags: keyeventfKeyUp},
	}
	sendInput.Call(2, uintptr(unsafe.Pointer(&in[0])), unsafe.Sizeof(in[0]))
}

func hook(code int, wparam uintptr, lparam uintptr) uintptr {
	if code == 0 {
		k := (*kbdllhookstruct)(unsafe.Pointer(lparam))
		isWin := k.vkCode == vkLWin || k.vkCode == vkRWin
		down := wparam == wmKeyDown || wparam == wmSysKeyDown
		up := wparam == wmKeyUp || wparam == wmSysKeyUp
		switch {
		case isWin && down:
			winDown = true
		case isWin && up:
			mask()
			winDown = false
		case k.vkCode == vkTab && down && k.flags&llkhfAltDown != 0 && !winDown:
			return 1 // ALT+Tab does nothing, like on Hyprland
		case !isWin && (down || up) && workspaceKey(k.vkCode, down):
			return 1 // slide.go switches the workspace
		}
	}
	r, _, _ := callNext.Call(0, uintptr(code), wparam, lparam)
	return r
}

// Hook callbacks and the message loop must stay on one OS thread.
func init() { runtime.LockOSThread() }

func title(h uintptr) string {
	var b [128]uint16
	n, _, _ := getText.Call(h, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
	return syscall.UTF16ToString(b[:n])
}

// onMinimize undoes minimizing (title bar button, Win+D, ...): Hyprland has
// no minimize. ponytail: also restores exclusive-fullscreen games that
// minimize on alt-tab; skip them here if that ever bites.
func onMinimize(_, _, hwnd, idObject, _, _, _ uintptr) uintptr {
	if idObject == 0 && !strings.HasPrefix(title(hwnd), "Zebar") {
		showAsync.Call(hwnd, 9) // SW_RESTORE, without waiting on the app
	}
	return 0
}

// appWindow: a top-level window with a title bar, the kind GlazeWM tiles.
func appWindow(h uintptr) bool {
	root, _, _ := ancestor.Call(h, 2)                              // GA_ROOT
	style, _, _ := getLong.Call(h, ^uintptr(15))                   // GWL_STYLE (-16)
	ex, _, _ := getLong.Call(h, ^uintptr(19))                      // GWL_EXSTYLE (-20)
	return root == h && style&0xC00000 == 0xC00000 && ex&0x80 == 0 // WS_CAPTION, not WS_EX_TOOLWINDOW
}

// redrawer re-applies GlazeWM's layout shortly after a new window appears:
// apps like Zen/Firefox size their new (popup) window themselves after
// GlazeWM tiled it, and GlazeWM keeps that wrong size until the window is
// touched. Hyprland never lets a client resize its tile.
// ponytail: fixed 0.3s + 1s retries, an app resizing later still slips through.
func redrawer() {
	for range redrawWake {
		for _, d := range []time.Duration{300, 700} {
			time.Sleep(d * time.Millisecond)
			if dragging.Load() {
				continue // a redraw would snap the dragged window back
			}
			if g, err := dialGlaze(); err == nil {
				g.ask("command wm-redraw")
				g.c.Close()
			}
		}
	}
}

// onShow focuses Zebar "mw4w" popups (every widget but the bar and dock) when they appear.
func onShow(_, _, hwnd, idObject, _, _, _ uintptr) uintptr {
	if idObject != 0 {
		return 0
	}
	if appWindow(hwnd) && !strings.HasPrefix(title(hwnd), "Zebar") {
		select {
		case redrawWake <- struct{}{}:
		default:
		}
	}
	if hideTaskbar && isTaskbar(hwnd) {
		showWindow.Call(hwnd, 0)
		return 0
	}
	t := title(hwnd)
	if !strings.HasPrefix(t, "Zebar - mw4w / ") || t == "Zebar - mw4w / bar" || t == "Zebar - mw4w / dock" {
		return 0
	}
	focusWindow(hwnd)
	return 0
}

func focusWindow(h uintptr) {
	mask() // input sent by us lifts the foreground lock for us
	fg, _, _ := getFg.Call()
	ft, _, _ := threadOf.Call(fg, 0)
	me, _, _ := curThread.Call()
	attachInput.Call(me, ft, 1)
	toTop.Call(h)
	setFg.Call(h)
	attachInput.Call(me, ft, 0)
}

// Keybind actions run here, off the keyboard hook: they talk to other
// processes, which must never stall the hook.
var actions = make(chan func(), 4)

func act(f func()) {
	select {
	case actions <- f:
	default:
	}
}

func actor() {
	runtime.LockOSThread()
	for f := range actions {
		f()
	}
}

// popupWindow is the (usually hidden, preloaded) Zebar popup, or 0.
func popupWindow(name string) uintptr {
	t, _ := syscall.UTF16PtrFromString("Zebar - mw4w / " + name)
	h, _, _ := findWindow.Call(0, uintptr(unsafe.Pointer(t)))
	return h
}

// togglePopup shows a preloaded popup at once; its key closes it again, like rofi.
// The page hides itself (Escape, blur), keeping its WebView2 warm for next time.
func togglePopup(h uintptr) {
	if fg, _, _ := getFg.Call(); fg == h {
		showWindow.Call(h, 0) // the page sees the blur and resets itself
		return
	}
	showWindow.Call(h, 5) // SW_SHOW
	focusWindow(h)
}

var envVar = regexp.MustCompile(`%([^%]+)%`)

// launch runs a "shell-exec" keybind with CreateProcess (~50ms here). GlazeWM
// uses ShellExecuteEx: ~0.3s, its main loop waits on it, and a program it can't
// find freezes it behind an error dialog. What CreateProcess can't start (URIs,
// App Paths names like msedge, elevation) still goes to GlazeWM.
func launch(c string) {
	orig := c
	hide := strings.HasPrefix(c, "--hide-window ")
	c = strings.TrimPrefix(c, "--hide-window ")
	// {terminal} etc., like mw4w.ps1's Expand-Command
	var d, s struct{ Apps map[string]string }
	readJSON("defaults.json", &d)
	readJSON("settings.json", &s)
	for _, apps := range []map[string]string{s.Apps, d.Apps} {
		for k, v := range apps {
			c = strings.ReplaceAll(c, "{"+k+"}", v)
		}
	}
	c = envVar.ReplaceAllStringFunc(c, func(m string) string {
		if v, ok := os.LookupEnv(m[1 : len(m)-1]); ok {
			return v
		}
		return m
	})
	if exe, args := program(c); exe != "" {
		cmd := exec.Command(exe, args...)
		cmd.Dir = os.Getenv("USERPROFILE")
		if hide {
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
		}
		if cmd.Start() == nil {
			cmd.Process.Release()
			return
		}
	}
	if g, err := dialGlaze(); err == nil {
		g.ask("command shell-exec " + orig)
		g.c.Close()
	}
}

// program splits "C:\Program Files\X\x.exe arg" like GlazeWM: the longest
// space-separated prefix that is an executable.
func program(c string) (string, []string) {
	parts := strings.Fields(c)
	for k := len(parts); k > 0; k-- {
		if exe, err := exec.LookPath(strings.Join(parts[:k], " ")); err == nil {
			return exe, parts[k:]
		}
	}
	return "", nil
}

var (
	kernel32  = syscall.NewLazyDLL("kernel32.dll")
	imageName = kernel32.NewProc("QueryFullProcessImageNameW")
)

// killActive kills the process of the focused window (SUPER+SHIFT+Q).
func killActive() {
	fg, _, _ := getFg.Call()
	var pid uint32
	threadOf.Call(fg, uintptr(unsafe.Pointer(&pid)))
	p, err := syscall.OpenProcess(0x1|0x1000, false, pid) // PROCESS_TERMINATE | PROCESS_QUERY_LIMITED_INFORMATION
	if err == nil {
		defer syscall.CloseHandle(p)
		var b [260]uint16
		n := uint32(len(b))
		imageName.Call(uintptr(p), 0, uintptr(unsafe.Pointer(&b[0])), uintptr(unsafe.Pointer(&n)))
		switch strings.ToLower(filepath.Base(syscall.UTF16ToString(b[:n]))) {
		case "explorer.exe", "glazewm.exe", "zebar.exe", "winmask.exe", "powershell.exe", "csrss.exe", "dwm.exe", "winlogon.exe":
			return
		}
		err = syscall.TerminateProcess(p, 1)
	}
	if err != nil { // elevated app: fall back to a normal close
		if g, err := dialGlaze(); err == nil {
			g.ask("command close")
			g.c.Close()
		}
	}
}

// ---------- mw4w.ps1 worker ----------
// Every "@mw4w <command>" keybind runs "winmask --mw4w <command>", which hands
// the command to the running winmask: it keeps one PowerShell ("mw4w.ps1
// serve") warm, so commands skip PowerShell's ~1.2s start.

var (
	sendTimeout = user32.NewProc("SendMessageTimeoutW")
	worker      io.WriteCloser // slider thread only
)

type copyData struct {
	data uintptr
	size uint32
	ptr  *uint16
}

func mw4wArgs(args ...string) *exec.Cmd {
	c := exec.Command("powershell", append([]string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(packDir(), "scripts", "mw4w.ps1")}, args...)...)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return c
}

// mw4wClient sends "<command> [arg]" to the running winmask, or runs mw4w.ps1 itself.
func mw4wClient(line string) {
	cls, _ := syscall.UTF16PtrFromString("mw4w-slide")
	if h, _, _ := findWindow.Call(uintptr(unsafe.Pointer(cls)), 0); h != 0 {
		s, _ := syscall.UTF16FromString(line)
		cd := copyData{size: uint32(len(s) * 2), ptr: &s[0]}
		var ok uintptr
		// WM_COPYDATA, SMTO_ABORTIFHUNG; waits out a running slide
		sendTimeout.Call(h, 0x004A, 0, uintptr(unsafe.Pointer(&cd)), 0x2, 3000, uintptr(unsafe.Pointer(&ok)))
		if ok == 1 {
			return
		}
	}
	mw4wArgs(strings.SplitN(line, " ", 2)...).Run()
}

func startWorker() {
	c := mw4wArgs("serve")
	if w, err := c.StdinPipe(); err == nil && c.Start() == nil {
		worker = w
	}
}

// toWorker runs on the slider thread (WM_COPYDATA). 1 = handed over.
func toWorker(line string) uintptr {
	for try := 0; try < 2; try++ {
		if worker == nil {
			startWorker()
		}
		if worker == nil {
			return 0
		}
		if _, err := io.WriteString(worker, line+"\n"); err == nil {
			return 1
		}
		worker.Close() // it died: start a new one
		worker = nil
	}
	return 0
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--mw4w" {
		if len(os.Args) > 2 {
			mw4wClient(strings.Join(os.Args[2:], " "))
		}
		return
	}
	// Newest instance wins: its hook must sit above GlazeWM's in the chain.
	kill := exec.Command("taskkill", "/F", "/IM", "winmask.exe", "/FI", "PID ne "+strconv.Itoa(os.Getpid()))
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	kill.Run()

	for _, a := range os.Args[1:] {
		switch a {
		case "--hide-taskbar":
			hideTaskbar = true
		case "--exit":
			setTaskbars(true)
			return
		}
	}
	setTaskbars(!hideTaskbar)
	// per-monitor aware, so hook coordinates and window rects use the same pixels
	dpiAware.Call(^uintptr(3)) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4)

	// Window events get their own thread: a slow callback there must never
	// delay the low-level hooks below, which hold up every key and mouse move.
	go func() {
		runtime.LockOSThread()
		// EVENT_OBJECT_SHOW, out of context
		winEventHook.Call(0x8002, 0x8002, 0, syscall.NewCallback(onShow), 0, 0, 0)
		// EVENT_SYSTEM_MINIMIZESTART
		winEventHook.Call(0x0016, 0x0016, 0, syscall.NewCallback(onMinimize), 0, 0, 0)
		// EVENT_SYSTEM_MOVESIZESTART
		winEventHook.Call(0x000A, 0x000A, 0, syscall.NewCallback(onMoveSizeStart), 0, 0, 0)
		pump()
	}()
	go dragger()
	go redrawer()
	go actor()
	loadWorkspaceKeys()
	go slider()

	h, _, err := setHook.Call(whKeyboardLL, syscall.NewCallback(hook), 0, 0)
	if h == 0 {
		panic(err)
	}
	if h, _, err := setHook.Call(whMouseLL, syscall.NewCallback(mouseHook), 0, 0); h == 0 {
		panic(err)
	}
	pump()
}

func pump() {
	var msg [48]byte
	for {
		if r, _, _ := getMessage.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0); int32(r) <= 0 {
			os.Exit(0)
		}
		dispatch.Call(uintptr(unsafe.Pointer(&msg[0]))) // slide.go's overlay gets its WM_APP this way
	}
}
