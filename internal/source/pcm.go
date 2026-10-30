package source

import (
	"context"
	"encoding/binary"
	"errors"
	"github.com/khenrix/home-sound/internal/airplay"
	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/resample"
	"io"
	"math"
	"strings"
	"sync"
)

type PCMReader interface {
	Read(context.Context, []byte) (int, error)
	Close() error
}
type PCM struct {
	reader    PCMReader
	rate      int
	metadata  airplay.Metadata
	converter *resample.Converter
	pending   []float32
	sample    uint64
	ended     bool
	once      sync.Once
	closeErr  error
}

func NewPCM(r PCMReader, rate int, m airplay.Metadata) (*PCM, error) {
	if r == nil || rate != 44100 && rate != 48000 {
		return nil, errors.New("invalid PCM source format")
	}
	for _, s := range []string{m.Title, m.Artist, m.Album} {
		if len(s) > 1024 || strings.ContainsAny(s, "\r\n\x00") {
			return nil, errors.New("invalid source metadata")
		}
	}
	p := &PCM{reader: r, rate: rate, metadata: m}
	if rate == 44100 {
		p.converter, _ = resample.New(44100, 48000, 2)
	}
	return p, nil
}
func (p *PCM) Format() audio.Format       { return audio.Canonical }
func (p *PCM) Metadata() airplay.Metadata { return p.metadata }

// Reserve one second of the engine's total send-ahead for the controlled helper,
// pipe/socket, codec frame, converter delay and at most one queued input frame.
func (p *PCM) BufferedSamples() uint64 { return 48000 }
func (p *PCM) Read(ctx context.Context) (audio.Frame, error) {
	if e := ctx.Err(); e != nil {
		return audio.Frame{}, e
	}
	for len(p.pending) == 0 && !p.ended {
		b := make([]byte, (p.rate/50)*8)
		n, empty := 0, 0
		for n < len(b) {
			got, e := p.reader.Read(ctx, b[n:])
			if got < 0 || got > len(b)-n {
				return audio.Frame{}, errors.New("invalid PCM reader count")
			}
			n += got
			if e != nil {
				if e != io.EOF {
					return audio.Frame{}, e
				}
				p.ended = true
				break
			}
			if got == 0 {
				empty++
				if empty > 3 {
					return audio.Frame{}, io.ErrNoProgress
				}
			}
		}
		if n%8 != 0 {
			return audio.Frame{}, errors.New("partial float/stereo PCM frame")
		}
		raw := make([]float32, n/4)
		for i := range raw {
			v := math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return audio.Frame{}, errors.New("nonfinite PCM sample")
			}
			raw[i] = v
		}
		if p.converter == nil {
			p.pending = raw
		} else {
			var e error
			p.pending, e = p.converter.Process(raw)
			if e != nil {
				return audio.Frame{}, e
			}
			if p.ended {
				tail, e := p.converter.Finish()
				if e != nil {
					return audio.Frame{}, e
				}
				p.pending = append(p.pending, tail...)
			}
		}
	}
	if len(p.pending) == 0 {
		return audio.Frame{}, io.EOF
	}
	n := min(1920, len(p.pending))
	f := audio.Frame{Sample: p.sample, Data: append([]float32(nil), p.pending[:n]...)}
	p.pending = p.pending[n:]
	p.sample += uint64(n / 2)
	return f, nil
}
func (p *PCM) Close() error { p.once.Do(func() { p.closeErr = p.reader.Close() }); return p.closeErr }
