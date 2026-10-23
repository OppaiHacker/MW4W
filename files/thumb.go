package main

// Thumbnails: Windows' own (IShellItemImageFactory: photos, videos, PDFs, Office files, app icons,
// from the system thumbnail cache) where there is one, else decoded here for images. Kept in the
// index database by path+size+mtime, and the URL carries those, so the WebView caches them forever.

import (
	"bytes"
	"database/sql"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"runtime"
	"sync"
	"time"

	"golang.org/x/image/draw"
)

type thumbber struct {
	db    *Index
	sem   chan struct{}
	mu    sync.Mutex
	inflt map[string]*thumbCall
}

type thumbCall struct {
	done chan struct{}
	mime string
	data []byte
}

func newThumbber(ix *Index) *thumbber {
	return &thumbber{db: ix, sem: make(chan struct{}, max(2, runtime.NumCPU()/2)), inflt: map[string]*thumbCall{}}
}

// get returns an encoded thumbnail no bigger than px, or nil.
func (t *thumbber) get(path string, px int) (string, []byte) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", nil
	}
	key := fmt.Sprintf("%s|%d|%d|%d", path, fi.Size(), fi.ModTime().UnixNano(), px)
	var mime string
	var data []byte
	if t.db.r.QueryRow(`SELECT mime, data FROM thumbs WHERE key = ?`, key).Scan(&mime, &data) == nil {
		return mime, data
	}
	// one decode per key, however many windows ask
	t.mu.Lock()
	if c := t.inflt[key]; c != nil {
		t.mu.Unlock()
		<-c.done
		return c.mime, c.data
	}
	c := &thumbCall{done: make(chan struct{})}
	t.inflt[key] = c
	t.mu.Unlock()

	t.sem <- struct{}{}
	img, err := shellThumb(path, px)
	if img == nil {
		img, err = decodeThumb(path, px)
	}
	<-t.sem
	if err == nil && img != nil {
		c.mime, c.data = encodeThumb(img)
		if c.data != nil {
			mime, data := c.mime, c.data
			t.db.later(func(tx *sql.Tx) {
				tx.Exec(`INSERT OR REPLACE INTO thumbs(key, mime, data, ts) VALUES(?,?,?,?)`, key, mime, data, time.Now().Unix())
			})
		}
	}
	t.mu.Lock()
	delete(t.inflt, key)
	t.mu.Unlock()
	close(c.done)
	return c.mime, c.data
}

func decodeThumb(path string, px int) (image.Image, error) {
	if kindOf(path, false) != "image" {
		return nil, fmt.Errorf("no thumbnail")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if c, _, err := image.DecodeConfig(f); err != nil || c.Width*c.Height > 80e6 {
		return nil, fmt.Errorf("can't decode")
	}
	f.Seek(0, 0)
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	return scale(src, px), nil
}

func scale(src image.Image, px int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= px && h <= px {
		return src
	}
	if w >= h {
		h, w = max(1, h*px/w), px
	} else {
		w, h = max(1, w*px/h), px
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst
}

// encodeThumb: JPEG when opaque (small), PNG when it has transparency (icons).
func encodeThumb(img image.Image) (string, []byte) {
	var b bytes.Buffer
	if opaque(img) {
		if jpeg.Encode(&b, img, &jpeg.Options{Quality: 82}) == nil {
			return "image/jpeg", b.Bytes()
		}
		return "", nil
	}
	e := png.Encoder{CompressionLevel: png.BestSpeed}
	if e.Encode(&b, img) == nil {
		return "image/png", b.Bytes()
	}
	return "", nil
}

func opaque(img image.Image) bool {
	if o, ok := img.(interface{ Opaque() bool }); ok {
		return o.Opaque()
	}
	return true
}
