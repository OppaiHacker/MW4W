//go:build windows

package main

// The donut anywhere in Windows: hold Ctrl (prefs: donut) and drag files, from Explorer, the desktop,
// Files, a browser's downloads, any app. A low-level mouse hook sees the gesture and shows a
// layered window at the cursor; it is an OLE drop target, so it knows the dragged files the moment
// they enter, offers what they can become, and a drop on a slice converts them (results next to
// the originals). Dropping in the hole, or moving far away, lets the drag go on as it was.
//
// Its own thread: the hook must answer in microseconds whatever Files' windows are doing, or the
// whole system's mouse would stutter.

import (
	"math"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procSetWindowsHookExW   = user32.NewProc("SetWindowsHookExW")
	procCallNextHookEx      = user32.NewProc("CallNextHookEx")
	procGetAsyncKeyState    = user32.NewProc("GetAsyncKeyState")
	procUpdateLayeredWindow = user32.NewProc("UpdateLayeredWindow")
	procSetTimer            = user32.NewProc("SetTimer")
	procKillTimer           = user32.NewProc("KillTimer")
	procGetForegroundWindow = user32.NewProc("GetForegroundWindow")
	procGetWindowRect       = user32.NewProc("GetWindowRect")
	procCreateCompatibleDC  = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection    = gdi32.NewProc("CreateDIBSection")
	procSelectObject        = gdi32.NewProc("SelectObject")
	procDeleteDC            = gdi32.NewProc("DeleteDC")
	procRegisterDragDrop    = ole32.NewProc("RegisterDragDrop")
	procGetDpiForMonitor    = windows.NewLazySystemDLL("shcore.dll").NewProc("GetDpiForMonitor")

	iidDropTarget = windows.GUID{Data1: 0x00000122, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
)

// donutKey: the key held while dragging (prefs.donut): 0x11 Ctrl (default), 0x12 Alt, 0x10 Shift, 0 off.
var donutKey atomic.Uint32

func init() { donutKey.Store(0x11) }

func setDonutKey(k string) {
	switch k {
	case "off":
		donutKey.Store(0)
	case "alt":
		donutKey.Store(0x12)
	case "shift":
		donutKey.Store(0x10)
	default:
		donutKey.Store(0x11)
	}
}

const (
	dpHidden = iota
	dpArmed  // shown, invisible: a drag may come in
	dpShown  // files are over it
	dpBusy   // converting, then the result
)

const (
	wmDnShow  = wmApp + 20 // lParam: x | y<<32 (screen)
	wmDnUp    = wmApp + 21
	wmDnWheel = wmApp + 22
	wmDnFar   = wmApp + 23
	wmDnRun   = wmApp + 24 // wParam: *func() to run on the donut's thread (SendMessage)
	wmTimer   = 0x0113
)

var dropTarget *struct{ vtbl *[7]uintptr }

// all of it lives on the donut's thread
var dn struct {
	hwnd         uintptr
	phase        int
	cx, cy       int32 // center, screen px
	scale        float64
	size         int
	paths        []string
	sides        [][]target
	side, hover  int
	title        string
	alpha        float64
	spin         float64
	job          int64
	result       int
	until        time.Time // hide then (after the result, or a drag that never came)
	tabDown      bool
	down         bool
	fired        bool // the gesture showed it already during this press
	downX, downY int32
	hiding       bool
}

func startDonut() {
	go func() {
		runtime.LockOSThread()
		windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED)
		procOleInitialize.Call(0)
		inst, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)
		cls, _ := windows.UTF16PtrFromString("MW4W.Donut")
		type wndClassEx struct {
			size, style            uint32
			proc                   uintptr
			clsExtra, wndExtra     int32
			inst, icon, cursor, bg uintptr
			menu, class            *uint16
			iconSm                 uintptr
		}
		wc := wndClassEx{proc: syscall.NewCallback(donutProc), inst: inst, class: cls}
		wc.size = uint32(unsafe.Sizeof(wc))
		procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		const exLayered, exTopmost, exToolWindow, exNoActivate = 0x80000, 0x8, 0x80, 0x08000000
		const wsPopup = 0x80000000
		dn.hwnd, _, _ = procCreateWindowExW.Call(exLayered|exTopmost|exToolWindow|exNoActivate, uintptr(unsafe.Pointer(cls)), 0, wsPopup, 0, 0, 1, 1, 0, 0, inst, 0)
		if dn.hwnd == 0 {
			return
		}
		dropTarget = newDropTarget() // kept: COM holds it, Go must not collect it
		if hr, _, _ := procRegisterDragDrop.Call(dn.hwnd, uintptr(unsafe.Pointer(dropTarget))); hr != 0 {
			return
		}
		const whMouseLL = 14
		if h, _, _ := procSetWindowsHookExW.Call(whMouseLL, syscall.NewCallback(donutHook), inst, 0); h == 0 {
			return
		}
		var msg struct {
			hwnd          uintptr
			message       uint32
			wParam, lParm uintptr
			time          uint32
			pt            struct{ x, y int32 }
			private       uint32
		}
		for {
			if r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0); int32(r) <= 0 {
				return
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
		}
	}()
}

// donutHook: the gesture (button down, key held, moved a little) and the wheel. Posts only.
func donutHook(code, wp, lp uintptr) uintptr {
	if int32(code) == 0 {
		ms := (*struct {
			x, y              int32
			data, flags, time uint32
			extra             uintptr
		})(unsafe.Pointer(lp))
		switch wp {
		case 0x0201: // WM_LBUTTONDOWN
			dn.down, dn.fired, dn.downX, dn.downY = true, false, ms.x, ms.y
		case 0x0202: // WM_LBUTTONUP
			dn.down = false
			procPostMessageW.Call(dn.hwnd, wmDnUp, 0, 0)
		case 0x0200: // WM_MOUSEMOVE
			k := donutKey.Load()
			if dn.down && !dn.fired && dn.phase == dpHidden && k != 0 {
				if dx, dy := ms.x-dn.downX, ms.y-dn.downY; dx*dx+dy*dy > 100 {
					if s, _, _ := procGetAsyncKeyState.Call(uintptr(k)); s&0x8000 != 0 {
						dn.phase, dn.fired = dpArmed, true
						procPostMessageW.Call(dn.hwnd, wmDnShow, 0, uintptr(uint32(ms.x))|uintptr(uint32(ms.y))<<32)
					}
				}
			} else if dn.phase == dpShown {
				if dx, dy := float64(ms.x-dn.cx), float64(ms.y-dn.cy); math.Hypot(dx, dy) > dnOuter*dn.scale*2.2 {
					procPostMessageW.Call(dn.hwnd, wmDnFar, 0, 0)
				}
			}
		case 0x020A: // WM_MOUSEWHEEL: the other side
			if dn.phase == dpShown && len(dn.sides) > 1 {
				procPostMessageW.Call(dn.hwnd, wmDnWheel, 0, 0)
				return 1
			}
		}
	}
	r, _, _ := procCallNextHookEx.Call(0, code, wp, lp)
	return r
}

func donutProc(hwnd, msg, wp, lp uintptr) uintptr {
	switch msg {
	case wmDnShow:
		donutShow(int32(uint32(lp)), int32(uint32(lp>>32)))
		return 0
	case wmDnUp:
		switch dn.phase {
		case dpArmed:
			donutHide()
		case dpShown: // dropped elsewhere (a drop on us comes first, or within a moment)
			dn.until = time.Now().Add(150 * time.Millisecond)
			procSetTimer.Call(dn.hwnd, 1, 16, 0)
		}
		return 0
	case wmDnFar:
		if dn.phase == dpShown {
			donutHide()
		}
		return 0
	case wmDnWheel:
		donutFlip()
		return 0
	case wmTimer:
		donutTick()
		return 0
	case wmDnRun:
		(*(*func())(unsafe.Pointer(wp)))()
		return 0
	case 0x0084: // WM_NCHITTEST: only where it's drawn (the layered window's alpha decides)
		return 1
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

func donutShow(x, y int32) {
	if fullscreenApp() {
		dn.phase = dpHidden
		return
	}
	mon, _, _ := procMonitorFromPoint.Call(uintptr(uint32(x))|uintptr(uint32(y))<<32, 2)
	var dx, dy uint32
	dn.scale = 1
	if r, _, _ := procGetDpiForMonitor.Call(mon, 0, uintptr(unsafe.Pointer(&dx)), uintptr(unsafe.Pointer(&dy))); r == 0 && dx > 0 {
		dn.scale = float64(dx) / 96
	}
	dn.size = int(math.Ceil(dnBox * dn.scale))
	// centered on the cursor, kept on the monitor
	mi := struct {
		size      uint32
		mon, work struct{ l, t, r, b int32 }
		flags     uint32
	}{size: 40}
	procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi)))
	h := int32(dn.size / 2)
	dn.cx = min(max(x, mi.mon.l+h), mi.mon.r-h)
	dn.cy = min(max(y, mi.mon.t+h), mi.mon.b-h)
	dn.phase, dn.paths, dn.sides, dn.side, dn.hover, dn.alpha, dn.result, dn.hiding = dpArmed, nil, nil, 0, -1, 0, 0, false
	// a drag that never shows up (a text selection with Ctrl held): gone in a few seconds anyway
	dn.until = time.Now().Add(8 * time.Second)
	donutPaint()
	procShowWindow.Call(dn.hwnd, 4) // SW_SHOWNOACTIVATE
	procSetTimer.Call(dn.hwnd, 1, 16, 0)
}

func donutHide() {
	dn.phase, dn.paths = dpHidden, nil
	procKillTimer.Call(dn.hwnd, 1)
	procShowWindow.Call(dn.hwnd, swHide)
}

// fullscreenApp: a game or a video in front: no donut over it.
func fullscreenApp() bool {
	fg, _, _ := procGetForegroundWindow.Call()
	if fg == 0 {
		return false
	}
	var r struct{ l, t, r, b int32 }
	procGetWindowRect.Call(fg, uintptr(unsafe.Pointer(&r)))
	mon, _, _ := procMonitorFromPoint.Call(uintptr(uint32(r.l+1))|uintptr(uint32(r.t+1))<<32, 2)
	mi := struct {
		size      uint32
		mon, work struct{ l, t, r, b int32 }
		flags     uint32
	}{size: 40}
	procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi)))
	cls := make([]uint16, 64)
	procGetClassNameW.Call(fg, uintptr(unsafe.Pointer(&cls[0])), 64)
	switch windows.UTF16ToString(cls) {
	case "Progman", "WorkerW", "Shell_TrayWnd": // the desktop is "fullscreen" too
		return false
	}
	return r.l <= mi.mon.l && r.t <= mi.mon.t && r.r >= mi.mon.r && r.b >= mi.mon.b
}

func donutSlices() []target {
	if len(dn.sides) == 0 {
		return nil
	}
	return dn.sides[dn.side]
}

func donutFlip() {
	if len(dn.sides) > 1 {
		dn.side = (dn.side + 1) % len(dn.sides)
		dn.hover = -1
		donutPaint()
	}
}

func donutState() donutView {
	v := donutView{slices: donutSlices(), hover: dn.hover, sides: len(dn.sides), side: dn.side, title: dn.title, alpha: dn.alpha}
	switch dn.phase {
	case dpArmed:
		v.invis = true
	case dpBusy:
		v.working = dn.result == 0
		v.done = dn.result
		v.spin = dn.spin
		for _, j := range convJobs.snapshot() {
			if j.ID == dn.job && j.Total > 0 {
				v.prog = float64(j.Done) / float64(j.Total)
			}
		}
	}
	if s := donutSlices(); dn.hover >= 0 && dn.hover < len(s) {
		v.sub = s[dn.hover].Hint
	}
	return v
}

// donutPaint: the picture into the layered window (premultiplied BGRA, per-pixel alpha).
func donutPaint() {
	img := drawDonut(donutState(), dn.scale)
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	screen, _, _ := procGetDC.Call(0)
	defer procReleaseDC.Call(0, screen)
	mem, _, _ := procCreateCompatibleDC.Call(screen)
	defer procDeleteDC.Call(mem)
	bi := struct {
		size                  uint32
		w, h                  int32
		planes, bpp           uint16
		compression, imgSize  uint32
		xppm, yppm            int32
		clrUsed, clrImportant uint32
	}{size: 40, w: int32(w), h: -int32(h), planes: 1, bpp: 32}
	var bits unsafe.Pointer
	bmp, _, _ := procCreateDIBSection.Call(mem, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 {
		return
	}
	defer procDeleteObject.Call(bmp)
	px := unsafe.Slice((*byte)(bits), w*h*4)
	for i := 0; i < len(px); i += 4 {
		px[i], px[i+1], px[i+2], px[i+3] = img.Pix[i+2], img.Pix[i+1], img.Pix[i], img.Pix[i+3]
	}
	old, _, _ := procSelectObject.Call(mem, bmp)
	defer procSelectObject.Call(mem, old)
	dst := struct{ x, y int32 }{dn.cx - int32(w/2), dn.cy - int32(h/2)}
	size := struct{ w, h int32 }{int32(w), int32(h)}
	src := struct{ x, y int32 }{}
	blend := uint32(0) | 255<<16 | 1<<24 // AC_SRC_OVER, flags 0, alpha 255, AC_SRC_ALPHA
	const ulwAlpha = 2
	procUpdateLayeredWindow.Call(dn.hwnd, screen, uintptr(unsafe.Pointer(&dst)), uintptr(unsafe.Pointer(&size)), mem, uintptr(unsafe.Pointer(&src)), 0, uintptr(unsafe.Pointer(&blend)), ulwAlpha)
}

// donutTick: the fade in, the spinner and progress, the result, and leaving.
func donutTick() {
	now := time.Now()
	switch dn.phase {
	case dpHidden:
		procKillTimer.Call(dn.hwnd, 1)
		return
	case dpArmed:
		if now.After(dn.until) || !dn.down {
			donutHide()
		}
		return
	case dpShown:
		if !dn.until.IsZero() && now.After(dn.until) && !dn.down {
			donutHide()
			return
		}
		// Tab: the other side (polled: the drag loop has the keyboard)
		s, _, _ := procGetAsyncKeyState.Call(0x09)
		if tab := s&0x8000 != 0; tab != dn.tabDown {
			dn.tabDown = tab
			if tab {
				donutFlip()
			}
		}
		if dn.alpha < 1 {
			dn.alpha = math.Min(1, dn.alpha+0.2)
			donutPaint()
		}
	case dpBusy:
		dn.spin += 0.18
		if dn.result == 0 {
			for _, j := range convJobs.snapshot() {
				if j.ID == dn.job && j.Ended != 0 {
					dn.result = 1
					if len(j.Errors) > 0 {
						dn.result = -1
						if len(j.Out) > 0 {
							dn.result = 1
						}
					}
					dn.until = now.Add(900 * time.Millisecond)
				}
			}
		} else if now.After(dn.until) {
			dn.alpha -= 0.15
			if dn.alpha <= 0 {
				donutHide()
				return
			}
		}
		donutPaint()
	}
}

// ---------------------------------------------------------------- IDropTarget

func newDropTarget() *struct{ vtbl *[7]uintptr } {
	effect := func(p uintptr, e uint32) {
		if p != 0 {
			*(*uint32)(unsafe.Pointer(p)) = e
		}
	}
	const copyEffect = 1
	pt := func(v uintptr) (float64, float64) {
		x, y := int32(uint32(v)), int32(uint32(v>>32))
		return float64(x - dn.cx), float64(y - dn.cy)
	}
	hover := func(v uintptr) {
		dx, dy := pt(v)
		h := sliceAt(dx, dy, len(donutSlices()), dn.scale)
		if h != dn.hover {
			dn.hover = h
			donutPaint()
		}
	}
	vt := &[7]uintptr{
		syscall.NewCallback(func(this, riid, out uintptr) uintptr {
			id := (*windows.GUID)(unsafe.Pointer(riid))
			if *id == iidUnknown || *id == iidDropTarget {
				*(*uintptr)(unsafe.Pointer(out)) = this
				return 0
			}
			*(*uintptr)(unsafe.Pointer(out)) = 0
			return 0x80004002
		}),
		syscall.NewCallback(func(this uintptr) uintptr { return 1 }),
		syscall.NewCallback(func(this uintptr) uintptr { return 1 }),
		// DragEnter(data, keys, pt, *effect)
		syscall.NewCallback(func(this, data, keys, p, eff uintptr) uintptr {
			if dn.phase != dpArmed && dn.phase != dpShown {
				effect(eff, 0)
				return 0
			}
			if dn.phase == dpArmed {
				paths := dataObjectFiles(data)
				if len(paths) == 0 {
					effect(eff, 0) // not files: stay out of the way
					return 0
				}
				dn.paths = paths
				var front, back []target
				for _, t := range targetsFor(paths) {
					if t.Back || len(front) >= 8 {
						back = append(back, t)
					} else {
						front = append(front, t)
					}
				}
				dn.sides = [][]target{front}
				if len(back) > 0 {
					dn.sides = append(dn.sides, back)
				}
				dn.title = filepath.Base(paths[0])
				if len(paths) > 1 {
					dn.title = strconv.Itoa(len(paths)) + " files"
				}
				dn.phase, dn.alpha, dn.until = dpShown, 0.2, time.Time{}
			}
			hover(p)
			donutPaint()
			effect(eff, map[bool]uint32{true: copyEffect, false: 0}[dn.hover >= 0])
			return 0
		}),
		// DragOver(keys, pt, *effect)
		syscall.NewCallback(func(this, keys, p, eff uintptr) uintptr {
			if dn.phase != dpShown {
				effect(eff, 0)
				return 0
			}
			hover(p)
			effect(eff, map[bool]uint32{true: copyEffect, false: 0}[dn.hover >= 0])
			return 0
		}),
		// DragLeave
		syscall.NewCallback(func(this uintptr) uintptr {
			if dn.phase == dpShown && dn.hover >= 0 {
				dn.hover = -1
				donutPaint()
			}
			return 0
		}),
		// Drop(data, keys, pt, *effect): "copy" to the source whatever happens (it keeps its files)
		syscall.NewCallback(func(this, data, keys, p, eff uintptr) uintptr {
			if dn.phase != dpShown {
				effect(eff, 0)
				return 0
			}
			dx, dy := pt(p)
			s := donutSlices()
			i := sliceAt(dx, dy, len(s), dn.scale)
			if i < 0 {
				effect(eff, 0)
				donutHide()
				return 0
			}
			effect(eff, copyEffect)
			j := convJobs.start(dn.paths, s[i], nil)
			dn.phase, dn.job, dn.hover, dn.alpha = dpBusy, j.ID, i, 1
			donutPaint()
			procSetTimer.Call(dn.hwnd, 1, 16, 0)
			return 0
		}),
	}
	return &struct{ vtbl *[7]uintptr }{vt}
}
