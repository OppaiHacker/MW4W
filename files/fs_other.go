//go:build !windows

package main

// Not Windows: enough to develop and test the UI (--serve). Trash is a folder in the data dir.

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const caseInsensitive = false

var watching = false

func isHidden(name string, fi fs.FileInfo) bool { return strings.HasPrefix(name, ".") }

func lowPriority() {}

func samePath(a, b string) bool { return filepath.Clean(a) == filepath.Clean(b) }

func allowForeground() {}

func singleInstance() bool { return true }

func defaultRoots() []string { return []string{homeDir()} }

func drives() []place {
	var st syscall.Statfs_t
	if syscall.Statfs("/", &st) != nil {
		return nil
	}
	return []place{{Name: "File System", Path: "/", Icon: "drive", Total: int64(st.Blocks) * st.Bsize, Free: int64(st.Bavail) * st.Bsize}}
}

func knownDirs() []place {
	var out []place
	for _, k := range [][2]string{{"Desktop", "desktop"}, {"Documents", "documents"}, {"Downloads", "downloads"}, {"Music", "music"}, {"Pictures", "pictures"}, {"Videos", "videos"}} {
		p := filepath.Join(homeDir(), k[0])
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			out = append(out, place{Name: k[0], Path: p, Icon: k[1]})
		}
	}
	return out
}

func shellOpen(p string) error  { return exec.Command("xdg-open", p).Start() }
func openWith(p string) error   { return errors.New("Open With is a Windows dialog") }
func properties(p string) error { return errors.New("Properties is a Windows dialog") }

func setClipFiles(paths []string, cut bool)                              {}
func getClipFiles() ([]string, bool, bool)                               { return nil, false, false }
func shellThumb(path string, px int) (image.Image, error)                { return nil, nil }
func watchRoots(roots []string, changed func([]string), overflow func()) {}

func runUI(a *App, path string, show bool) { select {} }

func trashDir() string { d := filepath.Join(dataDir(), "trash"); os.MkdirAll(d, 0o755); return d }

func trash(paths []string) error {
	for _, p := range paths {
		id := fmt.Sprintf("%d-%s", time.Now().UnixNano(), filepath.Base(p))
		dst := filepath.Join(trashDir(), id)
		if err := moveOne(p, dst); err != nil {
			return err
		}
		b, _ := json.Marshal(map[string]any{"orig": p, "deleted": time.Now().Unix()})
		os.WriteFile(dst+".trashinfo", b, 0o644)
	}
	return nil
}

func trashList() []trashItem {
	out := []trashItem{}
	ents, _ := os.ReadDir(trashDir())
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".trashinfo") {
			continue
		}
		var info struct {
			Orig    string
			Deleted int64
		}
		b, _ := os.ReadFile(filepath.Join(trashDir(), e.Name()))
		json.Unmarshal(b, &info)
		data := filepath.Join(trashDir(), strings.TrimSuffix(e.Name(), ".trashinfo"))
		fi, err := os.Stat(data)
		if err != nil {
			continue
		}
		it := trashItem{ID: data, Name: filepath.Base(info.Orig), Orig: info.Orig, Path: data, Deleted: info.Deleted, IsDir: fi.IsDir()}
		if !it.IsDir {
			it.Size = fi.Size()
		}
		it.Kind = kindOf(it.Name, it.IsDir)
		out = append(out, it)
	}
	return out
}

func trashRestore(ids []string) error {
	for _, id := range ids {
		var info struct{ Orig string }
		b, err := os.ReadFile(id + ".trashinfo")
		if err != nil || json.Unmarshal(b, &info) != nil {
			return errors.New("not in the trash")
		}
		dst := uniquePath(info.Orig)
		os.MkdirAll(filepath.Dir(dst), 0o755)
		if err := moveOne(id, dst); err != nil {
			return err
		}
		os.Remove(id + ".trashinfo")
	}
	return nil
}

func trashPurge(ids []string) error {
	for _, id := range ids {
		if filepath.Dir(id) != trashDir() {
			continue
		}
		os.RemoveAll(id)
		os.Remove(id + ".trashinfo")
	}
	return nil
}

func trashEmpty() error { os.RemoveAll(trashDir()); return nil }

func deleteForever(paths []string) error {
	for _, p := range paths {
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}

func transfer(paths []string, dest string, move bool) error {
	for _, p := range paths {
		dst := filepath.Join(dest, filepath.Base(p))
		if move && filepath.Dir(p) == filepath.Clean(dest) {
			continue
		}
		if under(dst, p) {
			return errors.New("Can't copy a folder into itself")
		}
		dst = uniquePath(dst)
		var err error
		if move {
			err = moveOne(p, dst)
		} else {
			err = copyTree(p, dst)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func moveOne(src, dst string) error {
	if os.Rename(src, dst) == nil {
		return nil
	}
	if err := copyTree(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(t, 0o755)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(t)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		out.Close()
		if fi, e := d.Info(); e == nil {
			os.Chtimes(t, fi.ModTime(), fi.ModTime())
		}
		return err
	})
}
