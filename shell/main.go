// mw4w-shell: MW4W as the Windows shell, instead of explorer.exe.
//
// Winlogon starts the program in HKCU\Software\Microsoft\Windows NT\CurrentVersion\Winlogon
// "Shell" at sign-in (mw4w.ps1 shell on|off sets it). Without Explorer nobody else does the
// shell's chores, so this does them, then keeps GlazeWM running (GlazeWM starts Zebar, winmask
// and mw4w.ps1 startup). There is no taskbar, Start menu or desktop icons: the bar, dock and
// launcher are those. Explorer still opens folders (it only becomes the shell when it is
// the registered one).
//
// Chores: tell Winlogon the shell is up (ends the "Welcome" screen), run the Run/RunOnce
// keys and Startup folders, like Explorer, honouring Task Manager's disabled startup apps.
//
// Safety net: if GlazeWM can't start or exits for good, explorer.exe starts, so a broken
// install never leaves a black screen. CTRL+SHIFT+ESC (Task Manager > Run) works as always.
//
//	mw4w-shell.exe            run as the shell
//	mw4w-shell.exe --dry-run  print what it would start
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	shell32      = windows.NewLazySystemDLL("shell32.dll")
	shellExecute = shell32.NewProc("ShellExecuteW")
	dryRun       = len(os.Args) > 1 && os.Args[1] == "--dry-run"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func main() {
	// one shell per session (started again by hand, it does nothing)
	name, _ := windows.UTF16PtrFromString(`Local\mw4w-shell`)
	if _, err := windows.CreateMutex(nil, true, name); err == windows.ERROR_ALREADY_EXISTS {
		return
	}
	if !dryRun {
		signalReady()
	}
	glaze := filepath.Join(os.Getenv("ProgramFiles"), `glzr.io\GlazeWM\glazewm.exe`)
	go startupApps()
	keepGlaze(glaze)
	rescue()
}

// signalReady: Winlogon waits on these before it drops the logon screen (Explorer and Cairo set them).
func signalReady() {
	for _, e := range []string{"ShellDesktopSwitchEvent", "msgina: ShellReadyEvent"} {
		n, _ := windows.UTF16PtrFromString(e)
		if h, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, n); err == nil {
			windows.SetEvent(h)
			windows.CloseHandle(h)
		}
	}
}

// keepGlaze runs GlazeWM and restarts it after a crash. It returns when GlazeWM won't stay up
// (or isn't installed), or exits cleanly (wm-exit, the uninstaller): then the rescue shell starts.
func keepGlaze(exe string) {
	if _, err := os.Stat(exe); err != nil {
		return
	}
	var crashes []time.Time
	for {
		if running("glazewm.exe") {
			// started by hand (or the installer): wait for it like for our own
			if code, ok := waitFor("glazewm.exe"); ok && code == 0 {
				return
			}
		} else {
			c := exec.Command(exe)
			c.Dir = filepath.Dir(exe)
			if dryRun {
				fmt.Println("glazewm:", exe)
				return
			}
			if c.Run() == nil {
				return // wm-exit
			}
		}
		now := time.Now()
		crashes = append(crashes, now)
		if len(crashes) > 3 && now.Sub(crashes[len(crashes)-4]) < 2*time.Minute {
			return // crash loop
		}
		time.Sleep(time.Second)
	}
}

// rescue starts Explorer as the shell for the rest of the session, unless Windows is signing out.
func rescue() {
	if dryRun || shuttingDown() {
		return
	}
	// Explorer only takes over the shell when it is the registered one, so register it for this
	// session's start and put MW4W back as soon as Explorer is up.
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows NT\CurrentVersion\Winlogon`, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	old, _, _ := k.GetStringValue("Shell")
	k.SetStringValue("Shell", "explorer.exe")
	exec.Command(filepath.Join(os.Getenv("WINDIR"), "explorer.exe")).Start()
	time.Sleep(8 * time.Second)
	if old != "" {
		k.SetStringValue("Shell", old)
	}
}

func shuttingDown() bool {
	const smShuttingDown = 0x2000
	r, _, _ := windows.NewLazySystemDLL("user32.dll").NewProc("GetSystemMetrics").Call(smShuttingDown)
	return r != 0
}

// ---------- startup apps ----------

func startupApps() {
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		runOnce(root)
	}
	approved := `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\`
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		runValues(root, runKey, approved+"Run")
		runValues(root, `Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Run`, approved+"Run32")
	}
	for _, dir := range []struct{ path, approved string }{
		{filepath.Join(os.Getenv("ProgramData"), `Microsoft\Windows\Start Menu\Programs\StartUp`), approved + "StartupFolder"},
		{filepath.Join(os.Getenv("APPDATA"), `Microsoft\Windows\Start Menu\Programs\Startup`), approved + "StartupFolder"},
	} {
		entries, _ := os.ReadDir(dir.path)
		for _, e := range entries {
			if e.IsDir() || strings.EqualFold(e.Name(), "desktop.ini") || disabled(dir.approved, e.Name()) {
				continue
			}
			open(filepath.Join(dir.path, e.Name()))
		}
	}
}

func runValues(root registry.Key, path, approved string) {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	names, _ := k.ReadValueNames(-1)
	for _, n := range names {
		// GlazeWM is ours to start (and to restart)
		if n == "MW4W-GlazeWM" || disabled(approved, n) {
			continue
		}
		if v, _, err := k.GetStringValue(n); err == nil && v != "" {
			start(v)
		}
	}
}

// runOnce: RunOnce entries run once, and are deleted first (a crashing one must not loop).
func runOnce(root registry.Key) {
	k, err := registry.OpenKey(root, `Software\Microsoft\Windows\CurrentVersion\RunOnce`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	names, _ := k.ReadValueNames(-1)
	for _, n := range names {
		v, _, err := k.GetStringValue(n)
		if err != nil || v == "" {
			continue
		}
		if !strings.HasPrefix(n, "!") && !dryRun {
			k.DeleteValue(n)
		}
		start(v)
	}
}

// disabled: Task Manager's Startup tab switch. The first byte of the value is odd when off.
func disabled(path, name string) bool {
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		b, _, err := k.GetBinaryValue(name)
		k.Close()
		if err == nil && len(b) > 0 {
			return b[0]&1 == 1
		}
	}
	return false
}

// start runs a Run-key command line as Explorer does: CreateProcess with the line as is.
func start(cmdline string) {
	cmdline = expand(cmdline)
	if dryRun {
		fmt.Println("run:", cmdline)
		return
	}
	line, err := windows.UTF16PtrFromString(cmdline)
	if err != nil {
		return
	}
	si := &windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{}))}
	var pi windows.ProcessInformation
	home, _ := windows.UTF16PtrFromString(os.Getenv("USERPROFILE"))
	if windows.CreateProcess(nil, line, nil, nil, false, 0, nil, home, si, &pi) == nil {
		windows.CloseHandle(pi.Process)
		windows.CloseHandle(pi.Thread)
		return
	}
	open(cmdline) // a bare document or URI
}

// open: ShellExecute, for shortcuts (.lnk) and documents.
func open(path string) {
	if dryRun {
		fmt.Println("open:", path)
		return
	}
	verb, _ := windows.UTF16PtrFromString("open")
	p, _ := windows.UTF16PtrFromString(path)
	shellExecute.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(p)), 0, 0, 1) // SW_SHOWNORMAL
}

func expand(s string) string {
	if r, err := registry.ExpandString(s); err == nil {
		return r
	}
	return s
}

// ---------- processes ----------

func running(exe string) bool { return pidOf(exe) != 0 }

func pidOf(exe string) uint32 {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), exe) {
			return e.ProcessID
		}
	}
	return 0
}

// waitFor blocks until process exe ends and returns its exit code, if it could see it
// (GlazeWM runs elevated at times: then it polls instead).
func waitFor(exe string) (code uint32, ok bool) {
	pid := pidOf(exe)
	if pid == 0 {
		return 0, false
	}
	if h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid); err == nil {
		defer windows.CloseHandle(h)
		windows.WaitForSingleObject(h, windows.INFINITE)
		return code, windows.GetExitCodeProcess(h, &code) == nil
	}
	for running(exe) {
		time.Sleep(2 * time.Second)
	}
	return 0, false
}
