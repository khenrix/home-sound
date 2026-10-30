package session

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/config"
)

// Models receiver buffering: native control affects every unplayed sample,
// including PCM submitted by an earlier connection until Stop flushes it.
type bufferedDevice struct {
	stopErr error
	mu      sync.Mutex
	native  int
	pending map[*bufferedOutput][]float32
	outputs []*bufferedOutput
	peak    float64
	last    float32
}
type bufferedOutput struct {
	*sink
	device   *bufferedDevice
	callback func(int)
}

func (d *bufferedDevice) create(ctx context.Context, r DeviceRequest) (Output, error) {
	o := &bufferedOutput{sink: &sink{}, device: d, callback: r.NativeChanged}
	d.mu.Lock()
	d.outputs = append(d.outputs, o)
	d.mu.Unlock()
	return o, nil
}
func (d *bufferedDevice) measureLocked() {
	for _, samples := range d.pending {
		for _, sample := range samples {
			db := -30 + 3*float64(d.native-10) + 20*math.Log10(float64(sample)/.25)
			d.peak = math.Max(d.peak, db)
		}
	}
}
func (o *bufferedOutput) SetNative(ctx context.Context, n int) error {
	if err := o.sink.SetNative(ctx, n); err != nil {
		return err
	}
	d := o.device
	d.mu.Lock()
	d.native = n
	d.measureLocked()
	d.mu.Unlock()
	return nil
}
func (o *bufferedOutput) Write(ctx context.Context, f audio.Frame) error {
	if err := o.sink.Write(ctx, f); err != nil {
		return err
	}
	d := o.device
	d.mu.Lock()
	d.pending[o] = append(d.pending[o], f.Data[0])
	d.last = f.Data[0]
	d.measureLocked()
	d.mu.Unlock()
	return nil
}
func (o *bufferedOutput) Stop() error {
	d := o.device
	d.mu.Lock()
	if d.stopErr != nil {
		err := d.stopErr
		d.mu.Unlock()
		return err
	}
	delete(d.pending, o)
	d.mu.Unlock()
	return o.sink.Stop()
}
func bufferedEngine(t *testing.T, ceiling float64) (*Engine, *testClock, *bufferedDevice) {
	t.Helper()
	c := newClock()
	d := &bufferedDevice{pending: map[*bufferedOutput][]float32{}, peak: math.Inf(-1)}
	cfg := testConfig()
	cfg.ReferenceCeilingDB = &ceiling
	e, err := New(cfg, c, 1700000000000000000, func(ctx context.Context, r DeviceRequest) (Output, error) {
		if r.ID == "tv" {
			return d.create(ctx, r)
		}
		return &sink{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Stop)
	return e, c, d
}
func (d *bufferedDevice) latest() *bufferedOutput {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.outputs) == 0 {
		return nil
	}
	return d.outputs[len(d.outputs)-1]
}
func TestNativeTransitionPreservesBufferedCeiling(t *testing.T) {
	e, _, d := bufferedEngine(t, -29)
	target := -30.
	epoch, err := e.Select(Selection{Group: config.TVAndSpeakers, Source: &testSource{}, TargetDB: &target})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return d.latest() != nil && d.latest().Status().Frames >= 96000 })
	old := d.latest()
	anchor := e.PresentationTime(48000)
	if err = e.SetTargetDB(-29); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.native == 12 })
	d.mu.Lock()
	peak := d.peak
	d.mu.Unlock()
	if peak > -29+1e-5 {
		t.Fatalf("buffered PCM reached %.1f dB; ceiling -29 dB", peak)
	}
	eventually(t, func() bool { return d.latest() != old && d.latest().Status().Frames > 0 })
	if !e.PresentationTime(48000).Equal(anchor) || e.Status().Epoch != epoch {
		t.Fatal("volume change moved session timeline or ownership")
	}
	if e.Status().Members["tv"].Errors != 0 {
		t.Fatal("intentional volume transition counted as transport loss")
	}
}
func TestNativeFeedbackReconcilesPhysicalStepAndGain(t *testing.T) {
	e, c, d := bufferedEngine(t, -24)
	selectSource(t, e, config.TVAndSpeakers, &testSource{})
	eventually(t, func() bool { return d.latest() != nil && d.latest().Status().Frames >= 96000 })
	old := d.latest()
	old.callback(12) // Consume the initial command acknowledgement.
	d.mu.Lock()
	d.native = 11
	d.mu.Unlock()
	old.callback(11)
	if target := e.Status().TargetDB; target == nil || *target != -27 {
		t.Fatalf("remote target: %v", target)
	}
	c.advance(20 * time.Millisecond)
	eventually(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return math.Abs(float64(d.last)-.17698645) < 1e-6 })
	d.mu.Lock()
	effective := -30 + 3*float64(d.native-10) + 20*math.Log10(float64(d.last)/.25)
	d.mu.Unlock()
	if math.Abs(effective-(-27)) > 1e-5 {
		t.Fatalf("physical native plus PCM gain produces %.1f dB, want -27", effective)
	}
	current := d.latest()
	current.callback(12)
	current.callback(12)
	old.callback(15)
	if *e.Status().TargetDB != -27 {
		t.Fatal("acknowledgement or stale callback changed target")
	}
}

func TestNativeChangeDoesNotProceedAfterFlushFailure(t *testing.T) {
	e, _, d := bufferedEngine(t, -29)
	target := -30.
	if _, err := e.Select(Selection{Group: config.TVAndSpeakers, Source: &testSource{}, TargetDB: &target}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return d.latest() != nil && d.latest().Status().Frames >= 96000 })
	d.mu.Lock()
	d.stopErr = errors.New("receiver teardown failed")
	d.mu.Unlock()
	if err := e.SetTargetDB(-29); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		d.mu.Lock()
		native := d.native
		d.mu.Unlock()
		return native == 12 || e.Status().Members["tv"].State == "stop_failed"
	})
	d.mu.Lock()
	native, peak := d.native, d.peak
	d.mu.Unlock()
	if native != 10 || peak > -29+1e-5 {
		t.Fatalf("native changed before successful flush: native=%d peak=%v", native, peak)
	}
}
func TestRemoteAboveCeilingRestoresAuthorizedPlan(t *testing.T) {
	e, _, d := bufferedEngine(t, -24)
	selectSource(t, e, config.TVAndSpeakers, &testSource{})
	eventually(t, func() bool { return d.latest() != nil && d.latest().Status().Frames >= 96000 })
	old := d.latest()
	old.callback(12)
	d.mu.Lock()
	d.native = 13
	d.mu.Unlock()
	old.callback(13)
	eventually(t, func() bool { return d.latest() != old && d.latest().Status().Frames > 0 })
	d.mu.Lock()
	native := d.native
	d.mu.Unlock()
	if native != 12 || *e.Status().TargetDB != -24 {
		t.Fatalf("unauthorized physical step not restored: native=%d target=%v", native, *e.Status().TargetDB)
	}
	current := d.latest()
	current.callback(12)
	if *e.Status().TargetDB != -24 {
		t.Fatal("restoration acknowledgement fed back")
	}
}

func TestNativeAcknowledgementsBelongToOutputAttempt(t *testing.T) {
	e, _, d := bufferedEngine(t, -24)
	target := -30.
	if _, err := e.Select(Selection{Group: config.TVAndSpeakers, Source: &testSource{}, TargetDB: &target}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return d.latest() != nil && d.latest().Status().Frames >= 96000 })
	old := d.latest() // Its native-10 acknowledgement never arrives.
	if err := e.SetTargetDB(-24); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return d.latest() != old && d.latest().Status().Frames > 0 })
	current := d.latest()
	current.callback(12)
	d.mu.Lock()
	d.native = 10
	d.mu.Unlock()
	current.callback(10)
	if *e.Status().TargetDB != -30 {
		t.Fatalf("old attempt's missing acknowledgement suppressed remote action: target=%v", *e.Status().TargetDB)
	}
}

func TestNativeTransitionAfterSustainedRecoveryGetsFreshBudget(t *testing.T) {
	e, c, f := setup(t)
	selectSource(t, e, config.AllSpeakers, &testSource{})
	eventually(t, func() bool { return f.latest("kitchen") != nil && f.latest("kitchen").Status().Frames >= 96000 })
	old := f.latest("kitchen")
	old.lost(true)
	c.advance(time.Second)
	eventually(t, func() bool { return e.Status().Members["kitchen"].State == "recovering" })
	c.advance(time.Second)
	eventually(t, func() bool { return f.latest("kitchen") != old && f.latest("kitchen").Status().Frames > 0 })
	recovered := f.latest("kitchen")
	initial := recovered.Status().Frames
	for i := 0; i < 125; i++ {
		c.advance(time.Second)
		eventually(t, func() bool { return recovered.Status().Frames >= initial+uint64(i+1)*48000 })
	}
	if err := e.SetTargetDB(-30); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		return f.latest("kitchen") != recovered && f.latest("kitchen").Status().Frames > 0 || e.Status().Members["kitchen"].State == "recovery_exhausted"
	})
	if st := e.Status().Members["kitchen"]; st.State == "recovery_exhausted" || st.Errors != 1 {
		t.Fatalf("healthy time retained expired recovery deadline: %+v", st)
	}
}
