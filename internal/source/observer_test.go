package source

import (
	"context"
	"github.com/khenrix/home-sound/internal/airplay"
	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/session"
	"testing"
	"time"
)

type endless struct{ next uint64 }

func (*endless) Format() audio.Format       { return audio.Canonical }
func (*endless) Metadata() airplay.Metadata { return airplay.Metadata{Title: "fixture"} }
func (*endless) Close() error               { return nil }
func (s *endless) Read(ctx context.Context) (audio.Frame, error) {
	if ctx.Err() != nil {
		return audio.Frame{}, ctx.Err()
	}
	f := audio.Frame{Sample: s.next, Data: make([]float32, 1920)}
	for i := range f.Data {
		f.Data[i] = .25
	}
	s.next += 960
	return f, nil
}
func TestObserverRealtimePauseAndBound(t *testing.T) {
	o := NewObserver()
	defer o.Stop()
	if _, e := o.Select(session.Selection{Group: "All Speakers", Source: &endless{}}); e != nil {
		t.Fatal(e)
	}
	time.Sleep(65 * time.Millisecond)
	a := o.Snapshot()
	if a.Frames < 1920 || a.Frames > 4800 || a.Peak != .25 || a.RMS != .25 {
		t.Fatalf("not realtime bounded PCM evidence: %+v", a)
	}
	if e := o.Pause(); e != nil {
		t.Fatal(e)
	}
	n := o.Snapshot().Frames
	time.Sleep(45 * time.Millisecond)
	if o.Snapshot().Frames != n {
		t.Fatal("pause consumed source")
	}
	o.Resume()
	time.Sleep(30 * time.Millisecond)
	if o.Snapshot().Frames <= n {
		t.Fatal("resume stalled")
	}
}

type processingSource struct{ endless }

func (s *processingSource) Read(ctx context.Context) (audio.Frame, error) {
	timer := time.NewTimer(8 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return audio.Frame{}, ctx.Err()
	case <-timer.C:
	}
	return s.endless.Read(ctx)
}
func TestObserverPacingIncludesSourceProcessingTime(t *testing.T) {
	o := NewObserver()
	defer o.Stop()
	o.Select(session.Selection{Group: "All Speakers", Source: &processingSource{}})
	time.Sleep(250 * time.Millisecond)
	if got := o.Snapshot().Frames; got < 10560 || got > 13440 {
		t.Fatalf("processing time accumulated into playback clock: %d frames", got)
	}
}

type stalledSource struct {
	endless
	first bool
}

func (s *stalledSource) Read(ctx context.Context) (audio.Frame, error) {
	if !s.first {
		s.first = true
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return audio.Frame{}, ctx.Err()
		case <-timer.C:
		}
	}
	return s.endless.Read(ctx)
}
func TestObserverDoesNotBurstAfterInputStall(t *testing.T) {
	o := NewObserver()
	defer o.Stop()
	o.Select(session.Selection{Group: "All Speakers", Source: &stalledSource{}})
	time.Sleep(115 * time.Millisecond)
	if got := o.Snapshot().Frames; got > 1920 {
		t.Fatalf("probe consumed catch-up burst after stall: %d frames", got)
	}
}
