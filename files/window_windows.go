//go:build windows

package main

// The windows: WebView2 in frameless top-level windows (the page draws the header; Windows keeps
// the shadow, rounded corners, snapping and an acrylic backdrop behind the translucent page).
// All windows live on one UI thread. A hidden window is kept loaded at home, so "open a window"
// is showing it and navigating in-page (a few ms); closing a window hides it for reuse when
// there's no spare one yet.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

var (
	dwmapi = windows.NewLazySystemDLL("dwmapi.dll")

	procRegisterClassExW         = user32.NewProc("RegisterClassExW")
	procCreateWindowExW          = user32.NewProc("CreateWindowExW")
	procDefWindowProcW           = user32.NewProc("DefWindowProcW")
	procDestroyWindow            = user32.NewProc("DestroyWindow")
	procShowWindow               = user32.NewProc("ShowWindow")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procGetMessageW              = user32.NewProc("GetMessageW")
	procTranslateMessage         = user32.NewProc("TranslateMessage")
	procDispatchMessageW         = user32.NewProc("DispatchMessageW")
	procPostMessageW             = user32.NewProc("PostMessageW")
	procPostThreadMessageW       = user32.NewProc("PostThreadMessageW")
	procSendMessageW             = user32.NewProc("SendMessageW")
	procReleaseCapture           = user32.NewProc("ReleaseCapture")
	procIsZoomed                 = user32.NewProc("IsZoomed")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procSetWindowTextW           = user32.NewProc("SetWindowTextW")
	procSetWindowPos             = user32.NewProc("SetWindowPos")
	procLoadCursorW              = user32.NewProc("LoadCursorW")
	procLoadImageW               = user32.NewProc("LoadImageW")
	procGetDpiForWindow          = user32.NewProc("GetDpiForWindow")
	procGetDpiForSystem          = user32.NewProc("GetDpiForSystem")
	procGetSystemMetricsForDpi   = user32.NewProc("GetSystemMetricsForDpi")
	procSetProcessDpiAwareCtx    = user32.NewProc("SetProcessDpiAwarenessContext")
	procMonitorFromPoint         = user32.NewProc("MonitorFromPoint")
	procGetMonitorInfoW          = user32.NewProc("GetMonitorInfoW")
	procGetCursorPos             = user32.NewProc("GetCursorPos")
	procDwmSetWindowAttribute    = dwmapi.NewProc("DwmSetWindowAttribute")
	procDwmExtendFrameIntoClient = dwmapi.NewProc("DwmExtendFrameIntoClientArea")
)

const (
	wmDestroy     = 0x0002
	wmMove        = 0x0003
	wmSize        = 0x0005
	wmActivate    = 0x0006
	wmClose       = 0x0010
	wmQuit        = 0x0012
	wmGetMinMax   = 0x0024
	wmNcCalcSize  = 0x0083
	wmNcLButtonDn = 0x00A1
	wmMoving      = 0x0216
	wmDpiChanged  = 0x02E0
	wmApp         = 0x8000
	wmAppWork     = wmApp + 1 // UI thread: run queued funcs
	wmAppCmd      = wmApp + 2 // a window: page command (wParam = index into cmds)
	htCaption     = 2
	swHide        = 0
	swShowNormal  = 1
	swMaximize    = 3
	swRestore     = 9
	wsOverlapped  = 0x00CF0000 // WS_OVERLAPPEDWINDOW
	wsExAppWindow = 0x00040000
	cwUseDefault  = 0x80000000
	smCxFrame     = 32
	smCxPadded    = 92
	swpFlags      = 0x0001 | 0x0002 | 0x0004 | 0x0010 | 0x0020 // NOSIZE NOMOVE NOZORDER NOACTIVATE FRAMECHANGED
)

type win struct {
	hwnd uintptr
	wv   *edge.Chromium
}

var (
	app       *App
	uiThread  uint32
	wins      = map[uintptr]*win{}
	warm      *win
	className *uint16
	dataPath  string
	baseURL   string
	workMu    sync.Mutex
	work      []func()
	cmdMu     sync.Mutex
	cmds      = map[uintptr][]string{}
	clipHwnd  uintptr
	uiReady   = make(chan struct{})
)

func runUI(a *App, path string, show bool) {
	runtime.LockOSThread()
	app = a
	uiThread = windows.GetCurrentThreadId()
	procSetProcessDpiAwareCtx.Call(^uintptr(3)) // PER_MONITOR_AWARE_V2 (-4)
	// transparent until the page paints (no white flash) and behind it (the acrylic shows through)
	os.Setenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR", "00000000")
	dataPath = filepath.Join(dataDir(), "webview")
	baseURL = fmt.Sprintf("http://127.0.0.1:%d/?t=%s", a.port, a.token)

	inst, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)
	className, _ = windows.UTF16PtrFromString("MW4W.Files")
	cursor, _, _ := procLoadCursorW.Call(0, 32512) // IDC_ARROW
	shell32.Load()
	icon, _, _ := procLoadImageW.Call(shell32.Handle(), 4, 1, 0, 0, 0x40|0x8000) // shell32 #4: folder; LR_DEFAULTSIZE|LR_SHARED
	type wndClassEx struct {
		size, style            uint32
		proc                   uintptr
		clsExtra, wndExtra     int32
		inst, icon, cursor, bg uintptr
		menu, class            *uint16
		iconSm                 uintptr
	}
	wc := wndClassEx{style: 0x0003, proc: syscall.NewCallback(wndProc), inst: inst, icon: icon, cursor: cursor, class: className, iconSm: icon}
	wc.size = uint32(unsafe.Sizeof(wc))
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	// a message-only window owns our clipboard data (needs this thread's message loop)
	static, _ := windows.UTF16PtrFromString("STATIC")
	clipHwnd, _, _ = procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(static)), 0, 0, 0, 0, 0, 0, ^uintptr(2) /* HWND_MESSAGE */, 0, inst, 0)

	a.openWindow = func(p string) { post(func() { openWin(p) }) }
	a.quit = func() { procPostThreadMessageW.Call(uintptr(uiThread), wmQuit, 0, 0) }
	close(uiReady)

	if show {
		openWin(path)
	}
	go func() {
		time.Sleep(1500 * time.Millisecond)
		post(ensureWarm)
	}()

	var msg struct {
		hwnd          uintptr
		message       uint32
		wParam, lParm uintptr
		time          uint32
		pt            struct{ x, y int32 }
		private       uint32
	}
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		if msg.hwnd == 0 && msg.message == wmAppWork {
			runWork()
			continue
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

// post runs f on the UI thread.
func post(f func()) {
	workMu.Lock()
	work = append(work, f)
	workMu.Unlock()
	procPostThreadMessageW.Call(uintptr(uiThread), wmAppWork, 0, 0)
}

func runWork() {
	for {
		workMu.Lock()
		q := work
		work = nil
		workMu.Unlock()
		if len(q) == 0 {
			return
		}
		for _, f := range q {
			f()
		}
	}
}

// uiDo runs f on the UI thread and waits (clipboard calls need the window that owns it).
func uiDo(f func()) {
	select {
	case <-uiReady:
	case <-time.After(3 * time.Second):
		return
	}
	if windows.GetCurrentThreadId() == uiThread {
		f()
		return
	}
	done := make(chan struct{})
	post(func() { f(); close(done) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

func clipOwner() uintptr { return clipHwnd }

// openWin shows a window at path: the warm one if there is one (instant), else a new one.
func openWin(path string) {
	if w := warm; w != nil {
		warm = nil
		b, _ := json.Marshal(path)
		w.wv.Eval("window.mw4wOpen && mw4wOpen(" + string(b) + ")")
		showWin(w)
		go func() {
			time.Sleep(1500 * time.Millisecond)
			post(ensureWarm)
		}()
		return
	}
	if w := newWin("&p="+url.QueryEscape(path), true); w != nil {
		showWin(w)
	}
	go func() {
		time.Sleep(1500 * time.Millisecond)
		post(ensureWarm)
	}()
}

func ensureWarm() {
	if warm == nil {
		warm = newWin("&warm=1", false)
	}
}

func showWin(w *win) {
	procShowWindow.Call(w.hwnd, swShowNormal)
	procSetForegroundWindow.Call(w.hwnd)
	w.wv.Resize()
	w.wv.Focus()
}

func dpiScale(hwnd uintptr, v int) int {
	dpi, _, _ := procGetDpiForWindow.Call(hwnd)
	if dpi == 0 {
		dpi, _, _ = procGetDpiForSystem.Call()
	}
	return v * int(dpi) / 96
}

func newWin(query string, visible bool) *win {
	inst, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)
	title, _ := windows.UTF16PtrFromString("Files")
	// centred on the monitor under the mouse (GlazeWM tiles it anyway)
	dpi, _, _ := procGetDpiForSystem.Call()
	w, h := 1180*int(dpi)/96, 760*int(dpi)/96
	x, y := int(cwUseDefault), int(cwUseDefault)
	var pt struct{ x, y int32 }
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	mon, _, _ := procMonitorFromPoint.Call(uintptr(uint32(pt.x))|uintptr(uint32(pt.y))<<32, 2)
	mi := struct {
		size      uint32
		mon, work struct{ l, t, r, b int32 }
		flags     uint32
	}{size: 40}
	if r, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); r != 0 {
		ww, wh := int(mi.work.r-mi.work.l), int(mi.work.b-mi.work.t)
		w, h = min(w, ww-40), min(h, wh-40)
		x, y = int(mi.work.l)+(ww-w)/2, int(mi.work.t)+(wh-h)/2
	}
	hwnd, _, _ := procCreateWindowExW.Call(wsExAppWindow, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(title)),
		wsOverlapped, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 0, 0, inst, 0)
	if hwnd == 0 {
		return nil
	}
	// dark frame, acrylic backdrop, client area over the whole window (the page is the title bar)
	one := int32(1)
	procDwmSetWindowAttribute.Call(hwnd, 20, uintptr(unsafe.Pointer(&one)), 4) // DWMWA_USE_IMMERSIVE_DARK_MODE
	backdrop := int32(3)                                                       // DWMSBT_TRANSIENTWINDOW (acrylic)
	procDwmSetWindowAttribute.Call(hwnd, 38, uintptr(unsafe.Pointer(&backdrop)), 4)
	round := int32(2) // DWMWCP_ROUND
	procDwmSetWindowAttribute.Call(hwnd, 33, uintptr(unsafe.Pointer(&round)), 4)
	margins := [4]int32{-1, -1, -1, -1}
	procDwmExtendFrameIntoClient.Call(hwnd, uintptr(unsafe.Pointer(&margins)))
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpFlags)

	wv := edge.NewChromium()
	wv.DataPath = dataPath
	wn := &win{hwnd: hwnd, wv: wv}
	wins[hwnd] = wn
	wv.MessageCallback = func(m string) {
		// act after WebView2's callback returns (moving/sizing runs a modal loop)
		cmdMu.Lock()
		cmds[hwnd] = append(cmds[hwnd], m)
		cmdMu.Unlock()
		procPostMessageW.Call(hwnd, wmAppCmd, 0, 0)
	}
	wv.SetPermission(edge.CoreWebView2PermissionKindClipboardRead, edge.CoreWebView2PermissionStateAllow)
	if !wv.Embed(hwnd) {
		delete(wins, hwnd)
		procDestroyWindow.Call(hwnd)
		return nil
	}
	debug := os.Getenv("MW4W_FILES_DEBUG") != ""
	if s, err := wv.GetSettings(); err == nil {
		s.PutAreDefaultContextMenusEnabled(debug)
		s.PutAreDevToolsEnabled(debug)
		s.PutIsStatusBarEnabled(false)
		s.PutIsZoomControlEnabled(false) // Ctrl+wheel zooms icons, not the page
		s.PutAreBrowserAcceleratorKeysEnabled(debug)
		s.PutIsPinchZoomEnabled(false)
	}
	if c := wv.GetController(); c != nil {
		if c2 := c.GetICoreWebView2Controller2(); c2 != nil {
			c2.PutDefaultBackgroundColor(edge.COREWEBVIEW2_COLOR{})
		}
	}
	wv.Resize()
	wv.Navigate(baseURL + query)
	return wn
}

func pageCommand(w *win, m string) {
	switch {
	case m == "drag" || strings.HasPrefix(m, "size:"):
		ht := uintptr(htCaption)
		if strings.HasPrefix(m, "size:") {
			var n int
			fmt.Sscan(m[5:], &n)
			ht = uintptr(n)
		}
		procReleaseCapture.Call()
		procSendMessageW.Call(w.hwnd, wmNcLButtonDn, ht, 0)
	case m == "max":
		if z, _, _ := procIsZoomed.Call(w.hwnd); z != 0 {
			procShowWindow.Call(w.hwnd, swRestore)
		} else {
			procShowWindow.Call(w.hwnd, swMaximize)
		}
	case m == "min":
		procShowWindow.Call(w.hwnd, 6) // SW_MINIMIZE
	case m == "close":
		procPostMessageW.Call(w.hwnd, wmClose, 0, 0)
	case strings.HasPrefix(m, "title:"):
		t, _ := windows.UTF16PtrFromString(m[6:] + " — Files")
		procSetWindowTextW.Call(w.hwnd, uintptr(unsafe.Pointer(t)))
	}
}

func wndProc(hwnd, msg, wp, lp uintptr) uintptr {
	w := wins[hwnd]
	switch msg {
	case wmNcCalcSize:
		if wp != 0 {
			// no title bar or borders: the client is the whole window; maximized, keep it on screen
			if z, _, _ := procIsZoomed.Call(hwnd); z != 0 {
				dpi, _, _ := procGetDpiForWindow.Call(hwnd)
				fx, _, _ := procGetSystemMetricsForDpi.Call(smCxFrame, dpi)
				pad, _, _ := procGetSystemMetricsForDpi.Call(smCxPadded, dpi)
				b := int32(fx + pad)
				r := (*[4]int32)(unsafe.Pointer(lp))
				r[0] += b
				r[1] += b
				r[2] -= b
				r[3] -= b
			}
			return 0
		}
	case wmSize:
		if w != nil {
			w.wv.Resize()
			z, _, _ := procIsZoomed.Call(hwnd)
			w.wv.Eval(fmt.Sprintf("document.documentElement.classList.toggle('max', %v)", z != 0))
		}
	case wmMove, wmMoving:
		if w != nil {
			w.wv.NotifyParentWindowPositionChanged()
		}
	case wmActivate:
		if w != nil && wp&0xFFFF != 0 {
			w.wv.Focus()
		}
	case wmGetMinMax:
		mm := (*[10]int32)(unsafe.Pointer(lp)) // MINMAXINFO: ptMinTrackSize at [6],[7]
		mm[6], mm[7] = int32(dpiScale(hwnd, 520)), int32(dpiScale(hwnd, 360))
		return 0
	case wmDpiChanged:
		r := (*[4]int32)(unsafe.Pointer(lp))
		procSetWindowPos.Call(hwnd, 0, uintptr(r[0]), uintptr(r[1]), uintptr(r[2]-r[0]), uintptr(r[3]-r[1]), 0x0004|0x0010)
		return 0
	case wmAppCmd:
		cmdMu.Lock()
		q := cmds[hwnd]
		delete(cmds, hwnd)
		cmdMu.Unlock()
		if w != nil {
			for _, m := range q {
				pageCommand(w, m)
			}
		}
		return 0
	case wmClose:
		// keep one closed window as the warm spare (already loaded: the next open is instant)
		if w != nil && warm == nil {
			procShowWindow.Call(hwnd, swHide)
			w.wv.Eval("window.mw4wOpen && mw4wOpen('')")
			warm = w
			return 0
		}
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		delete(wins, hwnd)
		if warm == w {
			warm = nil
		}
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}
