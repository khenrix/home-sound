package session

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/khenrix/home-sound/internal/config"
)

func TestPauseFreezesPositionAndResumesRetainedPrefill(t *testing.T) {
	e, c, f := setup(t)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("final status: %+v", e.Status())
		}
	})
	src := &testSource{}
	epoch := selectSource(t, e, config.AllSpeakers, src)
	eventually(t, func() bool { return f.latest("hall") != nil && f.latest("hall").Status().Frames >= 96000 })
	c.advance(1500 * time.Millisecond)
	eventually(t, func() bool { return src.count() >= 175 })
	old := f.latest("hall")
	if err := e.Pause(); err != nil {
		t.Fatal(err)
	}
	frames := old.Status().Frames
	reads := src.count()
	if e.Status().PositionSample != 24000 || !e.Status().Paused {
		t.Fatalf("wrong pause position: %+v", e.Status())
	}
	c.advance(10 * time.Second)
	if old.Status().Frames != frames || old.Status().Healthy {
		t.Fatal("pause left old adapter submitting audio")
	}
	if e.Status().PositionSample != 24000 || src.count() > reads+1 {
		t.Fatal("paused media advanced or source ran ahead")
	}
	if err := e.Resume(); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return f.latest("hall") != old && f.latest("hall").Status().Frames > 0 })
	resumed := f.latest("hall")
	resumed.mu.Lock()
	first := resumed.first
	resumed.mu.Unlock()
	if first != 24000 {
		t.Fatalf("resume skipped prefill: first sample %d", first)
	}
	if e.Status().PositionSample != 24000 || e.Status().Epoch != epoch {
		t.Fatal("resume lost frozen position or source ownership")
	}
	c.advance(1250 * time.Millisecond)
	if e.Status().PositionSample != 36000 {
		t.Fatalf("resumed position %d, want 36000", e.Status().PositionSample)
	}
}
func TestPauseSlicesRetainedFrameWithoutSkippingSamples(t *testing.T) {
	e, c, f := setup(t)
	selectSource(t, e, config.AllSpeakers, &testSource{})
	eventually(t, func() bool { return f.latest("hall") != nil && f.latest("hall").Status().Frames >= 96000 })
	c.advance(1505 * time.Millisecond)
	if err := e.Pause(); err != nil {
		t.Fatal(err)
	}
	old := f.latest("hall")
	if err := e.Resume(); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return f.latest("hall") != old && f.latest("hall").Status().Frames > 0 })
	out := f.latest("hall")
	out.mu.Lock()
	first, bad := out.first, out.bad
	out.mu.Unlock()
	if first != 24240 || bad != nil {
		t.Fatalf("partial frame lost: first=%d err=%v", first, bad)
	}
}
func TestSourcePausePlayingEventsControlOnlyCurrentGeneration(t *testing.T) {
	e, _, f := setup(t)
	src := &eventSource{events: make(chan SourceEvent, 8)}
	selectSource(t, e, config.AllSpeakers, src)
	eventually(t, func() bool { return f.latest("hall") != nil && f.latest("hall").Status().Frames >= 96000 })
	old := f.latest("hall")
	src.events <- SourceEvent{Kind: "transport", Transport: "paused"}
	eventually(t, func() bool { return e.Status().Paused && !old.Status().Healthy })
	src.events <- SourceEvent{Kind: "transport", Transport: "playing"}
	eventually(t, func() bool {
		return !e.Status().Paused && f.latest("hall") != old && f.latest("hall").Status().Frames > 0
	})
	selectSource(t, e, config.AllSpeakers, &testSource{})
	eventually(t, func() bool { return !e.Status().Paused })
	src.events <- SourceEvent{Kind: "transport", Transport: "paused"}
	if e.Status().Paused {
		t.Fatal("old source paused replacement")
	}
}

type delayedPrepare struct {
	*sink
	clock *testClock
	at    time.Duration
}

func (o *delayedPrepare) Prepare(ctx context.Context, epoch, first uint64) error {
	if err := o.clock.WaitUntil(ctx, o.at); err != nil {
		return err
	}
	return o.sink.Prepare(ctx, epoch, first)
}
func TestResumePreparationDoesNotConsumePausedMedia(t *testing.T) {
	e, c, f := setup(t)
	selectSource(t, e, config.AllSpeakers, &testSource{})
	eventually(t, func() bool { return f.latest("hall") != nil && f.latest("hall").Status().Frames >= 96000 })
	c.advance(1500 * time.Millisecond)
	if err := e.Pause(); err != nil {
		t.Fatal(err)
	}
	old := f.latest("hall")
	ready := make(chan struct{}, 3)
	e.factory = func(ctx context.Context, r DeviceRequest) (Output, error) {
		out, err := f.create(ctx, r)
		ready <- struct{}{}
		if err != nil {
			return nil, err
		}
		return &delayedPrepare{sink: out.(*sink), clock: c, at: 106500 * time.Millisecond}, nil
	}
	if err := e.Resume(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		select {
		case <-ready:
		case <-time.After(time.Second):
			t.Fatal("resume preparation did not begin")
		}
	}
	c.advance(3 * time.Second)
	if e.Status().PositionSample != 24000 || !e.Status().Paused || !e.Status().Resuming {
		t.Fatalf("media consumed during preparation: %+v", e.Status())
	}
	c.advance(2 * time.Second)
	eventually(t, func() bool { return f.latest("hall") != old && f.latest("hall").Status().Frames > 0 })
	if e.Status().PositionSample != 24000 {
		t.Fatal("preparation skipped paused content")
	}
	c.advance(1250 * time.Millisecond)
	if e.Status().PositionSample != 36000 {
		t.Fatalf("wrong resumed sample %d", e.Status().PositionSample)
	}
}

func TestResumeWhileMutedAdvancesWithoutStartingOutputs(t *testing.T) {
	e, c, f := setup(t)
	selectSource(t, e, config.AllSpeakers, &testSource{})
	eventually(t, func() bool { return f.latest("hall") != nil && f.latest("hall").Status().Frames >= 96000 })
	if err := e.Mute(true); err != nil {
		t.Fatal(err)
	}
	if err := e.Pause(); err != nil {
		t.Fatal(err)
	}
	attempts := f.count("hall")
	if err := e.Resume(); err != nil {
		t.Fatal(err)
	}
	c.advance(2 * time.Second)
	eventually(t, func() bool { return e.Status().PositionSample == 48000 })
	if e.Status().Paused || e.Status().PositionSample != 48000 || !e.Status().Muted {
		t.Fatalf("muted resume did not advance: %+v", e.Status())
	}
	if f.count("hall") != attempts {
		t.Fatal("muted resume started output")
	}
}

type callbackOnStop struct {
	*sink
	callback func(int)
}

func (o *callbackOnStop) Stop() error { o.callback(15); return o.sink.Stop() }
func TestCanceledAdapterCallbackDuringPauseCannotChangeTarget(t *testing.T) {
	e, _, f := setup(t)
	base := e.factory
	e.factory = func(ctx context.Context, r DeviceRequest) (Output, error) {
		out, err := base(ctx, r)
		if err != nil {
			return nil, err
		}
		if r.ID == "tv" {
			return &callbackOnStop{sink: out.(*sink), callback: r.NativeChanged}, nil
		}
		return out, nil
	}
	selectSource(t, e, config.TVAndSpeakers, &testSource{})
	eventually(t, func() bool { return f.latest("tv") != nil && f.latest("tv").Status().Frames > 0 })
	if err := e.Pause(); err != nil {
		t.Fatal(err)
	}
	if target := e.Status().TargetDB; target == nil || *target != -24 {
		t.Fatal("canceled adapter changed volume during teardown")
	}
}

func TestInitialPreparationDoesNotSkipBeginningOfSource(t *testing.T) {
	e, c, f := setup(t)
	started := make(chan struct{}, 3)
	src := &testSource{}
	e.factory = func(ctx context.Context, r DeviceRequest) (Output, error) {
		out, err := f.create(ctx, r)
		started <- struct{}{}
		if err != nil {
			return nil, err
		}
		return &delayedPrepare{sink: out.(*sink), clock: c, at: 105 * time.Second}, nil
	}
	selectSource(t, e, config.AllSpeakers, src)
	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("initial preparation did not begin")
		}
	}
	c.advance(5 * time.Second)
	eventually(t, func() bool { return f.latest("hall") != nil && f.latest("hall").Status().Frames > 0 })
	out := f.latest("hall")
	out.mu.Lock()
	first := out.first
	out.mu.Unlock()
	if first != 0 || e.Status().PositionSample != 0 {
		t.Fatalf("initial preparation skipped source: first %d position %d", first, e.Status().PositionSample)
	}
}

type heldStop struct {
	*sink
	entered chan struct{}
	release chan struct{}
}

func (o *heldStop) Stop() error {
	select {
	case o.entered <- struct{}{}:
	default:
	}
	<-o.release
	return o.sink.Stop()
}
func TestConcurrentPauseWaitsForExistingTeardown(t *testing.T) {
	e, _, f := setup(t)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	e.factory = func(ctx context.Context, r DeviceRequest) (Output, error) {
		out, err := f.create(ctx, r)
		if err != nil {
			return nil, err
		}
		return &heldStop{sink: out.(*sink), entered: entered, release: release}, nil
	}
	native := 10
	if _, err := e.Select(Selection{Group: config.TVAndSpeakers, Source: &testSource{}, TVCalibration: &native}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return f.latest("tv") != nil && f.latest("tv").Status().Frames > 0 })
	first := make(chan error, 1)
	go func() { first <- e.Pause() }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("teardown did not begin")
	}
	second := make(chan error, 1)
	go func() { second <- e.Pause() }()
	select {
	case <-second:
		t.Fatal("concurrent pause returned while the old adapter could still play")
	case <-time.After(20 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	for _, done := range []chan error{first, second} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("pause did not finish after teardown")
		}
	}
}
