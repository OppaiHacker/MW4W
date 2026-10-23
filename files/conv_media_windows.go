package main

// Video, and the sound of videos and of M4A/AAC/WMA: Windows' own Media Foundation codecs, called
// over COM from Go (no ffmpeg, no cgo). Reading gives RGB frames and 16-bit PCM; writing makes
// H.264 + AAC MP4 (or AAC M4A). Vtable slots and GUIDs are from the SDK's mfobjects.h,
// mfreadwrite.h and mfapi.h.

import (
	"errors"
	"fmt"
	"image"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	mfplat                          = windows.NewLazySystemDLL("mfplat.dll")
	mfrw                            = windows.NewLazySystemDLL("mfreadwrite.dll")
	procMFStartup                   = mfplat.NewProc("MFStartup")
	procMFCreateAttributes          = mfplat.NewProc("MFCreateAttributes")
	procMFCreateMediaType           = mfplat.NewProc("MFCreateMediaType")
	procMFCreateSample              = mfplat.NewProc("MFCreateSample")
	procMFCreateMemoryBuffer        = mfplat.NewProc("MFCreateMemoryBuffer")
	procMFCreateSourceReaderFromURL = mfrw.NewProc("MFCreateSourceReaderFromURL")
	procMFCreateSinkWriterFromURL   = mfrw.NewProc("MFCreateSinkWriterFromURL")
	mfOnce                          sync.Once
	mfErr                           error
)

func guid(d1 uint32, d2, d3 uint16, d4 ...byte) windows.GUID {
	g := windows.GUID{Data1: d1, Data2: d2, Data3: d3}
	copy(g.Data4[:], d4)
	return g
}

// mediaGUID: the media subtype GUIDs built from a FOURCC or WAVE_FORMAT tag.
func mediaGUID(tag uint32) windows.GUID {
	return guid(tag, 0, 0x10, 0x80, 0, 0, 0xaa, 0, 0x38, 0x9b, 0x71)
}

var (
	mtMajorType     = guid(0x48eba18e, 0xf8c9, 0x4687, 0xbf, 0x11, 0x0a, 0x74, 0xc9, 0xf9, 0x6a, 0x8f)
	mtSubtype       = guid(0xf7e34c9a, 0x42e8, 0x4714, 0xb7, 0x4b, 0xcb, 0x29, 0xd7, 0x2c, 0x35, 0xe5)
	mtFrameSize     = guid(0x1652c33d, 0xd6b2, 0x4012, 0xb8, 0x34, 0x72, 0x03, 0x08, 0x49, 0xa3, 0x7d)
	mtFrameRate     = guid(0xc459a2e8, 0x3d2c, 0x4e44, 0xb1, 0x32, 0xfe, 0xe5, 0x15, 0x6c, 0x7b, 0xb0)
	mtAvgBitrate    = guid(0x20332624, 0xfb0d, 0x4d9e, 0xbd, 0x0d, 0xcb, 0xf6, 0x78, 0x6c, 0x10, 0x2e)
	mtInterlace     = guid(0xe2724bb8, 0xe676, 0x4806, 0xb4, 0xb2, 0xa8, 0xd6, 0xef, 0xb4, 0x4c, 0xcd)
	mtPixelAspect   = guid(0xc6376a1e, 0x8d0a, 0x4027, 0xbe, 0x45, 0x6d, 0x9a, 0x0a, 0xd3, 0x9b, 0xb6)
	mtDefaultStride = guid(0x644b4e48, 0x1e02, 0x4516, 0xb0, 0xeb, 0xc0, 0x1c, 0xa9, 0xd4, 0x9a, 0xc6)
	mtAperture      = guid(0xd7388766, 0x18fe, 0x48c6, 0xa1, 0x77, 0xee, 0x89, 0x48, 0x67, 0xc8, 0xc4)
	mtProfile       = guid(0xad76a80b, 0x2d5c, 0x4e0b, 0xb3, 0x75, 0x64, 0xe5, 0x20, 0x13, 0x70, 0x36)
	mtChannels      = guid(0x37e48bf5, 0x645e, 0x4c5b, 0x89, 0xde, 0xad, 0xa9, 0xe2, 0x9b, 0x69, 0x6a)
	mtSampleRate    = guid(0x5faeeae7, 0x0290, 0x4c31, 0x9e, 0x8a, 0xc5, 0x34, 0xf6, 0x8d, 0x9d, 0xba)
	mtBits          = guid(0xf2deb57f, 0x40fa, 0x4764, 0xaa, 0x33, 0xed, 0x4f, 0x2d, 0x1f, 0xf6, 0x69)
	mtBytesPerSec   = guid(0x1aab75c8, 0xcfef, 0x451c, 0xab, 0x95, 0xac, 0x03, 0x4b, 0x8e, 0x17, 0x31)
	mtBlockAlign    = guid(0x322de230, 0x9eeb, 0x43bd, 0xab, 0x7a, 0xff, 0x41, 0x22, 0x51, 0x54, 0x1d)
	srVideoProc     = guid(0xfb394f3d, 0xccf1, 0x42ee, 0xbb, 0xb3, 0xf9, 0xb8, 0x45, 0xd5, 0x68, 0x1d)
	rwHardware      = guid(0xa634a91c, 0x822b, 0x41b9, 0xa4, 0x94, 0x4d, 0xe4, 0x64, 0x36, 0x12, 0xb0)
	pdDuration      = guid(0x6c990d33, 0xbb8e, 0x477a, 0x85, 0x98, 0x0d, 0x5d, 0x96, 0xfc, 0xd8, 0x8a)
	mediaVideo      = mediaGUID(0x73646976) // 'vids'
	mediaAudio      = mediaGUID(0x73647561) // 'auds'
	fmtRGB32        = mediaGUID(22)         // D3DFMT_X8R8G8B8
	fmtNV12         = mediaGUID(0x3231564e) // 'NV12'
	fmtH264         = mediaGUID(0x34363248) // 'H264'
	fmtPCM          = mediaGUID(1)
	fmtAAC          = mediaGUID(0x1610)
)

const (
	firstVideo   = 0xfffffffc
	firstAudio   = 0xfffffffd
	allStreams   = 0xfffffffe
	mediaSource  = 0xffffffff
	readerEOS    = 0x2
	readerError  = 0x1
	readerNewFmt = 0x20
)

func hr(r uintptr, what string) error {
	if int32(r) < 0 {
		return fmt.Errorf("%s failed (0x%08x)", what, uint32(r))
	}
	return nil
}

// mfThread: Media Foundation on this OS thread (COM is per thread); call the result when done.
func mfThread() (func(), error) {
	runtime.LockOSThread()
	windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED)
	mfOnce.Do(func() {
		if err := procMFStartup.Find(); err != nil {
			mfErr = errors.New("this Windows has no Media Foundation (N edition?)")
			return
		}
		r, _, _ := procMFStartup.Call(0x00020070, 0) // MF_VERSION, MFSTARTUP_FULL
		mfErr = hr(r, "MFStartup")
	})
	done := func() { windows.CoUninitialize(); runtime.UnlockOSThread() }
	if mfErr != nil {
		done()
		return nil, mfErr
	}
	return done, nil
}

// IMFAttributes, which media types and samples are too
func setU32(o uintptr, k windows.GUID, v uint32) {
	comCall(o, 21, uintptr(unsafe.Pointer(&k)), uintptr(v))
}
func setU64(o uintptr, k windows.GUID, hi, lo uint32) {
	comCall(o, 22, uintptr(unsafe.Pointer(&k)), uintptr(uint64(hi)<<32|uint64(lo)))
}
func setGUID(o uintptr, k, v windows.GUID) {
	comCall(o, 24, uintptr(unsafe.Pointer(&k)), uintptr(unsafe.Pointer(&v)))
}
func getU32(o uintptr, k windows.GUID) (uint32, bool) {
	var v uint32
	return v, int32(comCall(o, 7, uintptr(unsafe.Pointer(&k)), uintptr(unsafe.Pointer(&v)))) >= 0
}
func getU64(o uintptr, k windows.GUID) (hi, lo uint32, ok bool) {
	var v uint64
	ok = int32(comCall(o, 8, uintptr(unsafe.Pointer(&k)), uintptr(unsafe.Pointer(&v)))) >= 0
	return uint32(v >> 32), uint32(v), ok
}

func newType(major, sub windows.GUID) uintptr {
	var t uintptr
	procMFCreateMediaType.Call(uintptr(unsafe.Pointer(&t)))
	setGUID(t, mtMajorType, major)
	setGUID(t, mtSubtype, sub)
	return t
}

// ---------------------------------------------------------------- reading

type mfReader struct {
	r            uintptr
	stream       uint32 // firstVideo or firstAudio
	w, h, stride int
	crop         image.Rectangle
	fps          float64
	rate, ch     int
	durMs        int
	nv12         bool
}

// openReader opens one stream of a file: video as RGB32 frames or audio as 16-bit PCM.
func openReader(p string, video bool) (*mfReader, error) {
	var a uintptr
	procMFCreateAttributes.Call(uintptr(unsafe.Pointer(&a)), 2)
	defer comRelease(a)
	if video {
		setU32(a, srVideoProc, 1) // RGB32 for decoders without NV12
		// ponytail: CPU decoding; a D3D11 manager was tried and was slower (GPU->CPU copies of every frame)
	}
	m := &mfReader{stream: firstAudio}
	if video {
		m.stream = firstVideo
	}
	u, _ := windows.UTF16PtrFromString(p)
	if r, _, _ := procMFCreateSourceReaderFromURL.Call(uintptr(unsafe.Pointer(u)), a, uintptr(unsafe.Pointer(&m.r))); int32(r) < 0 {
		return nil, errors.New("Windows can't open this file (no codec for it?)")
	}
	comCall(m.r, 4, allStreams, 0)
	if err := hr(comCall(m.r, 4, uintptr(m.stream), 1), "select"); err != nil {
		m.close()
		if video {
			return nil, errors.New("there's no picture in it")
		}
		return nil, errors.New("there's no sound in it")
	}
	set := func(t uintptr) error {
		defer comRelease(t)
		return hr(comCall(m.r, 7, uintptr(m.stream), 0, t), "decode")
	}
	var err error
	if video {
		// NV12 is what decoders give: converting it here (and shrinking at once) is far faster
		// than Windows' own RGB conversion; RGB32 for the few that don't give it
		if m.nv12 = set(newType(mediaVideo, fmtNV12)) == nil; !m.nv12 {
			err = set(newType(mediaVideo, fmtRGB32))
		}
	} else {
		t := newType(mediaAudio, fmtPCM)
		setU32(t, mtBits, 16)
		err = set(t)
	}
	if err != nil {
		m.close()
		return nil, errors.New("Windows has no decoder for this file")
	}
	m.format()
	var pv [3]uint64 // PROPVARIANT: vt, then the value
	if hr(comCall(m.r, 12, mediaSource, uintptr(unsafe.Pointer(&pdDuration)), uintptr(unsafe.Pointer(&pv[0]))), "") == nil {
		m.durMs = int(pv[1] / 10000)
	}
	return m, nil
}

// format reads what the reader now gives (again after a format change mid-file).
func (m *mfReader) format() {
	var t uintptr
	if hr(comCall(m.r, 6, uintptr(m.stream), uintptr(unsafe.Pointer(&t))), "") != nil {
		return
	}
	defer comRelease(t)
	if m.stream == firstAudio {
		ch, _ := getU32(t, mtChannels)
		rate, _ := getU32(t, mtSampleRate)
		m.ch, m.rate = int(ch), int(rate)
		return
	}
	w, h, _ := getU64(t, mtFrameSize)
	m.w, m.h = int(w), int(h)
	m.stride = m.w * 4
	if m.nv12 {
		m.stride = m.w
	}
	if s, ok := getU32(t, mtDefaultStride); ok {
		m.stride = int(int32(s))
	}
	m.crop = image.Rect(0, 0, m.w, m.h)
	// a 1080p H.264 is decoded 1088 high: the aperture says what to show
	var area [4]int32 // MFVideoArea: OffsetX, OffsetY (16.16 fixed), cx, cy
	var n uint32
	if hr(comCall(t, 15, uintptr(unsafe.Pointer(&mtAperture)), uintptr(unsafe.Pointer(&area[0])), 16, uintptr(unsafe.Pointer(&n))), "") == nil && area[2] > 0 && area[3] > 0 {
		x, y := int(area[0]>>16), int(area[1]>>16)
		m.crop = image.Rect(x, y, x+int(area[2]), y+int(area[3])).Intersect(m.crop)
	}
	if num, den, ok := getU64(t, mtFrameRate); ok && den > 0 {
		m.fps = float64(num) / float64(den)
	}
}

// read: the next sample's bytes (a copy), its time and length in 100 ns; ok false at the end.
func (m *mfReader) read() (b []byte, ts, dur int64, ok bool, err error) {
	for {
		var idx, flags uint32
		var s uintptr
		if err := hr(comCall(m.r, 9, uintptr(m.stream), 0, uintptr(unsafe.Pointer(&idx)), uintptr(unsafe.Pointer(&flags)), uintptr(unsafe.Pointer(&ts)), uintptr(unsafe.Pointer(&s))), "read"); err != nil {
			return nil, 0, 0, false, err
		}
		if flags&readerError != 0 {
			comRelease(s)
			return nil, 0, 0, false, errors.New("the file is damaged")
		}
		if flags&readerNewFmt != 0 {
			m.format()
		}
		if s == 0 {
			if flags&readerEOS != 0 {
				return nil, 0, 0, false, nil
			}
			continue
		}
		comCall(s, 37, uintptr(unsafe.Pointer(&dur)))
		var buf uintptr
		comCall(s, 41, uintptr(unsafe.Pointer(&buf)))
		var p *byte
		var n uint32
		if buf != 0 && hr(comCall(buf, 3, uintptr(unsafe.Pointer(&p)), 0, uintptr(unsafe.Pointer(&n))), "") == nil {
			b = append([]byte(nil), unsafe.Slice(p, n)...)
			comCall(buf, 4)
		}
		comRelease(buf)
		comRelease(s)
		return b, ts, dur, true, nil
	}
}

// frame turns a read sample into a picture, shrunk by a whole factor while no side gets shorter
// than maxSide (0: full size).
func (m *mfReader) frame(b []byte, maxSide int) image.Image {
	if m.nv12 {
		return m.nv12Frame(b, maxSide)
	}
	c := m.crop
	img := image.NewRGBA(image.Rect(0, 0, c.Dx(), c.Dy()))
	for y := 0; y < c.Dy(); y++ {
		row := (c.Min.Y + y) * m.stride
		if m.stride < 0 { // bottom-up
			row = (m.h - 1 - c.Min.Y - y) * -m.stride
		}
		if row < 0 || row+c.Max.X*4 > len(b) {
			break
		}
		src, dst := b[row+c.Min.X*4:row+c.Max.X*4], img.Pix[y*img.Stride:]
		for x := 0; x < len(src); x += 4 {
			dst[x], dst[x+1], dst[x+2], dst[x+3] = src[x+2], src[x+1], src[x], 255
		}
	}
	return img
}

// nv12Frame: Y plane, then interleaved U/V at half size; k x k boxes averaged when shrinking.
// BT.709 for HD, BT.601 below, studio range.
func (m *mfReader) nv12Frame(b []byte, maxSide int) image.Image {
	c, st := m.crop, max(m.stride, -m.stride)
	k := 1
	if maxSide > 0 {
		k = max(1, max(c.Dx(), c.Dy())/maxSide)
	}
	w, h := c.Dx()/k, c.Dy()/k
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	uv := st * m.h
	if len(b) < uv+st*(m.h/2) || w == 0 || h == 0 {
		return img
	}
	rv, gu, gv, bu := 409, 100, 208, 516
	if c.Dy() >= 720 {
		rv, gu, gv, bu = 459, 55, 136, 541
	}
	var yt, rt, gut, gvt, bt [256]int32 // per byte value: its share of R, G, B (x256)
	for i := range 256 {
		yt[i], rt[i], gut[i], gvt[i], bt[i] = int32((i-16)*298+128), int32(rv*(i-128)), int32(gu*(i-128)), int32(gv*(i-128)), int32(bu*(i-128))
	}
	var sat [1024 + 256]uint8 // clamp of (v>>8) for v>>8 in -512..767
	for i := range sat {
		sat[i] = uint8(min(255, max(0, i-512)))
	}
	clamp := func(v int32) uint8 { return sat[(v>>8)+512] }
	kk := k * k
	rows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			sy := c.Min.Y + y*k
			dst := img.Pix[y*img.Stride : y*img.Stride+w*4]
			uvRow := b[uv+((sy+k/2)/2)*st:]
			for x := 0; x < w; x++ {
				sx := c.Min.X + x*k
				Y := 0
				if k == 1 {
					Y = int(b[sy*st+sx])
				} else {
					for j := 0; j < k; j++ {
						for _, v := range b[(sy+j)*st+sx : (sy+j)*st+sx+k] {
							Y += int(v)
						}
					}
					Y /= kk
				}
				cy := yt[Y]
				ci := ((sx + k/2) / 2) * 2
				u, v := uvRow[ci], uvRow[ci+1]
				d := dst[x*4 : x*4+4 : x*4+4]
				d[0], d[1], d[2], d[3] = clamp(cy+rt[v]), clamp(cy-gut[u]-gvt[v]), clamp(cy+bt[u]), 255
			}
		}
	})
	return img
}

func (m *mfReader) close() { comRelease(m.r) }

// videoFrames: every frame of a video and how long it shows, no side shorter than maxSide
// unless the video is smaller (0: full size).
func videoFrames(p string, maxSide int) (frameFn, error) {
	return func(yield func(image.Image, int) error) error {
		// decoding runs ahead on its own thread (the reader stays there) while frames are used here
		type frame struct {
			img image.Image
			ms  int
			err error
		}
		ch, stop := make(chan frame, 3), make(chan struct{})
		defer close(stop)
		go func() {
			defer close(ch)
			send := func(f frame) bool {
				select {
				case ch <- f:
					return true
				case <-stop:
					return false
				}
			}
			done, err := mfThread()
			if err != nil {
				send(frame{err: err})
				return
			}
			defer done()
			m, err := openReader(p, true)
			if err != nil {
				send(frame{err: err})
				return
			}
			defer m.close()
			for {
				b, _, dur, ok, err := m.read()
				if err != nil || !ok {
					if err != nil {
						send(frame{err: err})
					}
					return
				}
				ms := int(dur / 10000)
				if ms <= 0 && m.fps > 0 {
					ms = int(1000 / m.fps)
				}
				if !send(frame{img: m.frame(b, maxSide), ms: max(ms, 1)}) {
					return
				}
			}
		}()
		for f := range ch {
			if f.err != nil {
				return f.err
			}
			if err := yield(f.img, f.ms); err != nil {
				return err
			}
		}
		return nil
	}, nil
}

// videoFPS: the frame rate of a video (0 when unknown).
func videoFPS(p string) float64 {
	done, err := mfThread()
	if err != nil {
		return 0
	}
	defer done()
	m, err := openReader(p, true)
	if err != nil {
		return 0
	}
	defer m.close()
	return m.fps
}

// mfAudio: the whole sound track as PCM.
func mfAudio(p string) (*pcm, error) {
	done, err := mfThread()
	if err != nil {
		return nil, err
	}
	defer done()
	m, err := openReader(p, false)
	if err != nil {
		return nil, err
	}
	defer m.close()
	out := &pcm{rate: m.rate, channels: m.ch}
	for {
		b, _, _, ok, err := m.read()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		for i := 0; i+1 < len(b); i += 2 {
			out.data = append(out.data, int16(uint16(b[i])|uint16(b[i+1])<<8))
		}
	}
	if out.channels == 0 || out.rate == 0 {
		return nil, errors.New("there's no sound in it")
	}
	return out, nil
}

// ---------------------------------------------------------------- writing

type mfWriter struct {
	w             uintptr
	vs, as        uint32
	width, height int
	rate, ch      int
}

// newWriter makes an MP4 (or M4A when width is 0): H.264 video of width x height at fps, and AAC
// sound when rate > 0 (44.1 or 48 kHz, 1 or 2 channels: what the AAC encoder takes).
func newWriter(p string, width, height, fps, rate, ch int) (*mfWriter, error) {
	var a uintptr
	procMFCreateAttributes.Call(uintptr(unsafe.Pointer(&a)), 1)
	setU32(a, rwHardware, 1)
	defer comRelease(a)
	u, _ := windows.UTF16PtrFromString(p)
	m := &mfWriter{width: width, height: height, rate: rate, ch: ch}
	if r, _, _ := procMFCreateSinkWriterFromURL.Call(uintptr(unsafe.Pointer(u)), 0, a, uintptr(unsafe.Pointer(&m.w))); int32(r) < 0 {
		return nil, hr(r, "creating the file")
	}
	fail := func(err error) (*mfWriter, error) {
		comRelease(m.w)
		return nil, err
	}
	if width > 0 {
		out := newType(mediaVideo, fmtH264)
		setU32(out, mtAvgBitrate, uint32(min(20e6, max(1e6, float64(width*height*fps)*0.12))))
		setU32(out, mtInterlace, 2) // progressive
		setU64(out, mtFrameSize, uint32(width), uint32(height))
		setU64(out, mtFrameRate, uint32(fps), 1)
		setU64(out, mtPixelAspect, 1, 1)
		setU32(out, mtProfile, 100) // High
		err := hr(comCall(m.w, 3, out, uintptr(unsafe.Pointer(&m.vs))), "H.264 encoder")
		comRelease(out)
		if err != nil {
			return fail(err)
		}
		in := newType(mediaVideo, fmtNV12)
		setU32(in, mtInterlace, 2)
		setU64(in, mtFrameSize, uint32(width), uint32(height))
		setU64(in, mtFrameRate, uint32(fps), 1)
		setU64(in, mtPixelAspect, 1, 1)
		setU32(in, mtDefaultStride, uint32(width))
		err = hr(comCall(m.w, 4, uintptr(m.vs), in, 0), "video input")
		comRelease(in)
		if err != nil {
			return fail(err)
		}
	}
	if rate > 0 {
		out := newType(mediaAudio, fmtAAC)
		setU32(out, mtBits, 16)
		setU32(out, mtSampleRate, uint32(rate))
		setU32(out, mtChannels, uint32(ch))
		setU32(out, mtBytesPerSec, 24000) // 192 kbit/s
		err := hr(comCall(m.w, 3, out, uintptr(unsafe.Pointer(&m.as))), "AAC encoder")
		comRelease(out)
		if err != nil {
			return fail(err)
		}
		in := newType(mediaAudio, fmtPCM)
		setU32(in, mtBits, 16)
		setU32(in, mtSampleRate, uint32(rate))
		setU32(in, mtChannels, uint32(ch))
		setU32(in, mtBlockAlign, uint32(ch*2))
		setU32(in, mtBytesPerSec, uint32(rate*ch*2))
		err = hr(comCall(m.w, 4, uintptr(m.as), in, 0), "audio input")
		comRelease(in)
		if err != nil {
			return fail(err)
		}
	}
	if err := hr(comCall(m.w, 5), "start writing"); err != nil {
		return fail(err)
	}
	return m, nil
}

func (m *mfWriter) sample(stream uint32, n int, fill func([]byte), ts, dur int64) error {
	var buf, s uintptr
	if r, _, _ := procMFCreateMemoryBuffer.Call(uintptr(n), uintptr(unsafe.Pointer(&buf))); int32(r) < 0 {
		return hr(r, "memory")
	}
	defer comRelease(buf)
	var p *byte
	comCall(buf, 3, uintptr(unsafe.Pointer(&p)), 0, 0)
	fill(unsafe.Slice(p, n))
	comCall(buf, 4)
	comCall(buf, 6, uintptr(n))
	procMFCreateSample.Call(uintptr(unsafe.Pointer(&s)))
	defer comRelease(s)
	comCall(s, 42, buf)
	comCall(s, 36, uintptr(ts))
	comCall(s, 38, uintptr(dur))
	return hr(comCall(m.w, 6, uintptr(stream), s), "encode")
}

// frame writes one picture (already width x height, even sides) as NV12 at ts for dur (100 ns
// units): what the H.264 encoder takes, so Windows needn't convert. BT.709 for HD, BT.601 below.
func (m *mfWriter) frame(img *image.RGBA, ts, dur int64) error {
	w, h := m.width, m.height
	c := [9]int{66, 129, 25, -38, -74, 112, 112, -94, -18}
	if h >= 720 {
		c = [9]int{47, 157, 16, -26, -87, 112, 112, -102, -10}
	}
	return m.sample(m.vs, w*h*3/2, func(b []byte) {
		px, st := img.Pix, img.Stride
		rows(h, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				row, out := px[y*st:], b[y*w:]
				for x := 0; x < w; x++ {
					p := row[x*4:]
					out[x] = uint8(16 + (c[0]*int(p[0])+c[1]*int(p[1])+c[2]*int(p[2])+128)>>8)
				}
			}
		})
		uv := b[w*h:]
		rows(h/2, func(y0, y1 int) {
			for y := y0 * 2; y < y1*2; y += 2 {
				for x := 0; x < w; x += 2 {
					var r, g, bl int
					for _, o := range [4]int{y*st + x*4, y*st + x*4 + 4, (y+1)*st + x*4, (y+1)*st + x*4 + 4} {
						r, g, bl = r+int(px[o]), g+int(px[o+1]), bl+int(px[o+2])
					}
					i := (y/2)*w + x
					uv[i] = uint8(128 + (c[3]*r+c[4]*g+c[5]*bl+512)>>10)
					uv[i+1] = uint8(128 + (c[6]*r+c[7]*g+c[8]*bl+512)>>10)
				}
			}
		})
	}, ts, dur)
}

// audio writes interleaved samples starting at ts.
func (m *mfWriter) audio(d []int16, ts int64) error {
	dur := int64(len(d)/m.ch) * 1e7 / int64(m.rate)
	return m.sample(m.as, len(d)*2, func(b []byte) {
		for i, v := range d {
			b[2*i], b[2*i+1] = byte(v), byte(uint16(v)>>8)
		}
	}, ts, dur)
}

func (m *mfWriter) finish() error {
	defer comRelease(m.w)
	return hr(comCall(m.w, 11), "finish")
}

// aacPCM: sound the AAC encoder takes (44.1/48 kHz, mono or stereo).
func aacPCM(a *pcm) *pcm {
	if a.channels > 2 {
		a = downmix(a)
	}
	if a.rate != 44100 && a.rate != 48000 {
		a = resample(a, 48000)
	}
	return a
}

// writeMP4: frames (already retimed to fps) into an H.264 MP4 of at most maxSide, with sound
// when given. Pictures of another size than the first are letterboxed.
func writeMP4(p string, src frameFn, fps, maxSide int, sound *pcm) error {
	done, err := mfThread()
	if err != nil {
		return err
	}
	defer done()
	var w *mfWriter
	var canvas *image.RGBA
	n := int64(0)
	frame := 1e7 / int64(fps)
	pos := 0 // samples written
	err = retime(src, fps, true, func(img image.Image, _ int) error {
		if w == nil {
			b := fitSize(img.Bounds().Size(), maxSide)
			b.X, b.Y = max(2, b.X&^1), max(2, b.Y&^1) // H.264 wants even sides
			rate, ch := 0, 0
			if sound != nil {
				sound = aacPCM(sound)
				rate, ch = sound.rate, sound.channels
			}
			if w, err = newWriter(p, b.X, b.Y, fps, rate, ch); err != nil {
				return err
			}
			canvas = image.NewRGBA(image.Rectangle{Max: b})
		}
		letterbox(canvas, img)
		if err := w.frame(canvas, n*frame, frame); err != nil {
			return err
		}
		n++
		if sound != nil { // sound up to the end of this frame, so the two stay together
			end := min(len(sound.data), int(n*frame*int64(sound.rate)/1e7)*sound.channels)
			for pos < end {
				k := min(end, pos+sound.rate/10*sound.channels)
				if err := w.audio(sound.data[pos:k], int64(pos/sound.channels)*1e7/int64(sound.rate)); err != nil {
					return err
				}
				pos = k
			}
		}
		return nil
	})
	if w == nil {
		if err == nil {
			err = errors.New("no pictures in it")
		}
		return err
	}
	if ferr := w.finish(); err == nil {
		err = ferr
	}
	return err
}

// writeM4A: sound as AAC in an MP4 audio file.
func writeM4A(p string, a *pcm) error {
	done, err := mfThread()
	if err != nil {
		return err
	}
	defer done()
	a = aacPCM(a)
	w, err := newWriter(p, 0, 0, 0, a.rate, a.channels)
	if err != nil {
		return err
	}
	step := a.rate / 10 * a.channels
	for i := 0; i < len(a.data); i += step {
		if err := w.audio(a.data[i:min(len(a.data), i+step)], int64(i/a.channels)*1e7/int64(a.rate)); err != nil {
			w.finish()
			return err
		}
	}
	return w.finish()
}
