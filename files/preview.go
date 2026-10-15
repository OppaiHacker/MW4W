package main

// Previews: what the UI shows for a selected file. Media, PDF and fonts load straight from /raw
// (the WebView decodes them); text and code come highlighted from here (chroma), archives as a list.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

const (
	textMax      = 1 << 20   // text shown
	highlightMax = 256 << 10 // highlighted (beyond: plain)
)

type preview struct {
	Kind     string     `json:"kind"`
	Name     string     `json:"name"`
	Path     string     `json:"path"`
	Size     int64      `json:"size"`
	Mtime    int64      `json:"mtime"`
	IsDir    bool       `json:"isDir"`
	Hidden   bool       `json:"hidden"`
	ReadOnly bool       `json:"readOnly"`
	Html     string     `json:"html,omitempty"` // highlighted code
	Text     string     `json:"text,omitempty"` // markdown / csv / plain
	Lang     string     `json:"lang,omitempty"`
	Lines    int        `json:"lines,omitempty"`
	Cut      bool       `json:"cut,omitempty"` // only the start of the file
	Width    int        `json:"width,omitempty"`
	Height   int        `json:"height,omitempty"`
	Entries  []arcEntry `json:"entries,omitempty"`
	Total    int        `json:"total,omitempty"`
	Hex      string     `json:"hex,omitempty"`
	Items    int        `json:"items"`
	DirSize  int64      `json:"dirSize"`
	DirFiles int64      `json:"dirFiles"`
	Indexed  bool       `json:"indexed"`
	Ms       float64    `json:"ms"`
}

type arcEntry struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	IsDir bool   `json:"isDir"`
}

var codeFormatter = html.New(html.WithClasses(true), html.WithLineNumbers(true), html.TabWidth(4))

func chromaCSS() []byte {
	var b bytes.Buffer
	st := styles.Get("catppuccin-mocha")
	codeFormatter.WriteCSS(&b, st)
	return b.Bytes()
}

func (a *App) preview(path string) (*preview, error) {
	t0 := time.Now()
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	p := &preview{Name: filepath.Base(path), Path: path, Size: fi.Size(), Mtime: fi.ModTime().Unix(), IsDir: fi.IsDir(),
		Hidden: isHidden(filepath.Base(path), fi), ReadOnly: fi.Mode().Perm()&0o200 == 0}
	p.Kind = kindOf(p.Name, p.IsDir)
	switch p.Kind {
	case "dir":
		p.Size = 0
		if f, err := os.Open(path); err == nil {
			names, _ := f.Readdirnames(-1)
			p.Items = len(names)
			f.Close()
		}
		p.DirSize, p.DirFiles, p.Indexed = a.ix.dirSize(filepath.Clean(path))
	case "image":
		if f, err := os.Open(path); err == nil {
			if c, _, err := image.DecodeConfig(f); err == nil {
				p.Width, p.Height = c.Width, c.Height
			}
			f.Close()
		}
	case "archive":
		p.Entries, p.Total, err = listArchive(path)
		if err != nil {
			p.Kind = "file"
			err = nil
		}
	case "code", "text", "markdown", "file":
		if p.Size == 0 {
			p.Kind = "text"
			break
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		b := make([]byte, textMax)
		n, _ := io.ReadFull(f, b)
		f.Close()
		b = b[:n]
		p.Cut = int64(n) < p.Size
		if !looksText(b) {
			p.Kind = "file"
			p.Hex = hexDump(b[:min(n, 512)])
			break
		}
		s := strings.ToValidUTF8(string(b), "�")
		s = strings.TrimPrefix(s, "\ufeff")
		p.Lines = strings.Count(s, "\n") + 1
		ext := extOf(p.Name, false)
		switch {
		case p.Kind == "markdown":
			p.Text = s
			p.Html, p.Lang = highlight(p.Name, s)
		case ext == "csv" || ext == "tsv":
			p.Kind = "csv"
			p.Text = s
		default:
			if p.Kind == "file" {
				p.Kind = "text"
			}
			if len(s) <= highlightMax {
				p.Html, p.Lang = highlight(p.Name, s)
				if p.Lang != "plaintext" {
					p.Kind = "code"
				}
			}
			if p.Html == "" {
				p.Text = s
			}
		}
	}
	p.Ms = float64(time.Since(t0).Microseconds()) / 1000
	return p, nil
}

func highlight(name, src string) (string, string) {
	l := lexers.Match(name)
	if l == nil {
		l = lexers.Analyse(src)
	}
	if l == nil {
		l = lexers.Fallback
	}
	l = chroma.Coalesce(l)
	it, err := l.Tokenise(nil, src)
	if err != nil {
		return "", ""
	}
	var b bytes.Buffer
	if codeFormatter.Format(&b, styles.Get("catppuccin-mocha"), it) != nil {
		return "", ""
	}
	return b.String(), strings.ToLower(l.Config().Name)
}

func listArchive(path string) ([]arcEntry, int, error) {
	const max = 2000
	var out []arcEntry
	total := 0
	ext := strings.ToLower(path)
	switch {
	case strings.HasSuffix(ext, ".tar"), strings.HasSuffix(ext, ".tar.gz"), strings.HasSuffix(ext, ".tgz"),
		strings.HasSuffix(ext, ".tar.bz2"), strings.HasSuffix(ext, ".tbz"):
		f, err := os.Open(path)
		if err != nil {
			return nil, 0, err
		}
		defer f.Close()
		var r io.Reader = f
		if strings.HasSuffix(ext, "gz") {
			gz, err := gzip.NewReader(f)
			if err != nil {
				return nil, 0, err
			}
			r = gz
		} else if strings.Contains(ext, ".tbz") || strings.HasSuffix(ext, "bz2") {
			r = bzip2.NewReader(f)
		}
		tr := tar.NewReader(r)
		for {
			h, err := tr.Next()
			if err != nil {
				break
			}
			total++
			if len(out) < max {
				out = append(out, arcEntry{h.Name, h.Size, h.Typeflag == tar.TypeDir})
			}
		}
		return out, total, nil
	case strings.HasSuffix(ext, ".gz"):
		return nil, 0, fmt.Errorf("not an archive")
	}
	z, err := zip.OpenReader(path)
	if err != nil {
		return nil, 0, err
	}
	defer z.Close()
	for _, f := range z.File {
		total++
		if len(out) < max {
			out = append(out, arcEntry{f.Name, int64(f.UncompressedSize64), strings.HasSuffix(f.Name, "/")})
		}
	}
	return out, total, nil
}

func hexDump(b []byte) string {
	var s strings.Builder
	for i := 0; i < len(b); i += 16 {
		row := b[i:min(i+16, len(b))]
		fmt.Fprintf(&s, "%08x  ", i)
		for j := 0; j < 16; j++ {
			if j < len(row) {
				fmt.Fprintf(&s, "%02x ", row[j])
			} else {
				s.WriteString("   ")
			}
			if j == 7 {
				s.WriteByte(' ')
			}
		}
		s.WriteString(" ")
		for _, c := range row {
			if c < 32 || c > 126 {
				c = '.'
			}
			s.WriteByte(c)
		}
		s.WriteByte('\n')
	}
	return s.String()
}
