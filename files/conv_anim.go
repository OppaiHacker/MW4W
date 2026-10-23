package main

// Moving pictures: GIF, APNG, animated WebP and video are all a run of frames with how long each
// shows. Any of them (and still pictures, as slides) can become any other, one file or many in a
// row (photos -> GIF, MP4 -> APNG, GIF + clip -> MP4 ...).

import (
	"bufio"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"io"
	"os"
	"runtime"
	"sort"
	"sync"

	"github.com/gen2brain/webp"
	"github.com/kettek/apng"
)

// frameFn calls yield with each frame and how long it shows (ms).
type frameFn func(yield func(img image.Image, ms int) error) error

var convVideo = exts("mp4 m4v mov avi wmv asf mkv webm 3gp 3g2 mpg mpeg ts mts m2ts")

func isVideo(p string) bool { return convVideo[convExt(p)] }

// framesOf: a file's frames; a still picture is one frame of slideMs. Videos are read about
// maxSide big already (0: full size).
func framesOf(p string, slideMs, maxSide int) (frameFn, error) {
	if isVideo(p) {
		return videoFrames(p, maxSide)
	}
	switch convExt(p) {
	case ".gif":
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		g, err := gif.DecodeAll(bufio.NewReader(f))
		f.Close()
		if err != nil {
			return nil, err
		}
		if len(g.Image) > 1 {
			return gifFrames(g), nil
		}
	case ".png", ".apng":
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		a, err := apng.DecodeAll(bufio.NewReader(f))
		f.Close()
		if err == nil && len(a.Frames) > 1 {
			return apngFrames(a), nil
		}
	case ".webp":
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		w, err := webp.DecodeAll(bufio.NewReader(f))
		f.Close()
		if err == nil && len(w.Image) > 1 {
			return func(yield func(image.Image, int) error) error {
				for i, img := range w.Image {
					if err := yield(img, max(20, w.Delay[i])); err != nil {
						return err
					}
				}
				return nil
			}, nil
		}
	}
	img, err := decodeImage(p)
	if err != nil {
		return nil, err
	}
	return func(yield func(image.Image, int) error) error { return yield(img, slideMs) }, nil
}

// animated: more than one frame (videos always).
func animated(p string) bool {
	if isVideo(p) {
		return true
	}
	n := 0
	if f, err := framesOf(p, 1, 64); err == nil {
		f(func(image.Image, int) error {
			if n++; n > 1 {
				return io.EOF
			}
			return nil
		})
	}
	return n > 1
}

// gifFrames plays a GIF onto a canvas (disposal included), a full picture per frame.
func gifFrames(g *gif.GIF) frameFn {
	return func(yield func(image.Image, int) error) error {
		b := image.Rect(0, 0, g.Config.Width, g.Config.Height)
		if b.Empty() {
			b = g.Image[0].Bounds()
		}
		canvas := image.NewRGBA(b)
		for i, f := range g.Image {
			var prev *image.RGBA
			disposal := byte(0)
			if i < len(g.Disposal) {
				disposal = g.Disposal[i]
			}
			if disposal == gif.DisposalPrevious {
				prev = image.NewRGBA(b)
				copy(prev.Pix, canvas.Pix)
			}
			draw.Draw(canvas, f.Bounds(), f, f.Bounds().Min, draw.Over)
			out := image.NewRGBA(b)
			copy(out.Pix, canvas.Pix)
			ms := 100
			if i < len(g.Delay) && g.Delay[i] > 1 {
				ms = g.Delay[i] * 10
			}
			if err := yield(out, ms); err != nil {
				return err
			}
			switch disposal {
			case gif.DisposalBackground:
				draw.Draw(canvas, f.Bounds(), image.Transparent, image.Point{}, draw.Src)
			case gif.DisposalPrevious:
				canvas = prev
			}
		}
		return nil
	}
}

// apngFrames plays an APNG the same way (blend and dispose ops).
func apngFrames(a apng.APNG) frameFn {
	return func(yield func(image.Image, int) error) error {
		var frames []apng.Frame
		for _, f := range a.Frames {
			if !f.IsDefault || len(a.Frames) == 1 { // a default image outside the animation isn't shown
				frames = append(frames, f)
			}
		}
		if len(frames) == 0 {
			frames = a.Frames
		}
		b := a.Frames[0].Image.Bounds()
		canvas := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		for _, f := range frames {
			r := f.Image.Bounds().Sub(f.Image.Bounds().Min).Add(image.Pt(f.XOffset, f.YOffset))
			var prev *image.RGBA
			if f.DisposeOp == apng.DISPOSE_OP_PREVIOUS {
				prev = image.NewRGBA(canvas.Bounds())
				copy(prev.Pix, canvas.Pix)
			}
			op := draw.Over
			if f.BlendOp == apng.BLEND_OP_SOURCE {
				op = draw.Src
			}
			draw.Draw(canvas, r, f.Image, f.Image.Bounds().Min, op)
			out := image.NewRGBA(canvas.Bounds())
			copy(out.Pix, canvas.Pix)
			if err := yield(out, max(20, int(f.GetDelay()*1000+0.5))); err != nil {
				return err
			}
			switch f.DisposeOp {
			case apng.DISPOSE_OP_BACKGROUND:
				draw.Draw(canvas, r, image.Transparent, image.Point{}, draw.Src)
			case apng.DISPOSE_OP_PREVIOUS:
				canvas = prev
			}
		}
		return nil
	}
}

// concat plays sources one after another.
func concat(srcs ...frameFn) frameFn {
	return func(yield func(image.Image, int) error) error {
		for _, s := range srcs {
			if err := s(yield); err != nil {
				return err
			}
		}
		return nil
	}
}

var errEnough = errors.New("enough")

// retime puts frames on a grid of fps: frames shorter than a tick are dropped, longer ones last
// whole ticks (dup: repeated once per tick, for video; otherwise one frame, longer).
func retime(src frameFn, fps int, dup bool, yield func(image.Image, int) error) error {
	tick := 1000 / float64(fps)
	t, k := 0.0, 0
	return src(func(img image.Image, ms int) error {
		t += float64(ms)
		n := 0
		for float64(k)*tick < t {
			k++
			n++
		}
		if n == 0 {
			return nil
		}
		if !dup {
			return yield(img, int(float64(n)*tick+0.5))
		}
		for ; n > 0; n-- {
			if err := yield(img, int(tick)); err != nil {
				return err
			}
		}
		return nil
	})
}

func fitSize(s image.Point, px int) image.Point {
	if s.X <= px && s.Y <= px {
		return s
	}
	if s.X >= s.Y {
		return image.Pt(px, max(1, s.Y*px/s.X))
	}
	return image.Pt(max(1, s.X*px/s.Y), px)
}

// letterbox draws img into dst as large as it fits, centered on black.
func letterbox(dst *image.RGBA, img image.Image) {
	d, s := dst.Bounds(), img.Bounds()
	if s.Size() == d.Size() {
		draw.Draw(dst, d, img, s.Min, draw.Src)
		return
	}
	draw.Draw(dst, d, image.NewUniform(color.Black), image.Point{}, draw.Src)
	w, h := d.Dx(), s.Dy()*d.Dx()/max(1, s.Dx())
	if h > d.Dy() {
		w, h = s.Dx()*d.Dy()/max(1, s.Dy()), d.Dy()
	}
	if d.Dx()-w <= 2 && d.Dy()-h <= 2 { // rounding (even sides): fill, no hairline bars
		w, h = d.Dx(), d.Dy()
	}
	r := image.Rect(0, 0, w, h).Add(image.Pt((d.Dx()-w)/2, (d.Dy()-h)/2))
	scaleInto(dst, r, rgba(img))
}

// rows runs fn over [0, h) in bands, one per CPU.
func rows(h int, fn func(y0, y1 int)) {
	n := max(1, min(h, runtime.NumCPU()))
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(y0, y1 int) {
			defer wg.Done()
			fn(y0, y1)
		}(h*i/n, h*(i+1)/n)
	}
	wg.Wait()
}

// half: 2x2 boxes averaged.
func half(src *image.RGBA) *image.RGBA {
	w, h := src.Rect.Dx()/2, src.Rect.Dy()/2
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	rows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			a, b, d := src.Pix[2*y*src.Stride:], src.Pix[(2*y+1)*src.Stride:], dst.Pix[y*dst.Stride:]
			for x := 0; x < w*4; x++ {
				i := x/4*8 + x%4
				d[x] = uint8((int(a[i]) + int(a[i+4]) + int(b[i]) + int(b[i+4]) + 2) / 4)
			}
		}
	})
	return dst
}

// scaleInto draws src into r of dst, bilinear (halved first while more than twice too big, so
// nothing is skipped), in parallel.
func scaleInto(dst *image.RGBA, r image.Rectangle, src *image.RGBA) {
	w, h := r.Dx(), r.Dy()
	for src.Rect.Dx() >= 2*w && src.Rect.Dy() >= 2*h && src.Rect.Dx() > 1 && src.Rect.Dy() > 1 {
		src = half(src)
	}
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	if w <= 0 || h <= 0 || sw == 0 || sh == 0 {
		return
	}
	// 16.16 fixed point: pixel centres mapped
	at := func(i, n, sn int) (int, int, int) {
		f := max(0, (2*i+1)*sn*32768/n-32768)
		i0 := min(f>>16, sn-1)
		return i0, min(i0+1, sn-1), f & 0xffff
	}
	xs := make([][3]int, w)
	for x := range xs {
		a, b, f := at(x, w, sw)
		xs[x] = [3]int{a * 4, b * 4, f}
	}
	rows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			a, b, fy := at(y, h, sh)
			ra, rb := src.Pix[a*src.Stride:], src.Pix[b*src.Stride:]
			d := dst.Pix[(r.Min.Y+y)*dst.Stride+r.Min.X*4:]
			gy := 65536 - fy
			for x, X := range xs {
				i, j, f := X[0], X[1], X[2]
				g := 65536 - f
				a0, a1, b0, b1 := ra[i:i+4:i+4], ra[j:j+4:j+4], rb[i:i+4:i+4], rb[j:j+4:j+4]
				o := d[x*4 : x*4+4 : x*4+4]
				for c := range 4 {
					t := (int(a0[c])*g + int(a1[c])*f) >> 16
					u := (int(b0[c])*g + int(b1[c])*f) >> 16
					o[c] = uint8((t*gy + u*fy + 32768) >> 16)
				}
			}
		}
	})
}

// animSpec: how a run of frames becomes an animated picture.
type animSpec struct {
	fps, maxSide, maxMs int
}

// writeAnim: frames into an animated GIF, APNG or WebP (all frames are kept in memory: long
// videos are cut at maxMs).
// ponytail: in-memory encoders, 30 s cap for video sources; stream frames to disk if longer is wanted.
func writeAnim(w io.Writer, format string, src frameFn, sp animSpec) error {
	var imgs []*image.RGBA
	var delays []int
	total := 0
	err := retime(src, sp.fps, false, func(img image.Image, ms int) error {
		if sp.maxMs > 0 && total >= sp.maxMs {
			return errEnough
		}
		if len(imgs) == 0 {
			imgs = append(imgs, image.NewRGBA(image.Rectangle{Max: fitSize(img.Bounds().Size(), sp.maxSide)}))
		} else {
			imgs = append(imgs, image.NewRGBA(imgs[0].Bounds()))
		}
		letterbox(imgs[len(imgs)-1], img)
		delays = append(delays, ms)
		total += ms
		return nil
	})
	if err != nil && err != errEnough {
		return err
	}
	if len(imgs) == 0 {
		return errors.New("no pictures in it")
	}
	switch format {
	case "gif":
		g := &gif.GIF{}
		g.Image = make([]*image.Paletted, len(imgs))
		rows(len(imgs), func(a, b int) {
			for i := a; i < b; i++ {
				g.Image[i] = dither(imgs[i])
			}
		})
		for i := range imgs {
			g.Delay = append(g.Delay, max(2, (delays[i]+5)/10))
		}
		return gif.EncodeAll(w, g)
	case "apng":
		a := apng.APNG{}
		for i, img := range imgs {
			a.Frames = append(a.Frames, apng.Frame{Image: img, DelayNumerator: uint16(min(65535, delays[i])), DelayDenominator: 1000})
		}
		return apng.Encode(w, a)
	case "webp":
		a := &webp.WEBP{}
		for i, img := range imgs {
			a.Image = append(a.Image, img)
			a.Delay = append(a.Delay, delays[i])
		}
		return webp.EncodeAll(w, a, webp.Options{Quality: 80, Method: 4})
	}
	return errors.New("can't animate " + format)
}

// dither: a frame in its own 256 colours (the most used of a 5-6-5 histogram), Floyd–Steinberg,
// colours looked up once per 5-6-5 cell.
func dither(img *image.RGBA) *image.Paletted {
	var hist [1 << 16]uint32
	px := img.Pix
	key := func(r, g, b int) int { return r>>3<<11 | g>>2<<5 | b>>3 }
	for i := 0; i < len(px); i += 4 {
		hist[key(int(px[i]), int(px[i+1]), int(px[i+2]))]++
	}
	var bins []int
	for k, n := range hist {
		if n > 0 {
			bins = append(bins, k)
		}
	}
	sort.Slice(bins, func(i, j int) bool { return hist[bins[i]] > hist[bins[j]] })
	pal := color.Palette{}
	for _, k := range bins[:min(256, len(bins))] {
		pal = append(pal, color.RGBA{uint8(k>>11)<<3 | 4, uint8(k>>5&63)<<2 | 2, uint8(k&31)<<3 | 4, 255})
	}
	var lut [1 << 16]int16
	for i := range lut {
		lut[i] = -1
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewPaletted(image.Rect(0, 0, w, h), pal)
	// errors of this row and the next, x 16, 3 channels, one pixel of margin each side
	cur, next := make([]int, (w+2)*3), make([]int, (w+2)*3)
	clamp := func(v int) int { return min(255, max(0, v)) }
	for y := 0; y < h; y++ {
		row := px[y*img.Stride:]
		for x := 0; x < w; x++ {
			e := cur[(x+1)*3:]
			r, g, bl := clamp(int(row[x*4])+e[0]/16), clamp(int(row[x*4+1])+e[1]/16), clamp(int(row[x*4+2])+e[2]/16)
			k := key(r, g, bl)
			if lut[k] < 0 {
				lut[k] = int16(pal.Index(color.RGBA{uint8(r), uint8(g), uint8(bl), 255}))
			}
			i := lut[k]
			out.Pix[y*out.Stride+x] = uint8(i)
			c := pal[i].(color.RGBA)
			for ch, d := range [3]int{r - int(c.R), g - int(c.G), bl - int(c.B)} {
				cur[(x+2)*3+ch] += d * 7
				next[x*3+ch] += d * 3
				next[(x+1)*3+ch] += d * 5
				next[(x+2)*3+ch] += d
			}
		}
		cur, next = next, cur
		clear(next)
	}
	return out
}
