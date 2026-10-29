package measure

import (
	"math"
	"testing"

	"github.com/khenrix/home-sound/internal/audio"
)

func reference(n int) []float32 {
	x := uint32(1)
	s := make([]float32, n*2)
	for i := 0; i < n; i++ {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		v := float32(int32(x)) / 2147483648 * 0.2
		s[2*i] = v
		s[2*i+1] = v
	}
	return s
}
func TestShiftAndRelativeLevel(t *testing.T) {
	ref := reference(48000)
	rec := make([]float32, len(ref)+2400)
	for i, v := range ref {
		rec[i+2400] = v * 0.5
	}
	got, err := Analyze(audio.Canonical, ref, audio.Canonical, rec)
	if err != nil {
		t.Fatal(err)
	}
	if got.LagSamples != 1200 || math.Abs(got.RelativeDB+6.0206) > 0.05 {
		t.Fatalf("unexpected report: %+v", got)
	}
}
func TestRejectUnusableInputs(t *testing.T) {
	ref := reference(48000)
	tone := make([]float32, len(ref))
	for i := 0; i < len(tone)/2; i++ {
		v := float32(0.2 * math.Sin(2*math.Pi*1000*float64(i)/48000))
		tone[2*i] = v
		tone[2*i+1] = v
	}
	clipped := append([]float32(nil), ref...)
	for i := range clipped {
		clipped[i] = 1
	}
	for name, pair := range map[string][2][]float32{"silence": {ref, make([]float32, len(ref))}, "noise": {ref, referenceDifferent(48000)}, "tone": {tone, tone}, "clipping": {ref, clipped}} {
		t.Run(name, func(t *testing.T) {
			if _, err := Analyze(audio.Canonical, pair[0], audio.Canonical, pair[1]); err == nil {
				t.Fatal("accepted unusable input")
			}
		})
	}
}
func referenceDifferent(n int) []float32 { s := reference(n + 123); return s[246:] }
func TestRejectClockDrift(t *testing.T) {
	ref := reference(96000)
	rec := make([]float32, len(ref)+1000)
	for i := 0; i < 96000; i++ {
		j := i + 300 + i/1000
		rec[j*2] = ref[i*2]
		rec[j*2+1] = ref[i*2+1]
	}
	if _, err := Analyze(audio.Canonical, ref, audio.Canonical, rec); err == nil {
		t.Fatal("accepted drifting recording")
	}
}

func TestReportsResidualNoiseAndTimingUncertainty(t *testing.T) {
	ref := reference(48000)
	rec := make([]float32, len(ref)+960)
	noise := referenceDifferent(48000)
	for i, v := range ref {
		rec[i+960] = v*0.5 + noise[i]*0.01
	}
	got, err := Analyze(audio.Canonical, ref, audio.Canonical, rec)
	if err != nil {
		t.Fatal(err)
	}
	if got.NoiseDBFS > -45 || got.NoiseDBFS < -70 || got.UncertaintyMS > 2 {
		t.Fatalf("unexpected noise or uncertainty: %+v", got)
	}
}
