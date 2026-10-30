package session

import (
	"context"
	"errors"
	"time"

	"github.com/khenrix/home-sound/internal/audio"
)

func (e *Engine) Pause() error {
	s := e.currentSession()
	if s == nil {
		return errors.New("no session")
	}
	return s.pause()
}
func (e *Engine) Resume() error {
	s := e.currentSession()
	if s == nil {
		return errors.New("no session")
	}
	return s.resume()
}

func (s *running) positionLocked() uint64 {
	if s.paused {
		return s.pausedSample
	}
	s.timelineMu.RLock()
	delta := s.engine.clock.Now() - s.origin
	s.timelineMu.RUnlock()
	position := s.selection.FirstSample
	if delta > 0 {
		position = uint64(delta/time.Second)*48000 + uint64(delta%time.Second)*48000/uint64(time.Second)
	}
	return min(s.next, max(position, s.selection.FirstSample, s.resumeFloor))
}
func (s *running) pause() error {
	s.transportMu.Lock()
	defer s.transportMu.Unlock()
	s.mu.Lock()
	if !s.active || s.ctx.Err() != nil {
		s.mu.Unlock()
		return errors.New("no active session")
	}
	if s.paused && !s.resuming {
		s.mu.Unlock()
		return nil
	}
	s.pausedSample = s.positionLocked()
	s.paused = true
	s.resuming = false
	var done []chan struct{}
	for _, m := range s.members {
		m.resumePending = false
		if m.cancel != nil {
			m.cancel()
		}
		if m.attemptDone != nil {
			done = append(done, m.attemptDone)
		}
		select {
		case m.wake <- struct{}{}:
		default:
		}
	}
	s.signalLocked()
	s.eventLocked("", "paused")
	s.mu.Unlock()
	// Returning from pause means every previously active adapter has stopped.
	for _, ch := range done {
		select {
		case <-ch:
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	}
	return nil
}
func (s *running) resume() error {
	s.transportMu.Lock()
	defer s.transportMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active || s.ctx.Err() != nil {
		return errors.New("no active session")
	}
	if !s.paused || s.resuming {
		return nil
	}
	s.resuming = true
	if s.muted {
		s.scheduleResumeLocked()
		return nil
	}
	for _, m := range s.members {
		if !m.excluded {
			m.resumePending = true
			select {
			case m.wake <- struct{}{}:
			default:
			}
		}
	}
	s.signalLocked()
	s.eventLocked("", "resuming")
	return nil
}

// The first prepared member establishes the resume schedule. Preparation time
// never consumes the paused media; the global raw-to-Unix mapping stays frozen.
func (s *running) commitResume(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if s.paused && !s.resuming {
		return errors.New("resume canceled by pause")
	}
	if s.resuming {
		s.scheduleResumeLocked()
	}
	return nil
}
func (s *running) waitProduction(end uint64) error {
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
		at := s.rawTime(end) - sendAhead + s.inputReserve
		if err := s.engine.clock.WaitUntil(s.ctx, at); err != nil {
			return err
		}
		s.mu.Lock()
		paused = s.paused
		s.mu.Unlock()
		if !paused && s.engine.clock.Now() >= s.rawTime(end)-sendAhead+s.inputReserve {
			return s.ctx.Err()
		}
	}
}
func fromSample(f audio.Frame, first uint64) audio.Frame {
	if f.Sample < first {
		f.Data = f.Data[(first-f.Sample)*2:]
		f.Sample = first
	}
	return f
}

func (s *running) scheduleResumeLocked() {
	at := s.engine.clock.Now() + time.Second
	d, _ := audio.SampleTime(s.pausedSample, 48000)
	s.timelineMu.Lock()
	s.origin = at - d
	s.timelineMu.Unlock()
	s.resumeFloor = s.pausedSample
	s.resumeAt = at
	s.paused = false
	s.resuming = false
	s.signalLocked()
	s.eventLocked("", "resume_scheduled")
}
