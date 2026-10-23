package main

// PDFs (pdfcpu): merge, split, compress, rotate; their text goes the documents' way (TXT, DOCX...).

import (
	"context"
	"os"
	"path/filepath"
	"sort"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func init() {
	model.ConfigPath = "disable" // no config folder in the user's AppData
	families = append(families, &family{
		name: "pdf",
		in:   exts("pdf"),
		out: []target{
			{ID: "compress", Label: "Smaller", Hint: "optimized PDF", Icon: "resize"},
			{ID: "split", Label: "Split", Hint: "a PDF per page", Icon: "pdf"},
			{ID: "docx", Label: "DOCX", Hint: "its text", Icon: "doc"},
			{ID: "txt", Label: "TXT", Hint: "its text", Icon: "lines"},
			{ID: "rotr", Label: "↻ 90°", Back: true, Icon: "rotate"},
			{ID: "rotl", Label: "↺ 90°", Back: true, Icon: "rotate"},
			{ID: "md", Label: "Markdown", Hint: "its text", Back: true, Icon: "lines"},
			{ID: "html", Label: "HTML", Hint: "its text", Back: true, Icon: "code"},
		},
		multi:    []target{{ID: "merge", Label: "Merge", Hint: "one PDF, in name order", Multi: true, Icon: "pdf"}},
		convert:  convertPDF,
		convertN: mergePDF,
	})
}

func convertPDF(ctx context.Context, src string, t target, dir string) ([]string, error) {
	conf := model.NewDefaultConfiguration()
	switch t.ID {
	case "compress":
		p := outPath(dir, stem(src)+" (smaller)", ".pdf")
		return []string{p}, viaTemp(p, func(tmp string) error { return api.OptimizeFile(ctx, src, tmp, conf, nil) })
	case "rotr", "rotl":
		deg := 90
		if t.ID == "rotl" {
			deg = -90
		}
		p := outPath(dir, stem(src)+" (rotated)", ".pdf")
		return []string{p}, viaTemp(p, func(tmp string) error { return api.RotateFile(ctx, src, tmp, deg, nil, conf) })
	case "split":
		out := uniquePath(filepath.Join(dir, stem(src)+" (pages)"))
		if err := os.MkdirAll(out, 0o755); err != nil {
			return nil, err
		}
		if err := api.SplitFile(ctx, src, out, 1, conf); err != nil {
			os.RemoveAll(out)
			return nil, err
		}
		return []string{out}, nil
	}
	// the text: as a document
	tmp, err := os.MkdirTemp("", "mw4w-pdf-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	m, title, err := toMarkdownPDF(src)
	if err != nil {
		return nil, err
	}
	mp := filepath.Join(tmp, title+".md")
	if err := os.WriteFile(mp, []byte(m), 0o644); err != nil {
		return nil, err
	}
	return convertDoc(ctx, mp, t, dir)
}

// viaTemp: pdfcpu writes into tmp next to p; p only appears when it's complete.
func viaTemp(p string, write func(tmp string) error) error {
	tmp := p + ".part"
	if err := write(tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	if _, err := os.Lstat(p); err == nil {
		os.Remove(tmp)
		return os.ErrExist
	}
	return os.Rename(tmp, p)
}

func mergePDF(ctx context.Context, srcs []string, t target, dir string) ([]string, error) {
	s := append([]string(nil), srcs...)
	sort.Slice(s, func(i, j int) bool { return natLess(filepath.Base(s[i]), filepath.Base(s[j])) })
	p := outPath(dir, stem(s[0])+" (merged)", ".pdf")
	return []string{p}, viaTemp(p, func(tmp string) error {
		return api.MergeCreateFile(ctx, s, tmp, false, model.NewDefaultConfiguration())
	})
}

// natLess: "page 2" before "page 10".
func natLess(a, b string) bool {
	for a != "" && b != "" {
		ca, cb := a[0], b[0]
		if isDigit(ca) && isDigit(cb) {
			i, j := 0, 0
			for i < len(a) && isDigit(a[i]) {
				i++
			}
			for j < len(b) && isDigit(b[j]) {
				j++
			}
			na, nb := trimZeros(a[:i]), trimZeros(b[:j])
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			a, b = a[i:], b[j:]
			continue
		}
		la, lb := lowerByte(ca), lowerByte(cb)
		if la != lb {
			return la < lb
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func lowerByte(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}
func trimZeros(s string) string {
	for len(s) > 1 && s[0] == '0' {
		s = s[1:]
	}
	return s
}
