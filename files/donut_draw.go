package main

// The donut, drawn here (no web view: it shows the instant a drag starts, and its pixels' alpha is
// what the mouse hits, so it's round to the drop too). Colors are MW4W's (colors.css, from the
// wallpaper); text is Segoe UI.

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"unicode/utf8"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// donut geometry in DIPs (scaled by the monitor's DPI)
const (
	dnBox   = 340 // window
	dnOuter = 150
	dnInner = 60
	dnGap   = 3 // between slices, px at 96 dpi
)

type donutLook struct {
	primary, onPrimary, surface, surface2, text, dim, err color.NRGBA
}

var lookOnce = sync.OnceValue(func() donutLook { return loadLook(packDir()) })

// loadLook reads MW4W's palette (colors.css in the pack folder), else a neutral dark one.
func loadLook(packDir string) donutLook {
	l := donutLook{
		primary: color.NRGBA{132, 179, 245, 255}, onPrimary: color.NRGBA{17, 33, 54, 255},
		surface: color.NRGBA{15, 20, 26, 255}, surface2: color.NRGBA{40, 47, 57, 255},
		text: color.NRGBA{233, 236, 242, 255}, dim: color.NRGBA{169, 177, 188, 255}, err: color.NRGBA{242, 184, 181, 255},
	}
	b, err := os.ReadFile(filepath.Join(packDir, "colors.css"))
	if err != nil {
		return l
	}
	re := regexp.MustCompile(`--([A-Za-z0-9]+)-rgb:\s*(\d+),\s*(\d+),\s*(\d+)`)
	for _, m := range re.FindAllStringSubmatch(string(b), -1) {
		r, _ := strconv.Atoi(m[2])
		g, _ := strconv.Atoi(m[3])
		bl, _ := strconv.Atoi(m[4])
		c := color.NRGBA{uint8(r), uint8(g), uint8(bl), 255}
		switch m[1] {
		case "primary":
			l.primary = c
		case "onPrimary":
			l.onPrimary = c
		case "surface":
			l.surface = c
		case "surface3":
			l.surface2 = c
		case "text":
			l.text = c
		case "textDim":
			l.dim = c
		case "error":
			l.err = c
		}
	}
	return l
}

// donutView: what to draw.
type donutView struct {
	slices  []target
	hover   int    // slice under the cursor, -1 none
	sides   int    // 1 or 2
	side    int    // which one is showing
	title   string // center: "trip.jpg", "3 files"
	sub     string // center, small: the hovered slice's hint
	alpha   float64
	working bool
	prog    float64 // 0..1 while working
	spin    float64 // radians
	done    int     // 1 ok, -1 failed
	invis   bool    // armed: only hit-testable (alpha 1), nothing to see yet
}

type fonts struct {
	label, hint, title font.Face
}

var faceMu sync.Mutex
var faceCache = map[float64]*fonts{}

func fontsFor(scale float64) *fonts {
	faceMu.Lock()
	defer faceMu.Unlock()
	if f := faceCache[scale]; f != nil {
		return f
	}
	parse := func(win string, fallback []byte) *opentype.Font {
		if b, err := os.ReadFile(filepath.Join(os.Getenv("SystemRoot"), "Fonts", win)); err == nil {
			if f, err := opentype.Parse(b); err == nil {
				return f
			}
		}
		f, _ := opentype.Parse(fallback)
		return f
	}
	semi := parse("seguisb.ttf", gobold.TTF)
	reg := parse("segoeui.ttf", goregular.TTF)
	face := func(f *opentype.Font, px float64) font.Face {
		ff, _ := opentype.NewFace(f, &opentype.FaceOptions{Size: px * scale, DPI: 72, Hinting: font.HintingFull})
		return ff
	}
	f := &fonts{label: face(semi, 14), hint: face(reg, 11.5), title: face(semi, 13)}
	faceCache[scale] = f
	return f
}

// sliceAt: which slice a point (relative to the center, px) is on, -1 for the hole or outside.
func sliceAt(dx, dy float64, n int, scale float64) int {
	r := math.Hypot(dx, dy)
	if n == 0 || r < dnInner*scale || r > (dnOuter+8)*scale {
		return -1
	}
	a := math.Atan2(dx, -dy) // 0 at the top, clockwise
	if a < 0 {
		a += 2 * math.Pi
	}
	step := 2 * math.Pi / float64(n)
	return int(math.Mod(a+step/2, 2*math.Pi) / step)
}

func drawDonut(v donutView, scale float64) *image.RGBA {
	size := int(math.Ceil(dnBox * scale))
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	c := float64(size) / 2
	R, r := dnOuter*scale, dnInner*scale
	if v.invis {
		// alpha 1: the window takes the drag, nobody sees it
		disc(img, c, c, R, color.NRGBA{0, 0, 0, 1})
		return img
	}
	l := lookOnce()
	fade := func(col color.NRGBA, a float64) color.NRGBA {
		col.A = uint8(float64(col.A) * a * v.alpha)
		return col
	}
	// soft shadow, then the ring's body
	for i := 6; i >= 1; i-- {
		disc(img, c, c+3*scale, R+float64(i)*2.2*scale, color.NRGBA{0, 0, 0, uint8(10 * v.alpha)})
	}
	n := len(v.slices)
	disc(img, c, c, R, fade(l.surface, 0.94))
	step := 2 * math.Pi / math.Max(1, float64(n))
	gap := dnGap * scale
	f := fontsFor(scale)
	for i, t := range v.slices {
		a0, a1 := float64(i)*step-step/2, float64(i)*step+step/2
		bg := fade(l.surface2, 0.9)
		fg := l.text
		if i == v.hover && !v.working && v.done == 0 {
			bg, fg = fade(l.primary, 1), l.onPrimary
		}
		if n > 1 {
			sector(img, c, c, R-2*scale, r+2*scale, a0, a1, gap, bg)
		} else {
			ring(img, c, c, R-2*scale, r+2*scale, bg)
		}
		// label at the middle of the slice
		mid := (a0 + a1) / 2
		rm := (R + r) / 2
		x, y := c+rm*math.Sin(mid), c-rm*math.Cos(mid)
		lc := fade(fg, 1)
		if v.working || v.done != 0 {
			lc = fade(fg, 0.35)
		}
		label(img, f.label, t.Label, x, y, lc, (R-r)*2*math.Sin(math.Min(step/2, math.Pi/2))*0.9)
	}
	// center: a disc of its own (dropping there cancels)
	disc(img, c, c, r-3*scale, fade(l.surface, 0.97))
	if v.working || v.done != 0 {
		rp := r - 14*scale
		arcStroke(img, c, c, rp, 0, 2*math.Pi, 4*scale, fade(l.surface2, 1))
		switch {
		case v.done > 0:
			arcStroke(img, c, c, rp, 0, 2*math.Pi, 4*scale, fade(l.primary, 1))
			check(img, c, c, 13*scale, 3.6*scale, fade(l.primary, 1))
		case v.done < 0:
			arcStroke(img, c, c, rp, 0, 2*math.Pi, 4*scale, fade(l.err, 1))
			cross(img, c, c, 11*scale, 3.6*scale, fade(l.err, 1))
		case v.prog > 0:
			arcStroke(img, c, c, rp, -math.Pi/2, -math.Pi/2+2*math.Pi*v.prog, 4*scale, fade(l.primary, 1))
		default:
			arcStroke(img, c, c, rp, v.spin, v.spin+math.Pi*0.6, 4*scale, fade(l.primary, 1))
		}
		return img
	}
	title, sub := v.title, v.sub
	label(img, f.title, title, c, c-(func() float64 {
		if sub != "" || v.sides > 1 {
			return 8 * scale
		}
		return 0
	})(), fade(l.text, 1), 2*r-18*scale)
	if sub != "" {
		label(img, f.hint, sub, c, c+11*scale, fade(l.dim, 1), 2*r-14*scale)
	} else if v.sides > 1 {
		label(img, f.hint, fmt.Sprintf("Tab · %d/%d", v.side+1, v.sides), c, c+11*scale, fade(l.dim, 1), 2*r-14*scale)
	}
	return img
}

// ---------------------------------------------------------------- shapes

func fill(img *image.RGBA, z *vector.Rasterizer, col color.NRGBA) {
	z.DrawOp = draw.Over
	z.Draw(img, img.Bounds(), image.NewUniform(col), image.Point{})
}

func circlePath(z *vector.Rasterizer, cx, cy, r float64, rev bool) {
	const n = 96
	for i := 0; i <= n; i++ {
		a := 2 * math.Pi * float64(i) / n
		if rev {
			a = -a
		}
		x, y := float32(cx+r*math.Sin(a)), float32(cy-r*math.Cos(a))
		if i == 0 {
			z.MoveTo(x, y)
		} else {
			z.LineTo(x, y)
		}
	}
	z.ClosePath()
}

func disc(img *image.RGBA, cx, cy, r float64, col color.NRGBA) {
	b := img.Bounds()
	z := vector.NewRasterizer(b.Dx(), b.Dy())
	circlePath(z, cx, cy, r, false)
	fill(img, z, col)
}

// ring: the annulus between r and R (nonzero winding: the inner circle runs the other way).
func ring(img *image.RGBA, cx, cy, R, r float64, col color.NRGBA) {
	b := img.Bounds()
	z := vector.NewRasterizer(b.Dx(), b.Dy())
	circlePath(z, cx, cy, R, false)
	circlePath(z, cx, cy, r, true)
	fill(img, z, col)
}

// sector: a slice of the annulus from a0 to a1 (0 at the top, clockwise), its sides inset by gap/2.
func sector(img *image.RGBA, cx, cy, R, r, a0, a1, gap float64, col color.NRGBA) {
	b := img.Bounds()
	z := vector.NewRasterizer(b.Dx(), b.Dy())
	// the gap is constant in px: a smaller angle at the outer edge than at the inner one
	o0, o1 := a0+gap/2/R, a1-gap/2/R
	i0, i1 := a0+gap/2/r, a1-gap/2/r
	pt := func(rad, a float64) (float32, float32) { return float32(cx + rad*math.Sin(a)), float32(cy - rad*math.Cos(a)) }
	const n = 48
	x, y := pt(R, o0)
	z.MoveTo(x, y)
	for i := 1; i <= n; i++ {
		x, y = pt(R, o0+(o1-o0)*float64(i)/n)
		z.LineTo(x, y)
	}
	for i := 0; i <= n; i++ {
		x, y = pt(r, i1-(i1-i0)*float64(i)/n)
		z.LineTo(x, y)
	}
	z.ClosePath()
	fill(img, z, col)
}

// arcStroke: a round-capped arc of width w (angles: 0 at the top, clockwise).
func arcStroke(img *image.RGBA, cx, cy, r, a0, a1, w float64, col color.NRGBA) {
	b := img.Bounds()
	z := vector.NewRasterizer(b.Dx(), b.Dy())
	const n = 96
	steps := max(2, int(float64(n)*math.Abs(a1-a0)/(2*math.Pi)))
	pt := func(rad, a float64) (float32, float32) { return float32(cx + rad*math.Sin(a)), float32(cy - rad*math.Cos(a)) }
	x, y := pt(r+w/2, a0)
	z.MoveTo(x, y)
	for i := 1; i <= steps; i++ {
		x, y = pt(r+w/2, a0+(a1-a0)*float64(i)/float64(steps))
		z.LineTo(x, y)
	}
	for i := 0; i <= steps; i++ {
		x, y = pt(r-w/2, a1-(a1-a0)*float64(i)/float64(steps))
		z.LineTo(x, y)
	}
	z.ClosePath()
	fill(img, z, col)
	if math.Abs(a1-a0) < 2*math.Pi-0.01 {
		for _, a := range []float64{a0, a1} {
			x, y := pt(r, a)
			disc(img, float64(x), float64(y), w/2, col)
		}
	}
}

func line(img *image.RGBA, x0, y0, x1, y1, w float64, col color.NRGBA) {
	b := img.Bounds()
	z := vector.NewRasterizer(b.Dx(), b.Dy())
	dx, dy := x1-x0, y1-y0
	l := math.Hypot(dx, dy)
	nx, ny := -dy/l*w/2, dx/l*w/2
	z.MoveTo(float32(x0+nx), float32(y0+ny))
	z.LineTo(float32(x1+nx), float32(y1+ny))
	z.LineTo(float32(x1-nx), float32(y1-ny))
	z.LineTo(float32(x0-nx), float32(y0-ny))
	z.ClosePath()
	fill(img, z, col)
	disc(img, x0, y0, w/2, col)
	disc(img, x1, y1, w/2, col)
}

func check(img *image.RGBA, cx, cy, s, w float64, col color.NRGBA) {
	line(img, cx-s*0.75, cy+s*0.05, cx-s*0.2, cy+s*0.6, w, col)
	line(img, cx-s*0.2, cy+s*0.6, cx+s*0.8, cy-s*0.55, w, col)
}

func cross(img *image.RGBA, cx, cy, s, w float64, col color.NRGBA) {
	line(img, cx-s, cy-s, cx+s, cy+s, w, col)
	line(img, cx+s, cy-s, cx-s, cy+s, w, col)
}

// label: text centered on (x, y), shortened with … to fit maxW.
func label(img *image.RGBA, f font.Face, s string, x, y float64, col color.NRGBA, maxW float64) {
	if f == nil || s == "" {
		return
	}
	d := &font.Drawer{Dst: img, Src: image.NewUniform(col), Face: f}
	w := float64(d.MeasureString(s)) / 64
	for w > maxW && utf8.RuneCountInString(s) > 2 {
		rs := []rune(s)
		s = string(rs[:len(rs)-2]) + "…"
		w = float64(d.MeasureString(s)) / 64
	}
	m := f.Metrics()
	h := float64(m.Ascent+m.Descent) / 64
	d.Dot = fixed.Point26_6{X: fixed.Int26_6((x - w/2) * 64), Y: fixed.Int26_6((y - h/2 + float64(m.Ascent)/64) * 64)}
	d.DrawString(s)
}
