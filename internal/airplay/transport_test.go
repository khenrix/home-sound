package airplay

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"github.com/khenrix/home-sound/internal/audio"
	"os"
	"testing"
	"time"
)

func TestUnsupportedNativeCapabilitiesFailClosed(t *testing.T) {
	for _, line := range []string{
		"[STATUS] capabilities requested=0x100000 realtime_formats=0x40000 realtime_known=1 buffered_formats=0x40000 buffered_known=1",
		"[STATUS] route protocol=airplay2 flow=native timing=ntp buffered=1",
		"[STATUS] capabilities requested=invalid realtime_formats=0x0 realtime_known=0 buffered_formats=0x0 buffered_known=0",
	} {
		a := &Adapter{status: Status{Healthy: true, Ready: true}, changed: make(chan struct{})}
		a.consume(line)
		if a.Status().Healthy || a.Status().Ready {
			t.Fatalf("unsupported route remained usable: %s", line)
		}
	}
}

func TestTransportResamplesPCMAndPreservesCanonicalAnchor(t *testing.T) {
	cfg := fixture(t, "resample")
	cfg.Transport = Transport{Rate: 44100, Timing: "ntp"}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = a.Prepare(ctx, 9, 48000); err != nil {
		t.Fatal(err)
	}
	input := make([]float32, 960*2)
	for i := 0; i < 960; i++ {
		input[i*2] = .25
		input[i*2+1] = -.25
	}
	if err = a.Write(ctx, audio.Frame{Epoch: 9, Sample: 48000, Data: input}); err != nil {
		t.Fatal(err)
	}
	finisher, ok := any(a).(interface{ Finish(context.Context) error })
	if !ok {
		t.Fatal("output cannot drain bounded FIR lookahead")
	}
	if err = finisher.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	if err = a.Metadata(ctx, Metadata{Title: "collect fixture PCM"}); err != nil {
		t.Fatal(err)
	}
	var pcm []byte
	for ctx.Err() == nil {
		pcm, err = os.ReadFile(cfg.Helper + ".order.pcm")
		if err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(pcm) != 3528 || binary.LittleEndian.Uint16(pcm[400:]) != 8192 || binary.LittleEndian.Uint16(pcm[402:]) != 57344 {
		t.Fatalf("wrong 44.1k PCM: bytes=%d", len(pcm))
	}
	anchor, err := a.Start(ctx, 9, time.UnixMilli(2000000))
	if err != nil {
		t.Fatal(err)
	}
	if anchor.Sample != 48000 || a.Status().Frames != 960 || a.Status().Transport.Rate != 44100 {
		t.Fatalf("canonical/transport identity lost: %+v", a.Status())
	}
	a.consume("[STATUS] anchor_corrected requested_unix_ms=2000000 from_unix_ms=2000100 at_unix_ms=2000150 content_cut_ms=1")
	a.consume("[STATUS] content_cut requested_ms=1 cut_ms=1 cut_bytes=28 drain_ms=0")
	if a.Status().Anchor.Sample != 48008 {
		t.Fatalf("cut not conservatively converted: %+v", a.Status().Anchor)
	}
	if err = a.Write(ctx, audio.Frame{Epoch: 9, Sample: 48960, Data: []float32{0, 0}}); err == nil {
		t.Fatal("accepted PCM after Finish")
	}
}

func TestPartialEOFReportsConservativeTransportDuration(t *testing.T) {
	cfg := fixture(t, "resample_partial")
	cfg.Transport = Transport{Rate: 44100, Timing: "ntp"}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = a.Prepare(ctx, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err = a.Write(ctx, audio.Frame{Epoch: 1, Data: make([]float32, 1922)}); err != nil {
		t.Fatal(err)
	}
	if err = a.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(a.Status())
	var status map[string]any
	json.Unmarshal(raw, &status)
	if status["DurationPaddingNS"] != float64(1843) {
		t.Fatalf("partial EOF rounding not exposed: %s", raw)
	}
	if err = a.Metadata(ctx, Metadata{Title: "collect fixture PCM"}); err != nil {
		t.Fatal(err)
	}
	var pcm []byte
	for ctx.Err() == nil {
		pcm, err = os.ReadFile(cfg.Helper + ".order.pcm")
		if err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil || len(pcm) != 3532 {
		t.Fatalf("partial EOF frames: %d %v", len(pcm), err)
	}
}

func TestMeasuredPTPReadinessLossFailsClosed(t *testing.T) {
	a := &Adapter{cfg: Config{Transport: Transport{Rate: 44100, Timing: "ptp"}}, status: Status{Healthy: true, Ready: true, ClockReady: true}, changed: make(chan struct{})}
	a.consume("[STATUS] clock_ready mode=ptp state=stalled streak_ms=0 exchanges=0 ready_in_ms=0 ready_at_unix_ms=0")
	if a.Status().Ready || a.Status().Healthy {
		t.Fatal("lost measured PTP clock retained PCM readiness")
	}
}
