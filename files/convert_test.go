package main

import (
	"archive/zip"
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

func conv(t *testing.T, src, to string) []string {
	t.Helper()
	tg, ok := findTarget([]string{src}, to)
	if !ok {
		t.Fatalf("%s: no target %q in %v", filepath.Base(src), to, ids(targetsFor([]string{src})))
	}
	out, err := convertOne(context.Background(), src, tg)
	if err != nil {
		t.Fatalf("%s -> %s: %v", filepath.Base(src), to, err)
	}
	for _, o := range out {
		if fi, err := os.Stat(o); err != nil || (!fi.IsDir() && fi.Size() == 0) {
			t.Fatalf("%s -> %s: no output %s", filepath.Base(src), to, o)
		}
	}
	return out
}

func ids(ts []target) []string {
	var s []string
	for _, t := range ts {
		s = append(s, t.ID)
	}
	return s
}

func testPicture(t *testing.T, dir string, w, h int) string {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x * 255 / w), uint8(y * 255 / h), 120, uint8(128 + x%128)})
		}
	}
	p := filepath.Join(dir, "pic.png")
	f, _ := os.Create(p)
	png.Encode(f, img)
	f.Close()
	return p
}

func TestConvertImages(t *testing.T) {
	dir := t.TempDir()
	src := testPicture(t, dir, 64, 40)
	for _, to := range []string{"jpg", "webp", "avif", "jxl", "gif", "bmp", "tiff", "ico", "pdf", "half", "small", "gray", "rotr", "rotl", "flip"} {
		out := conv(t, src, to)
		if to == "pdf" {
			if n, err := api.PageCountFile(context.Background(), out[0]); err != nil || n != 1 {
				t.Fatalf("pdf: %d pages, %v", n, err)
			}
			continue
		}
		img, err := decodeImage(out[0])
		if err != nil {
			t.Fatalf("%s: can't read the result back: %v", to, err)
		}
		b := img.Bounds()
		want := map[string][2]int{"half": {32, 20}, "rotr": {40, 64}, "rotl": {40, 64}, "ico": {256, 256}}[to]
		if want == [2]int{} {
			want = [2]int{64, 40}
		}
		if b.Dx() != want[0] || b.Dy() != want[1] {
			t.Fatalf("%s: %dx%d, want %v", to, b.Dx(), b.Dy(), want)
		}
	}
	// formats read back in (and converted on)
	for _, p := range []string{"pic.webp", "pic.avif", "pic.jxl", "pic.ico", "pic.tiff"} {
		conv(t, filepath.Join(dir, p), "png")
	}
	// rotation: the right pixel moves to the right place
	r, _ := decodeImage(filepath.Join(dir, "pic (rotated).png"))
	o, _ := decodeImage(src)
	if c1, c2 := color.NRGBAModel.Convert(r.At(39, 0)), color.NRGBAModel.Convert(o.At(0, 0)); c1 != c2 {
		t.Fatalf("rotated right: corner %v, want %v", c1, c2)
	}
}

func TestJPEGOrientation(t *testing.T) {
	// EXIF APP1 with orientation 6, little-endian
	exif := []byte("Exif\x00\x00II*\x00\x08\x00\x00\x00\x01\x00\x12\x01\x03\x00\x01\x00\x00\x00\x06\x00\x00\x00\x00\x00\x00\x00")
	b := []byte{0xFF, 0xD8, 0xFF, 0xE1}
	b = binary.BigEndian.AppendUint16(b, uint16(len(exif)+2))
	b = append(b, exif...)
	b = append(b, 0xFF, 0xDA, 0, 2)
	if o := jpegOrientation(b); o != 6 {
		t.Fatalf("orientation %d, want 6", o)
	}
}

func TestImagesToOnePDFAndPDFTools(t *testing.T) {
	dir := t.TempDir()
	a := testPicture(t, dir, 30, 20)
	b := filepath.Join(dir, "b.jpg")
	conv(t, a, "jpg")
	os.Rename(filepath.Join(dir, "pic.jpg"), b)
	pdf, err := convertAll(context.Background(), []string{a, b}, target{ID: "pdf-one", Multi: true})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := api.PageCountFile(context.Background(), pdf[0]); err != nil || n != 2 {
		t.Fatalf("one pdf: %d pages, %v", n, err)
	}
	pages := conv(t, pdf[0], "split")
	ents, _ := os.ReadDir(pages[0])
	if len(ents) != 2 {
		t.Fatalf("split: %d files", len(ents))
	}
	var parts []string
	for _, e := range ents {
		parts = append(parts, filepath.Join(pages[0], e.Name()))
	}
	merged, err := convertAll(context.Background(), parts, target{ID: "merge", Multi: true})
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := api.PageCountFile(context.Background(), merged[0]); n != 2 {
		t.Fatalf("merged: %d pages", n)
	}
	conv(t, merged[0], "compress")
	conv(t, merged[0], "rotr")
	anim, err := convertAll(context.Background(), []string{a, b}, target{ID: "gif-anim", Multi: true})
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(anim[0]); fi == nil || fi.Size() == 0 {
		t.Fatal("no animation")
	}
}

func testWAV(t *testing.T, p string) *pcm {
	a := &pcm{rate: 44100, channels: 2}
	for i := 0; i < 44100/2; i++ {
		v := int16(math.Sin(2*math.Pi*440*float64(i)/44100) * 12000)
		a.data = append(a.data, v, -v)
	}
	if err := writeAudio(p, a, "wav"); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestConvertAudio(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "tone.wav")
	want := testWAV(t, src)
	// FLAC is lossless: the samples come back as they were
	fl := conv(t, src, "flac")
	got, err := decodeAudio(fl[0])
	if err != nil {
		t.Fatal(err)
	}
	if got.rate != 44100 || got.channels != 2 || len(got.data) != len(want.data) {
		t.Fatalf("flac: %d Hz %d ch %d samples", got.rate, got.channels, len(got.data))
	}
	for i := range want.data {
		if got.data[i] != want.data[i] {
			t.Fatalf("flac sample %d: %d, want %d", i, got.data[i], want.data[i])
		}
	}
	mp := conv(t, src, "mp3")
	m, err := decodeAudio(mp[0])
	if err != nil {
		t.Fatal(err)
	}
	if secs := float64(len(m.data)/m.channels) / float64(m.rate); secs < 0.4 || secs > 0.7 {
		t.Fatalf("mp3: %.2f s, want about 0.5", secs)
	}
	conv(t, fl[0], "wav")
	conv(t, mp[0], "flac")
	conv(t, src, "mono")
	j, err := convertAll(context.Background(), []string{src, fl[0]}, target{ID: "join", Multi: true})
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := decodeAudio(j[0]); a == nil || float64(len(a.data)/a.channels)/float64(a.rate) < 0.9 {
		t.Fatal("joined audio is too short")
	}
}

const testMD = "# Zażółć gęślą jaźń\n\nSome **bold** and *italic* text with `code` and a [link](https://example.com).\n\n" +
	"## List\n\n- one\n- two\n  - nested\n\n1. first\n2. second\n\n> quoted\n\n```\ncode block\n```\n\n| a | b |\n|---|---|\n| 1 | 2 |\n"

func TestConvertDocs(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "notes.md")
	os.WriteFile(src, []byte(testMD), 0o644)
	pdf := conv(t, src, "pdf")
	if n, err := api.PageCountFile(context.Background(), pdf[0]); err != nil || n < 1 {
		t.Fatalf("pdf: %d pages, %v", n, err)
	}
	docx := conv(t, src, "docx")
	back, _, err := toMarkdown(docx[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"# Zażółć gęślą jaźń", "**bold**", "*italic*", "- one", "| 1 | 2 |"} {
		if !strings.Contains(back, w) {
			t.Fatalf("docx -> md lost %q:\n%s", w, back)
		}
	}
	html := conv(t, src, "html")
	if b, _ := os.ReadFile(html[0]); !strings.Contains(string(b), "<strong>bold</strong>") || !strings.Contains(string(b), "<title>notes</title>") {
		t.Fatal("html: no formatting")
	}
	txt := conv(t, src, "txt")
	if b, _ := os.ReadFile(txt[0]); !strings.Contains(string(b), "• one") || strings.Contains(string(b), "**") {
		t.Fatalf("txt:\n%s", b)
	}
	epub := conv(t, src, "epub")
	if m, title, err := toMarkdown(epub[0]); err != nil || !strings.Contains(m, "bold") || title != "notes" {
		t.Fatalf("epub -> md: %q %q %v", m, title, err)
	}
	// the round trips go on from what was made
	conv(t, html[0], "md")
	conv(t, docx[0], "pdf")
	conv(t, txt[0], "docx")
	// a PDF's text, made here, read back
	m, _, err := toMarkdown(pdf[0])
	if err != nil || !strings.Contains(m, "bold") {
		t.Fatalf("pdf text: %q %v", m, err)
	}
}

func TestConvertData(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "people.json")
	os.WriteFile(src, []byte(`[{"name":"Ala","age":30,"city":"Kraków"},{"name":"Ola","age":25,"city":"Łódź"}]`), 0o644)
	var last string
	for _, to := range []string{"yaml", "toml", "xml", "csv", "tsv", "xlsx", "md"} {
		out := conv(t, src, to)
		last = out[0]
		if to == "md" {
			continue
		}
		// and back to JSON: the same records
		j := conv(t, out[0], "json")
		b, _ := os.ReadFile(j[0])
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatalf("%s -> json: %v", to, err)
		}
		s := string(b)
		if !strings.Contains(s, "Kraków") || !strings.Contains(s, "Łódź") {
			t.Fatalf("%s -> json lost the data:\n%s", to, s)
		}
		if to == "csv" || to == "xlsx" || to == "yaml" {
			if !strings.Contains(s, `"age": 30`) {
				t.Fatalf("%s -> json: numbers should stay numbers:\n%s", to, s)
			}
		}
	}
	if b, _ := os.ReadFile(last); !strings.Contains(string(b), "| name |") && !strings.Contains(string(b), "| age |") {
		t.Fatalf("markdown table:\n%s", b)
	}
}

func TestConvertArchives(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "box.zip")
	f, _ := os.Create(src)
	z := zip.NewWriter(f)
	for name, body := range map[string]string{"a.txt": "A", "sub/b.txt": "B", "../evil.txt": "no"} {
		w, _ := z.Create(name)
		w.Write([]byte(body))
	}
	z.Close()
	f.Close()
	for _, to := range []string{"tar.gz", "tar.xz", "tar.zst", "tar"} {
		out := conv(t, src, to)
		ex := conv(t, out[0], "extract")
		if b, err := os.ReadFile(filepath.Join(ex[0], "sub", "b.txt")); err != nil || string(b) != "B" {
			t.Fatalf("%s: extracted %q %v", to, b, err)
		}
		back := conv(t, out[0], "zip")
		if zr, err := zip.OpenReader(back[0]); err != nil || len(zr.File) < 2 {
			t.Fatalf("%s -> zip: %v", to, err)
		} else {
			zr.Close()
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.txt")); err == nil {
		t.Fatal("an entry got out of its folder (zip-slip)")
	}
	// one compressed file
	gz := filepath.Join(dir, "box.tar.gz")
	conv(t, gz, "extract")
}

func TestTargets(t *testing.T) {
	has := func(ts []target, id string) bool {
		for _, x := range ts {
			if x.ID == id {
				return true
			}
		}
		return false
	}
	ts := targetsFor([]string{`C:\x\a.png`})
	if has(ts, "png") || !has(ts, "jpg") || !has(ts, "zip-all") {
		t.Fatalf("png: %v", ids(ts))
	}
	ts = targetsFor([]string{`C:\x\a.png`, `C:\x\b.jpg`})
	if !has(ts, "pdf-one") || !has(ts, "png") {
		t.Fatalf("png+jpg: %v", ids(ts))
	}
	ts = targetsFor([]string{`C:\x\a.png`, `C:\x\b.mp3`})
	if len(ts) != 1 || ts[0].ID != "zip-all" {
		t.Fatalf("mixed: %v", ids(ts))
	}
	if ts := targetsFor([]string{`C:\x\a.pdf`, `C:\x\b.pdf`}); !has(ts, "merge") {
		t.Fatalf("pdfs: %v", ids(ts))
	}
	if ts := targetsFor([]string{`C:\x\unknown.qqq`}); len(ts) != 1 {
		t.Fatalf("unknown: %v", ids(ts))
	}
}

func TestJobs(t *testing.T) {
	dir := t.TempDir()
	a := testPicture(t, dir, 20, 20)
	tg, _ := findTarget([]string{a}, "jpg")
	done := make(chan *job, 1)
	convJobs.start([]string{a, filepath.Join(dir, "missing.png")}, tg, func(j *job) { done <- j })
	j := <-done
	if j.Done != 2 || len(j.Out) != 1 || len(j.Errors) != 1 || !strings.Contains(j.Errors[0], "missing.png") {
		t.Fatalf("job: %+v", j)
	}
}

// TestDonutDraw renders the donut's states (MW4W_DONUT_PNG=<dir> keeps them to look at).
func TestDonutDraw(t *testing.T) {
	ts := targetsFor([]string{`C:\x\trip.jpg`})
	var front []target
	for _, x := range ts {
		if !x.Back {
			front = append(front, x)
		}
	}
	out := os.Getenv("MW4W_DONUT_PNG")
	for name, v := range map[string]donutView{
		"idle":    {slices: front, hover: -1, sides: 2, title: "trip.jpg", alpha: 1},
		"hover":   {slices: front, hover: 2, sides: 2, title: "trip.jpg", sub: front[2].Hint, alpha: 1},
		"working": {slices: front, hover: -1, title: "trip.jpg", alpha: 1, working: true, prog: 0.4},
		"done":    {slices: front, hover: -1, title: "trip.jpg", alpha: 1, done: 1},
	} {
		img := drawDonut(v, 1.5)
		if img.Bounds().Dx() != 510 {
			t.Fatalf("%s: size %v", name, img.Bounds())
		}
		// round: the corners are clear (click-through), the ring isn't
		if _, _, _, a := img.At(2, 2).RGBA(); a != 0 {
			t.Fatalf("%s: corner not transparent", name)
		}
		if _, _, _, a := img.At(255, 255-157).RGBA(); a < 0xf000 {
			t.Fatalf("%s: ring not opaque (%x)", name, a)
		}
		if out != "" {
			f, _ := os.Create(filepath.Join(out, "donut-"+name+".png"))
			png.Encode(f, img)
			f.Close()
		}
	}
	// hit test: top is slice 0, clockwise, the hole is nothing
	if i := sliceAt(0, -100, 8, 1); i != 0 {
		t.Fatalf("top: %d", i)
	}
	if i := sliceAt(100, 0, 8, 1); i != 2 {
		t.Fatalf("right: %d", i)
	}
	if i := sliceAt(5, 5, 8, 1); i != -1 {
		t.Fatalf("hole: %d", i)
	}
}

// TestConvertMoving: SVG, GIF/APNG/WebP animations and video (Media Foundation) into each other.
func TestConvertMoving(t *testing.T) {
	done, err := mfThread()
	if err != nil {
		t.Skip(err) // Windows Server without Media Foundation
	}
	done()
	dir := t.TempDir()
	// a 1 s 320x240 video: half a second red, then blue, with a tone
	vid := filepath.Join(dir, "clip.mp4")
	solid := func(c color.RGBA) image.Image {
		img := image.NewRGBA(image.Rect(0, 0, 320, 240))
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, 255
		}
		return img
	}
	red, blue := solid(color.RGBA{220, 30, 30, 255}), solid(color.RGBA{30, 30, 220, 255})
	frames := func(yield func(image.Image, int) error) error {
		for i := 0; i < 30; i++ {
			img := red
			if i >= 15 {
				img = blue
			}
			if err := yield(img, 33); err != nil {
				return err
			}
		}
		return nil
	}
	tone := &pcm{rate: 48000, channels: 2}
	for i := 0; i < 48000; i++ {
		v := int16(math.Sin(2*math.Pi*440*float64(i)/48000) * 12000)
		tone.data = append(tone.data, v, v)
	}
	if err := writeMP4(vid, frames, 30, 1920, tone); err != nil {
		t.Fatal(err)
	}
	near := func(img image.Image, want color.RGBA) bool {
		r, g, b, _ := img.At(img.Bounds().Dx()/2, img.Bounds().Dy()/2).RGBA()
		d := func(a uint32, b uint8) int { return int(a>>8) - int(b) }
		return max(d(r, want.R), -d(r, want.R), d(g, want.G), -d(g, want.G), d(b, want.B), -d(b, want.B)) < 40
	}
	// frames back: count, size, colours, timing
	count := func(p string) (n, ms int, first, last image.Image) {
		t.Helper()
		f, err := framesOf(p, 1000, 0)
		if err != nil {
			t.Fatal(p, err)
		}
		if err := f(func(img image.Image, d int) error {
			if n++; first == nil {
				first = img
			}
			last, ms = img, ms+d
			return nil
		}); err != nil {
			t.Fatal(p, err)
		}
		return
	}
	n, ms, first, last := count(vid)
	if n < 28 || n > 31 || ms < 900 || ms > 1100 || first.Bounds().Dx() != 320 || !near(first, color.RGBA{220, 30, 30, 255}) || !near(last, color.RGBA{30, 30, 220, 255}) {
		t.Fatalf("video: %d frames, %d ms, %v, first %v last %v", n, ms, first.Bounds(), first.At(160, 120), last.At(160, 120))
	}
	if a, err := mfAudio(vid); err != nil || len(a.data)/a.channels < 40000 {
		t.Fatalf("sound: %v", err)
	}
	// video -> moving pictures keep the motion; stills are the first frame; sound comes out
	for _, to := range []string{"gif", "apng", "webp", "mp4-small"} {
		out := conv(t, vid, to)
		n, ms, first, last := count(out[0])
		if n < 2 || ms < 800 || !near(first, color.RGBA{220, 30, 30, 255}) || !near(last, color.RGBA{30, 30, 220, 255}) {
			t.Fatalf("%s: %d frames, %d ms", to, n, ms)
		}
	}
	if img, err := decodeImage(conv(t, vid, "jpg")[0]); err != nil || !near(img, color.RGBA{220, 30, 30, 255}) {
		t.Fatalf("jpg: %v", err)
	}
	for _, to := range []string{"mp3", "wav", "m4a"} {
		conv(t, vid, to)
	}
	if a, err := decodeAudio(filepath.Join(dir, "clip.m4a")); err != nil || len(a.data) == 0 {
		t.Fatalf("m4a back: %v", err)
	}
	// GIF -> APNG -> WebP -> MP4, motion all the way
	gif := filepath.Join(dir, "clip.gif")
	p := conv(t, gif, "apng")[0]
	p = conv(t, p, "webp")[0]
	p = conv(t, p, "mp4")[0]
	if n, _, _, last := count(p); n < 10 || !near(last, color.RGBA{30, 30, 220, 255}) {
		t.Fatalf("gif->apng->webp->mp4: %d frames", n)
	}
	// tools on moving pictures change every frame and stay moving
	if n, _, first, _ := count(conv(t, gif, "rotr")[0]); n < 2 || first.Bounds().Dx() != 240 {
		t.Fatalf("rotated gif: %d frames, %v", n, first.Bounds())
	}
	// SVG
	svg := filepath.Join(dir, "icon.svg")
	os.WriteFile(svg, []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><rect width="24" height="24" fill="#0c0"/></svg>`), 0o644)
	if img, err := decodeImage(conv(t, svg, "png")[0]); err != nil || img.Bounds().Dx() != 512 || !near(img, color.RGBA{0, 204, 0, 255}) {
		t.Fatalf("svg: %v", err)
	}
	// what real files use: blur filters, text, compact arcs, display-p3 colours with a fallback
	fancy := filepath.Join(dir, "fancy.svg")
	os.WriteFile(fancy, []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="200" height="100" style="background:#102030"><filter id="b"><feGaussianBlur stdDeviation="4"/></filter>`+
		`<circle cx="50" cy="50" r="30" style="fill:#ff0000;fill:color(display-p3 1 0 0)" filter="url(#b)"/><path d="M120 20a10 10 0 0 0 20 0z" fill="#0f0"/>`+
		`<text x="100" y="90" font-size="20" fill="#fff">Ag</text></svg>`), 0o644)
	img, err := decodeImage(fancy)
	if err != nil {
		t.Fatal("fancy svg:", err)
	}
	px := func(x, y int) color.RGBA {
		b := img.Bounds()
		return color.RGBAModel.Convert(img.At(x*b.Dx()/200, y*b.Dy()/100)).(color.RGBA)
	}
	if c := px(50, 50); c.R < 150 || c.G > 80 {
		t.Fatalf("blurred red circle: %v", c)
	}
	if c := px(5, 5); c != (color.RGBA{0x10, 0x20, 0x30, 255}) {
		t.Fatalf("css background: %v", c)
	}
	white := 0
	for x := 100; x < 130; x++ {
		for y := 72; y < 92; y++ {
			if c := px(x, y); c.R > 200 && c.B > 200 {
				white++
			}
		}
	}
	if white < 20 {
		t.Fatalf("no text drawn (%d white pixels)", white)
	}
	// anything together: a still, an SVG and a GIF in one animation and one video
	pic := testPicture(t, dir, 64, 48)
	for _, to := range []string{"gif-anim", "mp4-anim", "apng-anim", "webp-anim"} {
		out, err := convertAll(context.Background(), []string{pic, svg, gif}, target{ID: to, Multi: true})
		if err != nil {
			t.Fatal(to, err)
		}
		if n, _, _, _ := count(out[0]); n < 3 {
			t.Fatalf("%s: %d frames", to, n)
		}
	}
	ts := targetsFor([]string{pic, svg, vid})
	if !strings.Contains(strings.Join(ids(ts), " "), "mp4-anim gif-anim") {
		t.Fatalf("mixed targets: %v", ids(ts))
	}
}
