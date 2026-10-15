package main

// The file index: every file and folder under the roots, in SQLite.
//
//	dirs(id, path)                     one row per folder
//	files(id, parent, name, ext, ...)  one row per entry, UNIQUE(parent, name)
//	names                              FTS5 trigram over files.name: substring search in ~ms
//	                                   (the first scan fills it in one rebuild: 10x faster than per row)
//	body                               FTS5 (contentless) over the text of small text/code files
//
// Scans run on low-priority threads (several readers, one writer that batches whole folders into
// transactions); a scan diffs each folder against the rows it has, so rescans write only changes.
// On Windows ReadDirectoryChangesW reports changes as they happen (watch_windows.go).

import (
	"context"
	"database/sql"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS dirs(id INTEGER PRIMARY KEY, path TEXT NOT NULL UNIQUE, scanned INT NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS files(
  id INTEGER PRIMARY KEY, parent INT NOT NULL, name TEXT NOT NULL, ext TEXT NOT NULL,
  isdir INT NOT NULL, hidden INT NOT NULL, size INT NOT NULL, mtime INT NOT NULL, UNIQUE(parent, name));
CREATE INDEX IF NOT EXISTS files_mtime ON files(mtime);
CREATE VIRTUAL TABLE IF NOT EXISTS names USING fts5(name, content='files', content_rowid='id', tokenize='trigram');
DROP TRIGGER IF EXISTS files_ai;
CREATE TRIGGER IF NOT EXISTS files_ad AFTER DELETE ON files BEGIN
  INSERT INTO names(names, rowid, name) VALUES ('delete', old.id, old.name);
  DELETE FROM body WHERE rowid = old.id; DELETE FROM bodyver WHERE id = old.id; END;
CREATE VIRTUAL TABLE IF NOT EXISTS body USING fts5(text, content='', contentless_delete=1, tokenize='unicode61 remove_diacritics 2');
CREATE TABLE IF NOT EXISTS bodyver(id INTEGER PRIMARY KEY, size INT, mtime INT);
CREATE TABLE IF NOT EXISTS stars(path TEXT PRIMARY KEY, ts INT);
CREATE TABLE IF NOT EXISTS opened(path TEXT PRIMARY KEY, ts INT);
CREATE TABLE IF NOT EXISTS thumbs(key TEXT PRIMARY KEY, mime TEXT, data BLOB, ts INT);
CREATE TABLE IF NOT EXISTS meta(k TEXT PRIMARY KEY, v TEXT);
`

type Config struct {
	Roots        []string `json:"roots"`
	Exclude      []string `json:"exclude"`
	Content      bool     `json:"content"`
	ContentMaxKB int64    `json:"contentMaxKB"`
}

type entry struct {
	name   string
	dir    bool
	hidden bool
	link   bool
	size   int64
	mtime  int64
}

type dirScan struct {
	path  string
	ents  []entry
	found func(sub []string) // subfolders new to the index (a live update scans them too)
}

type bodyJob struct {
	id, size, mtime int64
	text            string // "" = not text: remember the version, index nothing
}

type Index struct {
	r, w    *sql.DB
	cfg     Config
	jobs    chan any
	busy    atomic.Int32 // scans running
	pending atomic.Int64 // folders waiting for the writer
	nFiles  atomic.Int64
	nDirs   atomic.Int64
	lastRun atomic.Int64
	kick    chan struct{} // content indexer: new or changed files
	excl    map[string]bool
	mu      sync.Mutex
	full    bool        // a full scan is running
	bulk    atomic.Bool // first scan: names is rebuilt at the end, search uses LIKE meanwhile
}

func openIndex(dataDir string) (*Index, error) {
	file := filepath.Join(dataDir, "index.db")
	dsn := "file:" + filepath.ToSlash(file) + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=temp_store(MEMORY)&_pragma=cache_size(-16000)&_pragma=mmap_size(268435456)"
	w, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	if _, err := w.Exec(schema); err != nil {
		w.Close()
		// a damaged or old index is only a cache: start over
		for _, f := range []string{"", "-wal", "-shm"} {
			os.Remove(file + f)
		}
		if w, err = sql.Open("sqlite", dsn); err != nil {
			return nil, err
		}
		w.SetMaxOpenConns(1)
		if _, err := w.Exec(schema); err != nil {
			return nil, err
		}
	}
	r, err := sql.Open("sqlite", dsn+"&_pragma=query_only(1)")
	if err != nil {
		return nil, err
	}
	r.SetMaxOpenConns(runtime.NumCPU())
	ix := &Index{r: r, w: w, jobs: make(chan any, 512), kick: make(chan struct{}, 1)}
	var n int64
	r.QueryRow(`SELECT count(*) FROM files`).Scan(&n)
	ix.bulk.Store(n == 0)
	ix.nFiles.Store(n)
	r.QueryRow(`SELECT count(*) FROM dirs`).Scan(&n)
	ix.nDirs.Store(n)
	ix.cfg = loadConfig(dataDir)
	ix.excl = map[string]bool{}
	for _, e := range ix.cfg.Exclude {
		ix.excl[strings.ToLower(e)] = true
	}
	return ix, nil
}

func loadConfig(dataDir string) Config {
	c := Config{
		Exclude:      []string{"node_modules", "$recycle.bin", "system volume information", "__pycache__", "windows", "$windows.~bt", "$winreagent", "recovery"},
		Content:      true,
		ContentMaxKB: 512,
	}
	f := filepath.Join(dataDir, "config.json")
	if b, err := os.ReadFile(f); err == nil {
		json.Unmarshal(b, &c)
	}
	if len(c.Roots) == 0 {
		c.Roots = defaultRoots()
		b, _ := json.MarshalIndent(c, "", "  ")
		os.WriteFile(f, b, 0o644) // written once, so it can be edited
	}
	return c
}

func (ix *Index) start() {
	go ix.writer()
	go ix.contentLoop()
	time.Sleep(1500 * time.Millisecond) // the first window first
	ix.count()
	go watchRoots(ix.cfg.Roots, ix.changed, func() { go ix.fullScan() })
	for {
		ix.fullScan()
		// the watcher keeps it current between full scans; without one (not Windows) scan more often
		if watching {
			time.Sleep(6 * time.Hour)
		} else {
			time.Sleep(20 * time.Minute)
		}
	}
}

func (ix *Index) fullScan() {
	ix.mu.Lock()
	if ix.full {
		ix.mu.Unlock()
		return
	}
	ix.full = true
	ix.mu.Unlock()
	defer func() { ix.mu.Lock(); ix.full = false; ix.mu.Unlock() }()
	for _, r := range ix.cfg.Roots {
		ix.scanTree([]string{filepath.Clean(r)})
	}
	if ix.bulk.Load() {
		done := make(chan struct{})
		ix.jobs <- func(tx *sql.Tx) { tx.Exec(`INSERT INTO names(names) VALUES('rebuild')`); close(done) }
		<-done
		ix.bulk.Store(false)
	}
	ix.lastRun.Store(time.Now().Unix())
	ix.count()
}

// skipDescent: folders whose insides aren't indexed (the folder itself is).
func (ix *Index) skipDescent(e entry) bool {
	return e.hidden || e.link || strings.HasPrefix(e.name, ".") || ix.excl[strings.ToLower(e.name)]
}

// scanTree reads every folder under dirs with a few low-priority threads.
func (ix *Index) scanTree(dirs []string) {
	ix.busy.Add(1)
	defer ix.busy.Add(-1)
	var (
		mu      sync.Mutex
		cond    = sync.NewCond(&mu)
		stack   = append([]string(nil), dirs...)
		working int
	)
	n := 4
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			lowPriority()
			for {
				mu.Lock()
				for len(stack) == 0 && working > 0 {
					cond.Wait()
				}
				if len(stack) == 0 {
					mu.Unlock()
					cond.Broadcast()
					return
				}
				d := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				working++
				mu.Unlock()

				ents, err := readDir(d)
				var subs []string
				if err == nil {
					for _, e := range ents {
						if e.dir && !ix.skipDescent(e) {
							subs = append(subs, filepath.Join(d, e.name))
						}
					}
					ix.pending.Add(1)
					ix.jobs <- dirScan{path: d, ents: ents}
				} else if os.IsNotExist(err) {
					ix.jobs <- dirGone(d)
				}
				mu.Lock()
				stack = append(stack, subs...)
				working--
				mu.Unlock()
				cond.Broadcast()
			}
		}()
	}
	wg.Wait()
}

type dirGone string

// changed: folders the watcher saw change. Each is read again; new subfolders are scanned whole.
func (ix *Index) changed(dirs []string) {
	for _, d := range dirs {
		// only folders the index has (AppData, caches and other skipped trees change all the time)
		var id int64
		if ix.r.QueryRow(`SELECT id FROM dirs WHERE path = ?`, d).Scan(&id) != nil {
			continue
		}
		ents, err := readDir(d)
		if err != nil {
			if os.IsNotExist(err) {
				ix.jobs <- dirGone(d)
			}
			continue
		}
		ix.pending.Add(1)
		ix.jobs <- dirScan{path: d, ents: ents, found: func(sub []string) { go ix.scanTree(sub) }}
	}
}

// sync: a folder the UI just listed updates the index too (free freshness, already read).
func (ix *Index) sync(dir string, ents []entry) {
	if !ix.underRoot(dir) {
		return
	}
	select {
	case ix.jobs <- dirScan{path: dir, ents: ents, found: func(sub []string) { go ix.scanTree(sub) }}:
		ix.pending.Add(1)
	default: // writer busy: the scan catches it
	}
}

func (ix *Index) underRoot(dir string) bool {
	for _, r := range ix.cfg.Roots {
		if under(dir, filepath.Clean(r)) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- writer

func (ix *Index) writer() {
	runtime.LockOSThread()
	lowPriority()
	for first := range ix.jobs {
		tx, err := ix.w.Begin()
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		st := newStmts(tx)
		deadline := time.Now().Add(300 * time.Millisecond)
		var found []func()
		job := first
		changed := false
		for {
			switch j := job.(type) {
			case dirScan:
				ix.pending.Add(-1)
				if subs, ch := ix.apply(tx, st, j); ch {
					changed = true
					if j.found != nil && len(subs) > 0 {
						f, s := j.found, subs
						found = append(found, func() { f(s) })
					}
				}
			case dirGone:
				ix.removeTree(tx, string(j))
				changed = true
			case bodyJob:
				st.bodyDel.Exec(j.id)
				if j.text != "" {
					st.bodyIns.Exec(j.id, j.text)
				}
				st.bodyVer.Exec(j.id, j.size, j.mtime)
			case func(*sql.Tx):
				j(tx)
			}
			if time.Now().After(deadline) {
				break
			}
			select {
			case job = <-ix.jobs:
				continue
			default:
			}
			break
		}
		st.close()
		tx.Commit()
		for _, f := range found {
			f()
		}
		if changed {
			select {
			case ix.kick <- struct{}{}:
			default:
			}
		}
	}
}

type stmts struct {
	dirGet, dirIns, dirTouch, kids, ins, name, upd, del, bodyDel, bodyIns, bodyVer *sql.Stmt
}

func newStmts(tx *sql.Tx) *stmts {
	p := func(q string) *sql.Stmt { s, _ := tx.Prepare(q); return s }
	return &stmts{
		dirGet:   p(`SELECT id FROM dirs WHERE path = ?`),
		dirIns:   p(`INSERT INTO dirs(path, scanned) VALUES(?, ?)`),
		dirTouch: p(`UPDATE dirs SET scanned = ? WHERE id = ?`),
		kids:     p(`SELECT id, name, isdir, hidden, size, mtime FROM files WHERE parent = ?`),
		ins:      p(`INSERT INTO files(parent, name, ext, isdir, hidden, size, mtime) VALUES(?,?,?,?,?,?,?)`),
		name:     p(`INSERT INTO names(rowid, name) VALUES(?, ?)`),
		upd:      p(`UPDATE files SET isdir = ?, hidden = ?, size = ?, mtime = ? WHERE id = ?`),
		del:      p(`DELETE FROM files WHERE id = ?`),
		bodyDel:  p(`DELETE FROM body WHERE rowid = ?`),
		bodyIns:  p(`INSERT INTO body(rowid, text) VALUES(?, ?)`),
		bodyVer:  p(`INSERT OR REPLACE INTO bodyver(id, size, mtime) VALUES(?,?,?)`),
	}
}

func (s *stmts) close() {
	for _, x := range []*sql.Stmt{s.dirGet, s.dirIns, s.dirTouch, s.kids, s.ins, s.name, s.upd, s.del, s.bodyDel, s.bodyIns, s.bodyVer} {
		if x != nil {
			x.Close()
		}
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// apply makes the rows of one folder match its listing. It returns the subfolders that are new.
func (ix *Index) apply(tx *sql.Tx, st *stmts, s dirScan) (newDirs []string, changed bool) {
	var id int64
	now := time.Now().Unix()
	if st.dirGet.QueryRow(s.path).Scan(&id) != nil {
		res, err := st.dirIns.Exec(s.path, now)
		if err != nil {
			return nil, false
		}
		id, _ = res.LastInsertId()
		ix.nDirs.Add(1)
	} else {
		st.dirTouch.Exec(now, id)
	}
	type row struct {
		id          int64
		dir, hidden bool
		size, mtime int64
		seen        bool
	}
	have := map[string]*row{}
	if rows, err := st.kids.Query(id); err == nil {
		for rows.Next() {
			var r row
			var name string
			rows.Scan(&r.id, &name, &r.dir, &r.hidden, &r.size, &r.mtime)
			have[name] = &r
		}
		rows.Close()
	}
	for _, e := range s.ents {
		r := have[e.name]
		if r == nil {
			res, err := st.ins.Exec(id, e.name, extOf(e.name, e.dir), b2i(e.dir), b2i(e.hidden), e.size, e.mtime)
			if err == nil && !ix.bulk.Load() {
				fid, _ := res.LastInsertId()
				st.name.Exec(fid, e.name)
			}
			ix.nFiles.Add(1)
			changed = true
			if e.dir && !ix.skipDescent(e) {
				newDirs = append(newDirs, filepath.Join(s.path, e.name))
			}
			continue
		}
		r.seen = true
		if r.dir != e.dir || r.hidden != e.hidden || r.size != e.size || r.mtime != e.mtime {
			st.upd.Exec(b2i(e.dir), b2i(e.hidden), e.size, e.mtime, r.id)
			changed = true
		}
	}
	for name, r := range have {
		if r.seen {
			continue
		}
		st.del.Exec(r.id)
		ix.nFiles.Add(-1)
		changed = true
		if r.dir {
			ix.removeTree(tx, filepath.Join(s.path, name))
		}
	}
	return newDirs, changed
}

// removeTree drops a folder that is gone, with everything under it.
func (ix *Index) removeTree(tx *sql.Tx, dir string) {
	pre := dir + string(filepath.Separator)
	tx.Exec(`DELETE FROM files WHERE parent IN (SELECT id FROM dirs WHERE path = ?1 OR substr(path, 1, ?2) = ?3)`, dir, len(pre), pre)
	tx.Exec(`DELETE FROM dirs WHERE path = ?1 OR substr(path, 1, ?2) = ?3`, dir, len(pre), pre)
}

func (ix *Index) count() {
	var n int64
	ix.r.QueryRow(`SELECT count(*) FROM files`).Scan(&n)
	ix.nFiles.Store(n)
	ix.r.QueryRow(`SELECT count(*) FROM dirs`).Scan(&n)
	ix.nDirs.Store(n)
}

// ---------------------------------------------------------------- contents

// contentLoop indexes the text of small text and code files that are new or changed since.
func (ix *Index) contentLoop() {
	if !ix.cfg.Content {
		return
	}
	runtime.LockOSThread()
	lowPriority()
	exts := make([]string, 0, len(textExts))
	for e := range textExts {
		exts = append(exts, "'"+e+"'")
	}
	sort.Strings(exts)
	q := `SELECT f.id, d.path, f.name, f.size, f.mtime FROM files f JOIN dirs d ON d.id = f.parent
	      LEFT JOIN bodyver b ON b.id = f.id
	      WHERE f.isdir = 0 AND f.size <= ? AND f.ext IN (` + strings.Join(exts, ",") + `)
	        AND (b.id IS NULL OR b.size <> f.size OR b.mtime <> f.mtime) LIMIT 400`
	max := ix.cfg.ContentMaxKB * 1024
	for {
		select {
		case <-ix.kick:
		case <-time.After(10 * time.Minute):
		}
		time.Sleep(2 * time.Second) // let a burst of changes settle
		for ix.busy.Load() > 0 {    // names first: the scan has the writer
			time.Sleep(time.Second)
		}
		for {
			rows, err := ix.r.Query(q, max)
			if err != nil {
				break
			}
			var jobs []bodyJob
			for rows.Next() {
				var j bodyJob
				var dir, name string
				rows.Scan(&j.id, &dir, &name, &j.size, &j.mtime)
				if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil && looksText(b) {
					j.text = string(b)
				}
				jobs = append(jobs, j)
			}
			rows.Close()
			for _, j := range jobs {
				ix.jobs <- j
			}
			if len(jobs) < 400 {
				break
			}
			// wait for the writer, so the next query sees these as done
			done := make(chan struct{})
			ix.jobs <- func(*sql.Tx) { close(done) }
			<-done
		}
	}
}

func looksText(b []byte) bool {
	n := len(b)
	if n > 8192 {
		n = 8192
	}
	for _, c := range b[:n] {
		if c == 0 {
			return false
		}
	}
	return utf8.Valid(b[:n]) || n < 8192 && utf8.Valid(b)
}

// ---------------------------------------------------------------- queries

type hit struct {
	Dir   string `json:"dir"`
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
	Line  int    `json:"line,omitempty"`
	Text  string `json:"text,omitempty"`
	score int
}

type searchReq struct {
	Q       string
	In      string // a folder: only under it
	Content bool
	Limit   int
}

// search: file names containing every word (trigram FTS), or with Content the text of files.
// Words may be ext:go, kind:image|video|audio|doc|code|archive|dir.
func (ix *Index) search(ctx context.Context, r searchReq) ([]hit, int, error) {
	var words, extF []string
	kind := ""
	for _, w := range strings.Fields(r.Q) {
		lw := strings.ToLower(w)
		switch {
		case strings.HasPrefix(lw, "ext:"):
			for _, e := range strings.Split(lw[4:], ",") {
				if e = strings.TrimPrefix(e, "."); e != "" {
					extF = append(extF, e)
				}
			}
		case strings.HasPrefix(lw, "kind:") || strings.HasPrefix(lw, "type:"):
			kind = lw[5:]
		default:
			words = append(words, lw)
		}
	}
	if len(words) == 0 && len(extF) == 0 && kind == "" {
		return nil, 0, nil
	}
	var (
		where []string
		args  []any
		from  = `files f JOIN dirs d ON d.id = f.parent`
		order = ""
	)
	if r.Content {
		if len(words) == 0 {
			return nil, 0, nil
		}
		var terms []string
		for _, w := range words {
			terms = append(terms, `"`+strings.ReplaceAll(w, `"`, `""`)+`"*`)
		}
		from = `body JOIN files f ON f.id = body.rowid JOIN dirs d ON d.id = f.parent`
		where = append(where, `body MATCH ?`)
		args = append(args, strings.Join(terms, " "))
		order = ` ORDER BY body.rank`
	} else {
		var terms []string
		for _, w := range words {
			if utf8.RuneCountInString(w) >= 3 && !ix.bulk.Load() {
				terms = append(terms, `"`+strings.ReplaceAll(w, `"`, `""`)+`"`)
			}
		}
		if len(terms) > 0 {
			from = `names JOIN files f ON f.id = names.rowid JOIN dirs d ON d.id = f.parent`
			where = append(where, `names MATCH ?`)
			args = append(args, strings.Join(terms, " AND "))
		}
		for _, w := range words {
			if utf8.RuneCountInString(w) < 3 || ix.bulk.Load() { // too short for trigrams: a scan, still fast
				where = append(where, `f.name LIKE ? ESCAPE '\'`)
				args = append(args, "%"+likeEsc(w)+"%")
			}
		}
	}
	if len(extF) > 0 {
		where = append(where, `f.ext IN (`+strings.TrimSuffix(strings.Repeat("?,", len(extF)), ",")+`)`)
		for _, e := range extF {
			args = append(args, e)
		}
	}
	if kind == "dir" || kind == "folder" {
		where = append(where, `f.isdir = 1`)
	} else if es := kindExts(kind); len(es) > 0 {
		where = append(where, `f.ext IN (`+strings.TrimSuffix(strings.Repeat("?,", len(es)), ",")+`)`)
		for _, e := range es {
			args = append(args, e)
		}
	}
	if r.In != "" {
		in := filepath.Clean(r.In)
		pre := in
		if !strings.HasSuffix(pre, string(filepath.Separator)) {
			pre += string(filepath.Separator)
		}
		if caseInsensitive {
			where = append(where, `(lower(d.path) = lower(?) OR lower(substr(d.path, 1, ?)) = lower(?))`)
		} else {
			where = append(where, `(d.path = ? OR substr(d.path, 1, ?) = ?)`)
		}
		args = append(args, in, len(pre), pre)
	}
	q := `SELECT d.path, f.name, f.isdir, f.size, f.mtime FROM ` + from + ` WHERE ` + strings.Join(where, " AND ") + order + ` LIMIT 20000`
	rows, err := ix.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var hits []hit
	home := homeDir()
	for rows.Next() {
		var h hit
		rows.Scan(&h.Dir, &h.Name, &h.IsDir, &h.Size, &h.Mtime)
		if !r.Content {
			h.score = nameScore(strings.ToLower(h.Name), words)
			if !under(h.Dir, home) {
				h.score += 4
			}
		} else {
			h.score = len(hits) // FTS rank order
		}
		hits = append(hits, h)
	}
	total := len(hits)
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.score != b.score {
			return a.score < b.score
		}
		if !r.Content {
			if len(a.Name) != len(b.Name) {
				return len(a.Name) < len(b.Name)
			}
			return a.Mtime > b.Mtime
		}
		return false
	})
	if r.Limit > 0 && len(hits) > r.Limit {
		hits = hits[:r.Limit]
	}
	if r.Content {
		n := len(hits)
		if n > 60 {
			n = 60
		}
		for i := range hits[:n] {
			hits[i].Line, hits[i].Text = snippet(filepath.Join(hits[i].Dir, hits[i].Name), words)
		}
	}
	return hits, total, nil
}

// nameScore: exact name 0, starts with the query 1, a word starts with it 2, anywhere 3.
func nameScore(name string, words []string) int {
	if len(words) == 0 {
		return 3
	}
	w := words[0]
	base := strings.TrimSuffix(name, filepath.Ext(name))
	switch {
	case name == w || base == w:
		return 0
	case strings.HasPrefix(name, w):
		return 1
	}
	i := strings.Index(name, w)
	if i > 0 && strings.ContainsRune(" ._-()[]", rune(name[i-1])) {
		return 2
	}
	return 3
}

func likeEsc(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// snippet: the first line of a file that has a query word.
func snippet(path string, words []string) (int, string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, ""
	}
	for i, line := range strings.Split(string(b), "\n") {
		l := strings.ToLower(line)
		for _, w := range words {
			if strings.Contains(l, w) {
				line = strings.TrimSpace(line)
				if len(line) > 200 {
					j := strings.Index(strings.ToLower(line), w)
					if j > 80 {
						line = "…" + line[j-60:]
					}
					if len(line) > 200 {
						line = line[:200] + "…"
					}
				}
				return i + 1, strings.ToValidUTF8(line, "")
			}
		}
	}
	return 0, ""
}

// recent: the files changed last under home (Nautilus's Recent, from the index).
func (ix *Index) recent(limit int) []hit {
	rows, err := ix.r.Query(`SELECT d.path, f.name, f.size, f.mtime FROM files f INDEXED BY files_mtime
		JOIN dirs d ON d.id = f.parent WHERE f.isdir = 0 AND f.hidden = 0 AND f.mtime <= ? ORDER BY f.mtime DESC LIMIT ?`,
		time.Now().Unix()+60, limit*3)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []hit
	for rows.Next() && len(out) < limit {
		var h hit
		rows.Scan(&h.Dir, &h.Name, &h.Size, &h.Mtime)
		if strings.Contains(strings.ToLower(h.Dir), string(filepath.Separator)+"appdata"+string(filepath.Separator)) {
			continue
		}
		out = append(out, h)
	}
	return out
}

// dirSize: the size of everything under a folder, as indexed. ok=false when it isn't indexed.
func (ix *Index) dirSize(dir string) (size, files int64, ok bool) {
	pre := dir + string(filepath.Separator)
	var id int64
	if ix.r.QueryRow(`SELECT id FROM dirs WHERE path = ?`, dir).Scan(&id) != nil {
		return 0, 0, false
	}
	ix.r.QueryRow(`SELECT coalesce(sum(f.size), 0), count(*) FROM files f WHERE f.isdir = 0 AND f.parent IN
		(SELECT id FROM dirs WHERE path = ?1 OR substr(path, 1, ?2) = ?3)`, dir, len(pre), pre).Scan(&size, &files)
	return size, files, true
}

func (ix *Index) meta(k string) string {
	var v string
	ix.r.QueryRow(`SELECT v FROM meta WHERE k = ?`, k).Scan(&v)
	return v
}

func (ix *Index) setMeta(k, v string) {
	ix.w.Exec(`INSERT OR REPLACE INTO meta(k, v) VALUES(?, ?)`, k, v)
}

// ---------------------------------------------------------------- listing

// readDir lists a folder: name, kind, size, mtime, hidden. Links are resolved to say what they point at.
// Big folders stat their entries on all cores (Windows has them from the enumeration already).
func readDir(dir string) ([]entry, error) {
	des, err := os.ReadDir(dir)
	if err != nil && len(des) == 0 {
		return nil, err
	}
	out := make([]entry, len(des))
	ok := make([]bool, len(des))
	fill := func(lo, hi int) {
		for i := lo; i < hi; i++ {
			de := des[i]
			fi, err := de.Info()
			if err != nil {
				continue
			}
			e := entry{name: de.Name(), dir: de.IsDir(), size: fi.Size(), mtime: fi.ModTime().Unix(), hidden: isHidden(de.Name(), fi)}
			if t := de.Type(); t&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
				e.link = true
				if st, err := os.Stat(filepath.Join(dir, de.Name())); err == nil {
					e.dir = st.IsDir()
					if !e.dir {
						e.size = st.Size()
					}
				}
			}
			if e.dir {
				e.size = 0
			}
			out[i], ok[i] = e, true
		}
	}
	if n := len(des); n < 2048 {
		fill(0, n)
	} else {
		var wg sync.WaitGroup
		step := (n + runtime.NumCPU() - 1) / runtime.NumCPU()
		for lo := 0; lo < n; lo += step {
			wg.Add(1)
			go func(lo, hi int) { defer wg.Done(); fill(lo, hi) }(lo, min(lo+step, n))
		}
		wg.Wait()
	}
	j := 0
	for i := range out {
		if ok[i] {
			out[j] = out[i]
			j++
		}
	}
	return out[:j], nil
}

func under(p, root string) bool {
	if caseInsensitive {
		p, root = strings.ToLower(p), strings.ToLower(root)
	}
	if p == root {
		return true
	}
	if !strings.HasSuffix(root, string(filepath.Separator)) {
		root += string(filepath.Separator)
	}
	return strings.HasPrefix(p, root)
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

func extOf(name string, dir bool) string {
	if dir {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
}
