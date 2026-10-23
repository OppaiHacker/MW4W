package main

// SVG: resvg (the full SVG renderer: filters, gradients, masks, text, embedded pictures) built to
// WebAssembly from svgwasm/ and run by wazero, so no cgo. One instance for the process, its
// compiled code cached on disk so later starts skip compiling.

import (
	"context"
	_ "embed"
	"encoding/binary"
	"errors"
	"image"
	"os"
	"path/filepath"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

//go:embed svg.wasm
var svgWasm []byte

var svgEngine struct {
	sync.Mutex
	once sync.Once
	mod  api.Module
	err  error
}

func svgCall(name string, args ...uint64) (uint64, error) {
	r, err := svgEngine.mod.ExportedFunction(name).Call(context.Background(), args...)
	if err != nil {
		return 0, err
	}
	if len(r) == 0 {
		return 0, nil
	}
	return r[0], nil
}

// svgPut copies b into the module; free it with dealloc.
func svgPut(b []byte) (uint64, error) {
	p, err := svgCall("alloc", uint64(len(b)))
	if err == nil && !svgEngine.mod.Memory().Write(uint32(p), b) {
		err = errors.New("svg: out of memory")
	}
	return p, err
}

func svgStart() {
	ctx := context.Background()
	cfg := wazero.NewRuntimeConfig()
	if dir, err := os.UserCacheDir(); err == nil {
		if c, err := wazero.NewCompilationCacheWithDir(filepath.Join(dir, "mw4w-files", "wasm")); err == nil {
			cfg = cfg.WithCompilationCache(c)
		}
	}
	svgEngine.mod, svgEngine.err = wazero.NewRuntimeWithConfig(ctx, cfg).Instantiate(ctx, svgWasm)
	if svgEngine.err != nil {
		return
	}
	// text: the first font is the default
	for _, f := range []string{"segoeui.ttf", "segoeuib.ttf", "segoeuii.ttf", "arial.ttf", "times.ttf", "consola.ttf"} {
		d, err := os.ReadFile(filepath.Join(os.Getenv("WINDIR"), "Fonts", f))
		if err != nil {
			continue
		}
		if p, err := svgPut(d); err == nil {
			svgCall("add_font", p, uint64(len(d)))
			svgCall("dealloc", p, uint64(len(d)))
		}
	}
}

// renderSVG draws an SVG at its own size, small ones scaled up to minSide, big ones down to maxSide.
func renderSVG(b []byte, minSide, maxSide int) (*image.RGBA, error) {
	svgEngine.once.Do(svgStart)
	if svgEngine.err != nil {
		return nil, svgEngine.err
	}
	svgEngine.Lock()
	defer svgEngine.Unlock()
	p, err := svgPut(b)
	if err != nil {
		return nil, err
	}
	res, err := svgCall("render", p, uint64(len(b)), uint64(minSide), uint64(maxSide))
	svgCall("dealloc", p, uint64(len(b)))
	if err != nil {
		return nil, err
	}
	if res == 0 {
		return nil, errors.New("not an SVG that can be drawn")
	}
	rp, rl := uint32(res>>32), uint32(res)
	defer svgCall("dealloc", uint64(rp), uint64(rl))
	data, ok := svgEngine.mod.Memory().Read(rp, rl)
	if !ok || rl < 8 {
		return nil, errors.New("svg: bad result")
	}
	w, h := int(binary.LittleEndian.Uint32(data)), int(binary.LittleEndian.Uint32(data[4:]))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	copy(img.Pix, data[8:])
	return img, nil
}
