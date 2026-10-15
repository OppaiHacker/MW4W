//go:build windows

package main

// The Windows parts, run for real (CI: windows-latest has the WebView2 runtime): windows and the
// page, the clipboard, the Recycle Bin, the copy engine, shell thumbnails, the watcher, and how
// fast a big folder lists.

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// one app + UI thread for the whole test binary (one UI thread per process)
var (
	uiOnce sync.Once
	uiApp  *App
	hitsMu sync.Mutex
	hits   []string
)

func startUI(t *testing.T) *App {
	uiOnce.Do(func() {
		data, _ := os.MkdirTemp("", "mw4wfiles")
		os.Setenv("MW4W_FILES_DATA", data)
		a, err := newApp(data)
		if err != nil {
			t.Fatal(err)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		a.token = "testtoken"
		a.port = ln.Addr().(*net.TCPAddr).Port
		h := a.routes()
		go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hitsMu.Lock()
			hits = append(hits, r.URL.String())
			hitsMu.Unlock()
			h.ServeHTTP(w, r)
		}))
		go runUI(a, "", false)
		<-uiReady
		uiApp = a
	})
	return uiApp
}

func waitHit(t *testing.T, sub string, d time.Duration) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		hitsMu.Lock()
		for _, h := range hits {
			if strings.Contains(h, sub) {
				hitsMu.Unlock()
				return
			}
		}
		hitsMu.Unlock()
		time.Sleep(100 * time.Millisecond)
	}
	hitsMu.Lock()
	defer hitsMu.Unlock()
	t.Fatalf("the page never requested %q; requests: %v", sub, hits)
}

func TestWindowLoadsAndOpens(t *testing.T) {
	a := startUI(t)
	// the warm window loads the UI on its own after 1.5 s
	waitHit(t, "/api/init", 60*time.Second)
	waitHit(t, "/app.js", 5*time.Second)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("x"), 0o644)
	t0 := time.Now()
	a.openWindow(dir)
	waitHit(t, "/api/list?p="+strings.ReplaceAll(urlEsc(dir), "%5C", "%5C"), 20*time.Second)
	t.Logf("open window -> folder listed in %v", time.Since(t0))
	var n int
	uiDo(func() { n = len(wins) })
	if n == 0 {
		t.Fatal("no window")
	}
}

func urlEsc(s string) string {
	r := strings.NewReplacer("%", "%25", " ", "%20", ":", "%3A", `\`, "%5C", "/", "%2F")
	return r.Replace(s)
}

func TestClipboard(t *testing.T) {
	startUI(t)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b c.txt")
	os.WriteFile(a, nil, 0o644)
	os.WriteFile(b, nil, 0o644)
	setClipFiles([]string{a, b}, true)
	got, cut, ok := getClipFiles()
	if !ok || !cut || len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("clipboard: %v cut=%v ok=%v", got, cut, ok)
	}
	setClipFiles([]string{a}, false)
	if got, cut, _ := getClipFiles(); cut || len(got) != 1 {
		t.Fatalf("copy: %v cut=%v", got, cut)
	}
}

func TestCopyMoveDelete(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	f := filepath.Join(src, "file.txt")
	os.WriteFile(f, []byte("hello"), 0o644)
	os.MkdirAll(filepath.Join(src, "sub", "deep"), 0o755)
	os.WriteFile(filepath.Join(src, "sub", "deep", "x.bin"), []byte{1, 2, 3}, 0o644)
	if err := transfer([]string{f, filepath.Join(src, "sub")}, dst, false); err != nil {
		t.Fatal("copy:", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "sub", "deep", "x.bin")); len(b) != 3 {
		t.Fatal("copied tree missing")
	}
	// copy into the same folder: "file - Copy.txt", not a conflict dialog
	if err := transfer([]string{f}, src, false); err != nil {
		t.Fatal("duplicate:", err)
	}
	ents, _ := os.ReadDir(src)
	if len(ents) != 3 {
		t.Fatalf("duplicate: %v", ents)
	}
	if err := transfer([]string{f}, filepath.Join(dst, "sub"), true); err != nil {
		t.Fatal("move:", err)
	}
	if _, err := os.Stat(f); err == nil {
		t.Fatal("move left the source")
	}
	if err := deleteForever([]string{filepath.Join(dst, "sub")}); err != nil {
		t.Fatal("delete:", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "sub")); err == nil {
		t.Fatal("still there")
	}
}

func TestRecycleBin(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, fmt.Sprintf("trash-me-%d.txt", time.Now().UnixNano()))
	os.WriteFile(f, []byte("bye"), 0o644)
	if err := trash([]string{f}); err != nil {
		t.Fatal("trash:", err)
	}
	if _, err := os.Stat(f); err == nil {
		t.Fatal("file still there")
	}
	var id string
	for _, it := range trashList() {
		if strings.EqualFold(it.Orig, f) {
			id = it.ID
			if it.Size != 3 || it.Kind != "text" {
				t.Errorf("item: %+v", it)
			}
		}
	}
	if id == "" {
		t.Fatalf("not in the bin: %+v", trashList())
	}
	if err := trashRestore([]string{id}); err != nil {
		t.Fatal("restore:", err)
	}
	if b, _ := os.ReadFile(f); string(b) != "bye" {
		t.Fatal("not restored")
	}
	// purge
	trash([]string{f})
	for _, it := range trashList() {
		if strings.EqualFold(it.Orig, f) {
			if err := trashPurge([]string{it.ID}); err != nil {
				t.Fatal("purge:", err)
			}
		}
	}
	for _, it := range trashList() {
		if strings.EqualFold(it.Orig, f) {
			t.Fatal("still in the bin")
		}
	}
}

func TestShellThumb(t *testing.T) {
	exe := filepath.Join(os.Getenv("SystemRoot"), "System32", "notepad.exe")
	t0 := time.Now()
	img, err := shellThumb(exe, 64)
	if err != nil || img == nil {
		t.Fatal("icon:", err)
	}
	t.Logf("notepad icon %v in %v", img.Bounds(), time.Since(t0))
	// a picture: its own thumbnail
	p := writePNG(t, filepath.Join(t.TempDir(), "pic.png"))
	if img, err := shellThumb(p, 128); err != nil || img == nil {
		t.Fatal("thumbnail:", err)
	}
}

func writePNG(t *testing.T, p string) string {
	// 2x2 red PNG
	b := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d, 0x49, 0x48, 0x44, 0x52, 0, 0, 0, 2, 0, 0, 0, 2, 8, 2, 0, 0, 0, 0xfd, 0xd4, 0x9a,
		0x73, 0, 0, 0, 0x12, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0xf8, 0xcf, 0xc0, 0x00, 0x42, 0x0c, 0x20, 0x8a, 0x01, 0x00, 0x2c, 0xef, 0x05, 0xfb, 0xa4,
		0x65, 0x1a, 0x4b, 0, 0, 0, 0, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWatcher(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	got := make(chan []string, 10)
	go watchRoots([]string{root}, func(d []string) { got <- d }, func() {})
	time.Sleep(300 * time.Millisecond)
	os.WriteFile(filepath.Join(root, "sub", "new.txt"), []byte("x"), 0o644)
	select {
	case d := <-got:
		found := false
		for _, x := range d {
			if strings.EqualFold(x, filepath.Join(root, "sub")) {
				found = true
			}
		}
		if !found {
			t.Fatalf("changed dirs: %v", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no change reported")
	}
}

func TestBigFolderSpeed(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 50000; i++ {
		f, err := os.Create(filepath.Join(dir, fmt.Sprintf("file_%05d.dat", i)))
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	best := time.Hour
	for k := 0; k < 3; k++ {
		t0 := time.Now()
		ents, err := readDir(dir)
		if err != nil || len(ents) != 50000 {
			t.Fatal(err, len(ents))
		}
		best = min(best, time.Since(t0))
	}
	t.Logf("readDir of 50,000 files: %v", best)
}

func TestDrivesAndKnown(t *testing.T) {
	ds := drives()
	if len(ds) == 0 || ds[0].Total == 0 {
		t.Fatalf("drives: %+v", ds)
	}
	if len(knownDirs()) == 0 {
		t.Fatal("no known folders")
	}
	t.Logf("drives %+v roots %v", ds, defaultRoots())
}
