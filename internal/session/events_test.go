package session

import (
	"testing"

	"github.com/khenrix/home-sound/internal/airplay"
	"github.com/khenrix/home-sound/internal/config"
)

type eventSource struct {
	testSource
	events chan SourceEvent
}

func (s *eventSource) Events() <-chan SourceEvent { return s.events }
func TestSourceEventsUpdateMetadataAndTransportWithoutChangingTimeline(t *testing.T) {
	e, _, f := setup(t)
	src := &eventSource{events: make(chan SourceEvent, 4)}
	selectSource(t, e, config.AllSpeakers, src)
	eventually(t, func() bool { return f.latest("hall") != nil && f.latest("hall").Status().Frames > 0 })
	at := e.PresentationTime(48000)
	src.events <- SourceEvent{Kind: "metadata", Metadata: airplay.Metadata{Title: "next track", Artist: "fixture"}}
	src.events <- SourceEvent{Kind: "transport", Transport: "buffering"}
	eventually(t, func() bool {
		return e.Status().SourceTransport == "buffering" && e.Status().Metadata.Title == "next track"
	})
	eventually(t, func() bool {
		o := f.latest("hall")
		o.mu.Lock()
		defer o.mu.Unlock()
		return o.metadata.Title == "next track"
	})
	if !e.PresentationTime(48000).Equal(at) {
		t.Fatal("source event moved sample timeline")
	}
	src.events <- SourceEvent{Kind: "transport", Transport: "fabricated-ready"}
	close(src.events)
	e.Stop()
	if e.Status().SourceTransport != "buffering" {
		t.Fatal("unknown transport state accepted")
	}
}
