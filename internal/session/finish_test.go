package session

import (
	"context"
	"errors"
	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/config"
	"io"
	"testing"
	"time"
)

type finalPartialSource struct {
	testSource
	index int
}

func (s *finalPartialSource) Read(ctx context.Context) (audio.Frame, error) {
	s.index++
	switch s.index {
	case 1:
		return audio.Frame{Data: make([]float32, 1920)}, nil
	case 2:
		return audio.Frame{Sample: 960, Data: []float32{.2, .2}}, nil
	}
	return audio.Frame{}, io.EOF
}

type finishingOutput struct {
	*sink
	finished chan uint64
	failure  bool
	block    bool
}

func (o *finishingOutput) Finish(ctx context.Context) error {
	o.finished <- o.Status().Frames
	if o.block {
		<-ctx.Done()
		return ctx.Err()
	}
	if o.failure {
		return errors.New("fixture drain failed")
	}
	return nil
}
func TestFinalPartialFrameFinishesBeforeReceiverStop(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failure], func(t *testing.T) {
			e, c, _ := setup(t)
			finished := make(chan uint64, 3)
			e.factory = func(context.Context, DeviceRequest) (Output, error) {
				return &finishingOutput{sink: &sink{}, finished: finished, failure: failure}, nil
			}
			selectSource(t, e, config.AllSpeakers, &finalPartialSource{})
			for i := 0; i < 3; i++ {
				select {
				case n := <-finished:
					if n != 961 {
						t.Fatalf("Finish before final partial frame: %d", n)
					}
				case <-time.After(time.Second):
					t.Fatal("EOF did not drain output lookahead")
				}
			}
			if failure {
				eventually(t, func() bool { return e.Status().Members["kitchen"].LastError == "finish_failed" })
			} else if !e.Status().Active {
				t.Fatal("Finish prematurely stopped receiver")
			}
			c.advance(5 * time.Second)
		})
	}
}
func TestStopCancelsBlockedFinish(t *testing.T) {
	e, _, _ := setup(t)
	finished := make(chan uint64, 3)
	e.factory = func(context.Context, DeviceRequest) (Output, error) {
		return &finishingOutput{sink: &sink{}, finished: finished, block: true}, nil
	}
	selectSource(t, e, config.AllSpeakers, &finalPartialSource{})
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("Finish not entered")
	}
	done := make(chan struct{})
	go func() { e.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("blocked Finish ignored cancellation")
	}
}

func TestEOFFinishReplaysAfterExplicitNativeReconfiguration(t *testing.T) {
	e, _, _ := setup(t)
	finished := make(chan uint64, 6)
	e.factory = func(context.Context, DeviceRequest) (Output, error) {
		return &finishingOutput{sink: &sink{}, finished: finished}, nil
	}
	selectSource(t, e, config.AllSpeakers, &finalPartialSource{})
	receive := func() {
		t.Helper()
		for i := 0; i < 3; i++ {
			select {
			case n := <-finished:
				if n != 961 {
					t.Fatalf("lost EOF partial tail: %d", n)
				}
			case <-time.After(time.Second):
				t.Fatal("EOF attempt did not Finish")
			}
		}
	}
	receive()
	anchor := e.PresentationTime(0)
	if err := e.SetTargetDB(-30); err != nil {
		t.Fatal(err)
	}
	receive()
	if !e.PresentationTime(0).Equal(anchor) {
		t.Fatal("EOF reconfiguration moved canonical timeline")
	}
}

func TestContentCutRoundingUncertaintyExtendsDrain(t *testing.T) {
	e, c, f := setup(t)
	selectSource(t, e, config.AllSpeakers, &testSource{limit: 48000})
	eventually(t, func() bool {
		return f.latest("hall") != nil && f.latest("hall").Status().Frames == 48000 && e.Status().SourceState == "ended"
	})
	out := f.latest("hall")
	out.mu.Lock()
	out.anchor.At = out.anchor.At.Add(2 * time.Second)
	out.anchor.SampleUncertaintyNS = 7937
	out.mu.Unlock()
	c.advance(2 * time.Second)
	eventually(t, func() bool { return c.waiting(104*time.Second + 7937*time.Nanosecond) })
}
