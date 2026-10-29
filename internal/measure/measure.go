// Package measure compares an explicitly recorded signal to a reference.
package measure

import (
	"errors"
	"math"
	"math/cmplx"

	"github.com/khenrix/home-sound/internal/audio"
)

// Report contains heuristic estimates only; it is not an acoustic acceptance or calibration.
type Report struct {
	ReferenceRate                  int     `json:"reference_rate_hz"`
	RecordingRate                  int     `json:"recording_rate_hz"`
	LagSamples                     int     `json:"lag_samples"`
	LagMS                          float64 `json:"lag_ms"`
	RelativeDB                     float64 `json:"relative_level_db"`
	NoiseDBFS                      float64 `json:"residual_noise_dbfs"`
	PeakUncertaintyHeuristicMS     float64 `json:"peak_uncertainty_heuristic_ms"`
	SNRDB                          float64 `json:"snr_db"`
	CorrelationConfidenceHeuristic float64 `json:"correlation_confidence_heuristic"`
	DriftPPM                       float64 `json:"drift_ppm"`
	ClippedFraction                float64 `json:"clipped_fraction"`
	AbsoluteSPL                    string  `json:"absolute_spl"`
	Quality                        string  `json:"quality"`
	CalibrationEligibility         string  `json:"calibration_eligibility"`
}

func Analyze(rf audio.Format, ref []float32, cf audio.Format, rec []float32) (Report, error) {
	var out Report
	if rf.Rate != cf.Rate || rf.Rate != 48000 || rf.Channels != 2 || cf.Channels != 2 || rf.SampleFormat != audio.Float32 || cf.SampleFormat != audio.Float32 || len(ref)%2 != 0 || len(rec)%2 != 0 {
		return out, errors.New("analysis requires matching 48 kHz stereo audio")
	}
	if len(ref) < 48000 || len(rec) < len(ref) {
		return out, errors.New("reference must be at least 0.5 seconds and fit within recording")
	}
	a := make([]float64, len(ref)/2)
	b := make([]float64, len(rec)/2)
	clipped := 0
	for i := range a {
		x, y := float64(ref[2*i]), float64(ref[2*i+1])
		if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
			return out, errors.New("nonfinite reference")
		}
		a[i] = (x + y) / 2
	}
	for i := range b {
		x, y := float64(rec[2*i]), float64(rec[2*i+1])
		if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
			return out, errors.New("nonfinite recording")
		}
		if math.Abs(x) >= 0.999 || math.Abs(y) >= 0.999 {
			clipped++
		}
		b[i] = (x + y) / 2
	}
	out.ClippedFraction = float64(clipped) / float64(len(b))
	if out.ClippedFraction > 0.001 {
		return out, errors.New("recording clipped")
	}
	window := 12000
	if len(a) < window*2 {
		window = len(a) / 2
	}
	first, err := match(a[:window], b)
	if err != nil {
		return out, err
	}
	last, err := match(a[len(a)-window:], b)
	if err != nil {
		return out, err
	}
	lag1 := first.pos
	lag2 := last.pos - (len(a) - window)
	drift := float64(lag2-lag1) / float64(len(a)-window) * 1e6
	if math.Abs(drift) > 200 {
		return out, errors.New("capture clock drift exceeds 200 ppm")
	}
	lag := (lag1 + lag2) / 2
	if lag < 0 || lag+len(a) > len(b) {
		return out, errors.New("incomplete reference in recording")
	}
	var rr, yy, ry float64
	for i, x := range a {
		y := b[lag+i]
		rr += x * x
		yy += y * y
		ry += x * y
	}
	if rr < 1e-8 || yy < 1e-8 || ry <= 0 {
		return out, errors.New("silent or inverted signal")
	}
	gain := ry / rr
	residual := math.Max(yy-ry*ry/rr, 1e-20)
	snr := 10 * math.Log10((ry*ry/rr)/residual)
	if snr < 10 {
		return out, errors.New("inadequate signal to noise ratio")
	}
	out.ReferenceRate = rf.Rate
	out.RecordingRate = cf.Rate
	out.LagSamples = lag
	out.LagMS = float64(lag) * 1000 / float64(rf.Rate)
	out.RelativeDB = 20 * math.Log10(gain)
	out.SNRDB = snr
	out.NoiseDBFS = 10 * math.Log10(residual/float64(len(a)))
	out.PeakUncertaintyHeuristicMS = float64(max(first.radius, last.radius)) * 1000 / float64(rf.Rate)
	out.CorrelationConfidenceHeuristic = math.Min(first.score, last.score)
	out.DriftPPM = drift
	out.AbsoluteSPL = "unverified"
	out.Quality = "heuristic"
	out.CalibrationEligibility = "not_acoustically_verified"
	return out, nil
}

type peak struct {
	pos    int
	score  float64
	radius int
}

func match(pattern, data []float64) (peak, error) {
	const block = 32768
	n := 1
	for n < block+2*len(pattern)-2 {
		n *= 2
	}
	kernel := make([]complex128, n)
	work := make([]complex128, n)
	var power float64
	for i, v := range pattern {
		kernel[len(pattern)-1-i] = complex(v, 0)
		power += v * v
	}
	if power < 1e-8 {
		return peak{}, errors.New("silent reference")
	}
	fft(kernel, false)
	count := len(data) - len(pattern) + 1
	scores := make([]float32, count)
	best := peak{pos: -1}
	for start := 0; start < count; start += block {
		positions := min(block, count-start)
		input := positions + len(pattern) - 1
		clear(work)
		for i := 0; i < input; i++ {
			work[i] = complex(data[start+i], 0)
		}
		fft(work, false)
		for i := range work {
			work[i] *= kernel[i]
		}
		fft(work, true)
		var sum float64
		for _, v := range data[start : start+len(pattern)] {
			sum += v * v
		}
		for i := 0; i < positions; i++ {
			if i > 0 {
				old := data[start+i-1]
				next := data[start+i+len(pattern)-1]
				sum += next*next - old*old
			}
			if sum > 1e-8 {
				scores[start+i] = float32(real(work[i+len(pattern)-1]) / math.Sqrt(power*sum))
			}
			if float64(scores[start+i]) > best.score {
				best = peak{pos: start + i, score: float64(scores[start+i])}
			}
		}
	}
	if best.pos < 0 || best.score < 0.9 {
		return peak{}, errors.New("weak correlation")
	}
	for i, v := range scores {
		if float64(v) > best.score*0.9 {
			distance := abs(i - best.pos)
			if distance > 96 {
				return peak{}, errors.New("ambiguous correlation peaks")
			}
			best.radius = max(best.radius, distance)
		}
	}
	return best, nil
}
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
func fft(a []complex128, inverse bool) {
	n := len(a)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	for length := 2; length <= n; length *= 2 {
		angle := -2 * math.Pi / float64(length)
		if inverse {
			angle = -angle
		}
		step := cmplx.Exp(complex(0, angle))
		for i := 0; i < n; i += length {
			w := complex(1, 0)
			for j := 0; j < length/2; j++ {
				u, v := a[i+j], a[i+j+length/2]*w
				a[i+j] = u + v
				a[i+j+length/2] = u - v
				w *= step
			}
		}
	}
	if inverse {
		for i := range a {
			a[i] /= complex(float64(n), 0)
		}
	}
}
