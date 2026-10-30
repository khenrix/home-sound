// Package resample provides a bounded stereo rational FIR converter.
package resample

import (
	"errors"
	"math"
)

const radius = 128
const maxFrames = 48000

// Converter keeps integer phase across chunks. The symmetric 256-tap Blackman
// FIR needs 128 future input samples (2.67ms at 48k, 2.90ms at 44.1k). Output
// sample zero represents input time zero: buffering adds no content-time shift.
// Boundaries are zero extended. Finish emits ceil(inputFrames*outRate/inRate)
// frames in total, cropping the FIR tail to the input duration (within one
// output sample). A Converter belongs to one stream and is not concurrent.
type Converter struct {
	in, out, channels, step, den int
	kernels                      [][]float64
	history                      []float32
	base, total, pos             uint64
	phase                        int
	emitted                      uint64
	ended                        bool
}

func New(in, out, channels int) (*Converter, error) {
	if (in != 44100 && in != 48000) || (out != 44100 && out != 48000) || channels < 1 || channels > 2 {
		return nil, errors.New("unsupported resampler format")
	}
	g := in
	b := out
	for b != 0 {
		g, b = b, g%b
	}
	r := &Converter{in: in, out: out, channels: channels, step: in / g, den: out / g}
	r.kernels = make([][]float64, r.den)
	cutoff := .46 * math.Min(1, float64(out)/float64(in))
	for phase := range r.kernels {
		k := make([]float64, 2*radius)
		var sum float64
		for i := range k {
			x := float64(i-radius+1) - float64(phase)/float64(r.den)
			sinc := 2 * cutoff
			if x != 0 {
				sinc = math.Sin(2*math.Pi*cutoff*x) / (math.Pi * x)
			}
			w := .42 + .5*math.Cos(math.Pi*x/radius) + .08*math.Cos(2*math.Pi*x/radius)
			k[i] = sinc * w
			sum += k[i]
		}
		for i := range k {
			k[i] /= sum
		}
		r.kernels[phase] = k
	}
	return r, nil
}

func (r *Converter) Process(data []float32) ([]float32, error) {
	if r.ended || len(data)%r.channels != 0 || len(data)/r.channels > maxFrames {
		return nil, errors.New("invalid resampler input")
	}
	for _, v := range data {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, errors.New("nonfinite sample")
		}
	}
	frames := uint64(len(data) / r.channels)
	if frames > math.MaxUint64-r.total || r.total+frames > math.MaxInt64/48000 {
		return nil, errors.New("resampler position overflow")
	}
	r.history = append(r.history, data...)
	r.total += frames
	return r.emit(false)
}
func (r *Converter) Finish() ([]float32, error) {
	if r.ended {
		return nil, nil
	}
	r.ended = true
	result, err := r.emit(true)
	r.history = nil
	return result, err
}
func (r *Converter) Reset() {
	r.history = nil
	r.base = 0
	r.total = 0
	r.pos = 0
	r.phase = 0
	r.emitted = 0
	r.ended = false
}

func (r *Converter) emit(eof bool) ([]float32, error) {
	target := (r.total*uint64(r.out) + uint64(r.in) - 1) / uint64(r.in)
	var output []float32
	for r.emitted < target && (eof || r.pos+radius < r.total) {
		k := r.kernels[r.phase]
		for channel := 0; channel < r.channels; channel++ {
			var sum float64
			for j, w := range k {
				index := int64(r.pos) + int64(j-radius+1)
				if index >= 0 && uint64(index) >= r.base && uint64(index) < r.total {
					sum += w * float64(r.history[(uint64(index)-r.base)*uint64(r.channels)+uint64(channel)])
				}
			}
			if math.Abs(sum) > math.MaxFloat32 {
				r.ended = true
				return nil, errors.New("resampler output overflow; reset required")
			}
			output = append(output, float32(sum))
		}
		r.emitted++
		r.phase += r.step
		r.pos += uint64(r.phase / r.den)
		r.phase %= r.den
	}
	if r.pos > radius {
		keep := r.pos - radius
		if keep > r.total {
			keep = r.total
		}
		drop := (keep - r.base) * uint64(r.channels)
		copy(r.history, r.history[drop:])
		r.history = r.history[:uint64(len(r.history))-drop]
		r.base = keep
	}
	return output, nil
}
