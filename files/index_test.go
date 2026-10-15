package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIndexSearch(t *testing.T) {
	root := t.TempDir()
	data := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "src", "glass"), 0o755))
	must(os.WriteFile(filepath.Join(root, "src", "glass", "liquid.js"), []byte("// liquid glass shader\nconst x = 1\n"), 0o644))
	must(os.WriteFile(filepath.Join(root, "notes.md"), []byte("# shopping\nmilk and eggs\n"), 0o644))
	must(os.MkdirAll(filepath.Join(root, "node_modules", "skip"), 0o755))
	must(os.WriteFile(filepath.Join(root, "node_modules", "skip", "liquid-dep.js"), nil, 0o644))

	ix, err := openIndex(data)
	must(err)
	ix.cfg.Roots = []string{root}
	go ix.writer()
	ix.fullScan()

	names := func(q string, content bool) []string {
		hs, _, err := ix.search(context.Background(), searchReq{Q: q, Content: content, Limit: 50})
		must(err)
		var out []string
		for _, h := range hs {
			out = append(out, h.Name)
		}
		return out
	}
	if got := names("liquid", false); len(got) != 1 || got[0] != "liquid.js" {
		t.Fatalf("name search: %v (node_modules must be skipped)", got)
	}
	if got := names("ext:md", false); len(got) != 1 || got[0] != "notes.md" {
		t.Fatalf("ext filter: %v", got)
	}
	if got := names("gl", false); len(got) != 1 || got[0] != "glass" {
		t.Fatalf("short query: %v", got)
	}

	// contents: the content indexer runs after a change
	go ix.contentLoop()
	ix.kick <- struct{}{}
	deadline := time.Now().Add(10 * time.Second)
	for len(names("eggs", true)) == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if got := names("eggs", true); len(got) != 1 || got[0] != "notes.md" {
		t.Fatalf("content search: %v", got)
	}

	// a change: the folder is read again and the gone file leaves the index
	must(os.Remove(filepath.Join(root, "src", "glass", "liquid.js")))
	ix.changed([]string{filepath.Join(root, "src", "glass")})
	deadline = time.Now().Add(5 * time.Second)
	for len(names("liquid", false)) != 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if got := names("liquid", false); len(got) != 0 {
		t.Fatalf("deleted file still found: %v", got)
	}
	if size, files, ok := ix.dirSize(root); !ok || files != 1 || size == 0 {
		t.Fatalf("dirSize = %d %d %v", size, files, ok)
	}
}

func TestAppendStr(t *testing.T) {
	got := string(appendStr(nil, "a\"b\\c\n\x01ż\xff"))
	want := `"a\"b\\c\u000a\u0001ż�"`
	if got != want {
		t.Fatalf("%s != %s", got, want)
	}
}
