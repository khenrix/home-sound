package spotify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/khenrix/home-sound/internal/airplay"
	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/config"
	"github.com/khenrix/home-sound/internal/resample"
	"github.com/khenrix/home-sound/internal/session"
	"github.com/khenrix/home-sound/internal/source"
	"github.com/khenrix/home-sound/internal/volume"
	"net"
	"os"
	"path/filepath"
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
func TestActualPrefetchKeepsEngineOutputsAndConverter(t *testing.T) {
	path := os.Getenv("HOME_SOUND_FRAMED_FIXTURE")
	if path == "" {
		t.Skip("explicit separately built GPL source fixture required")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(b)
	socket := filepath.Join(t.TempDir(), "pcm.sock")
	l, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proc, e := source.StartProcess(ctx, source.Helper{Path: path, SHA256: hex.EncodeToString(sum[:])}, []string{"-test.run=^TestCorePrefetchFixture$", socket}, 5*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer proc.Close()
	l.(*net.UnixListener).SetDeadline(time.Now().Add(2 * time.Second))
	conn, e := l.Accept()
	if e != nil {
		t.Fatal(e)
	}
	clock, e := session.NewRawClock()
	if e != nil {
		t.Fatal(e)
	}
	mapping := session.Freeze(clock, time.Now)
	var mu sync.Mutex
	outputs := map[string][]*continuityOutput{}
	prepared := make(chan struct{})
	made := 0
	engine, e := session.New(continuityConfig(), clock, mapping.OffsetNS, func(_ context.Context, r session.DeviceRequest) (session.Output, error) {
		o := &continuityOutput{prepared: prepared}
		mu.Lock()
		outputs[r.ID] = append(outputs[r.ID], o)
		made++
		if made == 3 {
			close(prepared)
		}
		mu.Unlock()
		return o, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	defer engine.Stop()
	co := source.NewCoordinator(engine, nil)
	var leases []source.Lease
	_ = (&Input{Group: config.AllSpeakers, Coordinator: co, TargetDB: -30, Lease: func(l source.Lease) { leases = append(leases, l) }}).Run(ctx, conn)

	if len(leases) != 2 || leases[0].Epoch != leases[1].Epoch {
		t.Fatalf("natural promotion replaced session epoch: %+v", leases)
	}
	raw := make([]float32, 13230*2)
	for i := range raw {
		raw[i] = .75
		if i < 4411*2 {
			raw[i] = .25
		}
	}
	converter, _ := resample.New(44100, 48000, 2)
	want, _ := converter.Process(raw)
	tail, _ := converter.Finish()
	want = append(want, tail...)
	mu.Lock()
	defer mu.Unlock()
	for _, id := range []string{"kitchen", "roam", "hall"} {
		if len(outputs[id]) != 1 {
			t.Fatalf("%s reconnected %d times at natural boundary", id, len(outputs[id]))
		}
		o := outputs[id][0]
		o.mu.Lock()
		if o.bad != nil || len(o.data) != len(want) {
			t.Fatalf("%s PCM count %d want %d: %v", id, len(o.data), len(want), o.bad)
		}
		for n, v := range want {
			if o.data[n] != v {
				t.Fatalf("%s FIR reset/lost sample at %d: %v want %v", id, n, o.data[n], v)
			}
		}
		found := false
		for index, m := range o.metadata {
			if m.Title == "second" {
				if o.metadataAt[index].Before(o.status.Anchor.At.Add(time.Duration(4802)*time.Second/48000 - time.Millisecond)) {
					t.Fatal("actual prefetch metadata preceded content boundary")
				}
				found = true
			}
		}
		if !found {
			t.Fatal("natural metadata boundary missing")
		}
		o.mu.Unlock()
	}
}
