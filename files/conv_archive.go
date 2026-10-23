package main

// Archives: ZIP, 7z, RAR, TAR (plain, gz, xz, bz2, zst) and single compressed files (gz, xz, bz2,
// zst). Each is unpacked to a folder; "Extract" keeps it, the others pack it again.

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/bodgit/sevenzip"
	"github.com/klauspost/compress/zstd"
	"github.com/nwaples/rardecode/v2"
	"github.com/ulikunitz/xz"
)

func init() {
	families = append(families, &family{
		name: "archive",
		in:   exts("zip 7z rar tar tgz tar.gz txz tar.xz tbz2 tar.bz2 tzst tar.zst gz xz bz2 zst"),
		out: []target{
			{ID: "extract", Label: "Extract", Hint: "into a folder", Icon: "folder"},
			{ID: "zip", Label: "ZIP", Icon: "zip"},
			{ID: "tar.gz", Label: "TAR.GZ", Icon: "zip"},
			{ID: "tar.xz", Label: "TAR.XZ", Hint: "smallest", Icon: "zip"},
			{ID: "tar.zst", Label: "TAR.ZST", Hint: "fast", Back: true, Icon: "zip"},
			{ID: "tar", Label: "TAR", Back: true, Icon: "zip"},
		},
		convert: convertArchive,
	})
}

// single: one compressed file, not an archive of many.
func single(p string) bool {
	switch convExt(p) {
	case ".gz", ".xz", ".bz2", ".zst":
		return true
	}
	return false
}

func convertArchive(ctx context.Context, src string, t target, dir string) ([]string, error) {
	if t.ID == "extract" {
		if single(src) {
			p := outPath(dir, stem(src), "")
			return []string{p}, writeOut(p, func(f *os.File) error { return unpackOne(src, f) })
		}
		out := uniquePath(filepath.Join(dir, stem(src)))
		if err := extractAny(ctx, src, out); err != nil {
			os.RemoveAll(out)
			return nil, err
		}
		return []string{out}, nil
	}
	tmp, err := os.MkdirTemp(dir, ".mw4w-convert-")
	if err != nil {
		tmp, err = os.MkdirTemp("", "mw4w-convert-")
		if err != nil {
			return nil, err
		}
	}
	defer os.RemoveAll(tmp)
	if single(src) {
		if err := writeOut(filepath.Join(tmp, stem(src)), func(f *os.File) error { return unpackOne(src, f) }); err != nil {
			return nil, err
		}
	} else if err := extractAny(ctx, src, tmp); err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(tmp)
	if err != nil {
		return nil, err
	}
	var roots []string
	for _, e := range ents {
		roots = append(roots, filepath.Join(tmp, e.Name()))
	}
	if t.ID == "zip" {
		r, err := compressZip(roots, filepath.Join(dir, stem(src)+".zip"))
		if err != nil {
			return nil, err
		}
		return []string{r.(map[string]string)["path"]}, nil
	}
	p := outPath(dir, stem(src), "."+t.ID)
	return []string{p}, writeOut(p, func(f *os.File) error { return writeTar(ctx, f, tmp, t.ID) })
}

func unpackOne(src string, w io.Writer) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	r, err := decompressor(convExt(src), bufio.NewReader(f))
	if err != nil {
		return err
	}
	_, err = io.Copy(w, r)
	return err
}

func decompressor(ext string, r io.Reader) (io.Reader, error) {
	switch ext {
	case ".gz", ".tgz", ".tar.gz":
		return gzip.NewReader(r)
	case ".xz", ".txz", ".tar.xz":
		return xz.NewReader(r)
	case ".bz2", ".tbz2", ".tar.bz2":
		return bzip2.NewReader(r), nil
	case ".zst", ".tzst", ".tar.zst":
		d, err := zstd.NewReader(r)
		if err != nil {
			return nil, err
		}
		return d.IOReadCloser(), nil
	}
	return r, nil
}

// extractAny unpacks an archive into out (made if needed). Entries can't leave it (zip-slip).
func extractAny(ctx context.Context, src, out string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	put := func(name string, dir bool, mod time.Time, open func() (io.ReadCloser, error)) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		p := filepath.Join(out, filepath.FromSlash(name))
		if !under(p, out) || p == out {
			return nil
		}
		if dir {
			return os.MkdirAll(p, 0o755)
		}
		os.MkdirAll(filepath.Dir(p), 0o755)
		rc, err := open()
		if err != nil {
			return err
		}
		defer rc.Close()
		w, err := os.Create(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, rc)
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		if err == nil && !mod.IsZero() {
			os.Chtimes(p, mod, mod)
		}
		return err
	}
	switch ext := convExt(src); ext {
	case ".zip":
		z, err := zip.OpenReader(src)
		if err != nil {
			return err
		}
		defer z.Close()
		for _, f := range z.File {
			if err := put(f.Name, f.FileInfo().IsDir(), f.Modified, f.Open); err != nil {
				return err
			}
		}
		return nil
	case ".7z":
		z, err := sevenzip.OpenReader(src)
		if err != nil {
			return err
		}
		defer z.Close()
		for _, f := range z.File {
			if err := put(f.Name, f.FileInfo().IsDir(), f.Modified, f.Open); err != nil {
				return err
			}
		}
		return nil
	case ".rar":
		r, err := rardecode.OpenReader(src)
		if err != nil {
			return err
		}
		defer r.Close()
		for {
			h, err := r.Next()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			if err := put(h.Name, h.IsDir, h.ModificationTime, func() (io.ReadCloser, error) { return io.NopCloser(r), nil }); err != nil {
				return err
			}
		}
	default: // tar, possibly compressed
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		defer f.Close()
		dr, err := decompressor(ext, bufio.NewReader(f))
		if err != nil {
			return err
		}
		tr := tar.NewReader(dr)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir {
				continue // links and devices: not on Windows
			}
			if err := put(h.Name, h.Typeflag == tar.TypeDir, h.ModTime, func() (io.ReadCloser, error) { return io.NopCloser(tr), nil }); err != nil {
				return err
			}
		}
	}
}

// writeTar packs root's contents (not root itself) as tar, tar.gz, tar.xz or tar.zst.
func writeTar(ctx context.Context, w io.Writer, root, format string) error {
	bw := bufio.NewWriterSize(w, 1<<16)
	var cw io.WriteCloser
	var err error
	switch format {
	case "tar.gz":
		cw, err = gzip.NewWriterLevel(bw, gzip.BestCompression)
	case "tar.xz":
		cw, err = xz.NewWriter(bw)
	case "tar.zst":
		cw, err = zstd.NewWriter(bw, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))
	case "tar":
		cw = nopWriteCloser{bw}
	default:
		return errors.New("unknown archive format")
	}
	if err != nil {
		return err
	}
	tw := tar.NewWriter(cw)
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if !fi.IsDir() && !fi.Mode().IsRegular() {
			return nil
		}
		h, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		h.Name = filepath.ToSlash(rel)
		if fi.IsDir() {
			h.Name += "/"
		}
		h.Format = tar.FormatPAX // long and non-ASCII names
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		f.Close()
		return err
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := cw.Close(); err != nil {
		return err
	}
	return bw.Flush()
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
