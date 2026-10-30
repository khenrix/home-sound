package youtube

import (
	"context"
	"errors"
	"github.com/khenrix/home-sound/internal/airplay"
	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/config"
	"github.com/khenrix/home-sound/internal/session"
	"github.com/khenrix/home-sound/internal/source"
	"github.com/khenrix/home-sound/internal/volume"
	"sync"
	"testing"
	"time"
)

type continuityOutput struct {
	mu          sync.Mutex
	next, epoch uint64
	first       uint64
	prepared    <-chan struct{}
	data        []float32
	metadata    []airplay.Metadata
	metadataAt  []time.Time
	status      airplay.Status
	bad         error
}

func (o *continuityOutput) Prepare(ctx context.Context, e, n uint64) error {
	select {
	case <-o.prepared:
	case <-ctx.Done():
		return ctx.Err()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.epoch = e
	o.next = n
	o.first = n
	o.status.Healthy = true
	return nil
}
func (*continuityOutput) SetNative(context.Context, int) error { return nil }
func (o *continuityOutput) Write(_ context.Context, f audio.Frame) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if f.Sample != o.next || f.Epoch != o.epoch {
		o.bad = errors.New("noncontiguous output")
		return o.bad
	}
	o.next += uint64(len(f.Data) / 2)
	o.data = append(o.data, f.Data...)
	o.status.Frames += uint64(len(f.Data) / 2)
	return nil
}
func (o *continuityOutput) Start(_ context.Context, e uint64, at time.Time) (airplay.Anchor, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	a := airplay.Anchor{Epoch: e, Sample: o.first, At: at, Requested: at}
	o.status.Anchor = a
	return a, nil
}
func (o *continuityOutput) Stop() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.status.Healthy = false
	return nil
}
func (o *continuityOutput) Status() airplay.Status { o.mu.Lock(); defer o.mu.Unlock(); return o.status }
func (o *continuityOutput) Metadata(_ context.Context, m airplay.Metadata) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.metadata = append(o.metadata, m)
	o.metadataAt = append(o.metadataAt, time.Now())
	return nil
}
func continuityConfig() config.Config {
	max := 15
	ceiling := -18.
	p := volume.Profile{Version: 1, Verified: true, Provenance: volume.Provenance{Transport: "airplay2/native/realtime/48000/stereo/pcm16/ntp/identity/", Adapter: "airplay", Format: "48000/stereo/pcm16", Sensor: "fixture", NativeControl: "airplay", Reference: "synthetic-only"}, Points: []volume.Point{{Native: 10, DB: -30}, {Native: 15, DB: -18}}, NativeMaximum: &max}
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

func TestEOFRetainsEnginePresentationDrain(t *testing.T) {
	clock, err := session.NewRawClock()
	if err != nil {
		t.Fatal(err)
	}
	mapping := session.Freeze(clock, time.Now)
	ready := make(chan struct{})
	close(ready)
	engine, err := session.New(continuityConfig(), clock, mapping.OffsetNS, func(context.Context, session.DeviceRequest) (session.Output, error) {
		return &continuityOutput{prepared: ready}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Stop()
	r := &resolveFixture{make(chan string, 8), make(chan struct{})}
	close(r.wait)
	p := NewPlaybackController(config.AllSpeakers, "44444444444444444444444444444444", source.NewCoordinator(engine, nil), r, eofDecoder{make(chan string, 8)}, -30)
	defer p.Close()
	if err = p.Handle(context.Background(), Message{Name: "setPlaylist", Values: map[string]string{"videoIds": "abcdefghijk"}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for engine.Status().SourceState != "ended" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	before := engine.Status()
	if before.SourceState != "ended" || !before.Active || before.PositionSample >= before.NextSample {
		t.Fatalf("fixture did not establish pending drain: %+v", before)
	}
	p.Tick(context.Background())
	after := engine.Status()
	t.Logf("Before Tick: active=%v, produced=%d, presented=%d; after Tick: active=%v, state=%s", before.Active, before.NextSample, before.PositionSample, after.Active, after.SourceState)
	if !after.Active {
		t.Fatal("production EOF stopped the Engine before pending audio presented")
	}
}

func TestEOFPresentsBeforeQueueSuccessorAndReportsPresentation(t *testing.T) {
	for _, queue := range []string{"abcdefghijk", "abcdefghijk,lmnopqrstuv"} {
		t.Run(queue, func(t *testing.T) {
			clock, e := session.NewRawClock()
			if e != nil {
				t.Fatal(e)
			}
			mapping := session.Freeze(clock, time.Now)
			ready := make(chan struct{})
			close(ready)
			engine, e := session.New(continuityConfig(), clock, mapping.OffsetNS, func(context.Context, session.DeviceRequest) (session.Output, error) {
				return &continuityOutput{prepared: ready}, nil
			})
			if e != nil {
				t.Fatal(e)
			}
			defer engine.Stop()
			r := &resolveFixture{make(chan string, 8), make(chan struct{})}
			close(r.wait)
			calls := make(chan string, 8)
			p := NewPlaybackController(config.AllSpeakers, "77777777777777777777777777777777", source.NewCoordinator(engine, nil), r, eofDecoder{calls}, -30)
			defer p.Close()
			ctx := context.Background()
			if e = p.Handle(ctx, Message{Name: "setPlaylist", Values: map[string]string{"videoIds": queue}}); e != nil {
				t.Fatal(e)
			}
			<-calls
			deadline := time.Now().Add(time.Second)
			for engine.Status().SourceState != "ended" && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			p.Tick(ctx)
			state, _ := p.Snapshot()
			if state.Position != 0 || !engine.Status().Active || len(calls) != 0 {
				t.Fatalf("production reported/passed presentation: state=%+v engine=%+v calls=%d", state, engine.Status(), len(calls))
			}
			p.Handle(ctx, Message{Name: "pause"})
			time.Sleep(1100 * time.Millisecond)
			p.Tick(ctx)
			if !engine.Status().Active || len(calls) != 0 {
				t.Fatal("paused drain completed")
			}
			p.Handle(ctx, Message{Name: "play"})
			deadline = time.Now().Add(2 * time.Second)
			for engine.Status().Active && time.Now().Before(deadline) {
				if len(calls) > 0 {
					t.Fatal("successor began before prior presentation completed")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if engine.Status().Active {
				t.Fatal("resumed presentation did not drain")
			}
			p.Tick(ctx)
			if queue == "abcdefghijk" {
				state, _ = p.Snapshot()
				if state.State != 4 || state.Position != 20*time.Millisecond {
					t.Fatalf("final presentation %+v", state)
				}
			} else {
				select {
				case id := <-calls:
					if id != "lmnopqrstuv" {
						t.Fatal(id)
					}
				case <-time.After(time.Second):
					t.Fatal("completed presentation did not admit successor")
				}
			}
		})
	}
}
