package main

// Conversions (Filee's idea: drop files on a format, the result lands next to them), all in Go.
//
// Every format belongs to a family with one in-between form: images decode to image.Image, audio to
// PCM, documents to Markdown, data to Go values, archives to a folder. A conversion is decode + encode,
// so every input of a family reaches every output of it, and documents reach PDF (DOCX -> MD -> PDF).
// Tools that take all the files at once (merge PDFs, pictures into one PDF, ZIP) are "multi" targets.
//
//	GET  /api/convert?p=<path>&p=...     what these files can become (front: common, back: the rest)
//	POST /api/convert {paths, to}        start a job; GET /api/jobs: progress, results, errors
//	mw4w-files.exe convert <to> <files>  the same from a terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// target: one slice of the donut.
type target struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Hint  string `json:"hint,omitempty"`
	Back  bool   `json:"back,omitempty"`  // the donut's other side (Tab / wheel)
	Multi bool   `json:"multi,omitempty"` // all the files into one result
	Icon  string `json:"icon,omitempty"`
}

// family: formats with one in-between form.
type family struct {
	name string
	in   map[string]bool // extensions read (".png")
	out  []target        // what each file can become
	// targets, when set, picks them itself (out and multi aren't used)
	targets func(paths []string) []target
	// convert writes src as target t into dir, returns the files made
	convert func(ctx context.Context, src string, t target, dir string) ([]string, error)
	// multi: targets over all the files (they all belong to this family)
	multi    []target
	convertN func(ctx context.Context, srcs []string, t target, dir string) ([]string, error)
}

var families []*family

func exts(s string) map[string]bool {
	m := map[string]bool{}
	for _, e := range strings.Fields(s) {
		m["."+e] = true
	}
	return m
}

// convExt: ".tar.gz" and friends count as one extension.
func convExt(p string) string {
	l := strings.ToLower(filepath.Base(p))
	for _, e := range []string{".tar.gz", ".tar.xz", ".tar.bz2", ".tar.zst"} {
		if strings.HasSuffix(l, e) {
			return e
		}
	}
	return filepath.Ext(l)
}

func stem(p string) string {
	b := filepath.Base(p)
	return b[:len(b)-len(convExt(p))]
}

func familyOf(p string) *family {
	e := convExt(p)
	for _, f := range families {
		if f.in[e] {
			return f
		}
	}
	return nil
}

// targetsFor: what all these files can become. Each file's own format isn't offered for itself
// (PNG -> PNG), but tools (resize, compress) are.
func targetsFor(paths []string) []target {
	if len(paths) == 0 {
		return nil
	}
	var out []target
	var fam *family
	same := true
	for i, p := range paths {
		f := familyOf(p)
		if f == nil {
			same = false
			break
		}
		if i == 0 {
			fam = f
		} else if f != fam {
			same = false
		}
	}
	if same && fam != nil {
		allSame := map[string]bool{}
		for _, p := range paths {
			allSame[convExt(p)] = true
		}
		tl := fam.out
		if fam.targets != nil {
			tl = fam.targets(paths)
		}
		for _, t := range tl {
			if len(allSame) == 1 && allSame["."+t.ID] {
				continue
			}
			out = append(out, t)
		}
		if len(paths) > 1 {
			out = append(out, fam.multi...)
		}
	}
	// any files: one archive
	out = append(out, target{ID: "zip-all", Label: "ZIP", Hint: "all in one archive", Multi: true, Back: same && fam != nil, Icon: "zip"})
	return out
}

func findTarget(paths []string, id string) (target, bool) {
	for _, t := range targetsFor(paths) {
		if t.ID == id {
			return t, true
		}
	}
	return target{}, false
}

// outPath: name.ext next to the source, never over an existing file.
func outPath(dir, name, ext string) string {
	return uniquePath(filepath.Join(dir, name+ext))
}

// writeOut creates p only if it's new and removes it again when writing fails (no half files).
func writeOut(p string, write func(f *os.File) error) error {
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	err = write(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(p)
	}
	return err
}

// ---------------------------------------------------------------- jobs

type job struct {
	ID      int64    `json:"id"`
	To      string   `json:"to"`
	Label   string   `json:"label"`
	Paths   []string `json:"paths"`
	Total   int      `json:"total"`
	Done    int      `json:"done"`
	Out     []string `json:"out"`
	Errors  []string `json:"errors"`
	Started int64    `json:"started"`
	Ended   int64    `json:"ended,omitempty"`
	cancel  context.CancelFunc
}

type jobs struct {
	mu   sync.Mutex
	list []*job
	n    atomic.Int64
	sem  chan struct{}
}

var convJobs = &jobs{sem: make(chan struct{}, max(2, runtime.NumCPU()/2))}

// start runs t over paths in the background (files in parallel) and returns the job at once.
func (js *jobs) start(paths []string, t target, done func(*job)) *job {
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{ID: js.n.Add(1), To: t.ID, Label: t.Label, Paths: paths, Total: len(paths), Started: time.Now().Unix(), cancel: cancel, Out: []string{}, Errors: []string{}}
	if t.Multi {
		j.Total = 1
	}
	js.mu.Lock()
	js.list = append(js.list, j)
	if len(js.list) > 50 {
		js.list = js.list[len(js.list)-50:]
	}
	js.mu.Unlock()
	go func() {
		defer cancel()
		finish := func(out []string, err error) {
			js.mu.Lock()
			j.Done++
			j.Out = append(j.Out, out...)
			if err != nil {
				j.Errors = append(j.Errors, err.Error())
			}
			js.mu.Unlock()
		}
		if t.Multi {
			js.sem <- struct{}{}
			finish(convertAll(ctx, paths, t))
			<-js.sem
		} else {
			var wg sync.WaitGroup
			for _, p := range paths {
				wg.Add(1)
				js.sem <- struct{}{}
				go func(p string) {
					defer func() { <-js.sem; wg.Done() }()
					out, err := convertOne(ctx, p, t)
					if err != nil {
						err = fmt.Errorf("%s: %s", filepath.Base(p), friendly(err))
					}
					finish(out, err)
				}(p)
			}
			wg.Wait()
		}
		js.mu.Lock()
		j.Ended = time.Now().Unix()
		js.mu.Unlock()
		if done != nil {
			done(j)
		}
	}()
	return j
}

func (js *jobs) snapshot() []job {
	js.mu.Lock()
	defer js.mu.Unlock()
	out := make([]job, len(js.list))
	for i, j := range js.list {
		out[i] = *j
		out[i].Out = append([]string{}, j.Out...)
		out[i].Errors = append([]string{}, j.Errors...)
	}
	return out
}

func (js *jobs) cancelJob(id int64) {
	js.mu.Lock()
	defer js.mu.Unlock()
	for _, j := range js.list {
		if j.ID == id && j.cancel != nil {
			j.cancel()
		}
	}
}

func convertOne(ctx context.Context, p string, t target) (out []string, err error) {
	defer func() {
		if r := recover(); r != nil { // a broken file must not take the app down
			err = fmt.Errorf("couldn't read it (%v)", r)
		}
	}()
	f := familyOf(p)
	if f == nil {
		return nil, errors.New("unknown format")
	}
	return f.convert(ctx, p, t, filepath.Dir(p))
}

func convertAll(ctx context.Context, paths []string, t target) (out []string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("couldn't read the files (%v)", r)
		}
	}()
	dir := filepath.Dir(paths[0])
	if t.ID == "zip-all" {
		name := "Archive"
		if len(paths) == 1 {
			name = stem(paths[0])
		}
		r, err := compressZip(paths, filepath.Join(dir, name+".zip"))
		if err != nil {
			return nil, err
		}
		return []string{r.(map[string]string)["path"]}, nil
	}
	f := familyOf(paths[0])
	if f == nil || f.convertN == nil {
		return nil, errors.New("unknown format")
	}
	return f.convertN(ctx, paths, t, dir)
}

// ---------------------------------------------------------------- API

func (a *App) apiConvert(r *http.Request) (any, error) {
	if r.Method == "POST" {
		var req struct {
			Paths []string `json:"paths"`
			To    string   `json:"to"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return nil, err
		}
		t, ok := findTarget(req.Paths, req.To)
		if !ok {
			return nil, fmt.Errorf("Can't make %s of these files", req.To)
		}
		for _, p := range req.Paths {
			if _, err := os.Stat(p); err != nil {
				return nil, err
			}
		}
		j := convJobs.start(req.Paths, t, nil)
		return map[string]any{"id": j.ID}, nil
	}
	return map[string]any{"targets": orNone(targetsFor(r.URL.Query()["p"]))}, nil
}

func orNone(t []target) []target {
	if t == nil {
		return []target{}
	}
	return t
}

func (a *App) apiJobs(r *http.Request) (any, error) {
	if r.Method == "POST" {
		var req struct {
			Cancel int64 `json:"cancel"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		convJobs.cancelJob(req.Cancel)
	}
	list := convJobs.snapshot()
	sort.Slice(list, func(i, j int) bool { return list[i].ID > list[j].ID })
	return list, nil
}

// ---------------------------------------------------------------- command line

// cliConvert: mw4w-files.exe convert <to> <files...>   (to: an id from "mw4w-files.exe convert list <files>")
func cliConvert(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mw4w-files convert <format> <files...>\n       mw4w-files convert list <files...>")
		return 2
	}
	to, paths := args[0], args[1:]
	for i, p := range paths {
		paths[i] = resolvePath(p)
	}
	if to == "list" {
		for _, t := range targetsFor(paths) {
			fmt.Printf("%-14s %s %s\n", t.ID, t.Label, t.Hint)
		}
		return 0
	}
	t, ok := findTarget(paths, to)
	if !ok {
		fmt.Fprintf(os.Stderr, "can't make %s of these files (see: convert list)\n", to)
		return 1
	}
	done := make(chan *job)
	convJobs.start(paths, t, func(j *job) { done <- j })
	j := <-done
	for _, o := range j.Out {
		fmt.Println(o)
	}
	for _, e := range j.Errors {
		fmt.Fprintln(os.Stderr, e)
	}
	if len(j.Errors) > 0 {
		return 1
	}
	return 0
}
