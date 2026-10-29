package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"testing"
	"time"

	"github.com/khenrix/home-sound/internal/airplay"
	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/config"
	"github.com/khenrix/home-sound/internal/wave"
)

func TestWAVSourceContiguousImmutableAndCancelable(t *testing.T) {
	samples := make([]float32, 2500)
	samples[0] = .5
	samples[1920] = -.5
	var b bytes.Buffer
	if e := wave.Write(&b, audio.Canonical, samples); e != nil {
		t.Fatal(e)
	}
	src, e := WAV(&b)
	if e != nil {
		t.Fatal(e)
	}
	a, e := src.Read(context.Background())
	if e != nil || a.Sample != 0 || len(a.Data) != 1920 || a.Data[0] != .5 {
		t.Fatalf("first: %+v %v", a, e)
	}
	z, e := src.Read(context.Background())
	if e != nil || z.Sample != 960 || len(z.Data) != 580 || z.Data[0] != -.5 || a.Data[0] != .5 {
		t.Fatalf("tail sample %d len %d err %v", z.Sample, len(z.Data), e)
	}
	if _, e = src.Read(context.Background()); !errors.Is(e, io.EOF) {
		t.Fatal("missing EOF")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = src.Read(ctx); !errors.Is(e, context.Canceled) {
		t.Fatal("read ignored cancellation")
	}
	src.Close()
}
func TestFrozenOffsetIgnoresWallStep(t *testing.T) {
	c := newClock()
	wall := time.Unix(1700000000, 0)
	mapping := Freeze(c, func() time.Time { return wall })
	if mapping.OffsetNS != 1699999900000000000 {
		t.Fatalf("wrong raw/Unix mapping %d", mapping.OffsetNS)
	}
	wall = wall.Add(-time.Hour)
	c.advance(time.Second)
	if got := mapping.Time(c.Now()); !got.Equal(time.Unix(1700000001, 0)) {
		t.Fatalf("clock stepped with wall: %s", got)
	}
}
func TestRawClockCancellationAndProgress(t *testing.T) {
	c, e := NewRawClock()
	if e != nil {
		t.Fatal(e)
	}
	before := c.Now()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = c.WaitUntil(ctx, before+time.Hour); !errors.Is(e, context.Canceled) {
		t.Fatal("raw clock wait ignored cancel")
	}
	if c.Now() < before {
		t.Fatal("clock went backwards")
	}
}
func TestRecoveryBackoffAndEpisodeBound(t *testing.T) {
	e, c, f := setup(t)
	f.downUntil = 1000 * time.Second
	selectSource(t, e, config.AllSpeakers, &testSource{})
	eventually(t, func() bool { return f.count("kitchen") == 1 })
	for i, wait := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second} {
		eventually(t, func() bool { return e.Status().Members["kitchen"].State == "recovering" })
		c.advance(wait)
		eventually(t, func() bool { return f.count("kitchen") == i+2 })
	}
	eventually(t, func() bool { return e.Status().Members["kitchen"].State == "recovery_exhausted" })
	c.advance(200 * time.Second)
	if n := f.count("kitchen"); n != 11 {
		t.Fatalf("episode retried beyond bound: %d", n)
	}
	f.mu.Lock()
	got := append([]time.Duration(nil), f.attempts["kitchen"]...)
	f.mu.Unlock()
	want := []time.Duration{100, 101, 103, 107, 115, 130, 145, 160, 175, 190, 205}
	for i, v := range want {
		if got[i] != v*time.Second {
			t.Fatalf("attempt %d at %v, want %vs", i, got[i], v)
		}
	}
}
func TestMuteStopsWithoutFailureAndUnmuteUsesCurrentSample(t *testing.T) {
	e, c, f := setup(t)
	selectSource(t, e, config.AllSpeakers, &testSource{})
	eventually(t, func() bool { return f.latest("hall") != nil && f.latest("hall").Status().Frames > 0 })
	old := f.latest("hall")
	if e.Mute(true) != nil {
		t.Fatal("mute failed")
	}
	eventually(t, func() bool { return e.Status().Members["hall"].State == "muted" })
	c.advance(10 * time.Second)
	if e.Status().Members["hall"].Errors != 0 {
		t.Fatal("intentional mute counted as failure")
	}
	if e.Mute(false) != nil {
		t.Fatal("unmute failed")
	}
	eventually(t, func() bool { return f.latest("hall") != old && f.latest("hall").Status().Frames > 0 })
	f.latest("hall").mu.Lock()
	first := f.latest("hall").first
	f.latest("hall").mu.Unlock()
	if first < 480000 {
		t.Fatalf("unmute rewound to %d", first)
	}
}
func TestUnverifiedConfigCannotPlaySpeakers(t *testing.T) {
	c := testConfig()
	c.ReferenceVerified = false
	calls := 0
	e, err := New(c, newClock(), 0, func(context.Context, DeviceRequest) (Output, error) { calls++; return nil, errors.New("must not run") })
	if err != nil {
		t.Fatal(err)
	}
	target := -30.
	if _, err = e.Select(Selection{Group: config.AllSpeakers, Source: &testSource{}, TargetDB: &target}); err == nil || calls != 0 {
		t.Fatal("unverified reference reached device factory")
	}
}

type brokenSource struct{ testSource }

func (s *brokenSource) Read(ctx context.Context) (audio.Frame, error) {
	return audio.Frame{}, errors.New("decoder failed")
}
func TestSourceFailureRemainsVisibleAndClosesSource(t *testing.T) {
	e, _, _ := setup(t)
	src := &brokenSource{}
	selectSource(t, e, config.AllSpeakers, src)
	eventually(t, func() bool { return !e.Status().Active })
	if e.Status().SourceState != "error" {
		t.Fatalf("source failure hidden as %q", e.Status().SourceState)
	}
	src.mu.Lock()
	closed := src.closed
	src.mu.Unlock()
	if !closed {
		t.Fatal("failed source not closed")
	}
}
func TestEOFCancelsPendingRecoveryAndDrainsHealthy(t *testing.T) {
	e, c, f := setup(t)
	f.downUntil = 1000 * time.Second
	src := &testSource{limit: 48000}
	selectSource(t, e, config.AllSpeakers, src)
	eventually(t, func() bool {
		return e.Status().SourceState == "ended" && f.latest("hall") != nil && f.latest("hall").Status().Frames == 48000
	})
	attempts := f.count("kitchen")
	c.advance(3 * time.Second)
	eventually(t, func() bool { return !e.Status().Active })
	if f.count("kitchen") != attempts {
		t.Fatal("EOF retried a dead source")
	}
	if st := e.Status().Members["hall"]; st.Transport.Ready || st.State != "ended" {
		t.Fatalf("closed transport still advertised active: %+v", st)
	}
	if e.Status().Members["hall"].Transport.Anchor.At.IsZero() {
		t.Fatal("final acknowledged anchor discarded")
	}
}
func TestAcknowledgedAnchorCorrectionExtendsDrain(t *testing.T) {
	e, c, f := setup(t)
	selectSource(t, e, config.AllSpeakers, &testSource{limit: 48000})
	eventually(t, func() bool {
		return f.latest("hall") != nil && f.latest("hall").Status().Frames == 48000 && e.Status().SourceState == "ended"
	})
	out := f.latest("hall")
	out.mu.Lock()
	out.anchor.At = out.anchor.At.Add(2 * time.Second)
	out.anchor.Corrected = true
	out.mu.Unlock()
	c.advance(2 * time.Second)
	// Wait for the engine to extend its raw-clock drain deadline.
	eventually(t, func() bool { return c.waiting(104 * time.Second) })
	if !e.Status().Active {
		t.Fatal("drain used requested anchor instead of corrected acknowledgement")
	}
	c.advance(2 * time.Second)
	eventually(t, func() bool { return !e.Status().Active })
}

type irregularSource struct {
	next  uint64
	reads int
}

func (s *irregularSource) Format() audio.Format       { return audio.Canonical }
func (s *irregularSource) Metadata() airplay.Metadata { return airplay.Metadata{} }
func (s *irregularSource) Close() error               { return nil }
func (s *irregularSource) Read(ctx context.Context) (audio.Frame, error) {
	if ctx.Err() != nil {
		return audio.Frame{}, ctx.Err()
	}
	s.reads++
	n := 600
	f := audio.Frame{Sample: s.next, Data: make([]float32, n*2)}
	s.next += uint64(n)
	return f, nil
}
func TestArbitraryContiguousSourceChunksNormalizeForRejoin(t *testing.T) {
	e, c, f := setup(t)
	selectSource(t, e, config.AllSpeakers, &irregularSource{})
	eventually(t, func() bool { return f.latest("hall") != nil && f.latest("hall").Status().Frames > 0 })
	eventually(t, func() bool { return f.latest("kitchen") != nil })
	bad := f.latest("kitchen")
	bad.lost(true)
	c.advance(1030 * time.Millisecond)
	eventually(t, func() bool { return e.Status().Members["kitchen"].State == "recovering" })
	c.advance(time.Second)
	eventually(t, func() bool { return f.latest("kitchen") != bad && f.latest("kitchen").Status().Frames > 0 })
	joined := f.latest("kitchen")
	joined.mu.Lock()
	broken := joined.bad
	joined.mu.Unlock()
	if broken != nil {
		t.Fatal(broken)
	}
}

type stuckPrepare struct{ sink }

func (o *stuckPrepare) Prepare(ctx context.Context, e, first uint64) error {
	<-ctx.Done()
	return ctx.Err()
}
func TestPrepareDeadlineAndStopCancelIndependentOutput(t *testing.T) {
	e, c, f := setup(t)
	realFactory := e.factory
	e.factory = func(ctx context.Context, r DeviceRequest) (Output, error) {
		if r.ID == "kitchen" {
			return &stuckPrepare{}, nil
		}
		return realFactory(ctx, r)
	}
	selectSource(t, e, config.AllSpeakers, &testSource{})
	eventually(t, func() bool { return f.latest("hall") != nil && f.latest("hall").Status().Frames > 0 })
	c.advance(20 * time.Second)
	eventually(t, func() bool { return e.Status().Members["kitchen"].Errors == 1 })
	e.Stop()
	if e.Status().Active {
		t.Fatal("stop failed to cancel prepare/recovery")
	}
}
func TestStaleRecoveryNativeCallbackCannotChangeTarget(t *testing.T) {
	e, c, f := setup(t)
	realFactory := e.factory
	callbacks := make(chan func(int), 8)
	e.factory = func(ctx context.Context, r DeviceRequest) (Output, error) {
		if r.ID == "tv" {
			callbacks <- r.NativeChanged
		}
		return realFactory(ctx, r)
	}
	selectSource(t, e, config.TVAndSpeakers, &testSource{})
	old := <-callbacks
	eventually(t, func() bool { return f.latest("tv") != nil && f.latest("tv").Status().Frames > 0 })
	bad := f.latest("tv")
	bad.lost(true)
	c.advance(time.Second)
	eventually(t, func() bool { return e.Status().Members["tv"].State == "recovering" })
	c.advance(time.Second)
	<-callbacks
	eventually(t, func() bool { return f.latest("tv") != bad && f.latest("tv").Status().Frames > 0 })
	old(15)
	if target := e.Status().TargetDB; target == nil || *target != -24 {
		t.Fatalf("old adapter callback changed target: %v", target)
	}
}

func TestEOFCancelsInFlightRecoveryBeforeHealthyDrainEnds(t *testing.T) {
	e, c, f := setup(t)
	realFactory := e.factory
	retry := make(chan context.Context, 1)
	attempts := 0
	e.factory = func(ctx context.Context, r DeviceRequest) (Output, error) {
		if r.ID == "kitchen" {
			attempts++
			if attempts > 1 {
				retry <- ctx
				return &stuckPrepare{}, nil
			}
		}
		return realFactory(ctx, r)
	}
	selectSource(t, e, config.AllSpeakers, &testSource{limit: 192000})
	eventually(t, func() bool { return f.latest("kitchen") != nil && f.latest("kitchen").Status().Frames > 0 })
	f.latest("kitchen").lost(true)
	c.advance(time.Second)
	eventually(t, func() bool { return e.Status().Members["kitchen"].State == "recovering" })
	c.advance(time.Second)
	var pending context.Context
	select {
	case pending = <-retry:
	case <-time.After(time.Second):
		t.Fatal("retry did not begin")
	}
	c.advance(100 * time.Millisecond)
	eventually(t, func() bool { return e.Status().SourceState == "ended" })
	if pending.Err() == nil {
		t.Fatal("EOF left pending recovery running")
	}
	eventually(t, func() bool { return e.Status().Members["kitchen"].State == "ended" })
	if e.Status().Members["kitchen"].Errors != 1 {
		t.Fatal("intentional EOF cancellation counted as another failure")
	}
	if !e.Status().Active {
		t.Fatal("EOF interrupted healthy drain")
	}
}

func TestWAVSeekStartsAtRequestedSampleAndRejectsPastEnd(t *testing.T) {
	var b bytes.Buffer
	samples := make([]float32, 4000)
	samples[2000] = .5
	wave.Write(&b, audio.Canonical, samples)
	src, err := WAVAt(bytes.NewReader(b.Bytes()), 1000)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := src.Read(context.Background())
	if err != nil || frame.Sample != 1000 || frame.Data[0] != .5 || len(frame.Data) != 1920 {
		t.Fatalf("seek returned sample %d len %d err %v", frame.Sample, len(frame.Data), err)
	}
	if _, err = WAVAt(bytes.NewReader(b.Bytes()), 2000); err == nil {
		t.Fatal("seek at EOF accepted")
	}
}

func TestLossAfterLongHealthyRunGetsFreshRecoveryBudget(t *testing.T) {
	e, c, f := setup(t)
	selectSource(t, e, config.AllSpeakers, &testSource{})
	eventually(t, func() bool { return f.latest("kitchen") != nil && f.latest("kitchen").Status().Frames >= 96000 })
	out := f.latest("kitchen")
	for i := 0; i < 125; i++ {
		c.advance(time.Second)
		eventually(t, func() bool { return out.Status().Frames >= 96000+uint64(i+1)*48000 })
	}
	out.lost(true)
	c.advance(time.Second)
	eventually(t, func() bool { return e.Status().Members["kitchen"].Errors > 0 })
	if st := e.Status().Members["kitchen"]; st.State == "recovery_exhausted" || st.RecoveryUntil < c.Now()+119*time.Second {
		t.Fatalf("old healthy runtime consumed new episode: %+v", st)
	}
}

func TestConfigReplacementPreservesGenerationAndRequiresIdle(t *testing.T) {
	e, _, _ := setup(t)
	old := selectSource(t, e, config.AllSpeakers, &testSource{})
	cfg := testConfig()
	d := cfg.Devices["hall"]
	d.RoomTrimDB = 3
	cfg.Devices["hall"] = d
	if e.Reconfigure(cfg) == nil {
		t.Fatal("active configuration changed")
	}
	e.Stop()
	if err := e.Reconfigure(cfg); err != nil {
		t.Fatal(err)
	}
	next := selectSource(t, e, config.AllSpeakers, &testSource{})
	if next <= old || e.Status().Members["hall"].Plan.Native != 15 {
		t.Fatalf("configuration/generation not retained: %+v", e.Status())
	}
}

func TestMemberPCMUsesIndependentAttenuationAndRoomTrim(t *testing.T) {
	e, _, f := setup(t)
	cfg := testConfig()
	device := cfg.Devices["kitchen"]
	device.RoomTrimDB = 2
	cfg.Devices["kitchen"] = device
	if err := e.Reconfigure(cfg); err != nil {
		t.Fatal(err)
	}
	target := -25.
	if _, err := e.Select(Selection{Group: config.AllSpeakers, Source: &testSource{}, TargetDB: &target}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		return f.latest("hall") != nil && f.latest("hall").Status().Frames > 0 && f.latest("kitchen") != nil && f.latest("kitchen").Status().Frames > 0
	})
	for id, want := range map[string]float32{"hall": .22281273, "kitchen": .14058533} {
		out := f.latest(id)
		out.mu.Lock()
		got := out.firstData
		out.mu.Unlock()
		if math.Abs(float64(got-want)) > 1e-6 {
			t.Fatalf("%s PCM got %v want %v", id, got, want)
		}
	}
	if err := e.SetTargetDB(-22); err != nil {
		t.Fatal(err)
	}
	status := e.Status()
	if status.Members["hall"].Plan.EffectiveDB != -22 || status.Members["kitchen"].Plan.EffectiveDB != -20 {
		t.Fatal("common delta changed room trim")
	}
}

type slowMetadata struct {
	*sink
	clock *testClock
}

func (o *slowMetadata) Metadata(ctx context.Context, m airplay.Metadata) error {
	o.clock.advance(2 * time.Second)
	return o.sink.Metadata(ctx, m)
}
func TestRecoveryLeadIncludesAllPreparationBeforeAudio(t *testing.T) {
	e, c, f := setup(t)
	e.factory = func(ctx context.Context, r DeviceRequest) (Output, error) {
		out, err := f.create(ctx, r)
		if err != nil {
			return nil, err
		}
		return &slowMetadata{sink: out.(*sink), clock: c}, nil
	}
	native := 10
	if _, err := e.Select(Selection{Group: config.TVAndSpeakers, Source: &testSource{}, TVCalibration: &native}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return f.latest("tv") != nil && f.latest("tv").Status().Frames > 0 })
	old := f.latest("tv")
	old.lost(true)
	c.advance(time.Second)
	eventually(t, func() bool { return e.Status().Members["tv"].State == "recovering" })
	c.advance(time.Second)
	eventually(t, func() bool { return f.latest("tv") != old && f.latest("tv").Status().Frames > 0 })
}
