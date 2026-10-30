// Package volume maps a common relative acoustic level to measured native steps.
package volume

import (
	"errors"
	"math"
)

type Point struct {
	Native int     `json:"native"`
	DB     float64 `json:"db"`
}
type Provenance struct {
	Transport     string `json:"transport,omitempty"`
	Adapter       string `json:"adapter"`
	Format        string `json:"format"`
	Sensor        string `json:"sensor"`
	NativeControl string `json:"native_control"`
	Reference     string `json:"reference"`
}
type Profile struct {
	Version    int        `json:"version"`
	Verified   bool       `json:"verified"`
	Provenance Provenance `json:"provenance"`
	Points     []Point    `json:"points"`
	// NativeMaximum is a declared device limit, not evidence of an acoustic measurement.
	NativeMaximum *int `json:"native_maximum,omitempty"`
}
type Plan struct {
	Native             int     `json:"native"`
	AttenuationDB      float64 `json:"attenuation_db"`
	EffectiveDB        float64 `json:"effective_db"`
	Saturated          bool    `json:"saturated"`
	CalibrationLimited bool    `json:"calibration_limited"`
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func (p Profile) Validate() error {
	if p.Version != 1 {
		return errors.New("unsupported profile version")
	}
	if p.NativeMaximum != nil && (*p.NativeMaximum < 0 || *p.NativeMaximum > 100) {
		return errors.New("invalid native maximum")
	}
	if len(p.Points) == 0 && !p.Verified {
		return nil
	}
	if len(p.Points) < 2 || len(p.Points) > 101 {
		return errors.New("profile needs 2 to 101 measured points")
	}
	for i, v := range p.Points {
		if !finite(v.DB) || v.Native < 0 || v.Native > 100 || p.NativeMaximum != nil && v.Native > *p.NativeMaximum {
			return errors.New("invalid measured point")
		}
		if i > 0 && (v.Native <= p.Points[i-1].Native || v.DB <= p.Points[i-1].DB) {
			return errors.New("curve must strictly increase")
		}
	}
	v := p.Provenance
	if v.Adapter == "" || v.Format == "" || v.Sensor == "" || v.NativeControl == "" || v.Reference == "" {
		return errors.New("measurement provenance required")
	}
	return nil
}
func (p Profile) Map(target, trim float64) (Plan, error) {
	if e := p.Validate(); e != nil {
		return Plan{}, e
	}
	if !p.Verified {
		return Plan{}, errors.New("profile is unverified")
	}
	if !finite(target) || !finite(trim) || !finite(target+trim) {
		return Plan{}, errors.New("nonfinite target or trim")
	}
	wanted := target + trim
	for _, point := range p.Points {
		if point.DB >= wanted {
			if !finite(wanted - point.DB) {
				return Plan{}, errors.New("attenuation exceeds numeric range")
			}
			return Plan{Native: point.Native, AttenuationDB: wanted - point.DB, EffectiveDB: wanted}, nil
		}
	}
	top := p.Points[len(p.Points)-1]
	plan := Plan{Native: top.Native, EffectiveDB: top.DB}
	if p.NativeMaximum != nil && *p.NativeMaximum == top.Native {
		plan.Saturated = true
		return plan, nil
	}
	plan.CalibrationLimited = true
	return plan, errors.New("target exceeds measured calibration range")
}

// Feedback is owned by one session's serial control path. Acknowledgements are
// bounded and consumed once; a repeated observed level is not a new user action.
type Feedback struct {
	profile              Profile
	trim                 float64
	expected             []int
	last, latestExpected int
	seen                 bool
}

func NewFeedback(p Profile, trim float64) *Feedback { return &Feedback{profile: p, trim: trim} }
func (f *Feedback) Expect(n int) {
	f.latestExpected = n
	if len(f.expected) == 16 {
		f.expected = f.expected[1:]
	}
	f.expected = append(f.expected, n)
}
func (f *Feedback) Observe(n int) (float64, bool, error) {
	if !finite(f.trim) {
		return 0, false, errors.New("nonfinite room trim")
	}
	for i, v := range f.expected {
		if v == n {
			f.expected = append(f.expected[:i], f.expected[i+1:]...)
			if n == f.latestExpected {
				f.last = n
				f.seen = true
			}
			return 0, false, nil
		}
	}
	if f.seen && f.last == n {
		return 0, false, nil
	}
	if e := f.profile.Validate(); e != nil {
		return 0, false, e
	}
	if !f.profile.Verified {
		return 0, false, errors.New("profile is unverified")
	}
	for i, v := range f.profile.Points {
		if v.Native == n {
			if !finite(v.DB - f.trim) {
				return 0, false, errors.New("feedback exceeds numeric range")
			}
			f.last = n
			f.seen = true
			return v.DB - f.trim, true, nil
		}
		if i > 0 && n > f.profile.Points[i-1].Native && n < v.Native {
			lower := f.profile.Points[i-1]
			db := lower.DB + float64(n-lower.Native)/float64(v.Native-lower.Native)*(v.DB-lower.DB)
			if !finite(db - f.trim) {
				return 0, false, errors.New("feedback exceeds numeric range")
			}
			f.last = n
			f.seen = true
			return db - f.trim, true, nil
		}
	}
	return 0, false, errors.New("native level has no measured reference point")
}
