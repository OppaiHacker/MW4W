package main

// Images: decoded to image.Image (EXIF orientation applied), encoded to the target. HEIC, AVIF,
// JPEG XL and WebP are libheif/libavif/libjxl/libwebp compiled to WebAssembly (gen2brain/*, no cgo).

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gen2brain/avif"
	"github.com/gen2brain/heic"
	"github.com/gen2brain/jpegxl"
	"github.com/gen2brain/webp"
	"golang.org/x/image/bmp"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/tiff"
)

func init() {
	families = append(families, &family{
		name:     "image",
		in:       exts("png apng jpg jpeg jfif gif bmp tif tiff webp heic heif avif jxl ico svg " + strings.Join(keysOf(convVideo), " ")),
		targets:  imageTargets,
		convert:  convertImage,
		convertN: convertImages,
	})
}

func keysOf(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k[1:])
	}
	return out
}

var (
	stillTools = []target{
		{ID: "gray", Label: "Gray", Hint: "black & white", Back: true, Icon: "contrast"},
		{ID: "rotr", Label: "↻ 90°", Back: true, Icon: "rotate"},
		{ID: "rotl", Label: "↺ 90°", Back: true, Icon: "rotate"},
		{ID: "flip", Label: "Mirror", Back: true, Icon: "flip"},
	}
	pictureOut = append([]target{
		{ID: "jpg", Label: "JPG", Icon: "img"},
		{ID: "png", Label: "PNG", Icon: "img"},
		{ID: "webp", Label: "WebP", Icon: "img"},
		{ID: "avif", Label: "AVIF", Icon: "img"},
		{ID: "pdf", Label: "PDF", Icon: "pdf"},
		{ID: "ico", Label: "ICO", Hint: "16–256 px icon", Icon: "img"},
		{ID: "half", Label: "50%", Hint: "half the size", Icon: "resize"},
		{ID: "small", Label: "Small", Hint: "max 1920 px", Icon: "resize"},
		{ID: "gif", Label: "GIF", Back: true, Icon: "img"},
		{ID: "apng", Label: "APNG", Hint: "animated PNG", Back: true, Icon: "play"},
		{ID: "mp4", Label: "MP4", Hint: "video", Back: true, Icon: "play"},
		{ID: "bmp", Label: "BMP", Back: true, Icon: "img"},
		{ID: "tiff", Label: "TIFF", Back: true, Icon: "img"},
		{ID: "jxl", Label: "JPEG XL", Back: true, Icon: "img"},
		{ID: "svg", Label: "SVG", Hint: "picture inside", Back: true, Icon: "img"},
	}, stillTools...)
	// GIFs (moving, mostly) first offer the other moving formats
	movingOut = append([]target{
		{ID: "mp4", Label: "MP4", Hint: "video", Icon: "play"},
		{ID: "gif", Label: "GIF", Icon: "play"},
		{ID: "webp", Label: "WebP", Hint: "animated", Icon: "play"},
		{ID: "apng", Label: "APNG", Hint: "animated PNG", Icon: "play"},
		{ID: "png", Label: "PNG", Hint: "first frame", Icon: "img"},
		{ID: "jpg", Label: "JPG", Hint: "first frame", Icon: "img"},
		{ID: "half", Label: "50%", Hint: "half the size", Icon: "resize"},
		{ID: "small", Label: "Small", Hint: "max 1920 px", Icon: "resize"},
		{ID: "avif", Label: "AVIF", Hint: "first frame", Back: true, Icon: "img"},
		{ID: "pdf", Label: "PDF", Hint: "first frame", Back: true, Icon: "pdf"},
	}, stillTools...)
	videoOut = append([]target{
		{ID: "mp4", Label: "MP4", Icon: "play"},
		{ID: "mp4-small", Label: "Smaller", Hint: "720p MP4", Icon: "resize"},
		{ID: "gif", Label: "GIF", Hint: "first 30 s", Icon: "play"},
		{ID: "webp", Label: "WebP", Hint: "animated, first 30 s", Icon: "play"},
		{ID: "apng", Label: "APNG", Hint: "animated PNG, first 30 s", Icon: "play"},
		{ID: "mp3", Label: "MP3", Hint: "the sound", Icon: "music"},
		{ID: "jpg", Label: "JPG", Hint: "first frame", Icon: "img"},
		{ID: "png", Label: "PNG", Hint: "first frame", Icon: "img"},
		{ID: "wav", Label: "WAV", Hint: "the sound", Back: true, Icon: "music"},
		{ID: "m4a", Label: "M4A", Hint: "the sound, AAC", Back: true, Icon: "music"},
		{ID: "flac", Label: "FLAC", Hint: "the sound", Back: true, Icon: "music"},
	}, stillTools...)
	togetherOut = []target{
		{ID: "mp4-anim", Label: "Video", Hint: "all in one MP4", Multi: true, Icon: "play"},
		{ID: "gif-anim", Label: "GIF", Hint: "all in one animation", Multi: true, Icon: "play"},
		{ID: "webp-anim", Label: "WebP", Hint: "all in one animation", Multi: true, Back: true, Icon: "play"},
		{ID: "apng-anim", Label: "APNG", Hint: "all in one animation", Multi: true, Back: true, Icon: "play"},
	}
)

// imageTargets: pictures, moving pictures and videos, alone or together.
func imageTargets(paths []string) []target {
	videos, gifs := 0, 0
	for _, p := range paths {
		if isVideo(p) {
			videos++
		} else if convExt(p) == ".gif" {
			gifs++
		}
	}
	var out []target
	if len(paths) > 1 {
		out = append(out, togetherOut...)
		if videos == 0 {
			out = append(out, target{ID: "pdf-one", Label: "One PDF", Hint: "a page per picture", Multi: true, Icon: "pdf"})
		}
	}
	switch {
	case videos == len(paths):
		return append(out, videoOut...)
	case gifs+videos == len(paths):
		return append(out, movingOut...)
	}
	return append(out, pictureOut...)
}

func decodeImage(p string) (image.Image, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var img image.Image
	if isVideo(p) {
		return firstFrame(p)
	}
	switch convExt(p) {
	case ".svg":
		img, err = decodeSVG(r)
	case ".heic", ".heif":
		img, err = heic.Decode(r)
	case ".avif":
		img, err = avif.Decode(r)
	case ".jxl":
		img, err = jpegxl.Decode(r)
	case ".webp":
		img, err = webp.Decode(r, webp.Options{AutoRotate: true})
	case ".ico":
		img, err = decodeICO(r)
	case ".bmp":
		img, err = bmp.Decode(r)
	case ".tif", ".tiff":
		img, err = tiff.Decode(r)
	case ".jpg", ".jpeg", ".jfif":
		b, rerr := io.ReadAll(r)
		if rerr != nil {
			return nil, rerr
		}
		if img, err = jpeg.Decode(bytes.NewReader(b)); err == nil {
			img = orient(img, jpegOrientation(b))
		}
	default:
		img, _, err = image.Decode(r)
	}
	if err != nil {
		return nil, fmt.Errorf("can't read the picture (%v)", err)
	}
	return img, nil
}

func encodeImage(w io.Writer, img image.Image, format string) error {
	switch format {
	case "jpg":
		return jpeg.Encode(w, flatten(img), &jpeg.Options{Quality: 90})
	case "png":
		return (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(w, img)
	case "webp":
		return webp.Encode(w, img, webp.Options{Quality: 85, Method: 4})
	case "avif":
		return avif.Encode(w, img, avif.Options{Quality: 70, Speed: 8})
	case "jxl":
		return jpegxl.Encode(w, img, jpegxl.Options{Quality: 90, Effort: 7})
	case "gif":
		return gif.Encode(w, img, &gif.Options{NumColors: 256, Drawer: draw.FloydSteinberg})
	case "bmp":
		return bmp.Encode(w, img)
	case "tiff":
		return tiff.Encode(w, img, &tiff.Options{Compression: tiff.Deflate, Predictor: true})
	case "ico":
		return encodeICO(w, img)
	case "pdf":
		return imagesPDF(w, []image.Image{img})
	case "svg": // the picture as a PNG inside, for tools that only take SVG
		var b bytes.Buffer
		if err := png.Encode(&b, img); err != nil {
			return err
		}
		r := img.Bounds()
		_, err := fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %[1]d %[2]d"><image width="%[1]d" height="%[2]d" href="data:image/png;base64,%s"/></svg>`, r.Dx(), r.Dy(), base64.StdEncoding.EncodeToString(b.Bytes()))
		return err
	}
	return fmt.Errorf("can't write %s", format)
}

// frameOps: the tools, per frame of a moving picture too.
var frameOps = map[string]func(image.Image) image.Image{
	"half": func(img image.Image) image.Image {
		b := img.Bounds()
		return resize(img, max(1, b.Dx()/2), max(1, b.Dy()/2))
	},
	"small": func(img image.Image) image.Image { return fit(img, 1920) },
	"gray":  grayscale,
	"rotr":  func(img image.Image) image.Image { return transform(img, "rotr") },
	"rotl":  func(img image.Image) image.Image { return transform(img, "rotl") },
	"flip":  func(img image.Image) image.Image { return transform(img, "flip") },
}

var opSuffix = map[string]string{"half": " (50%)", "small": " (small)", "gray": " (gray)", "rotr": " (rotated)", "rotl": " (rotated)", "flip": " (mirrored)"}

func convertImage(ctx context.Context, src string, t target, dir string) ([]string, error) {
	switch t.ID {
	case "mp3", "wav", "flac", "m4a": // a video's sound
		a, err := mfAudio(src)
		if err != nil {
			return nil, err
		}
		p := outPath(dir, stem(src), "."+t.ID)
		return []string{p}, writeSound(p, a, t.ID)
	case "mp4", "mp4-small", "gif", "webp", "apng":
		if t.ID[:3] == "mp4" || animated(src) {
			return makeAnim(ctx, []string{src}, t.ID, stem(src), dir, nil)
		}
	}
	op := frameOps[t.ID]
	if op != nil && animated(src) { // the same moving format, every frame changed
		format := map[string]string{".gif": "gif", ".webp": "webp", ".png": "apng", ".apng": "apng"}[convExt(src)]
		if format == "" {
			format = "mp4"
		}
		return makeAnim(ctx, []string{src}, format, stem(src)+opSuffix[t.ID], dir, op)
	}
	img, err := decodeImage(src)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	format, suffix := t.ID, ""
	same := strings.TrimPrefix(convExt(src), ".")
	switch same {
	case "jpeg", "jfif":
		same = "jpg"
	case "tif":
		same = "tiff"
	case "heic", "heif", "jxl", "ico", "svg", "apng":
		same = "png" // a changed picture comes back as something everything opens
	}
	if isVideo(src) {
		same = "jpg"
	}
	if format == "apng" {
		format = "png" // one frame
	}
	if op != nil {
		img, format, suffix = op(img), same, opSuffix[t.ID]
		if t.ID == "small" {
			format = "jpg"
		}
	}
	p := outPath(dir, stem(src)+suffix, "."+format)
	return []string{p}, writeOut(p, func(f *os.File) error {
		w := bufio.NewWriter(f)
		if err := encodeImage(w, img, format); err != nil {
			return err
		}
		return w.Flush()
	})
}

func convertImages(ctx context.Context, srcs []string, t target, dir string) ([]string, error) {
	if f, ok := strings.CutSuffix(t.ID, "-anim"); ok {
		return makeAnim(ctx, srcs, f, stem(srcs[0]), dir, nil)
	}
	var imgs []image.Image
	for _, s := range srcs {
		img, err := decodeImage(s)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", s, err)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		imgs = append(imgs, img)
	}
	p := outPath(dir, stem(srcs[0]), ".pdf")
	return []string{p}, writeOut(p, func(f *os.File) error { return imagesPDF(f, imgs) })
}

// makeAnim: the files' frames one after another (stills as slides) into an animated GIF, WebP,
// APNG or an MP4, op applied to every frame.
func makeAnim(ctx context.Context, srcs []string, format, name, dir string, op func(image.Image) image.Image) ([]string, error) {
	video := false
	for _, s := range srcs {
		video = video || isVideo(s)
	}
	mp4 := strings.HasPrefix(format, "mp4")
	slide := 500
	if mp4 {
		slide = 2000
		if len(srcs) == 1 {
			slide = 3000
		}
	}
	fps, side, suffix := 30, 1920, ""
	var sound *pcm
	sp := animSpec{fps: 50, maxSide: 800}
	if mp4 {
		if len(srcs) == 1 && video {
			if f := videoFPS(srcs[0]); f > 0 {
				fps = min(60, int(f+0.5))
			}
			sound, _ = mfAudio(srcs[0]) // a silent video is fine
			side = 3840
		}
		if format == "mp4-small" {
			side, suffix = 1280, " (small)"
		}
	} else if video {
		sp = animSpec{fps: 15, maxSide: 480, maxMs: 30000}
		side = 480
	} else {
		side = sp.maxSide
	}
	var fns []frameFn
	for _, s := range srcs {
		f, err := framesOf(s, slide, side)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", filepath.Base(s), err)
		}
		fns = append(fns, f)
	}
	all := concat(fns...)
	src := func(yield func(image.Image, int) error) error {
		return all(func(img image.Image, ms int) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if op != nil {
				img = op(img)
			}
			return yield(img, ms)
		})
	}
	if mp4 {
		p := outPath(dir, name+suffix, ".mp4")
		if err := writeMP4(p, src, fps, side, sound); err != nil {
			os.Remove(p)
			return nil, err
		}
		return []string{p}, nil
	}
	ext := "." + format
	if format == "apng" {
		ext = ".png"
	}
	p := outPath(dir, name, ext)
	return []string{p}, writeOut(p, func(f *os.File) error {
		w := bufio.NewWriter(f)
		if err := writeAnim(w, format, src, sp); err != nil {
			return err
		}
		return w.Flush()
	})
}

func firstFrame(p string) (image.Image, error) {
	f, err := videoFrames(p, 0)
	if err != nil {
		return nil, err
	}
	var img image.Image
	f(func(i image.Image, _ int) error { img = i; return errEnough })
	if img == nil {
		return nil, errors.New("no pictures in it")
	}
	return img, nil
}

// decodeSVG draws an SVG at its own size (small icons at 512 px, so they're usable).
func decodeSVG(r io.Reader) (image.Image, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	// what browsers draw and resvg doesn't: CSS colours like color(display-p3 ...) after a #hex
	// fallback (dropped, the fallback stays), and a CSS background on the <svg> itself
	b = svgModernCSS.ReplaceAll(b, nil)
	var bg color.Color
	if m := svgBackground.FindSubmatch(b); m != nil {
		var c color.RGBA
		h := string(m[1])
		if len(h) == 3 {
			h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
		}
		if _, err := fmt.Sscanf(h[:6], "%02x%02x%02x", &c.R, &c.G, &c.B); err == nil {
			c.A = 255
			bg = c
		}
	}
	img, err := renderSVG(b, 512, 8192)
	if err != nil {
		return nil, err
	}
	if bg == nil {
		return img, nil
	}
	out := image.NewRGBA(img.Bounds())
	draw.Draw(out, out.Rect, image.NewUniform(bg), image.Point{}, draw.Src)
	draw.Draw(out, out.Rect, img, img.Bounds().Min, draw.Over)
	return out, nil
}

var (
	svgModernCSS  = regexp.MustCompile(`;?\s*[a-z-]+\s*:\s*(?:color|oklch|oklab|lab|lch|color-mix)\([^;"']*`)
	svgBackground = regexp.MustCompile(`<svg\b[^>]*style="[^"]*background(?:-color)?\s*:\s*#([0-9a-fA-F]{6}|[0-9a-fA-F]{3})\b`)
)

// ---------------------------------------------------------------- pixels

func resize(img image.Image, w, h int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), xdraw.Src, nil)
	return dst
}

// fit: no side longer than px (smaller pictures stay as they are).
func fit(img image.Image, px int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= px && h <= px {
		return img
	}
	if w >= h {
		return resize(img, px, max(1, h*px/w))
	}
	return resize(img, max(1, w*px/h), px)
}

// rgba: the picture as RGBA from (0,0), copied once so pixels are read straight from Pix (and
// scaled on x/image's fast path).
func rgba(img image.Image) *image.RGBA {
	if n, ok := img.(*image.RGBA); ok && n.Rect.Min == (image.Point{}) {
		return n
	}
	b := img.Bounds()
	n := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(n, n.Rect, img, b.Min, draw.Src)
	return n
}

func grayscale(img image.Image) image.Image {
	src := rgba(img) // premultiplied: the gray of it is the premultiplied gray
	dst := image.NewRGBA(src.Rect)
	for i := 0; i < len(src.Pix); i += 4 {
		p := src.Pix[i : i+4]
		l := uint8((19595*uint32(p[0]) + 38470*uint32(p[1]) + 7471*uint32(p[2]) + 1<<15) >> 16)
		dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = l, l, l, p[3]
	}
	return dst
}

// transform: rotr / rotl (90°), flip (mirror), and the EXIF orientations built from them.
func transform(img image.Image, op string) image.Image {
	// pixels moved as they are: NRGBA stays NRGBA (lossless), the rest as RGBA
	var srcPix []byte
	var stride int
	var w, h int
	n, keep := img.(*image.NRGBA)
	if keep && n.Rect.Min == (image.Point{}) {
		srcPix, stride, w, h = n.Pix, n.Stride, n.Rect.Dx(), n.Rect.Dy()
	} else {
		keep = false
		r := rgba(img)
		srcPix, stride, w, h = r.Pix, r.Stride, r.Rect.Dx(), r.Rect.Dy()
	}
	var to func(x, y int) (int, int) // where pixel (x, y) goes
	size := image.Rect(0, 0, w, h)
	switch op {
	case "rotr":
		size, to = image.Rect(0, 0, h, w), func(x, y int) (int, int) { return h - 1 - y, x }
	case "rotl":
		size, to = image.Rect(0, 0, h, w), func(x, y int) (int, int) { return y, w - 1 - x }
	case "rot180":
		to = func(x, y int) (int, int) { return w - 1 - x, h - 1 - y }
	case "flip":
		to = func(x, y int) (int, int) { return w - 1 - x, y }
	case "flipv":
		to = func(x, y int) (int, int) { return x, h - 1 - y }
	default:
		return img
	}
	dst := image.NewRGBA(size)
	rows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				dx, dy := to(x, y)
				copy(dst.Pix[dy*dst.Stride+dx*4:dy*dst.Stride+dx*4+4], srcPix[y*stride+x*4:])
			}
		}
	})
	if keep {
		return &image.NRGBA{Pix: dst.Pix, Stride: dst.Stride, Rect: dst.Rect}
	}
	return dst
}

func orient(img image.Image, o int) image.Image {
	switch o {
	case 2:
		return transform(img, "flip")
	case 3:
		return transform(img, "rot180")
	case 4:
		return transform(img, "flipv")
	case 5:
		return transform(transform(img, "rotr"), "flip")
	case 6:
		return transform(img, "rotr")
	case 7:
		return transform(transform(img, "rotl"), "flip")
	case 8:
		return transform(img, "rotl")
	}
	return img
}

// jpegOrientation reads EXIF tag 0x0112 from a JPEG's APP1 segment (1 when there is none).
func jpegOrientation(b []byte) int {
	for i := 2; i+4 < len(b) && b[i] == 0xFF; {
		marker, n := b[i+1], int(binary.BigEndian.Uint16(b[i+2:]))
		if marker == 0xDA { // image data: no EXIF before it
			break
		}
		seg := b[i+4 : min(len(b), i+2+n)]
		if marker == 0xE1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			t := seg[6:]
			var bo binary.ByteOrder = binary.BigEndian
			if string(t[:2]) == "II" {
				bo = binary.LittleEndian
			}
			off := int(bo.Uint32(t[4:]))
			if off+2 > len(t) {
				return 1
			}
			cnt := int(bo.Uint16(t[off:]))
			for k := 0; k < cnt; k++ {
				e := off + 2 + k*12
				if e+12 > len(t) {
					break
				}
				if bo.Uint16(t[e:]) == 0x0112 {
					return int(bo.Uint16(t[e+8:]))
				}
			}
			return 1
		}
		i += 2 + n
	}
	return 1
}

// flatten: transparency on white (JPEG has none).
func flatten(img image.Image) image.Image {
	if opaque(img) {
		return img
	}
	dst := image.NewRGBA(img.Bounds())
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), img, img.Bounds().Min, draw.Over)
	return dst
}

func hasAlpha(img image.Image) bool {
	if o, ok := img.(interface{ Opaque() bool }); ok {
		return !o.Opaque()
	}
	return true
}

// ---------------------------------------------------------------- ICO

func encodeICO(w io.Writer, img image.Image) error {
	sizes := []int{16, 24, 32, 48, 64, 128, 256}
	var pngs [][]byte
	for _, s := range sizes {
		sq := image.NewNRGBA(image.Rect(0, 0, s, s))
		f := fit(img, s)
		if b := f.Bounds(); b.Dx() < s || b.Dy() < s {
			f = resize(img, max(1, b.Dx()*s/max(b.Dx(), b.Dy())), max(1, b.Dy()*s/max(b.Dx(), b.Dy())))
		}
		fb := f.Bounds()
		off := image.Pt((s-fb.Dx())/2, (s-fb.Dy())/2)
		draw.Draw(sq, fb.Sub(fb.Min).Add(off), f, fb.Min, draw.Src)
		var b bytes.Buffer
		if err := png.Encode(&b, sq); err != nil {
			return err
		}
		pngs = append(pngs, b.Bytes())
	}
	h := []byte{0, 0, 1, 0, byte(len(sizes)), 0}
	off := 6 + 16*len(sizes)
	for i, s := range sizes {
		d := make([]byte, 16)
		d[0], d[1] = byte(s), byte(s) // 256 is written as 0
		binary.LittleEndian.PutUint16(d[4:], 1)
		binary.LittleEndian.PutUint16(d[6:], 32)
		binary.LittleEndian.PutUint32(d[8:], uint32(len(pngs[i])))
		binary.LittleEndian.PutUint32(d[12:], uint32(off))
		off += len(pngs[i])
		h = append(h, d...)
	}
	if _, err := w.Write(h); err != nil {
		return err
	}
	for _, b := range pngs {
		if _, err := w.Write(b); err != nil {
			return err
		}
	}
	return nil
}

// decodeICO: the biggest picture in the icon (PNG, or a 32-bit bitmap).
func decodeICO(r io.Reader) (image.Image, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(b) < 6 || binary.LittleEndian.Uint16(b[2:]) != 1 {
		return nil, errors.New("not an icon")
	}
	n := int(binary.LittleEndian.Uint16(b[4:]))
	best, bestPx := -1, -1
	for i := 0; i < n && 6+16*i+16 <= len(b); i++ {
		d := b[6+16*i:]
		px := int(d[0])
		if px == 0 {
			px = 256
		}
		if px > bestPx {
			best, bestPx = i, px
		}
	}
	if best < 0 {
		return nil, errors.New("empty icon")
	}
	d := b[6+16*best:]
	size, off := int(binary.LittleEndian.Uint32(d[8:])), int(binary.LittleEndian.Uint32(d[12:]))
	if off+size > len(b) || size < 40 {
		return nil, errors.New("broken icon")
	}
	data := b[off : off+size]
	if bytes.HasPrefix(data, []byte("\x89PNG")) {
		return png.Decode(bytes.NewReader(data))
	}
	w, h := int(int32(binary.LittleEndian.Uint32(data[4:]))), int(int32(binary.LittleEndian.Uint32(data[8:])))/2
	bpp := binary.LittleEndian.Uint16(data[14:])
	hs := int(binary.LittleEndian.Uint32(data))
	if bpp != 32 || w <= 0 || h <= 0 || hs+w*h*4 > len(data) {
		return nil, errors.New("only 32-bit icons are read")
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	px := data[hs:]
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			s := px[((h-1-y)*w+x)*4:]
			img.SetNRGBA(x, y, color.NRGBA{s[2], s[1], s[0], s[3]})
		}
	}
	return img, nil
}

// ---------------------------------------------------------------- PDF

// imagesPDF writes a PDF with one page per picture, each page the picture's size (96 dpi): JPEG
// pages are the JPEG itself (DCTDecode); pictures with transparency are deflated with a soft mask.
func imagesPDF(w io.Writer, imgs []image.Image) error {
	var buf bytes.Buffer
	var offs []int
	obj := func(body string, stream []byte) int {
		offs = append(offs, buf.Len())
		n := len(offs)
		fmt.Fprintf(&buf, "%d 0 obj\n%s\n", n, body)
		if stream != nil {
			buf.WriteString("stream\n")
			buf.Write(stream)
			buf.WriteString("\nendstream\n")
		}
		buf.WriteString("endobj\n")
		return n
	}
	deflate := func(b []byte) []byte {
		var z bytes.Buffer
		zw := zlib.NewWriter(&z)
		zw.Write(b)
		zw.Close()
		return z.Bytes()
	}
	buf.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	// 1: catalog, 2: pages (written last, numbers fixed up front)
	offs = append(offs, 0, 0)
	var kids []string
	for _, img := range imgs {
		b := img.Bounds()
		iw, ih := b.Dx(), b.Dy()
		var xobj int
		if hasAlpha(img) {
			rgb := make([]byte, 0, iw*ih*3)
			alpha := make([]byte, 0, iw*ih)
			for y := b.Min.Y; y < b.Max.Y; y++ {
				for x := b.Min.X; x < b.Max.X; x++ {
					c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
					rgb = append(rgb, c.R, c.G, c.B)
					alpha = append(alpha, c.A)
				}
			}
			sm := deflate(alpha)
			mask := obj(fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>", iw, ih, len(sm)), sm)
			d := deflate(rgb)
			xobj = obj(fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /FlateDecode /SMask %d 0 R /Length %d >>", iw, ih, mask, len(d)), d)
		} else {
			var j bytes.Buffer
			if err := jpeg.Encode(&j, img, &jpeg.Options{Quality: 92}); err != nil {
				return err
			}
			xobj = obj(fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>", iw, ih, j.Len()), j.Bytes())
		}
		pw, ph := float64(iw)*0.75, float64(ih)*0.75
		content := []byte(fmt.Sprintf("q %.2f 0 0 %.2f 0 0 cm /Im0 Do Q", pw, ph))
		c := obj(fmt.Sprintf("<< /Length %d >>", len(content)), content)
		p := obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>", pw, ph, xobj, c), nil)
		kids = append(kids, fmt.Sprintf("%d 0 R", p))
	}
	offs[0] = buf.Len()
	buf.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	offs[1] = buf.Len()
	fmt.Fprintf(&buf, "2 0 obj\n<< /Type /Pages /Kids [%s] /Count %d >>\nendobj\n", strings.Join(kids, " "), len(kids))
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offs)+1)
	for _, o := range offs {
		fmt.Fprintf(&buf, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offs)+1, xref)
	_, err := w.Write(buf.Bytes())
	return err
}
