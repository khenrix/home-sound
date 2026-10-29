package session

import (
	"context"
	"errors"
	"io"

	"github.com/khenrix/home-sound/internal/airplay"
	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/wave"
)

type wavSource struct {
	samples []float32
	next    uint64
}

// WAV is the bounded reference input: existing wave.Read limits files to 64 MiB.
// It has no network identity, resampling, or claims about future receiver inputs.
func WAV(r io.Reader) (Source, error) {
	format, data, err := wave.Read(r)
	if err != nil {
		return nil, err
	}
	if format != audio.Canonical || len(data) == 0 {
		return nil, errors.New("nonempty canonical WAV required")
	}
	return &wavSource{samples: data}, nil
}
func WAVAt(r io.Reader, first uint64) (Source, error) {
	src, err := WAV(r)
	if err != nil {
		return nil, err
	}
	s := src.(*wavSource)
	if first >= uint64(len(s.samples)/2) {
		return nil, errors.New("seek past WAV end")
	}
	s.next = first
	return s, nil
}
func (s *wavSource) Format() audio.Format       { return audio.Canonical }
func (s *wavSource) Metadata() airplay.Metadata { return airplay.Metadata{Title: "Reference WAV"} }
func (s *wavSource) Read(ctx context.Context) (audio.Frame, error) {
	if err := ctx.Err(); err != nil {
		return audio.Frame{}, err
	}
	if s.next >= uint64(len(s.samples)/2) {
		return audio.Frame{}, io.EOF
	}
	end := min(s.next+chunkFrames, uint64(len(s.samples)/2))
	f := audio.Frame{Sample: s.next, Data: s.samples[s.next*2 : end*2]}
	s.next = end
	return f, nil
}
func (s *wavSource) Close() error { s.samples = nil; return nil }
