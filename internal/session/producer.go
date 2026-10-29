package session

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/khenrix/home-sound/internal/audio"
)

func (s *running) run() {
	var workers sync.WaitGroup
	if source, ok := s.selection.Source.(EventSource); ok {
		events := source.Events()
		workers.Go(func() { s.sourceEvents(events) })
	}
	for _, m := range s.members {
		workers.Go(func() { s.memberLoop(m) })
	}
	err := s.produce()
	s.mu.Lock()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			s.sourceState = "stopped"
		} else {
			s.sourceState = "error"
		}
		s.eventLocked("", "source_"+s.sourceState)
	} else {
		s.sourceState = "ended"
		s.eventLocked("", "source_end")
	}
	s.cancelRecovery()
	for _, m := range s.members {
		if m.state.Attempts > 1 && m.state.State == "preparing" && !s.resuming && !m.resumePending && m.cancel != nil {
			m.cancel()
		}
	}
	s.signalLocked()
	end := s.next
	s.mu.Unlock()
	if err == nil {
		// Drain only already-produced audio; no recovery can begin after EOF.
		s.drain(end)
	}
	s.cancel()
	workers.Wait()
	_ = s.selection.Source.Close()
	s.mu.Lock()
	s.active = false
	for _, m := range s.members {
		if !m.excluded && m.state.State != "recovery_exhausted" {
			m.state.State = "stopped"
			if s.sourceState == "ended" {
				m.state.State = "ended"
			}
		}
	}
	s.replay = nil
	s.signalLocked()
	s.mu.Unlock()
	close(s.done)
}
func (s *running) produce() error {
	next := s.selection.FirstSample
	input := chunkReader{session: s, next: next}
	for {
		// Include the entire maximum-sized frame in the three-second send-ahead.
		if err := s.waitProduction(next + chunkFrames); err != nil {
			return err
		}
		f, err := input.read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if f.Sample != next || len(f.Data) == 0 || len(f.Data) > chunkFrames*2 {
			return errors.New("source produced empty, oversized or noncontiguous frame")
		}
		if err = f.Validate(audio.Canonical); err != nil {
			return err
		}
		count := uint64(len(f.Data) / 2)
		if next+count < next {
			return errors.New("source sample overflow")
		}
		if _, err = audio.SampleTime(next+count, 48000); err != nil {
			return err
		}
		if err = s.waitProduction(next + count); err != nil {
			return err
		}
		f.Epoch = s.epoch
		// Isolate the source's storage from per-output gain processing.
		f.Data = append([]float32(nil), f.Data...)
		s.mu.Lock()
		s.replay = append(s.replay, f)
		total := uint64(0)
		for _, v := range s.replay {
			total += uint64(len(v.Data) / 2)
		}
		for total > 192000 {
			total -= uint64(len(s.replay[0].Data) / 2)
			s.replay[0] = audio.Frame{}
			s.replay = s.replay[1:]
		}
		next += count
		s.next = next
		for _, m := range s.members {
			if m.queue == nil || m.excluded || s.muted {
				continue
			}
			select {
			case m.queue <- f:
			default:
				m.state.QueueOverflows++
				m.state.LastError = "queue_overflow"
				m.cancel()
				m.queue = nil
				s.eventLocked(m.id, "queue_overflow")
			}
		}
		s.signalLocked()
		s.mu.Unlock()
	}
}

func (s *running) joinSample(lead time.Duration) uint64 {
	s.timelineMu.RLock()
	origin := s.origin
	s.timelineMu.RUnlock()
	delta := s.engine.clock.Now() + lead - origin
	if delta <= 0 {
		return s.selection.FirstSample
	}
	// Round upward to a 20-ms boundary relative to the source's starting sample.
	frames := uint64(delta/time.Second)*48000 + uint64(delta%time.Second)*48000/uint64(time.Second)
	if frames < s.selection.FirstSample {
		return s.selection.FirstSample
	}
	return s.selection.FirstSample + ((frames-s.selection.FirstSample+chunkFrames-1)/chunkFrames)*chunkFrames
}

// Re-evaluate at the prospective end because the helper can acknowledge a later
// anchor or actual content cut after START has returned.
func (s *running) drain(end uint64) {
	for {
		s.mu.Lock()
		paused, changed := s.paused, s.changed
		s.mu.Unlock()
		if paused {
			select {
			case <-s.ctx.Done():
				return
			case <-changed:
				continue
			}
		}
		until := s.rawTime(end)
		s.mu.Lock()
		outputs := make([]Output, 0, len(s.members))
		for _, m := range s.members {
			if m.output != nil {
				outputs = append(outputs, m.output)
			}
		}
		s.mu.Unlock()
		for _, o := range outputs {
			a := o.Status().Anchor
			if a.At.IsZero() || a.Sample > end {
				continue
			}
			length, err := audio.SampleTime(end-a.Sample, 48000)
			if err != nil {
				continue
			}
			actual := time.Duration(a.At.UnixNano()-s.engine.offset) + length
			if actual > until {
				until = actual
			}
		}
		if s.engine.clock.Now() >= until {
			return
		}
		if s.engine.clock.WaitUntil(s.ctx, until) != nil {
			return
		}
	}
}

type chunkReader struct {
	session *running
	next    uint64
	pending []float32
	eof     bool
}

// Normalize transport-specific chunk boundaries to the shared 20-ms join grid.
// Input reads themselves are paced, including any not-yet-emitted partial frame.
func (r *chunkReader) read() (audio.Frame, error) {
	start := r.next - uint64(len(r.pending)/2)
	for len(r.pending) < chunkFrames*2 && !r.eof {
		if _, err := audio.SampleTime(r.next+chunkFrames, 48000); err != nil {
			return audio.Frame{}, err
		}
		if err := r.session.engine.clock.WaitUntil(r.session.ctx, r.session.rawTime(r.next+chunkFrames)-sendAhead); err != nil {
			return audio.Frame{}, err
		}
		f, err := r.session.selection.Source.Read(r.session.ctx)
		if errors.Is(err, io.EOF) {
			r.eof = true
			break
		}
		if err != nil {
			return audio.Frame{}, err
		}
		if f.Sample != r.next || len(f.Data) == 0 || len(f.Data) > chunkFrames*2 {
			return audio.Frame{}, errors.New("source produced empty, oversized or noncontiguous frame")
		}
		if err = f.Validate(audio.Canonical); err != nil {
			return audio.Frame{}, err
		}
		r.pending = append(r.pending, f.Data...)
		r.next += uint64(len(f.Data) / 2)
	}
	if len(r.pending) == 0 {
		return audio.Frame{}, io.EOF
	}
	count := min(len(r.pending), chunkFrames*2)
	data := append([]float32(nil), r.pending[:count]...)
	r.pending = r.pending[count:]
	return audio.Frame{Sample: start, Data: data}, nil
}
