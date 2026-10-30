package session

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/khenrix/home-sound/internal/airplay"
	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/config"
	"github.com/khenrix/home-sound/internal/volume"
)

type testClock struct {
	mu      sync.Mutex
	now     time.Duration
	changed chan struct{}
	waits   map[time.Duration]int
}

func newClock() *testClock {
	return &testClock{now: 100 * time.Second, changed: make(chan struct{}), waits: map[time.Duration]int{}}
}
func (c *testClock) Now() time.Duration { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *testClock) WaitUntil(ctx context.Context, at time.Duration) error {
	c.mu.Lock()
	c.waits[at]++
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.waits[at]--; c.mu.Unlock() }()
	for {
		c.mu.Lock()
		now, ch := c.now, c.changed
		c.mu.Unlock()
		if now >= at {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}
func (c *testClock) waiting(at time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waits[at] > 0
}
func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now += d
	close(c.changed)
	c.changed = make(chan struct{})
	c.mu.Unlock()
}

type testSource struct {
	mu          sync.Mutex
	next, reads uint64
	closed      bool
	limit       uint64
}

func (s *testSource) Format() audio.Format       { return audio.Canonical }
func (s *testSource) Metadata() airplay.Metadata { return airplay.Metadata{Title: "synthetic"} }
func (s *testSource) Read(ctx context.Context) (audio.Frame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return audio.Frame{}, ctx.Err()
	}
	if s.limit > 0 && s.next >= s.limit {
		return audio.Frame{}, io.EOF
	}
	f := audio.Frame{Sample: s.next, Data: make([]float32, 1920)}
	for i := range f.Data {
		f.Data[i] = .25
	}
	s.next += 960
	s.reads++
	return f, nil
}
func (s *testSource) Close() error  { s.mu.Lock(); s.closed = true; s.mu.Unlock(); return nil }
func (s *testSource) count() uint64 { s.mu.Lock(); defer s.mu.Unlock(); return s.reads }

type sink struct {
	mu                         sync.Mutex
	epoch, first, next, frames uint64
	stopped, prepared, started bool
	native                     int
	anchor                     airplay.Anchor
	block                      bool
	fail                       bool
	bad                        error
	firstData                  float32
	metadata                   airplay.Metadata
}

func (o *sink) Prepare(ctx context.Context, e, first uint64) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.epoch = e
	o.first = first
	o.next = first
	o.prepared = true
	return nil
}
func (o *sink) SetNative(ctx context.Context, n int) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.prepared {
		return errors.New("native set before prepare")
	}
	o.native = n
	return nil
}
func (o *sink) Write(ctx context.Context, f audio.Frame) error {
	o.mu.Lock()
	block, fail := o.block, o.fail
	if !o.prepared || o.native == 0 || f.Epoch != o.epoch || f.Sample != o.next {
		o.bad = errors.New("lifecycle or contiguity violation")
		o.mu.Unlock()
		return o.bad
	}
	o.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	if fail {
		return errors.New("link lost")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.next += uint64(len(f.Data) / 2)
	o.frames += uint64(len(f.Data) / 2)
	if o.frames == uint64(len(f.Data)/2) {
		o.firstData = f.Data[0]
	}
	return nil
}
func (o *sink) Start(ctx context.Context, e uint64, at time.Time) (airplay.Anchor, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.started || o.frames == 0 || e != o.epoch {
		return airplay.Anchor{}, errors.New("invalid START")
	}
	o.started = true
	o.anchor = airplay.Anchor{Epoch: e, Sample: o.first, Requested: at, At: at}
	return o.anchor, nil
}
func (o *sink) Metadata(ctx context.Context, m airplay.Metadata) error {
	o.mu.Lock()
	o.metadata = m
	o.mu.Unlock()
	return nil
}
func (o *sink) Status() airplay.Status {
	o.mu.Lock()
	defer o.mu.Unlock()
	return airplay.Status{Healthy: !o.stopped, Ready: o.prepared && !o.stopped, Connected: o.prepared, Frames: o.frames, Anchor: o.anchor, ClockMode: "ntp", ClockState: "cold"}
}
func (o *sink) Stop() error { o.mu.Lock(); o.stopped = true; o.mu.Unlock(); return nil }
func (o *sink) lost(v bool) { o.mu.Lock(); o.fail = v; o.mu.Unlock() }

type sinks struct {
	mu        sync.Mutex
	byID      map[string][]*sink
	attempts  map[string][]time.Duration
	clock     *testClock
	downUntil time.Duration
	blocked   string
}

func (f *sinks) create(ctx context.Context, r DeviceRequest) (Output, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts[r.ID] = append(f.attempts[r.ID], f.clock.Now())
	if r.ID == "kitchen" && f.clock.Now() < f.downUntil {
		return nil, errors.New("offline")
	}
	o := &sink{block: r.ID == f.blocked}
	f.byID[r.ID] = append(f.byID[r.ID], o)
	return o, nil
}
func (f *sinks) latest(id string) *sink {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.byID[id]
	if len(a) == 0 {
		return nil
	}
	return a[len(a)-1]
}
func (f *sinks) count(id string) int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.attempts[id]) }
func testConfig() config.Config {
	max := 15
	ceiling := -18.
	p := volume.Profile{Version: 1, Verified: true, Provenance: volume.Provenance{Transport: "airplay2/native/realtime/48000/stereo/pcm16/ntp/identity/", Adapter: "airplay", Format: "48000/stereo/pcm16", Sensor: "fixture", NativeControl: "airplay", Reference: "synthetic-only"}, Points: []volume.Point{{Native: 10, DB: -30}, {Native: 12, DB: -24}, {Native: 15, DB: -18}}, NativeMaximum: &max}
	c := config.Config{Version: 1, Groups: map[string][]string{config.TVAndSpeakers: {"tv", "kitchen", "roam", "hall"}, config.AllSpeakers: {"kitchen", "roam", "hall"}}, Devices: map[string]config.Device{}, Profiles: map[string]volume.Profile{}, ReferenceVerified: true, ReferenceCeilingDB: &ceiling}
	for _, id := range []string{"tv", "kitchen", "roam", "hall"} {
		kind := "speaker"
		pp := p
		if id == "tv" {
			kind = "tv"
			pp.Provenance.NativeControl = "webos"
		}
		c.Devices[id] = config.Device{Kind: kind, Adapter: "airplay"}
		c.Profiles[id] = pp
	}
	return c
}
func setup(t *testing.T) (*Engine, *testClock, *sinks) {
	t.Helper()
	c := newClock()
	f := &sinks{clock: c, byID: map[string][]*sink{}, attempts: map[string][]time.Duration{}}
	e, err := New(testConfig(), c, 1700000000000000000, f.create)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Stop() })
	return e, c, f
}
func eventually(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if f() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("condition did not become true")
		case <-tick.C:
		}
	}
}
func selectSource(t *testing.T, e *Engine, group string, s Source) uint64 {
	t.Helper()
	target := -24.
	epoch, err := e.Select(Selection{Group: group, Source: s, TargetDB: &target})
	if err != nil {
		t.Fatal(err)
	}
	return epoch
}
func TestRecastRemovesTVAndRejectsStaleNativeEvents(t *testing.T) {
	e, _, f := setup(t)
	oldSource := &testSource{}
	old := selectSource(t, e, config.TVAndSpeakers, oldSource)
	eventually(t, func() bool { return f.latest("tv") != nil && f.latest("tv").Status().Frames > 0 })
	tv := f.latest("tv")
	next := selectSource(t, e, config.AllSpeakers, &testSource{})
	if next <= old {
		t.Fatal("generation did not advance")
	}
	tv.mu.Lock()
	stopped := tv.stopped
	tv.mu.Unlock()
	if !stopped {
		t.Fatal("TV survived recast")
	}
	if e.NativeChanged(old, "tv", 15) {
		t.Fatal("stale event changed new owner")
	}
	if _, ok := e.Status().Members["tv"]; ok {
		t.Fatal("TV retained in speakers group")
	}
	oldSource.mu.Lock()
	closed := oldSource.closed
	oldSource.mu.Unlock()
	if !closed {
		t.Fatal("old source not released")
	}
}
func TestProducerBoundedBySamplesAndFrozenClock(t *testing.T) {
	e, c, f := setup(t)
	s := &testSource{}
	selectSource(t, e, config.AllSpeakers, s)
	eventually(t, func() bool {
		return s.count() >= 100 && f.latest("hall") != nil && f.latest("hall").Status().Frames > 0
	})
	if n := s.count(); n > 150 {
		t.Fatalf("read %d chunks ahead of timeline", n)
	}
	st := e.Status()
	if st.ReplayFrames > 192000 {
		t.Fatal("replay exceeded four seconds")
	}
	// A wall-clock step cannot enter this mapping: its only time input is raw monotonic.
	before := e.PresentationTime(48000)
	c.advance(time.Second)
	after := e.PresentationTime(48000)
	if !before.Equal(after) {
		t.Fatal("presentation timestamp followed wall clock")
	}
	eventually(t, func() bool { return s.count() >= 150 })
	if s.count() > 200 {
		t.Fatal("producer raced helper ring")
	}
}
func TestTenSecondLossIsolatedAndRejoinsCurrentTimeline(t *testing.T) {
	e, c, f := setup(t)
	selectSource(t, e, config.AllSpeakers, &testSource{})
	eventually(t, func() bool {
		return f.latest("kitchen") != nil && f.latest("kitchen").Status().Frames > 0 && f.latest("hall") != nil && f.latest("hall").Status().Frames > 0
	})
	bad := f.latest("kitchen")
	healthy := f.latest("hall")
	initial := healthy.Status().Frames
	f.mu.Lock()
	f.downUntil = c.Now() + 10*time.Second
	f.mu.Unlock()
	bad.lost(true)
	for i := 0; i < 16; i++ {
		c.advance(time.Second)
		eventually(t, func() bool { return healthy.Status().Frames >= initial+uint64(i+1)*48000 })
	}
	eventually(t, func() bool { return f.latest("kitchen") != bad && f.latest("kitchen").Status().Frames > 0 })
	if f.latest("hall") != healthy {
		t.Fatal("healthy output restarted")
	}
	joined := f.latest("kitchen")
	joined.mu.Lock()
	first := joined.first
	badWrite := joined.bad
	joined.mu.Unlock()
	if first < 480000 || badWrite != nil {
		t.Fatalf("stale/noncontiguous rejoin sample %d: %v", first, badWrite)
	}
	status := e.Status().Members["kitchen"]
	if status.Audibility != "unverified" || status.Transport.ClockMeasurementKnown {
		t.Fatalf("invented acoustic evidence: %+v", status)
	}
}
func TestSlowOutputQueueBoundedAndStopCancelsRecovery(t *testing.T) {
	e, c, f := setup(t)
	f.blocked = "kitchen"
	selectSource(t, e, config.AllSpeakers, &testSource{})
	eventually(t, func() bool { return f.latest("hall") != nil && f.latest("hall").Status().Frames > 0 })
	for i := 0; i < 6; i++ {
		c.advance(time.Second)
		eventually(t, func() bool { return f.latest("hall").Status().Frames >= uint64(i+1)*48000 })
		if e.Status().Members["kitchen"].QueueFrames > 144000 {
			t.Fatal("unbounded queue")
		}
	}
	if e.Status().Members["kitchen"].QueueOverflows == 0 {
		t.Fatal("slow sink never isolated")
	}
	e.Stop()
	n := f.count("kitchen")
	c.advance(200 * time.Second)
	if f.count("kitchen") != n || e.Status().Active {
		t.Fatal("stop left active retries")
	}
}
func TestManualExclusionPersistsUntilNewSelection(t *testing.T) {
	e, c, f := setup(t)
	epoch := selectSource(t, e, config.TVAndSpeakers, &testSource{})
	eventually(t, func() bool { return f.latest("tv") != nil })
	if !e.Exclude(epoch, "tv") {
		t.Fatal("exclusion failed")
	}
	n := f.count("tv")
	c.advance(20 * time.Second)
	if f.count("tv") != n {
		t.Fatal("manual exit retried")
	}
	selectSource(t, e, config.TVAndSpeakers, &testSource{})
	eventually(t, func() bool { return f.count("tv") > n })
}
