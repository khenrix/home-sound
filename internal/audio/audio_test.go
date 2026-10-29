package audio

import (
	"math"
	"testing"
	"time"
)

func TestPCM16Quantization(t *testing.T) {
	for _, tc := range []struct {
		in   float32
		want int16
	}{{-2, -32768}, {-1, -32768}, {-0.5, -16384}, {0, 0}, {0.5, 16384}, {0.999999, 32767}, {1, 32767}, {2, 32767}} {
		got, err := PCM16(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("PCM16(%v) = %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []float32{float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1))} {
		if _, err := PCM16(in); err == nil {
			t.Errorf("PCM16(%v) accepted nonfinite sample", in)
		}
	}
}

func TestFrameValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format Format
		data   []float32
		valid  bool
	}{
		{"canonical", Canonical, []float32{0, 0}, true}, {"partial frame", Canonical, []float32{0}, false},
		{"nonfinite", Canonical, []float32{0, float32(math.NaN())}, false},
		{"wrong rate", Format{44100, 2, Float32}, []float32{0, 0}, false},
		{"wrong channels", Format{48000, 1, Float32}, []float32{0}, false},
		{"wrong sample format", Format{48000, 2, "other"}, []float32{0, 0}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := (Frame{Data: tc.data}).Validate(tc.format)
			if (err == nil) != tc.valid {
				t.Fatalf("validation: %v", err)
			}
		})
	}
}

func TestSampleTimeUsesAbsoluteSampleCount(t *testing.T) {
	for _, tc := range []struct {
		sample uint64
		want   time.Duration
	}{{0, 0}, {1, 20833}, {3, 62500}, {48000, time.Second}, {48001, time.Second + 20833}} {
		got, err := SampleTime(tc.sample, 48000)
		if err != nil || got != tc.want {
			t.Errorf("SampleTime(%d) = %v, %v; want %v", tc.sample, got, err, tc.want)
		}
	}
	if _, err := SampleTime(1, 0); err == nil {
		t.Error("zero rate accepted")
	}
	if _, err := SampleTime(math.MaxUint64, 1); err == nil {
		t.Error("overflow accepted")
	}
}
