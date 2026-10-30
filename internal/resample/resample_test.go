package resample

import (
	"math"
	"reflect"
	"testing"
)

func convert(t *testing.T, in, out int, data []float32, chunks []int) []float32 {
	t.Helper()
	r, err := New(in, out, 2)
	if err != nil {
		t.Fatal(err)
	}
	var result []float32
	for n, k := 0, 0; n < len(data); k++ {
		size := chunks[k%len(chunks)] * 2
		if size > len(data)-n {
			size = len(data) - n
		}
		v, e := r.Process(data[n : n+size])
		if e != nil {
			t.Fatal(e)
		}
		result = append(result, v...)
		n += size
	}
	tail, err := r.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return append(result, tail...)
}
func TestRationalCountsAndChunkInvariant(t *testing.T) {
	for _, rates := range [][2]int{{48000, 44100}, {44100, 48000}} {
		input := make([]float32, 961*2)
		for i := range input {
			input[i] = float32(math.Sin(float64(i) * .17))
		}
		a := convert(t, rates[0], rates[1], input, []int{961})
		b := convert(t, rates[0], rates[1], input, []int{1, 7, 13, 147, 160})
		want := 883
		if rates[0] == 44100 {
			want = 1046
		}
		if len(a) != want*2 {
			t.Fatalf("%v frames=%d want=%d", rates, len(a)/2, want)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatal("chunk boundaries changed waveform")
		}
	}
}
func TestImpulseIsAlignedAndStereoIsolated(t *testing.T) {
	for _, rates := range [][2]int{{48000, 44100}, {44100, 48000}} {
		x := make([]float32, 3200)
		at := 160
		if rates[0] == 44100 {
			at = 147
		}
		x[at*2] = 1
		y := convert(t, rates[0], rates[1], x, []int{19})
		peak := 0
		for i := 0; i < len(y)/2; i++ {
			if math.Abs(float64(y[2*i])) > math.Abs(float64(y[2*peak])) {
				peak = i
			}
			if y[2*i+1] != 0 {
				t.Fatal("channel leak")
			}
		}
		want := 147
		if rates[0] == 44100 {
			want = 160
		}
		if peak != want {
			t.Fatalf("filter shifted content: peak=%d want=%d", peak, want)
		}
	}
}
func TestPassbandAndAliasRejection(t *testing.T) {
	for _, tc := range []struct {
		in, out, hz int
		min, max    float64
	}{{48000, 44100, 18000, .99, 1.01}, {48000, 44100, 23000, 0, .0001}, {44100, 48000, 18000, .99, 1.01}, {44100, 48000, 21800, 0, .001}} {
		x := make([]float32, tc.in*2)
		for i := 0; i < tc.in; i++ {
			x[2*i] = float32(math.Sin(2 * math.Pi * float64(tc.hz*i) / float64(tc.in)))
		}
		y := convert(t, tc.in, tc.out, x, []int{4096})
		var power float64
		n := len(y)/2 - 1024
		for i := 512; i < len(y)/2-512; i++ {
			power += float64(y[i*2]) * float64(y[i*2])
		}
		gain := math.Sqrt(2 * power / float64(n))
		if gain < tc.min || gain > tc.max {
			t.Fatalf("%+v gain=%g", tc, gain)
		}
	}
}
func TestLookaheadEOFResetAndBoundedHistory(t *testing.T) {
	r, err := New(48000, 44100, 2)
	if err != nil {
		t.Fatal(err)
	}
	y, err := r.Process([]float32{1, 0})
	if err != nil || len(y) != 0 {
		t.Fatalf("lookahead: %v %v", y, err)
	}
	y, err = r.Finish()
	if err != nil || len(y) != 2 || y[0] <= 0 {
		t.Fatalf("partial EOF: %v %v", y, err)
	}
	if _, err = r.Process([]float32{0, 0}); err == nil {
		t.Fatal("accepted data after EOF")
	}
	r.Reset()
	a, _ := r.Process(make([]float32, 960*2))
	b, _ := r.Finish()
	if len(a)+len(b) != 882*2 {
		t.Fatal("reset retained phase")
	}
	r.Reset()
	for i := 0; i < 100; i++ {
		if _, err = r.Process(make([]float32, 960*2)); err != nil {
			t.Fatal(err)
		}
		if len(r.history) > 1024 {
			t.Fatal("history grows with stream")
		}
	}
}
func TestRejectsInvalidInputWithoutChangingState(t *testing.T) {
	r, _ := New(48000, 44100, 2)
	for _, x := range [][]float32{{1}, {float32(math.NaN()), 0}, {0, float32(math.Inf(1))}, make([]float32, 48001*2)} {
		if _, err := r.Process(x); err == nil {
			t.Fatal("accepted invalid input")
		}
	}
	a, _ := r.Process(make([]float32, 320))
	b, _ := r.Finish()
	if len(a)+len(b) != 294 {
		t.Fatal("invalid input changed phase")
	}
	for _, rates := range [][3]int{{0, 48000, 2}, {48000, 32000, 2}, {48000, 44100, 0}} {
		if _, err := New(rates[0], rates[1], rates[2]); err == nil {
			t.Fatal("invalid configuration")
		}
	}
}

func TestFiniteInputCannotOverflowOutputSilently(t *testing.T) {
	r, _ := New(48000, 44100, 2)
	data := make([]float32, 1920)
	for i := range data {
		data[i] = math.MaxFloat32
	}
	if _, err := r.Process(data); err == nil {
		t.Fatal("FIR overflow was not rejected")
	}
}
