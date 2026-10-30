package session

import (
	"testing"
	"time"

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

func TestSourceMetadataWaitsForContentBoundary(t *testing.T) {
	e, c, _ := setup(t)
	s := &eventSource{events: make(chan SourceEvent, 8)}
	selectSource(t, e, config.AllSpeakers, s)
	eventually(t, func() bool { return e.Status().NextSample >= 48000 })
	sample := uint64(24000)
	s.events <- SourceEvent{Kind: "metadata", Metadata: airplay.Metadata{Title: "next track"}, Sample: &sample}
	time.Sleep(20 * time.Millisecond)
	if e.Status().Metadata.Title == "next track" {
		t.Fatal("metadata jumped ahead of content boundary")
	}
	c.advance(1500 * time.Millisecond)
	eventually(t, func() bool { return e.Status().Metadata.Title == "next track" })
}

type artworkSource struct {
	eventSource
	metadata airplay.Metadata
}

func (s *artworkSource) Metadata() airplay.Metadata { return s.metadata }
func TestArtworkReachesStatusAndOutputAndClearsOnReplacement(t *testing.T) {
	e, _, f := setup(t)
	art := airplay.Metadata{Title: "first", ArtworkID: "spotify:image:0123456789abcdef0123456789abcdef01234567", ArtworkURL: "https://i.scdn.co/image/0123456789abcdef0123456789abcdef01234567"}
	first := &artworkSource{eventSource: eventSource{events: make(chan SourceEvent, 4)}, metadata: art}
	selectSource(t, e, config.AllSpeakers, first)
	eventually(t, func() bool {
		status := e.Status()
		o := f.latest("hall")
		if o == nil {
			return false
		}
		o.mu.Lock()
		defer o.mu.Unlock()
		return o.metadata == art && status.Metadata == art
	})
	invalid := art
	invalid.ArtworkURL = "https://127.0.0.1/private"
	first.events <- SourceEvent{Kind: "metadata", Metadata: invalid}
	time.Sleep(20 * time.Millisecond)
	if e.Status().Metadata != art {
		t.Fatal("invalid artwork entered status")
	}
	next := &artworkSource{eventSource: eventSource{events: make(chan SourceEvent, 4)}, metadata: airplay.Metadata{Title: "clear"}}
	selectSource(t, e, config.AllSpeakers, next)
	first.events <- SourceEvent{Kind: "metadata", Metadata: art}
	eventually(t, func() bool {
		status := e.Status()
		o := f.latest("hall")
		if o == nil {
			return false
		}
		o.mu.Lock()
		defer o.mu.Unlock()
		return o.metadata.Title == "clear" && o.metadata.ArtworkURL == "" && status.Metadata.ArtworkID == ""
	})
	nextArt := airplay.Metadata{Title: "video", ArtworkID: "youtube:video:abcdefghijk", ArtworkURL: "https://i.ytimg.com/vi/abcdefghijk/hqdefault.jpg"}
	next.events <- SourceEvent{Kind: "metadata", Metadata: nextArt}
	eventually(t, func() bool {
		status := e.Status()
		o := f.latest("hall")
		o.mu.Lock()
		defer o.mu.Unlock()
		return o.metadata == nextArt && status.Metadata == nextArt
	})
	next.events <- SourceEvent{Kind: "metadata", Metadata: airplay.Metadata{Title: "clear again"}}
	eventually(t, func() bool { return e.Status().Metadata.ArtworkURL == "" })
}
