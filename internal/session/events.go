package session

import (
	"github.com/khenrix/home-sound/internal/airplay"
)

// EventSource is optional. Events describes metadata and source transport state;
// it never seeks, resets samples or replaces Read's EOF/error/cancel semantics.
// Paused/playing transitions control the retained media timeline. Buffering is
// informational; EOF/error and explicit new selections retain their own semantics.
type SourceEvent struct {
	Sample    *uint64
	Kind      string
	Metadata  airplay.Metadata
	Transport string
}
type EventSource interface{ Events() <-chan SourceEvent }

func (s *running) sourceEvents(events <-chan SourceEvent) {
	for {
		select {
		case <-s.ctx.Done():
			return
		case event, ok := <-events:
			if !ok || s.ctx.Err() != nil {
				return
			}
			if event.Kind == "metadata" && event.Sample != nil {
				if s.waitMetadata(*event.Sample) != nil {
					return
				}
			}
			if event.Kind == "transport" {
				if event.Transport == "paused" {
					_ = s.pause()
				} else if event.Transport == "playing" {
					_ = s.resume()
				}
			}
			s.mu.Lock()
			switch event.Kind {
			case "metadata":
				valid := event.Metadata.Validate() == nil
				if valid {
					s.metadata = event.Metadata
					for _, m := range s.members {
						select {
						case m.wake <- struct{}{}:
						default:
						}
					}
					s.eventLocked("", "source_metadata")
				}
			case "transport":
				if event.Transport == "playing" || event.Transport == "paused" || event.Transport == "buffering" {
					s.sourceTransport = event.Transport
					s.eventLocked("", "source_"+event.Transport)
				}
			}
			s.mu.Unlock()
		}
	}
}

func (s *running) waitMetadata(sample uint64) error {
	for {
		s.mu.Lock()
		paused, changed := s.paused, s.changed
		s.mu.Unlock()
		if paused {
			select {
			case <-s.ctx.Done():
				return s.ctx.Err()
			case <-changed:
				continue
			}
		}
		if e := s.engine.clock.WaitUntil(s.ctx, s.rawTime(sample)); e != nil {
			return e
		}
		s.mu.Lock()
		paused = s.paused
		s.mu.Unlock()
		if !paused && s.engine.clock.Now() >= s.rawTime(sample) {
			return s.ctx.Err()
		}
	}
}
