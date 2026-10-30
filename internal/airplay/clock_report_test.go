package airplay

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/webos"
)

// The optional executable is built from the pinned C CLI by clock.py. Go does
// not manufacture its status lines; only the receiver measurement is controlled.
func actualClockReporter(t *testing.T, ctx context.Context, a *Adapter) func(string) int {
	t.Helper()
	path := os.Getenv("HOME_SOUND_C_CLOCK_FIXTURE")
	if path == "" {
		t.Skip("actual C integration requires helpers/airplay/tests/clock.py --output and HOME_SOUND_C_CLOCK_FIXTURE")
	}
	cmd := exec.CommandContext(ctx, path, "stream")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { input.Close(); cmd.Wait() })
	reader := bufio.NewReader(output)
	return func(state string) int {
		t.Helper()
		if _, err := fmt.Fprintln(input, state); err != nil {
			t.Fatal(err)
		}
		reports := 0
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(line, "[FIXTURE]") {
				return reports
			}
			if strings.HasPrefix(line, "[STATUS]") {
				reports++
				a.consume(strings.TrimSpace(line))
			}
		}
	}
}

func TestActualCReceiverLossStopsOnlyDependentOutput(t *testing.T) {
	if os.Getenv("HOME_SOUND_C_CLOCK_FIXTURE") == "" {
		t.Skip("actual C reporter executable not configured")
	}
	cfg := fixture(t, "ptp_stream")
	offset := int64(123)
	cfg.ClockOffsetNS = &offset
	cfg.Transport = Transport{Rate: 44100, Timing: "ptp"}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = a.Prepare(ctx, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err = b.Prepare(ctx, 2, 0); err != nil {
		t.Fatal(err)
	}
	if a.lease.daemon != b.lease.daemon {
		t.Fatal("outputs did not share the owned daemon")
	}
	tickA, tickB := actualClockReporter(t, ctx, a), actualClockReporter(t, ctx, b)
	if tickA("ready") != 1 || tickB("ready") != 1 {
		t.Fatal("actual C did not report initial readiness")
	}
	tickA("lost")
	if a.Status().Healthy || a.Status().Ready {
		t.Fatal("actual C receiver loss left output accepting PCM")
	}
	if err = a.Write(ctx, audio.Frame{Epoch: 1, Data: []float32{.1, .1}}); err == nil {
		t.Fatal("PCM accepted after actual receiver loss")
	}
	if _, err = a.Start(ctx, 1, time.UnixMilli(2000000)); err == nil {
		t.Fatal("START accepted after actual receiver loss")
	}
	select {
	case <-a.done:
	case <-ctx.Done():
		t.Fatal("receiver loss did not terminate the dependent helper")
	}
	if err = a.Stop(); err != nil {
		t.Fatal(err)
	}
	tickB("ready")
	if !b.Status().Healthy || !b.Status().ClockReady || !b.Status().PTPDaemonHealthy {
		t.Fatal("one receiver's loss stopped healthy receiver/daemon")
	}
	if err = b.Write(ctx, audio.Frame{Epoch: 2, Data: make([]float32, 1920)}); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Start(ctx, 2, time.UnixMilli(2000000)); err != nil {
		t.Fatal(err)
	}
}

type clockLossDuringGate struct {
	volumeFixture
	adapter *Adapter
}

func (v *clockLossDuringGate) SetVolume(ctx context.Context, level int) error {
	if err := v.volumeFixture.SetVolume(ctx, level); err != nil {
		return err
	}
	if v.calls == 2 {
		v.adapter.consume("[STATUS] clock_ready mode=ptp state=cold streak_ms=0 exchanges=0 ready_in_ms=0 ready_at_unix_ms=0")
	}
	return nil
}
func TestPrepareRechecksClockAfterNativeGate(t *testing.T) {
	cfg := fixture(t, "ptp_stream")
	offset := int64(123)
	cfg.ClockOffsetNS = &offset
	cfg.Transport = Transport{Rate: 44100, Timing: "ptp"}
	cfg.InitialVolume = 0
	native := &clockLossDuringGate{volumeFixture: volumeFixture{updates: make(chan webos.Volume, 1)}}
	native.updates <- webos.Volume{Valid: true, Level: 10}
	cfg.LG = native
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	native.adapter = a
	defer a.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = a.Prepare(ctx, 1, 0); err == nil {
		t.Fatal("Prepare committed Ready after receiver clock was lost during native gate")
	}
	if native.calls != 2 {
		t.Fatal("fixture did not reach post-readiness native gate")
	}
	if a.Status().Ready {
		t.Fatal("lost receiver readiness was published")
	}
}
