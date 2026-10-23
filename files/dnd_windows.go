//go:build windows

package main

// Drag and drop with every other app (Explorer, the desktop, browsers, mail, editors).
//
// Out: the page cancels its HTML drag and asks for a shell drag: SHDoDragDrop with the files' shell
// data object (CF_HDROP and shell IDs, Explorer's drag image and its move/copy rules), so a drop
// anywhere gets the files, not their paths as text.
// In: WebView2 gives a page dropped files without paths; the page hands the File objects back with
// chrome.webview.postMessageWithAdditionalObjects and their paths come from ICoreWebView2File.
// WebView2's interfaces (IIDs, vtable slots) are from WebView2.h of the WebView2 SDK.

import (
	"encoding/json"
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ole32                                 = windows.NewLazySystemDLL("ole32.dll")
	procOleInitialize                     = ole32.NewProc("OleInitialize")
	procSHDoDragDrop                      = shell32.NewProc("SHDoDragDrop")
	procSHParseDisplayName                = shell32.NewProc("SHParseDisplayName")
	procSHCreateShellItemArrayFromIDLists = shell32.NewProc("SHCreateShellItemArrayFromIDLists")
	procILFree                            = shell32.NewProc("ILFree")

	bhidDataObject = windows.GUID{Data1: 0xb8c0bd9f, Data2: 0xed24, Data3: 0x455c, Data4: [8]byte{0x83, 0xe6, 0xd5, 0x39, 0x0c, 0x4f, 0xe8, 0xc4}}
	iidDataObject  = windows.GUID{Data1: 0x0000010e, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidUnknown     = windows.GUID{Data1: 0x00000000, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	// ICoreWebView2WebMessageReceivedEventHandler, ...EventArgs2, ICoreWebView2File
	iidMsgHandler = windows.GUID{Data1: 0x57213f19, Data2: 0x00e6, Data3: 0x49fa, Data4: [8]byte{0x8e, 0x07, 0x89, 0x8e, 0xa0, 0x1e, 0xcb, 0xd2}}
	iidMsgArgs2   = windows.GUID{Data1: 0x06fc7ab7, Data2: 0xc90c, Data3: 0x4297, Data4: [8]byte{0x93, 0x89, 0x33, 0xca, 0x01, 0xcf, 0x6d, 0x5e}}
	iidWVFile     = windows.GUID{Data1: 0xf2c19559, Data2: 0x6bc1, Data3: 0x4583, Data4: [8]byte{0xa7, 0x57, 0x90, 0x02, 0x1b, 0xe9, 0xaf, 0xec}}
)

// comCall calls vtable slot i of a COM object.
//
//go:uintptrescapes
func comCall(obj uintptr, i int, args ...uintptr) uintptr {
	vt := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(vt + uintptr(i)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return r
}

func comRelease(obj uintptr) {
	if obj != 0 {
		comCall(obj, 2)
	}
}

// shellDataObject: the IDataObject Explorer itself would drag or copy for these files.
func shellDataObject(paths []string) (uintptr, error) {
	if len(paths) == 0 {
		return 0, errors.New("nothing to drag")
	}
	pidls := make([]uintptr, 0, len(paths))
	defer func() {
		for _, p := range pidls {
			procILFree.Call(p)
		}
	}()
	for _, p := range paths {
		s, err := windows.UTF16PtrFromString(p)
		if err != nil {
			return 0, err
		}
		var pidl uintptr
		if hr, _, _ := procSHParseDisplayName.Call(uintptr(unsafe.Pointer(s)), 0, uintptr(unsafe.Pointer(&pidl)), 0, 0); hr != 0 {
			return 0, fmt.Errorf("Couldn't drag “%s” (0x%x)", p, uint32(hr))
		}
		pidls = append(pidls, pidl)
	}
	var arr uintptr
	if hr, _, _ := procSHCreateShellItemArrayFromIDLists.Call(uintptr(len(pidls)), uintptr(unsafe.Pointer(&pidls[0])), uintptr(unsafe.Pointer(&arr))); hr != 0 {
		return 0, fmt.Errorf("no shell items (0x%x)", uint32(hr))
	}
	defer comRelease(arr)
	var do uintptr
	// IShellItemArray::BindToHandler(nil, BHID_DataObject, IID_IDataObject, &do)
	if hr := comCall(arr, 3, 0, uintptr(unsafe.Pointer(&bhidDataObject)), uintptr(unsafe.Pointer(&iidDataObject)), uintptr(unsafe.Pointer(&do))); hr != 0 {
		return 0, fmt.Errorf("no data object (0x%x)", uint32(hr))
	}
	return do, nil
}

// dragOut runs the shell drag (modal, on the UI thread) and tells the page when it's over.
func dragOut(w *win, paths []string) {
	eff := uint32(0)
	do, err := shellDataObject(paths)
	if err == nil {
		const copyMoveLink = 1 | 2 | 4
		procSHDoDragDrop.Call(w.hwnd, do, 0, copyMoveLink, uintptr(unsafe.Pointer(&eff)))
		comRelease(do)
		releaseMouse(w.hwnd)
	}
	w.wv.Eval(fmt.Sprintf("window.mw4wDragEnd && mw4wDragEnd(%d)", eff))
}

var (
	procEnumChildWindows = user32.NewProc("EnumChildWindows")
	procGetClassNameW    = user32.NewProc("GetClassNameW")
	procScreenToClient   = user32.NewProc("ScreenToClient")
	findRenderer         = syscall.NewCallback(func(h, out uintptr) uintptr {
		buf := make([]uint16, 64)
		procGetClassNameW.Call(h, uintptr(unsafe.Pointer(&buf[0])), 64)
		if windows.UTF16ToString(buf) == "Chrome_RenderWidgetHostHWND" {
			*(*uintptr)(unsafe.Pointer(out)) = h
			return 0
		}
		return 1
	})
)

// releaseMouse: the drag loop ate the button's release, and the page would still think it held
// (the next move would start another drag): give the page's window its WM_LBUTTONUP.
func releaseMouse(hwnd uintptr) {
	var r uintptr
	procEnumChildWindows.Call(hwnd, findRenderer, uintptr(unsafe.Pointer(&r)))
	if r == 0 {
		return
	}
	var pt struct{ x, y int32 }
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procScreenToClient.Call(r, uintptr(unsafe.Pointer(&pt)))
	procPostMessageW.Call(r, 0x0202, 0, uintptr(uint16(pt.x))|uintptr(uint16(pt.y))<<16) // WM_LBUTTONUP
}

// ---------------------------------------------------------------- drops: file paths for the page

// msgHandler is a COM ICoreWebView2WebMessageReceivedEventHandler, one per window.
type msgHandler struct {
	vtbl *[4]uintptr
	hwnd uintptr
}

var (
	msgVtbl     *[4]uintptr
	msgHandlers = map[uintptr]*msgHandler{} // kept alive while their windows are
)

func msgHandlerFor(hwnd uintptr) *msgHandler {
	if msgVtbl == nil {
		msgVtbl = &[4]uintptr{
			syscall.NewCallback(func(this, riid, out uintptr) uintptr {
				id := (*windows.GUID)(unsafe.Pointer(riid))
				if *id == iidUnknown || *id == iidMsgHandler {
					*(*uintptr)(unsafe.Pointer(out)) = this
					return 0
				}
				*(*uintptr)(unsafe.Pointer(out)) = 0
				return 0x80004002 // E_NOINTERFACE
			}),
			syscall.NewCallback(func(this uintptr) uintptr { return 1 }),
			syscall.NewCallback(func(this uintptr) uintptr { return 1 }),
			syscall.NewCallback(func(this, sender, args uintptr) uintptr {
				onWebMessage((*msgHandler)(unsafe.Pointer(this)).hwnd, args)
				return 0
			}),
		}
	}
	h := &msgHandler{vtbl: msgVtbl, hwnd: hwnd}
	msgHandlers[hwnd] = h
	return h
}

// watchDrops adds our handler next to go-webview2's (which only reads the message string).
func watchDrops(w *win) {
	c := w.wv.GetController()
	if c == nil {
		return
	}
	var wv uintptr
	comCall(uintptr(unsafe.Pointer(c)), 25, uintptr(unsafe.Pointer(&wv))) // ICoreWebView2Controller::get_CoreWebView2
	if wv == 0 {
		return
	}
	defer comRelease(wv)
	var token int64
	comCall(wv, 34, uintptr(unsafe.Pointer(msgHandlerFor(w.hwnd))), uintptr(unsafe.Pointer(&token))) // add_WebMessageReceived
}

// onWebMessage: a message that carries File objects ("drop:<id>") gets their paths sent back as
// mw4wDropped(id, paths); a File with no path on disk (from a browser, say) is "".
func onWebMessage(hwnd, args uintptr) {
	var a2 uintptr
	if comCall(args, 0, uintptr(unsafe.Pointer(&iidMsgArgs2)), uintptr(unsafe.Pointer(&a2))) != 0 || a2 == 0 {
		return // an older runtime: the page uploads the files instead
	}
	defer comRelease(a2)
	var coll uintptr
	if comCall(a2, 6, uintptr(unsafe.Pointer(&coll))) != 0 || coll == 0 { // get_AdditionalObjects
		return
	}
	defer comRelease(coll)
	var n uint32
	comCall(coll, 3, uintptr(unsafe.Pointer(&n))) // get_Count
	if n == 0 {
		return
	}
	var ms *uint16
	comCall(a2, 5, uintptr(unsafe.Pointer(&ms))) // TryGetWebMessageAsString
	msg := windows.UTF16PtrToString(ms)
	windows.CoTaskMemFree(unsafe.Pointer(ms))
	var id int
	if _, err := fmt.Sscanf(msg, "drop:%d", &id); err != nil {
		return
	}
	paths := make([]string, n)
	for i := uint32(0); i < n; i++ {
		var obj, f uintptr
		comCall(coll, 4, uintptr(i), uintptr(unsafe.Pointer(&obj))) // GetValueAtIndex
		if obj == 0 {
			continue
		}
		comCall(obj, 0, uintptr(unsafe.Pointer(&iidWVFile)), uintptr(unsafe.Pointer(&f)))
		comRelease(obj)
		if f == 0 {
			continue
		}
		var s *uint16
		if comCall(f, 3, uintptr(unsafe.Pointer(&s))) == 0 && s != nil { // get_Path
			paths[i] = windows.UTF16PtrToString(s)
			windows.CoTaskMemFree(unsafe.Pointer(s))
		}
		comRelease(f)
	}
	b, _ := json.Marshal(paths)
	post(func() {
		if w := wins[hwnd]; w != nil {
			w.wv.Eval(fmt.Sprintf("window.mw4wDropped && mw4wDropped(%d, %s)", id, b))
		}
	})
}

var procReleaseStgMedium = ole32.NewProc("ReleaseStgMedium")

// dataObjectFiles: the files (CF_HDROP) in an IDataObject, none when it carries something else.
func dataObjectFiles(do uintptr) []string {
	fe := struct {
		cf     uint16
		ptd    uintptr
		aspect uint32
		lindex int32
		tymed  uint32
	}{cf: cfHDROP, aspect: 1, lindex: -1, tymed: 1} // DVASPECT_CONTENT, TYMED_HGLOBAL
	var stg struct {
		tymed   uint32
		hglobal uintptr
		unk     uintptr
	}
	if do == 0 || comCall(do, 3, uintptr(unsafe.Pointer(&fe)), uintptr(unsafe.Pointer(&stg))) != 0 { // GetData
		return nil
	}
	defer procReleaseStgMedium.Call(uintptr(unsafe.Pointer(&stg)))
	n, _, _ := procDragQueryFileW.Call(stg.hglobal, 0xFFFFFFFF, 0, 0)
	paths := make([]string, 0, n)
	for i := uintptr(0); i < n; i++ {
		l, _, _ := procDragQueryFileW.Call(stg.hglobal, i, 0, 0)
		buf := make([]uint16, l+1)
		procDragQueryFileW.Call(stg.hglobal, i, uintptr(unsafe.Pointer(&buf[0])), l+1)
		paths = append(paths, windows.UTF16ToString(buf))
	}
	return paths
}
