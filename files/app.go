package main

import (
	"archive/zip"
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

//go:embed ui
var uiFS embed.FS

type App struct {
	ix         *Index
	th         *thumbber
	token      string
	port       int
	openWindow func(path string) // set by the UI host
	quit       func()
	packDir    string
	chroma     []byte
	clipMu     sync.Mutex
	clip       []string
	clipCut    bool
}

func newApp(data string) (*App, error) {
	ix, err := openIndex(data)
	if err != nil {
		return nil, err
	}
	a := &App{ix: ix, th: newThumbber(ix), chroma: chromaCSS(), quit: func() { os.Exit(0) }}
	a.packDir = os.Getenv("MW4W_PACK")
	if a.packDir == "" {
		if exe, err := os.Executable(); err == nil {
			a.packDir = filepath.Dir(filepath.Dir(exe)) // <pack>\bin\mw4w-files.exe
		}
	}
	return a, nil
}

func (a *App) routes() http.Handler {
	m := http.NewServeMux()
	ui, _ := fs.Sub(uiFS, "ui")
	static := http.FileServer(http.FS(ui))
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			b, _ := uiFS.ReadFile("ui/index.html")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.Write(b)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		static.ServeHTTP(w, r)
	})
	m.HandleFunc("/chroma.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		w.Write(a.chroma)
	})
	m.HandleFunc("/colors.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		w.Header().Set("Cache-Control", "no-cache")
		if b, err := os.ReadFile(filepath.Join(a.packDir, "colors.css")); err == nil {
			w.Write(b)
		}
	})
	// the wallpaper's blurred copy stands in for acrylic outside the app window (browser, --serve)
	m.HandleFunc("/wallpaper", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=300")
		http.ServeFile(w, r, filepath.Join(a.packDir, "cache", "glass.jpg"))
	})
	api := func(path string, h func(r *http.Request) (any, error)) {
		m.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			v, err := h(r)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			if err != nil {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]string{"error": friendly(err)})
				return
			}
			if b, ok := v.([]byte); ok {
				w.Write(b)
				return
			}
			json.NewEncoder(w).Encode(v)
		})
	}
	api("/api/init", a.apiInit)
	api("/api/list", a.apiList)
	api("/api/stamp", a.apiStamp)
	api("/api/counts", a.apiCounts)
	api("/api/search", a.apiSearch)
	api("/api/recent", func(r *http.Request) (any, error) { return a.hits(a.ix.recent(300)), nil })
	api("/api/starred", a.apiStarred)
	api("/api/trash", func(r *http.Request) (any, error) { return trashList(), nil })
	api("/api/preview", func(r *http.Request) (any, error) { return a.preview(r.URL.Query().Get("p")) })
	api("/api/op", a.apiOp)
	api("/api/prefs", a.apiPrefs)
	api("/api/index", a.apiIndex)
	api("/api/ping", func(r *http.Request) (any, error) { return "ok", nil })
	api("/api/window", func(r *http.Request) (any, error) {
		var b struct{ Path string }
		json.NewDecoder(r.Body).Decode(&b)
		if a.openWindow == nil {
			return nil, errors.New("no windows in --serve mode")
		}
		a.openWindow(b.Path)
		return "ok", nil
	})
	api("/api/quit", func(r *http.Request) (any, error) {
		go func() { time.Sleep(100 * time.Millisecond); a.quit() }()
		return "ok", nil
	})
	m.HandleFunc("/raw", a.raw)
	m.HandleFunc("/thumb", a.thumb)
	return a.guard(m)
}

// guard: the server is for this user's own windows only.
//   - it listens on 127.0.0.1 (never the network) and drops anything not from loopback
//   - Host must be 127.0.0.1:<port> (a web page can't reach it through DNS rebinding)
//   - every request needs the random per-run token (HttpOnly SameSite=Strict cookie, or X-Token
//     for the hand-off from a second start); a page from elsewhere can't read or send it
//   - Origin, when sent, must be ours (no cross-site POSTs)
//   - our page runs under a strict CSP: own scripts only, nothing loaded from outside
func (a *App) guard(next http.Handler) http.Handler {
	csp := "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; " +
		"media-src 'self'; font-src 'self' data:; frame-src 'self'; object-src 'self'; connect-src 'self'; frame-ancestors 'self'; base-uri 'none'; form-action 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := fmt.Sprintf("http://127.0.0.1:%d", a.port)
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if ip := net.ParseIP(ip); ip == nil || !ip.IsLoopback() {
			http.Error(w, "forbidden", 403)
			return
		}
		if r.Host != origin[len("http://"):] {
			http.Error(w, "forbidden", 403)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != origin {
			http.Error(w, "forbidden", 403)
			return
		}
		ok := tokenOK(r.Header.Get("X-Token"), a.token)
		if c, err := r.Cookie("t"); err == nil && tokenOK(c.Value, a.token) {
			ok = true
		}
		if !ok && r.URL.Path == "/" && tokenOK(r.URL.Query().Get("t"), a.token) {
			http.SetCookie(w, &http.Cookie{Name: "t", Value: a.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			ok = true
		}
		if !ok {
			http.Error(w, "forbidden", 403)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func tokenOK(got, want string) bool {
	return want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func friendly(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		switch {
		case errors.Is(err, fs.ErrPermission):
			return "Permission denied: " + pe.Path
		case errors.Is(err, fs.ErrNotExist):
			return "Not found: " + pe.Path
		case errors.Is(err, fs.ErrExist):
			return "Already exists: " + pe.Path
		}
	}
	var le *os.LinkError
	if errors.As(err, &le) && errors.Is(err, fs.ErrExist) {
		return "Already exists: " + filepath.Base(le.New)
	}
	return err.Error()
}

// ---------------------------------------------------------------- init / prefs

type place struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Icon  string `json:"icon"`
	Total int64  `json:"total,omitempty"`
	Free  int64  `json:"free,omitempty"`
}

func (a *App) apiInit(r *http.Request) (any, error) {
	known := knownDirs()
	var bm []string
	if v := a.ix.meta("bookmarks"); v != "" {
		json.Unmarshal([]byte(v), &bm)
	} else {
		for _, k := range known {
			if k.Icon != "desktop" {
				bm = append(bm, k.Path)
			}
		}
	}
	var marks []place
	for _, p := range bm {
		icon := "folder"
		for _, k := range known {
			if k.Path == p {
				icon = k.Icon
			}
		}
		marks = append(marks, place{Name: filepath.Base(p), Path: p, Icon: icon})
	}
	return map[string]any{
		"home": homeDir(), "sep": string(filepath.Separator), "version": version, "windows": caseInsensitive,
		"known": known, "bookmarks": marks, "drives": drives(), "prefs": json.RawMessage(orEmpty(a.ix.meta("prefs"))),
		"embedded": a.openWindow != nil,
	}, nil
}

func orEmpty(s string) string {
	if s == "" {
		return "{}"
	}
	return s
}

func (a *App) apiPrefs(r *http.Request) (any, error) {
	if r.Method == "POST" {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if !json.Valid(b) {
			return nil, errors.New("bad prefs")
		}
		a.ix.setMeta("prefs", string(b))
	}
	return json.RawMessage(orEmpty(a.ix.meta("prefs"))), nil
}

func (a *App) apiIndex(r *http.Request) (any, error) {
	if r.Method == "POST" {
		go a.ix.fullScan()
	}
	return map[string]any{
		"files": a.ix.nFiles.Load(), "dirs": a.ix.nDirs.Load(), "scanning": a.ix.busy.Load() > 0,
		"pending": a.ix.pending.Load(), "lastScan": a.ix.lastRun.Load(), "roots": a.ix.cfg.Roots,
		"watching": watching, "content": a.ix.cfg.Content,
	}, nil
}

// ---------------------------------------------------------------- listing

// apiList: a folder as compact rows [name, flags, size, mtime, kind] (flags: 1 dir, 2 hidden, 4 link,
// 8 starred). A file path lists its folder and names the file in "select".
func (a *App) apiList(r *http.Request) (any, error) {
	p := filepath.Clean(r.URL.Query().Get("p"))
	if p == "." || p == "" {
		p = homeDir()
	}
	sel := ""
	if fi, err := os.Stat(p); err != nil {
		return nil, err
	} else if !fi.IsDir() {
		sel = filepath.Base(p)
		p = filepath.Dir(p)
	}
	t0 := time.Now()
	ents, err := readDir(p)
	if err != nil {
		return nil, err
	}
	stars := a.starsIn(p)
	b := make([]byte, 0, 64+len(ents)*72)
	b = append(b, `{"path":`...)
	b = appendStr(b, p)
	parent := filepath.Dir(p)
	if parent == p {
		parent = ""
	}
	b = append(b, `,"parent":`...)
	b = appendStr(b, parent)
	b = append(b, `,"select":`...)
	b = appendStr(b, sel)
	b = append(b, `,"stamp":`...)
	b = strconv.AppendInt(b, dirStamp(p), 10)
	b = append(b, `,"entries":[`...)
	for i, e := range ents {
		if i > 0 {
			b = append(b, ',')
		}
		fl := 0
		if e.dir {
			fl |= 1
		}
		if e.hidden {
			fl |= 2
		}
		if e.link {
			fl |= 4
		}
		if stars[e.name] {
			fl |= 8
		}
		b = append(b, '[')
		b = appendStr(b, e.name)
		b = append(b, ',')
		b = strconv.AppendInt(b, int64(fl), 10)
		b = append(b, ',')
		b = strconv.AppendInt(b, e.size, 10)
		b = append(b, ',')
		b = strconv.AppendInt(b, e.mtime, 10)
		b = append(b, ',', '"')
		b = append(b, kindOf(e.name, e.dir)...)
		b = append(b, '"', ']')
	}
	b = append(b, `],"ms":`...)
	b = strconv.AppendFloat(b, float64(time.Since(t0).Microseconds())/1000, 'f', 2, 64)
	b = append(b, '}')
	a.ix.sync(p, ents)
	return b, nil
}

func dirStamp(p string) int64 {
	if fi, err := os.Stat(p); err == nil {
		return fi.ModTime().UnixNano()
	}
	return 0
}

// apiStamp: has the folder changed (entries added, removed, renamed)? Polled by an open window.
func (a *App) apiStamp(r *http.Request) (any, error) {
	return map[string]int64{"stamp": dirStamp(r.URL.Query().Get("p"))}, nil
}

// apiCounts: how many items each folder holds ("12 items"), read in parallel.
func (a *App) apiCounts(r *http.Request) (any, error) {
	var req struct{ Paths []string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, err
	}
	out := make(map[string]int, len(req.Paths))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, p := range req.Paths {
		wg.Add(1)
		sem <- struct{}{}
		go func(p string) {
			defer func() { <-sem; wg.Done() }()
			n := -1
			if f, err := os.Open(p); err == nil {
				names, _ := f.Readdirnames(-1)
				n = len(names)
				f.Close()
			}
			mu.Lock()
			out[p] = n
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	return out, nil
}

func (a *App) apiSearch(r *http.Request) (any, error) {
	q := r.URL.Query()
	t0 := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	hits, total, err := a.ix.search(ctx, searchReq{Q: q.Get("q"), In: q.Get("in"), Content: q.Get("c") == "1", Limit: 500})
	if err != nil {
		return nil, err
	}
	res := a.hits(hits)
	res["total"] = total
	res["ms"] = float64(time.Since(t0).Microseconds()) / 1000
	res["indexing"] = a.ix.busy.Load() > 0
	res["indexed"] = a.ix.nFiles.Load()
	return res, nil
}

// hits: search/recent/starred results, with their kind and star.
func (a *App) hits(hs []hit) map[string]any {
	type row struct {
		hit
		Kind    string `json:"kind"`
		Starred bool   `json:"starred"`
	}
	stars := map[string]bool{}
	if rows, err := a.ix.r.Query(`SELECT path FROM stars`); err == nil {
		for rows.Next() {
			var p string
			rows.Scan(&p)
			stars[p] = true
		}
		rows.Close()
	}
	out := make([]row, len(hs))
	for i, h := range hs {
		out[i] = row{h, kindOf(h.Name, h.IsDir), stars[filepath.Join(h.Dir, h.Name)]}
	}
	return map[string]any{"hits": out}
}

func (a *App) apiStarred(r *http.Request) (any, error) {
	rows, err := a.ix.r.Query(`SELECT path FROM stars ORDER BY ts DESC`)
	if err != nil {
		return nil, err
	}
	var paths []string
	for rows.Next() {
		var p string
		rows.Scan(&p)
		paths = append(paths, p)
	}
	rows.Close()
	var hs []hit
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			h := hit{Dir: filepath.Dir(p), Name: filepath.Base(p), IsDir: fi.IsDir(), Mtime: fi.ModTime().Unix()}
			if !h.IsDir {
				h.Size = fi.Size()
			}
			hs = append(hs, h)
		}
	}
	return a.hits(hs), nil
}

func (a *App) starsIn(dir string) map[string]bool {
	out := map[string]bool{}
	pre := dir + string(filepath.Separator)
	if strings.HasSuffix(dir, string(filepath.Separator)) {
		pre = dir
	}
	rows, err := a.ix.r.Query(`SELECT path FROM stars WHERE substr(path, 1, ?) = ?`, len(pre), pre)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		rows.Scan(&p)
		if rest := p[len(pre):]; !strings.ContainsRune(rest, filepath.Separator) {
			out[rest] = true
		}
	}
	return out
}

// ---------------------------------------------------------------- files

func (a *App) raw(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("p")
	f, err := os.Open(p)
	if err != nil {
		http.Error(w, friendly(err), 404)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		http.Error(w, "not a file", 400)
		return
	}
	ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(p)))
	switch strings.ToLower(filepath.Ext(p)) {
	case ".mkv":
		ct = "video/webm" // Chromium plays most MKVs (VP9/AV1/H.264) when told it's WebM
	case ".svg":
		ct = "image/svg+xml"
	case ".pdf":
		ct = "application/pdf"
	}
	if ct == "" || strings.HasPrefix(ct, "text/html") {
		ct = "text/plain; charset=utf-8" // never run a file's HTML in our origin
	}
	w.Header().Set("Content-Type", ct)
	// a file's own content never runs script in our origin (SVG, anything opened directly);
	// PDFs are left to the built-in viewer, which needs its own
	if ct == "application/pdf" {
		w.Header().Del("Content-Security-Policy")
	} else {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; media-src 'self'")
	}
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

func (a *App) thumb(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	px, _ := strconv.Atoi(q.Get("s"))
	if px <= 0 || px > 1024 {
		px = 256
	}
	ct, data := a.th.get(q.Get("p"), px)
	if data == nil {
		w.Header().Set("Cache-Control", "max-age=60")
		http.Error(w, "no thumbnail", 404)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "max-age=31536000, immutable") // the URL has size+mtime
	w.Write(data)
}

// ---------------------------------------------------------------- operations

type opReq struct {
	Op    string   `json:"op"`
	Paths []string `json:"paths"`
	Dest  string   `json:"dest"`
	Name  string   `json:"name"`
	On    bool     `json:"on"`
	Cut   bool     `json:"cut"`
}

func (a *App) apiOp(r *http.Request) (any, error) {
	var o opReq
	if err := json.NewDecoder(r.Body).Decode(&o); err != nil {
		return nil, err
	}
	one := func() (string, error) {
		if len(o.Paths) == 0 {
			return "", errors.New("nothing selected")
		}
		return o.Paths[0], nil
	}
	switch o.Op {
	case "open":
		p, err := one()
		if err != nil {
			return nil, err
		}
		a.ix.w.Exec(`INSERT OR REPLACE INTO opened(path, ts) VALUES(?, ?)`, p, time.Now().Unix())
		return nil, shellOpen(p)
	case "openwith":
		p, err := one()
		if err != nil {
			return nil, err
		}
		return nil, openWith(p)
	case "props":
		p, err := one()
		if err != nil {
			return nil, err
		}
		return nil, properties(p)
	case "terminal":
		return nil, a.terminal(o.Dest)
	case "trash":
		return nil, trash(o.Paths)
	case "delete":
		return nil, deleteForever(o.Paths)
	case "copy", "move":
		return nil, transfer(o.Paths, o.Dest, o.Op == "move")
	case "rename":
		p, err := one()
		if err != nil {
			return nil, err
		}
		if err := validName(o.Name); err != nil {
			return nil, err
		}
		np := filepath.Join(filepath.Dir(p), o.Name)
		if np != p && !strings.EqualFold(np, p) {
			if _, err := os.Lstat(np); err == nil {
				return nil, fmt.Errorf("“%s” already exists", o.Name)
			}
		}
		if err := os.Rename(p, np); err != nil {
			return nil, err
		}
		a.ix.w.Exec(`UPDATE stars SET path = ? WHERE path = ?`, np, p)
		return map[string]string{"path": np}, nil
	case "mkdir", "newfile":
		if err := validName(o.Name); err != nil {
			return nil, err
		}
		np := filepath.Join(o.Dest, o.Name)
		var err error
		if o.Op == "mkdir" {
			err = os.Mkdir(np, 0o755)
		} else {
			var f *os.File
			if f, err = os.OpenFile(np, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644); err == nil {
				f.Close()
			}
		}
		if err != nil {
			return nil, err
		}
		return map[string]string{"path": np}, nil
	case "clip":
		a.clipMu.Lock()
		a.clip, a.clipCut = o.Paths, o.Cut
		a.clipMu.Unlock()
		setClipFiles(o.Paths, o.Cut)
		return "ok", nil
	case "paste":
		paths, cut := a.clipboard()
		if len(paths) == 0 {
			return nil, errors.New("Nothing to paste")
		}
		if err := transfer(paths, o.Dest, cut); err != nil {
			return nil, err
		}
		if cut {
			a.clipMu.Lock()
			a.clip = nil
			a.clipMu.Unlock()
			setClipFiles(nil, false)
		}
		return map[string]any{"count": len(paths)}, nil
	case "clipget":
		paths, cut := a.clipboard()
		return map[string]any{"paths": paths, "cut": cut}, nil
	case "star":
		for _, p := range o.Paths {
			if o.On {
				a.ix.w.Exec(`INSERT OR REPLACE INTO stars(path, ts) VALUES(?, ?)`, p, time.Now().Unix())
			} else {
				a.ix.w.Exec(`DELETE FROM stars WHERE path = ?`, p)
			}
		}
		return "ok", nil
	case "bookmarks":
		b, _ := json.Marshal(o.Paths)
		a.ix.setMeta("bookmarks", string(b))
		return "ok", nil
	case "restore":
		return nil, trashRestore(o.Paths)
	case "purge":
		return nil, trashPurge(o.Paths)
	case "empty":
		return nil, trashEmpty()
	case "untrash": // undo: the newest trash items that came from these paths
		var ids []string
		items := trashList()
		sort.Slice(items, func(i, j int) bool { return items[i].Deleted > items[j].Deleted })
		for _, p := range o.Paths {
			for _, it := range items {
				if samePath(it.Orig, p) {
					ids = append(ids, it.ID)
					break
				}
			}
		}
		return nil, trashRestore(ids)
	case "extract":
		p, err := one()
		if err != nil {
			return nil, err
		}
		return extractZip(p, o.Dest)
	case "compress":
		if err := validName(o.Name); err != nil {
			return nil, err
		}
		return compressZip(o.Paths, filepath.Join(o.Dest, o.Name))
	}
	return nil, fmt.Errorf("unknown op %q", o.Op)
}

// clipboard: files copied in any app (Windows clipboard) win over our own list.
func (a *App) clipboard() ([]string, bool) {
	if p, cut, ok := getClipFiles(); ok && len(p) > 0 {
		return p, cut
	}
	a.clipMu.Lock()
	defer a.clipMu.Unlock()
	return append([]string(nil), a.clip...), a.clipCut
}

func validName(n string) error {
	if strings.TrimSpace(n) == "" || n == "." || n == ".." || strings.ContainsAny(n, `/\:*?"<>|`) || !utf8.ValidString(n) {
		return fmt.Errorf("“%s” isn't a valid name", n)
	}
	return nil
}

// terminal opens MW4W's terminal (settings: apps.terminal) in dir.
func (a *App) terminal(dir string) error {
	term := "wt"
	var s struct{ Apps struct{ Terminal string } }
	if b, err := os.ReadFile(filepath.Join(a.packDir, "settings.json")); err == nil && json.Unmarshal(b, &s) == nil && s.Apps.Terminal != "" {
		term = os.ExpandEnv(s.Apps.Terminal)
	}
	if !caseInsensitive && term == "wt" {
		term = "x-terminal-emulator"
	}
	var c *exec.Cmd
	switch strings.ToLower(strings.TrimSuffix(filepath.Base(term), ".exe")) {
	case "wt":
		c = exec.Command(term, "-d", dir)
	case "alacritty":
		c = exec.Command(term, "--working-directory", dir)
	case "wezterm":
		c = exec.Command(term, "start", "--cwd", dir)
	default:
		c = exec.Command(term)
	}
	c.Dir = dir
	return c.Start()
}

// ---------------------------------------------------------------- zip

func extractZip(src, dest string) (any, error) {
	z, err := zip.OpenReader(src)
	if err != nil {
		return nil, err
	}
	defer z.Close()
	base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	out := uniquePath(filepath.Join(dest, base))
	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}
	for _, f := range z.File {
		p := filepath.Join(out, filepath.FromSlash(f.Name))
		if !under(p, out) { // zip-slip
			continue
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(p, 0o755)
			continue
		}
		os.MkdirAll(filepath.Dir(p), 0o755)
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		w, err := os.Create(p)
		if err != nil {
			rc.Close()
			return nil, err
		}
		_, err = io.Copy(w, rc)
		rc.Close()
		w.Close()
		if err != nil {
			return nil, err
		}
		os.Chtimes(p, f.Modified, f.Modified)
	}
	return map[string]string{"path": out}, nil
}

func compressZip(paths []string, dest string) (any, error) {
	if !strings.HasSuffix(strings.ToLower(dest), ".zip") {
		dest += ".zip"
	}
	dest = uniquePath(dest)
	f, err := os.Create(dest)
	if err != nil {
		return nil, err
	}
	zw := zip.NewWriter(f)
	for _, root := range paths {
		baseDir := filepath.Dir(root)
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(baseDir, p)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				_, err := zw.Create(rel + "/")
				return err
			}
			fi, err := d.Info()
			if err != nil || !fi.Mode().IsRegular() {
				return nil
			}
			h, _ := zip.FileInfoHeader(fi)
			h.Name, h.Method = rel, zip.Deflate
			w, err := zw.CreateHeader(h)
			if err != nil {
				return err
			}
			src, err := os.Open(p)
			if err != nil {
				return nil
			}
			_, err = io.Copy(w, src)
			src.Close()
			return err
		})
		if err != nil {
			break
		}
	}
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	f.Close()
	if err != nil {
		os.Remove(dest)
		return nil, err
	}
	return map[string]string{"path": dest}, nil
}

// uniquePath: p, or "name (2).ext" etc. when p exists.
func uniquePath(p string) string {
	if _, err := os.Lstat(p); err != nil {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		base, ext = p, ""
	}
	for i := 2; ; i++ {
		q := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Lstat(q); err != nil {
			return q
		}
	}
}

// appendStr appends s as a JSON string.
func appendStr(b []byte, s string) []byte {
	const hex = "0123456789abcdef"
	b = append(b, '"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch {
			case c == '"' || c == '\\':
				b = append(b, '\\', c)
			case c < 0x20:
				b = append(b, '\\', 'u', '0', '0', hex[c>>4], hex[c&15])
			default:
				b = append(b, c)
			}
			i++
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && n == 1 {
			b = append(b, `�`...)
		} else {
			b = append(b, s[i:i+n]...)
		}
		i += n
	}
	return append(b, '"')
}
