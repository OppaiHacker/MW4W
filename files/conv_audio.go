package main

// Audio: decoded to 16-bit PCM, encoded as WAV, FLAC (lossless) or MP3 (shine). Reads WAV, MP3,
// FLAC and Ogg Vorbis. All pure Go.

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"

	mp3enc "github.com/braheezy/shine-mp3/pkg/mp3"
	"github.com/hajimehoshi/go-mp3"
	"github.com/jfreymuth/oggvorbis"
	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"
)

func init() {
	families = append(families, &family{
		name: "audio",
		in:   exts("wav mp3 flac ogg oga m4a aac wma"),
		out: []target{
			{ID: "mp3", Label: "MP3", Icon: "music"},
			{ID: "flac", Label: "FLAC", Hint: "lossless", Icon: "music"},
			{ID: "wav", Label: "WAV", Icon: "music"},
			{ID: "m4a", Label: "M4A", Hint: "AAC", Back: true, Icon: "music"},
			{ID: "mono", Label: "Mono", Hint: "MP3, one channel", Back: true, Icon: "music"},
		},
		multi: []target{
			{ID: "join", Label: "Join", Hint: "one MP3, one after another", Multi: true, Back: true, Icon: "music"},
		},
		convert:  convertAudio,
		convertN: joinAudio,
	})
}

// pcm: interleaved 16-bit samples.
type pcm struct {
	rate, channels int
	data           []int16
}

func decodeAudio(p string) (*pcm, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<16)
	switch convExt(p) {
	case ".mp3":
		d, err := mp3.NewDecoder(r)
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(d)
		if err != nil {
			return nil, err
		}
		out := &pcm{rate: d.SampleRate(), channels: 2, data: make([]int16, len(b)/2)}
		for i := range out.data {
			out.data[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
		}
		return out, nil
	case ".ogg", ".oga":
		s, format, err := oggvorbis.ReadAll(r)
		if err != nil {
			return nil, err
		}
		out := &pcm{rate: format.SampleRate, channels: format.Channels, data: make([]int16, len(s))}
		for i, v := range s {
			out.data[i] = int16(math.Max(-32768, math.Min(32767, float64(v)*32767)))
		}
		return out, nil
	case ".flac":
		st, err := flac.New(r)
		if err != nil {
			return nil, err
		}
		out := &pcm{rate: int(st.Info.SampleRate), channels: int(st.Info.NChannels)}
		shift := int(st.Info.BitsPerSample) - 16
		for {
			fr, err := st.ParseNext()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			for i := 0; i < int(fr.BlockSize); i++ {
				for _, sf := range fr.Subframes {
					v := sf.Samples[i]
					if shift > 0 {
						v >>= shift
					} else if shift < 0 {
						v <<= -shift
					}
					out.data = append(out.data, int16(v))
				}
			}
		}
		return out, nil
	case ".wav":
		return decodeWAV(r)
	}
	return mfAudio(p) // M4A, AAC, WMA: Windows' decoders
}

// decodeWAV: PCM 8/16/24/32-bit and 32-bit float.
func decodeWAV(r io.Reader) (*pcm, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil || string(hdr[:4]) != "RIFF" || string(hdr[8:]) != "WAVE" {
		return nil, errors.New("not a WAV file")
	}
	var format, channels, bits uint16
	var rate uint32
	for {
		var ch [8]byte
		if _, err := io.ReadFull(r, ch[:]); err != nil {
			return nil, errors.New("no audio data in the WAV file")
		}
		id, n := string(ch[:4]), binary.LittleEndian.Uint32(ch[4:])
		switch id {
		case "fmt ":
			b := make([]byte, n)
			if _, err := io.ReadFull(r, b); err != nil || n < 16 {
				return nil, errors.New("broken WAV header")
			}
			format, channels, rate, bits = binary.LittleEndian.Uint16(b), binary.LittleEndian.Uint16(b[2:]), binary.LittleEndian.Uint32(b[4:]), binary.LittleEndian.Uint16(b[14:])
			if format == 0xFFFE && n >= 26 { // WAVE_FORMAT_EXTENSIBLE: the sub-format's first two bytes
				format = binary.LittleEndian.Uint16(b[24:])
			}
		case "data":
			if channels == 0 {
				return nil, errors.New("broken WAV header")
			}
			b, err := io.ReadAll(io.LimitReader(r, int64(n)))
			if err != nil {
				return nil, err
			}
			out := &pcm{rate: int(rate), channels: int(channels)}
			switch {
			case format == 1 && bits == 16:
				out.data = make([]int16, len(b)/2)
				for i := range out.data {
					out.data[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
				}
			case format == 1 && bits == 8:
				out.data = make([]int16, len(b))
				for i, v := range b {
					out.data[i] = (int16(v) - 128) << 8
				}
			case format == 1 && bits == 24:
				out.data = make([]int16, len(b)/3)
				for i := range out.data {
					out.data[i] = int16(uint16(b[3*i+1]) | uint16(b[3*i+2])<<8)
				}
			case format == 1 && bits == 32:
				out.data = make([]int16, len(b)/4)
				for i := range out.data {
					out.data[i] = int16(int32(binary.LittleEndian.Uint32(b[4*i:])) >> 16)
				}
			case format == 3 && bits == 32:
				out.data = make([]int16, len(b)/4)
				for i := range out.data {
					v := math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
					out.data[i] = int16(math.Max(-32768, math.Min(32767, float64(v)*32767)))
				}
			default:
				return nil, fmt.Errorf("WAV format %d, %d-bit isn't read", format, bits)
			}
			return out, nil
		default:
			if _, err := io.CopyN(io.Discard, r, int64(n+n&1)); err != nil {
				return nil, err
			}
		}
	}
}

func encodeWAV(w io.Writer, a *pcm) error {
	n := uint32(len(a.data) * 2)
	h := make([]byte, 44)
	copy(h, "RIFF")
	binary.LittleEndian.PutUint32(h[4:], 36+n)
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], uint16(a.channels))
	binary.LittleEndian.PutUint32(h[24:], uint32(a.rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(a.rate*a.channels*2))
	binary.LittleEndian.PutUint16(h[32:], uint16(a.channels*2))
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], n)
	if _, err := w.Write(h); err != nil {
		return err
	}
	b := make([]byte, len(a.data)*2)
	for i, v := range a.data {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	_, err := w.Write(b)
	return err
}

func encodeFLAC(w io.Writer, a *pcm) error {
	const block = 4096
	frames := len(a.data) / a.channels
	info := &meta.StreamInfo{BlockSizeMin: block, BlockSizeMax: block, SampleRate: uint32(a.rate), NChannels: uint8(a.channels), BitsPerSample: 16, NSamples: uint64(frames)}
	enc, err := flac.NewEncoder(w, info)
	if err != nil {
		return err
	}
	chans := frame.ChannelsLR
	if a.channels == 1 {
		chans = frame.ChannelsMono
	} else if a.channels != 2 {
		return errors.New("FLAC: only mono and stereo")
	}
	for off := 0; off < frames; off += block {
		n := min(block, frames-off)
		f := &frame.Frame{Header: frame.Header{HasFixedBlockSize: true, BlockSize: uint16(n), SampleRate: uint32(a.rate), Channels: chans, BitsPerSample: 16}}
		for c := 0; c < a.channels; c++ {
			s := make([]int32, n)
			for i := range s {
				s[i] = int32(a.data[(off+i)*a.channels+c])
			}
			f.Subframes = append(f.Subframes, &frame.Subframe{SubHeader: frame.SubHeader{Pred: frame.PredVerbatim}, Samples: s, NSamples: n})
		}
		if err := enc.WriteFrame(f); err != nil {
			return err
		}
	}
	return enc.Close()
}

// encodeMP3: shine takes 44.1/48/32 kHz (and the MPEG-2 rates); anything else is resampled to 44.1.
func encodeMP3(w io.Writer, a *pcm) error {
	switch a.rate {
	case 48000, 44100, 32000, 24000, 22050, 16000:
	default:
		a = resample(a, 44100)
	}
	if a.channels > 2 {
		a = downmix(a)
	}
	return mp3enc.NewEncoder(a.rate, a.channels).Write(w, a.data)
}

func resample(a *pcm, rate int) *pcm {
	frames := len(a.data) / a.channels
	n := int(int64(frames) * int64(rate) / int64(a.rate))
	out := &pcm{rate: rate, channels: a.channels, data: make([]int16, n*a.channels)}
	for i := 0; i < n; i++ {
		pos := float64(i) * float64(a.rate) / float64(rate)
		j, fr := int(pos), pos-math.Floor(pos)
		for c := 0; c < a.channels; c++ {
			s0 := float64(a.data[min(j, frames-1)*a.channels+c])
			s1 := float64(a.data[min(j+1, frames-1)*a.channels+c])
			out.data[i*a.channels+c] = int16(s0 + (s1-s0)*fr)
		}
	}
	return out
}

func downmix(a *pcm) *pcm {
	frames := len(a.data) / a.channels
	out := &pcm{rate: a.rate, channels: 1, data: make([]int16, frames)}
	for i := 0; i < frames; i++ {
		s := 0
		for c := 0; c < a.channels; c++ {
			s += int(a.data[i*a.channels+c])
		}
		out.data[i] = int16(s / a.channels)
	}
	return out
}

// writeSound: writeAudio, and M4A (written by Media Foundation itself).
func writeSound(p string, a *pcm, format string) error {
	if format != "m4a" {
		return writeAudio(p, a, format)
	}
	err := writeM4A(p, a)
	if err != nil {
		os.Remove(p)
	}
	return err
}

func writeAudio(p string, a *pcm, format string) error {
	return writeOut(p, func(f *os.File) error {
		w := bufio.NewWriterSize(f, 1<<16)
		var err error
		switch format {
		case "wav":
			err = encodeWAV(w, a)
		case "flac":
			err = encodeFLAC(w, a)
		case "mp3":
			err = encodeMP3(w, a)
		}
		if err != nil {
			return err
		}
		return w.Flush()
	})
}

func convertAudio(ctx context.Context, src string, t target, dir string) ([]string, error) {
	a, err := decodeAudio(src)
	if err != nil {
		return nil, err
	}
	if len(a.data) == 0 {
		return nil, errors.New("no sound in it")
	}
	format, name := t.ID, stem(src)
	if t.ID == "mono" {
		a, format, name = downmix(a), "mp3", name+" (mono)"
	}
	p := outPath(dir, name, "."+format)
	return []string{p}, writeSound(p, a, format)
}

func joinAudio(ctx context.Context, srcs []string, t target, dir string) ([]string, error) {
	var all *pcm
	for _, s := range srcs {
		a, err := decodeAudio(s)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", s, err)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if a.channels > 2 {
			a = downmix(a)
		}
		if a.channels == 1 { // everything stereo
			st := &pcm{rate: a.rate, channels: 2, data: make([]int16, len(a.data)*2)}
			for i, v := range a.data {
				st.data[2*i], st.data[2*i+1] = v, v
			}
			a = st
		}
		if all == nil {
			all = &pcm{rate: a.rate, channels: a.channels}
		} else if a.rate != all.rate {
			a = resample(a, all.rate)
		}
		all.data = append(all.data, a.data...)
	}
	p := outPath(dir, stem(srcs[0])+" (joined)", ".mp3")
	return []string{p}, writeAudio(p, all, "mp3")
}
