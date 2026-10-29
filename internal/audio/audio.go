// Package audio defines the canonical in-memory audio representation.
package audio

import (
	"errors"
	"math"
	"time"
)

type SampleFormat string

const Float32 SampleFormat = "float32"

type Format struct {
	Rate         int
	Channels     int
	SampleFormat SampleFormat
}

var Canonical = Format{48000, 2, Float32}

// Frame.Data is interleaved and must remain immutable while consumed.
type Frame struct {
	Epoch  uint64
	Sample uint64
	Data   []float32
}

func (f Frame) Validate(format Format) error {
	if format != Canonical {
		return errors.New("audio must be 48000 Hz stereo float32")
	}
	if len(f.Data)%format.Channels != 0 {
		return errors.New("unaligned frame")
	}
	for _, v := range f.Data {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return errors.New("nonfinite sample")
		}
	}
	return nil
}
func PCM16(v float32) (int16, error) {
	if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
		return 0, errors.New("nonfinite sample")
	}
	if v <= -1 {
		return -32768, nil
	}
	if v >= 1 {
		return 32767, nil
	}
	q := math.Round(float64(v) * 32768)
	if q > 32767 {
		q = 32767
	}
	return int16(q), nil
}

// SampleTime derives an offset from an absolute sample position, truncating to nanoseconds.
func SampleTime(sample uint64, rate int) (time.Duration, error) {
	if rate <= 0 {
		return 0, errors.New("invalid sample rate")
	}
	seconds, remainder := sample/uint64(rate), sample%uint64(rate)
	if seconds > uint64(math.MaxInt64)/uint64(time.Second) {
		return 0, errors.New("timestamp overflow")
	}
	// Bound the multiplication used for the fractional second.
	if uint64(rate) > math.MaxUint64/uint64(time.Second) {
		return 0, errors.New("sample rate too large")
	}
	ns := seconds*uint64(time.Second) + (remainder*uint64(time.Second))/uint64(rate)
	if ns > math.MaxInt64 {
		return 0, errors.New("timestamp overflow")
	}
	return time.Duration(ns), nil
}
