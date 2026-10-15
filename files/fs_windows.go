//go:build windows

package main

// Windows: drives, known folders, the Recycle Bin, shell operations (SHFileOperation: the system
// copy engine with its progress, conflict and undo), the clipboard (CF_HDROP, interchangeable with
// every other app) and shell thumbnails.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const caseInsensitive = true

var (
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")

	procSHFileOperationW       = shell32.NewProc("SHFileOperationW")
	procShellExecuteW          = shell32.NewProc("ShellExecuteW")
	procSHOpenWithDialog       = shell32.NewProc("SHOpenWithDialog")
	procSHObjectProperties     = shell32.NewProc("SHObjectProperties")
	procSHEmptyRecycleBinW     = shell32.NewProc("SHEmptyRecycleBinW")
	procDragQueryFileW         = shell32.NewProc("DragQueryFileW")
	procSHCreateItemFromParsed = shell32.NewProc("SHCreateItemFromParsingName")

	procOpenClipboard            = user32.NewProc("OpenClipboard")
	procCloseClipboard           = user32.NewProc("CloseClipboard")
	procEmptyClipboard           = user32.NewProc("EmptyClipboard")
	procSetClipboardData         = user32.NewProc("SetClipboardData")
	procGetClipboardData         = user32.NewProc("GetClipboardData")
	procRegisterClipboardFormatW = user32.NewProc("RegisterClipboardFormatW")
	procAllowSetForegroundWindow = user32.NewProc("AllowSetForegroundWindow")
	procGetDC                    = user32.NewProc("GetDC")
	procReleaseDC                = user32.NewProc("ReleaseDC")
	procSetThreadPriority        = kernel32.NewProc("SetThreadPriority")
	procGetCurrentThread         = kernel32.NewProc("GetCurrentThread")
	procGlobalAlloc              = kernel32.NewProc("GlobalAlloc")
	procGlobalLock               = kernel32.NewProc("GlobalLock")
	procGlobalUnlock             = kernel32.NewProc("GlobalUnlock")
	procGlobalFree               = kernel32.NewProc("GlobalFree")
	procGetObjectW               = gdi32.NewProc("GetObjectW")
	procGetDIBits                = gdi32.NewProc("GetDIBits")
	procDeleteObject             = gdi32.NewProc("DeleteObject")
)

// isHidden: the Hidden attribute, and dotfiles (.git, .config: Linux habits).
func isHidden(name string, fi fs.FileInfo) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok {
		return d.FileAttributes&windows.FILE_ATTRIBUTE_HIDDEN != 0
	}
	return false
}

// lowPriority: background mode for this thread (low CPU, disk and memory priority), so indexing
// never competes with what the user is doing.
func lowPriority() {
	const threadModeBackgroundBegin = 0x00010000
	h, _, _ := procGetCurrentThread.Call()
	procSetThreadPriority.Call(h, threadModeBackgroundBegin)
}

var instanceMutex windows.Handle

func singleInstance() bool {
	name, _ := windows.UTF16PtrFromString(`Local\mw4w-files`)
	h, err := windows.CreateMutex(nil, true, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		windows.CloseHandle(h)
		return false
	}
	instanceMutex = h
	return true
}

// allowForeground lets the resident process bring its window to the front for us.
func allowForeground() {
	procAllowSetForegroundWindow.Call(uintptr(0xFFFFFFFF)) // ASFW_ANY
}

// defaultRoots: the profile, and every other fixed drive whole (C:\ outside the profile is
// mostly Windows and programs).
func defaultRoots() []string {
	roots := []string{homeDir()}
	sys := strings.ToUpper(filepath.VolumeName(os.Getenv("SystemRoot")))
	for _, d := range driveLetters() {
		if t := driveType(d); t == windows.DRIVE_FIXED && !strings.EqualFold(d[:2], sys) {
			roots = append(roots, d)
		}
	}
	return roots
}

func driveLetters() []string {
	buf := make([]uint16, 256)
	n, err := windows.GetLogicalDriveStrings(uint32(len(buf)), &buf[0])
	if err != nil {
		return nil
	}
	var out []string
	for _, s := range strings.Split(string(utf16.Decode(buf[:n])), "\x00") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func driveType(root string) uint32 {
	p, _ := windows.UTF16PtrFromString(root)
	return windows.GetDriveType(p)
}

func drives() []place {
	var out []place
	for _, d := range driveLetters() {
		t := driveType(d)
		if t != windows.DRIVE_FIXED && t != windows.DRIVE_REMOVABLE && t != windows.DRIVE_REMOTE && t != windows.DRIVE_CDROM {
			continue
		}
		p, _ := windows.UTF16PtrFromString(d)
		var free, total, totalFree uint64
		if windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree) != nil {
			continue // an empty card reader or a drive with no disc
		}
		label := make([]uint16, 261)
		windows.GetVolumeInformation(p, &label[0], uint32(len(label)), nil, nil, nil, nil, 0)
		name := windows.UTF16ToString(label)
		icon := "drive"
		switch t {
		case windows.DRIVE_REMOVABLE:
			icon = "usb"
			if name == "" {
				name = "USB Drive"
			}
		case windows.DRIVE_REMOTE:
			icon = "net"
			if name == "" {
				name = "Network Drive"
			}
		default:
			if name == "" {
				name = "Local Disk"
			}
		}
		out = append(out, place{Name: fmt.Sprintf("%s (%s)", name, d[:2]), Path: d, Icon: icon, Total: int64(total), Free: int64(free)})
	}
	return out
}

func knownDirs() []place {
	var out []place
	for _, k := range []struct {
		id         *windows.KNOWNFOLDERID
		name, icon string
	}{
		{windows.FOLDERID_Desktop, "Desktop", "desktop"},
		{windows.FOLDERID_Documents, "Documents", "documents"},
		{windows.FOLDERID_Downloads, "Downloads", "downloads"},
		{windows.FOLDERID_Music, "Music", "music"},
		{windows.FOLDERID_Pictures, "Pictures", "pictures"},
		{windows.FOLDERID_Videos, "Videos", "videos"},
	} {
		if p, err := windows.KnownFolderPath(k.id, 0); err == nil {
			if fi, err := os.Stat(p); err == nil && fi.IsDir() {
				out = append(out, place{Name: k.name, Path: p, Icon: k.icon})
			}
		}
	}
	return out
}

// ---------------------------------------------------------------- shell calls

// sta runs f on a fresh thread in a COM single-threaded apartment (what shell APIs expect).
func sta(f func() error) error {
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE)
		defer windows.CoUninitialize()
		done <- f()
	}()
	return <-done
}

func shellOpen(p string) error {
	return sta(func() error {
		verb, _ := windows.UTF16PtrFromString("open")
		file, _ := windows.UTF16PtrFromString(p)
		var dir *uint16
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			dir, _ = windows.UTF16PtrFromString(filepath.Dir(p))
		}
		r, _, _ := procShellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)), 0, uintptr(unsafe.Pointer(dir)), windows.SW_SHOWNORMAL)
		if r <= 32 {
			if r == 31 { // SE_ERR_NOASSOC
				return openWith(p)
			}
			return fmt.Errorf("Couldn't open “%s” (%d)", filepath.Base(p), r)
		}
		return nil
	})
}

// openWith shows Windows' "How do you want to open this file?" (modal: it gets its own thread).
func openWith(p string) error {
	go sta(func() error {
		type openAsInfo struct {
			file, class *uint16
			flags       uint32
		}
		f, _ := windows.UTF16PtrFromString(p)
		info := openAsInfo{file: f, flags: 1 | 2 | 4} // allow registration, register ext, exec
		procSHOpenWithDialog.Call(0, uintptr(unsafe.Pointer(&info)))
		return nil
	})
	return nil
}

func properties(p string) error {
	return sta(func() error {
		f, _ := windows.UTF16PtrFromString(p)
		const shopFilepath = 2
		if r, _, _ := procSHObjectProperties.Call(0, shopFilepath, uintptr(unsafe.Pointer(f)), 0); r == 0 {
			return errors.New("Couldn't show properties")
		}
		time.Sleep(200 * time.Millisecond) // the sheet starts its own thread from this one
		return nil
	})
}

// shFileOp: the shell's copy engine (progress window, conflict questions, Ctrl+Z in Explorer).
func shFileOp(fn uint32, from []string, to string, flags uint16) error {
	type shFileOpStruct struct {
		hwnd                  uintptr
		wFunc                 uint32
		pFrom, pTo            *uint16
		fFlags                uint16
		fAnyOperationsAborted int32
		hNameMappings         uintptr
		lpszProgressTitle     *uint16
	}
	var src []uint16
	for _, p := range from {
		src = append(src, utf16.Encode([]rune(filepath.Clean(p)))...)
		src = append(src, 0)
	}
	src = append(src, 0)
	op := shFileOpStruct{wFunc: fn, pFrom: &src[0], fFlags: flags}
	var dst []uint16
	if to != "" {
		dst = append(utf16.Encode([]rune(filepath.Clean(to))), 0, 0)
		op.pTo = &dst[0]
	}
	return sta(func() error {
		r, _, _ := procSHFileOperationW.Call(uintptr(unsafe.Pointer(&op)))
		runtime.KeepAlive(src)
		runtime.KeepAlive(dst)
		if op.fAnyOperationsAborted != 0 {
			return errors.New("Cancelled")
		}
		if r != 0 {
			return fmt.Errorf("The operation failed (0x%x)", r)
		}
		return nil
	})
}

const (
	foMove, foCopy, foDelete = 1, 2, 3
	fofSilent                = 0x4
	fofRenameOnCollision     = 0x8
	fofNoConfirmation        = 0x10
	fofAllowUndo             = 0x40
	fofNoConfirmMkdir        = 0x200
	fofWantNukeWarning       = 0x4000
)

func trash(paths []string) error {
	return shFileOp(foDelete, paths, "", fofAllowUndo|fofNoConfirmation|fofWantNukeWarning)
}

func deleteForever(paths []string) error {
	return shFileOp(foDelete, paths, "", fofNoConfirmation) // asked in the UI already
}

func transfer(paths []string, dest string, move bool) error {
	var src []string
	same := false
	for _, p := range paths {
		if strings.EqualFold(filepath.Dir(p), filepath.Clean(dest)) {
			if move {
				continue // already there
			}
			same = true
		}
		if under(dest, p) {
			return errors.New("Can't copy a folder into itself")
		}
		src = append(src, p)
	}
	if len(src) == 0 {
		return nil
	}
	flags := uint16(fofAllowUndo | fofNoConfirmMkdir)
	if same {
		flags |= fofRenameOnCollision // Ctrl+C, Ctrl+V in one folder: "name - Copy"
	}
	fn := uint32(foCopy)
	if move {
		fn = foMove
	}
	return shFileOp(fn, src, dest, flags)
}

// ---------------------------------------------------------------- Recycle Bin

// The bin is X:\$Recycle.Bin\<SID>\ on every drive: $I<id> holds the original path and time,
// $R<id> the data.
func trashDirs() []string {
	tok := windows.GetCurrentProcessToken()
	u, err := tok.GetTokenUser()
	if err != nil {
		return nil
	}
	sid := u.User.Sid.String()
	var out []string
	for _, d := range driveLetters() {
		if t := driveType(d); t == windows.DRIVE_FIXED || t == windows.DRIVE_REMOVABLE {
			out = append(out, filepath.Join(d, "$Recycle.Bin", sid))
		}
	}
	return out
}

func trashList() []trashItem {
	out := []trashItem{}
	for _, dir := range trashDirs() {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if !strings.HasPrefix(e.Name(), "$I") {
				continue
			}
			ip := filepath.Join(dir, e.Name())
			orig, size, deleted, ok := readInfo(ip)
			if !ok {
				continue
			}
			rp := filepath.Join(dir, "$R"+e.Name()[2:])
			fi, err := os.Stat(rp)
			if err != nil {
				continue
			}
			it := trashItem{ID: ip, Name: filepath.Base(orig), Orig: orig, Path: rp, Deleted: deleted, Size: size, IsDir: fi.IsDir()}
			if it.IsDir {
				it.Size = 0
			}
			it.Kind = kindOf(it.Name, it.IsDir)
			out = append(out, it)
		}
	}
	return out
}

func readInfo(p string) (orig string, size, deleted int64, ok bool) {
	b, err := os.ReadFile(p)
	if err != nil || len(b) < 24 {
		return
	}
	ver := binary.LittleEndian.Uint64(b)
	size = int64(binary.LittleEndian.Uint64(b[8:]))
	ft := int64(binary.LittleEndian.Uint64(b[16:]))
	deleted = (ft - 116444736000000000) / 1e7
	var name []byte
	switch {
	case ver == 2 && len(b) >= 28:
		n := int(binary.LittleEndian.Uint32(b[24:]))
		if 28+2*n > len(b) {
			return
		}
		name = b[28 : 28+2*n]
	case ver == 1 && len(b) >= 24+520:
		name = b[24 : 24+520]
	default:
		return
	}
	u := make([]uint16, len(name)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(name[2*i:])
	}
	return windows.UTF16ToString(u), size, deleted, true
}

// longPath: p with 8.3 short names expanded (C:\Users\RUNNER~1 -> C:\Users\runneradmin). The file may be
// gone (it's in the bin), so its folder is expanded.
func longPath(p string) string {
	dir := filepath.Dir(p)
	src, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return p
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	if n, err := windows.GetLongPathName(src, &buf[0], uint32(len(buf))); err == nil && n > 0 && int(n) < len(buf) {
		return filepath.Join(windows.UTF16ToString(buf[:n]), filepath.Base(p))
	}
	return p
}

func samePath(a, b string) bool { return strings.EqualFold(longPath(a), longPath(b)) }

func binItem(id string) (info, data string, err error) {
	if !strings.Contains(strings.ToLower(id), `\$recycle.bin\`) || !strings.HasPrefix(filepath.Base(id), "$I") {
		return "", "", errors.New("not in the Recycle Bin")
	}
	return id, filepath.Join(filepath.Dir(id), "$R"+filepath.Base(id)[2:]), nil
}

func trashRestore(ids []string) error {
	for _, id := range ids {
		info, data, err := binItem(id)
		if err != nil {
			return err
		}
		orig, _, _, ok := readInfo(info)
		if !ok {
			return errors.New("not in the Recycle Bin")
		}
		dst := uniquePath(orig)
		os.MkdirAll(filepath.Dir(dst), 0o755)
		if err := os.Rename(data, dst); err != nil {
			return err
		}
		os.Remove(info)
	}
	return nil
}

func trashPurge(ids []string) error {
	for _, id := range ids {
		info, data, err := binItem(id)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(data); err != nil {
			return err
		}
		os.Remove(info)
	}
	return nil
}

func trashEmpty() error {
	const noConfirm, noProgress, noSound = 1, 2, 4
	r, _, _ := procSHEmptyRecycleBinW.Call(0, 0, noConfirm|noSound)
	if r != 0 && r != 0x8000FFFF { // E_UNEXPECTED: already empty
		return fmt.Errorf("Couldn't empty the Recycle Bin (0x%x)", r)
	}
	return nil
}

// ---------------------------------------------------------------- clipboard (CF_HDROP)

const cfHDROP = 15

var dropEffectFmt = sync.OnceValue(func() uintptr {
	n, _ := windows.UTF16PtrFromString("Preferred DropEffect")
	r, _, _ := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(n)))
	return r
})

func openClip() bool {
	for i := 0; i < 10; i++ { // another app may hold it for a moment
		if r, _, _ := procOpenClipboard.Call(clipOwner()); r != 0 {
			return true
		}
		time.Sleep(15 * time.Millisecond)
	}
	return false
}

func globalBytes(b []byte) uintptr {
	h, _, _ := procGlobalAlloc.Call(0x0002, uintptr(len(b))) // GMEM_MOVEABLE
	if h == 0 {
		return 0
	}
	p, _, _ := procGlobalLock.Call(h)
	copy(unsafe.Slice((*byte)(unsafe.Pointer(p)), len(b)), b)
	procGlobalUnlock.Call(h)
	return h
}

// setClipFiles puts files on the Windows clipboard, as Explorer does (paste works in any app).
func setClipFiles(paths []string, cut bool) {
	uiDo(func() {
		if !openClip() {
			return
		}
		defer procCloseClipboard.Call()
		procEmptyClipboard.Call()
		if len(paths) == 0 {
			return
		}
		// DROPFILES {pFiles=20, pt, fNC, fWide=1} then the paths, each \0-terminated, then \0
		b := make([]byte, 20)
		binary.LittleEndian.PutUint32(b, 20)
		binary.LittleEndian.PutUint32(b[16:], 1)
		for _, p := range paths {
			for _, c := range utf16.Encode([]rune(p)) {
				b = binary.LittleEndian.AppendUint16(b, c)
			}
			b = append(b, 0, 0)
		}
		b = append(b, 0, 0)
		if h := globalBytes(b); h != 0 {
			if r, _, _ := procSetClipboardData.Call(cfHDROP, h); r == 0 {
				procGlobalFree.Call(h)
			}
		}
		effect := uint32(1) // DROPEFFECT_COPY
		if cut {
			effect = 2 // DROPEFFECT_MOVE
		}
		if h := globalBytes(binary.LittleEndian.AppendUint32(nil, effect)); h != 0 {
			if r, _, _ := procSetClipboardData.Call(dropEffectFmt(), h); r == 0 {
				procGlobalFree.Call(h)
			}
		}
	})
}

// getClipFiles reads files copied in any app. ok is true when the clipboard could be read at all
// (no files then means nothing to paste, not "use our own list").
func getClipFiles() (paths []string, cut bool, ok bool) {
	uiDo(func() {
		if !openClip() {
			return
		}
		defer procCloseClipboard.Call()
		ok = true
		h, _, _ := procGetClipboardData.Call(cfHDROP)
		if h == 0 {
			return
		}
		n, _, _ := procDragQueryFileW.Call(h, 0xFFFFFFFF, 0, 0)
		for i := uintptr(0); i < n; i++ {
			l, _, _ := procDragQueryFileW.Call(h, i, 0, 0)
			buf := make([]uint16, l+1)
			procDragQueryFileW.Call(h, i, uintptr(unsafe.Pointer(&buf[0])), l+1)
			paths = append(paths, windows.UTF16ToString(buf))
		}
		if d, _, _ := procGetClipboardData.Call(dropEffectFmt()); d != 0 {
			if p, _, _ := procGlobalLock.Call(d); p != 0 {
				cut = *(*uint32)(unsafe.Pointer(p))&2 != 0
				procGlobalUnlock.Call(d)
			}
		}
	})
	return
}

// ---------------------------------------------------------------- shell thumbnails

type thumbReq struct {
	path string
	px   int
	res  chan thumbRes
}

type thumbRes struct {
	img image.Image
	err error
}

var (
	thumbQ          = make(chan thumbReq)
	thumbOnce       sync.Once
	iidImageFactory = windows.GUID{Data1: 0xbcc18b79, Data2: 0xba16, Data3: 0x442f, Data4: [8]byte{0x80, 0xc4, 0x8a, 0x59, 0xc3, 0x0c, 0x46, 0x3b}}
)

// shellThumb: what Explorer shows for the file (thumbnail, or its icon), from the system cache.
func shellThumb(path string, px int) (image.Image, error) {
	thumbOnce.Do(func() {
		for i := 0; i < 4; i++ {
			go thumbWorker()
		}
	})
	r := thumbReq{path, px, make(chan thumbRes, 1)}
	select {
	case thumbQ <- r:
	case <-time.After(5 * time.Second):
		return nil, errors.New("busy")
	}
	select {
	case res := <-r.res:
		return res.img, res.err
	case <-time.After(10 * time.Second):
		return nil, errors.New("timeout")
	}
}

func thumbWorker() {
	runtime.LockOSThread()
	windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE)
	for r := range thumbQ {
		img, err := factoryImage(r.path, r.px)
		r.res <- thumbRes{img, err}
	}
}

func factoryImage(path string, px int) (image.Image, error) {
	type vtbl struct{ QueryInterface, AddRef, Release, GetImage uintptr }
	type factory struct{ v *vtbl }
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var f *factory
	hr, _, _ := procSHCreateItemFromParsed.Call(uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&iidImageFactory)), uintptr(unsafe.Pointer(&f)))
	if hr != 0 || f == nil {
		return nil, fmt.Errorf("no shell item (0x%x)", hr)
	}
	defer syscall.SyscallN(f.v.Release, uintptr(unsafe.Pointer(f)))
	size := uintptr(uint32(px)) | uintptr(uint32(px))<<32 // SIZE by value
	var hbm uintptr
	hr, _, _ = syscall.SyscallN(f.v.GetImage, uintptr(unsafe.Pointer(f)), size, 0, uintptr(unsafe.Pointer(&hbm)))
	if hr != 0 || hbm == 0 {
		return nil, fmt.Errorf("no thumbnail (0x%x)", hr)
	}
	defer procDeleteObject.Call(hbm)
	return bitmapImage(hbm)
}

func bitmapImage(hbm uintptr) (image.Image, error) {
	type bitmap struct {
		typ, w, h, stride int32
		planes, bpp       uint16
		bits              uintptr
	}
	type bitmapInfoHeader struct {
		size                  uint32
		w, h                  int32
		planes, bpp           uint16
		compression, imgSize  uint32
		xppm, yppm            int32
		clrUsed, clrImportant uint32
	}
	var bm bitmap
	if r, _, _ := procGetObjectW.Call(hbm, unsafe.Sizeof(bm), uintptr(unsafe.Pointer(&bm))); r == 0 || bm.w <= 0 || bm.h <= 0 {
		return nil, errors.New("bad bitmap")
	}
	w, h := int(bm.w), int(bm.h)
	bi := struct {
		hdr    bitmapInfoHeader
		colors [4]byte
	}{hdr: bitmapInfoHeader{size: 40, w: int32(w), h: -int32(h), planes: 1, bpp: 32}}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	dc, _, _ := procGetDC.Call(0)
	r, _, _ := procGetDIBits.Call(dc, hbm, 0, uintptr(h), uintptr(unsafe.Pointer(&img.Pix[0])), uintptr(unsafe.Pointer(&bi)), 0)
	procReleaseDC.Call(0, dc)
	if r == 0 {
		return nil, errors.New("GetDIBits failed")
	}
	// BGRA (premultiplied, like image.RGBA) -> RGBA; no alpha at all means an opaque picture
	anyAlpha := false
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] != 0 {
			anyAlpha = true
			break
		}
	}
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+2] = img.Pix[i+2], img.Pix[i]
		if !anyAlpha {
			img.Pix[i+3] = 255
		}
	}
	return img, nil
}
