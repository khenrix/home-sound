package volume

import (
	"math"
	"testing"
)

func fixture() Profile {
	return Profile{Version: 1, Verified: true, Provenance: Provenance{Adapter: "fixture", Format: "48000/stereo/pcm16", Sensor: "fixture", NativeControl: "fixture", Reference: "synthetic-only"}, Points: []Point{{10, -30}, {12, -24}, {15, -18}}, NativeMaximum: ptr(15)}
}
func ptr(n int) *int { return &n }
func TestEqualDBChangesAndReturnFromSaturation(t *testing.T) {
	p := fixture()
	for _, tc := range []struct {
		target, trim           float64
		native                 int
		attenuation, effective float64
		saturated              bool
	}{{-29, 0, 12, -5, -29, false}, {-26, 0, 12, -2, -26, false}, {-15, 0, 15, 0, -18, true}, {-27, 0, 12, -3, -27, false}, {-29, 2, 12, -3, -27, false}} {
		got, err := p.Map(tc.target, tc.trim)
		if err != nil {
			t.Fatal(err)
		}
		if got.Native != tc.native || got.AttenuationDB != tc.attenuation || got.EffectiveDB != tc.effective || got.Saturated != tc.saturated {
			t.Fatalf("target %v: %+v", tc.target, got)
		}
	}
	for target := -40.; target <= -18; target += .25 {
		got, e := p.Map(target, 0)
		if e != nil || math.Abs(got.EffectiveDB-target) > 1e-9 || got.AttenuationDB > 0 {
			t.Fatalf("nonmonotonic target %v: %+v %v", target, got, e)
		}
	}
}
func TestCalibrationLimitIsNotNativeSaturation(t *testing.T) {
	p := fixture()
	p.NativeMaximum = ptr(100)
	got, e := p.Map(-15, 0)
	if e == nil || !got.CalibrationLimited || got.Saturated {
		t.Fatalf("unmeasured upper range: %+v %v", got, e)
	}
	p.Verified = false
	if _, e = p.Map(-25, 0); e == nil {
		t.Fatal("unverified profile used live")
	}
}
func TestRejectMalformedProfiles(t *testing.T) {
	for _, change := range []func(*Profile){func(p *Profile) { p.Version = 2 }, func(p *Profile) { p.Points[1].DB = -31 }, func(p *Profile) { p.Points[1].Native = 10 }, func(p *Profile) { p.Points[0].DB = math.NaN() }, func(p *Profile) { p.Points[0].DB = math.Inf(1) }, func(p *Profile) { p.NativeMaximum = ptr(11) }, func(p *Profile) { p.Provenance.Sensor = "" }, func(p *Profile) { p.Points = nil }} {
		p := fixture()
		change(&p)
		if p.Validate() == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
	p := fixture()
	for _, n := range []float64{math.NaN(), math.Inf(1)} {
		if _, e := p.Map(n, 0); e == nil {
			t.Fatal("nonfinite target accepted")
		}
	}
}
func TestNativeFeedbackSuppressesAcknowledgementsAndDuplicates(t *testing.T) {
	f := NewFeedback(fixture(), 2)
	f.Expect(12)
	if _, changed, e := f.Observe(12); e != nil || changed {
		t.Fatalf("own acknowledgement fed back: %v %v", changed, e)
	}
	if got, changed, e := f.Observe(15); e != nil || !changed || got != -20 {
		t.Fatalf("remote change lost: %v %v %v", got, changed, e)
	}
	if _, changed, _ := f.Observe(15); changed {
		t.Fatal("duplicate generated loop")
	}
	if _, _, e := f.Observe(99); e == nil {
		t.Fatal("unmeasured native feedback accepted")
	}
}

func TestNativeFeedbackInterpolatesWithinMeasuredSpanAndIgnoresOldAcks(t *testing.T) {
	f := NewFeedback(fixture(), 2)
	f.Expect(12)
	f.Expect(15)
	if _, changed, _ := f.Observe(15); changed {
		t.Fatal("new acknowledgement fed back")
	}
	if _, changed, _ := f.Observe(12); changed {
		t.Fatal("old acknowledgement fed back")
	}
	if _, changed, _ := f.Observe(15); changed {
		t.Fatal("old acknowledgement erased current level")
	}
	if target, changed, err := f.Observe(11); err != nil || !changed || target != -29 {
		t.Fatalf("in-range inverse curve: %v %v %v", target, changed, err)
	}
}

func TestFiniteInputsCannotProduceNonfiniteVolumePlan(t *testing.T) {
	p := fixture()
	p.Points = []Point{{10, 1e308}, {12, 1.1e308}, {15, 1.2e308}}
	if _, err := p.Map(-1e308, 0); err == nil {
		t.Fatal("overflowing attenuation accepted")
	}
	f := NewFeedback(fixture(), math.NaN())
	if _, _, err := f.Observe(12); err == nil {
		t.Fatal("nonfinite feedback trim accepted")
	}
}
