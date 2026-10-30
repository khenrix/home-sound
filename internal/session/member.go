package session

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/volume"
)

var backoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second}

func (s *running) memberLoop(m *member) {
	delayIndex := 0
	var episode time.Duration
	var recovering bool
	lead := time.Second
	for {
		s.mu.Lock()
		if s.ctx.Err() != nil || m.excluded {
			s.mu.Unlock()
			return
		}
		if s.muted || s.paused && !s.resuming {
			m.state.State = "muted"
			if s.paused {
				m.state.State = "paused"
			}
			s.mu.Unlock()
			select {
			case <-s.ctx.Done():
				return
			case <-m.wake:
				continue
			}
		}
		if s.engine.isQuarantined(m.id, s.engine.cfg.Devices[m.id]) {
			m.state.State = "quarantined"
			m.state.LastError = "stop_failed"
			s.mu.Unlock()
			return
		}
		if recovering && s.sourceState != "reading" && !s.resuming && !m.resumePending && !m.reconfigure {
			m.state.State = "ended"
			s.mu.Unlock()
			return
		}
		resume := m.resumePending || s.resuming
		firstResume := s.pausedSample
		m.resumePending = false
		m.reconfiguring = m.reconfigure
		m.reconfigure = false
		m.attemptDone = make(chan struct{})
		m.state.Attempts++
		m.state.State = "preparing"
		s.mu.Unlock()
		attemptStart := s.engine.clock.Now()
		first := s.joinSample(lead)
		if resume {
			first = firstResume
		}
		deadline := attemptStart + 20*time.Second
		if recovering && episode+120*time.Second < deadline {
			deadline = episode + 120*time.Second
		}
		err, stable, preparedIn := s.attempt(m, first, deadline, resume)
		if preparedIn+time.Second > lead {
			lead = preparedIn + time.Second
		}
		s.mu.Lock()
		if s.ctx.Err() != nil || m.excluded {
			s.mu.Unlock()
			return
		}
		if m.state.LastError == "stop_failed" {
			// A failed flush leaves the receiver's buffered gain unknown.
			m.state.State = "stop_failed"
			s.mu.Unlock()
			return
		}
		if s.muted || s.paused && !s.resuming {
			s.mu.Unlock()
			recovering = false
			delayIndex = 0
			continue
		}
		if m.reconfigure || m.resumePending {
			if stable {
				recovering = false
				delayIndex = 0
			}
			s.mu.Unlock()
			continue
		}
		if err == nil {
			s.mu.Unlock()
			return
		}
		if s.sourceState != "reading" {
			m.state.State = "ended"
			s.mu.Unlock()
			return
		}
		m.state.Errors++
		m.state.LastError = "output_failed"
		m.state.State = "recovering"
		s.eventLocked(m.id, "output_failed")
		if stable {
			recovering = true
			episode = s.engine.clock.Now()
			delayIndex = 0
		}
		if !recovering {
			episode = attemptStart
			recovering = true
		}
		deadline = episode + 120*time.Second
		m.state.RecoveryUntil = deadline
		now := s.engine.clock.Now()
		wait := backoff[min(delayIndex, len(backoff)-1)]
		delayIndex++
		if now+wait >= deadline {
			m.state.State = "recovery_exhausted"
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		waitCtx, cancelWait := context.WithCancel(s.recovery)
		s.mu.Lock()
		m.cancel = cancelWait
		if s.paused && !s.resuming || s.muted || m.excluded {
			cancelWait()
		}
		s.mu.Unlock()
		waitErr := s.engine.clock.WaitUntil(waitCtx, now+wait)
		cancelWait()
		if waitErr != nil {
			if s.recovery.Err() != nil {
				return
			}
			recovering = false
			delayIndex = 0
		}

	}
}

func (s *running) attempt(m *member, first uint64, deadline time.Duration, resume bool) (err error, stable bool, preparedIn time.Duration) {
	started := false
	var startedAt time.Duration
	defer func() { stable = started && s.engine.clock.Now()-startedAt >= 30*time.Second }()
	ctx, cancel := context.WithCancel(s.ctx)
	s.mu.Lock()
	m.cancel = cancel
	m.context = ctx
	token := m.state.Attempts
	done := m.attemptDone
	if m.reconfigure || s.paused && !s.resuming || token > 1 && s.sourceState != "reading" && !resume && !m.reconfiguring {
		cancel()
	}
	s.mu.Unlock()
	defer close(done)
	defer cancel()
	deadlineCtx, stopDeadline := context.WithCancel(ctx)
	defer stopDeadline()
	go func() {
		if s.engine.clock.WaitUntil(deadlineCtx, deadline) == nil {
			cancel()
		}
	}()
	req := DeviceRequest{ID: m.id, Device: s.engine.cfg.Devices[m.id], Epoch: s.epoch, ClockOffsetNS: s.engine.offset, NativeChanged: func(n int) { s.engine.nativeChanged(s.epoch, m.id, token, n) }}
	before := s.engine.clock.Now()
	if ctx.Err() != nil {
		return ctx.Err(), false, 0
	}
	out, err := s.engine.factory(ctx, req)
	if err != nil {
		return err, false, s.engine.clock.Now() - before
	}
	s.mu.Lock()
	m.output = out
	s.mu.Unlock()
	defer func() {
		stopErr := out.Stop()
		if stopErr != nil {
			s.engine.quarantineOutput(m.id, req.Device, out)
		}
		last := out.Status()
		s.mu.Lock()
		if stopErr != nil {
			m.state.Errors++
			m.state.LastError = "stop_failed"
		}
		if s.paused {
			m.state.State = "paused"
		} else if s.muted {
			m.state.State = "muted"
		}
		m.state.Transport = last
		m.queue = nil
		m.output = nil
		s.mu.Unlock()
	}()
	if err = out.Prepare(ctx, s.epoch, first); err != nil {
		return err, false, s.engine.clock.Now() - before
	}
	preparedIn = s.engine.clock.Now() - before

	s.mu.Lock()
	plan := m.plan
	m.feedback = volume.NewFeedback(s.engine.cfg.Profiles[m.id], s.engine.cfg.Devices[m.id].RoomTrimDB)
	m.feedback.Expect(plan.Native)
	s.mu.Unlock()
	if err = out.SetNative(ctx, plan.Native); err != nil {
		return err, false, preparedIn
	}
	s.mu.Lock()
	metadata := s.metadata
	s.mu.Unlock()
	if err = out.Metadata(ctx, metadata); err != nil {
		return err, false, preparedIn
	}
	preparedIn = s.engine.clock.Now() - before
	if resume {
		if err = s.commitResume(ctx); err != nil {
			return err, false, preparedIn
		}
	}
	if s.rawTime(first) < s.engine.clock.Now()+250*time.Millisecond {
		return errors.New("prepared anchor is no longer feasible"), false, preparedIn
	}
	q := make(chan audio.Frame, queueChunks)
	s.mu.Lock()
	if len(s.replay) > 0 && first < s.replay[0].Sample {
		s.mu.Unlock()
		return errors.New("replay expired"), false, preparedIn
	}
	for _, f := range s.replay {
		if f.Sample+uint64(len(f.Data)/2) > first {
			f = fromSample(f, first)
			select {
			case q <- f:
			default:
				s.mu.Unlock()
				return errors.New("replay exceeds member queue"), false, preparedIn
			}
		}
	}
	m.queue = q
	s.mu.Unlock()
	next := first
	for {
		select {
		case <-ctx.Done():
			return ctx.Err(), started, preparedIn
		case <-m.wake:
			s.mu.Lock()
			desired := m.plan
			nextMetadata := s.metadata
			s.mu.Unlock()
			if desired.Native != plan.Native {
				// The control path canceled this attempt; never apply a native
				// step to PCM buffered with this output's earlier attenuation.
				return context.Canceled, started, preparedIn
			}
			plan = desired
			if metadata != nextMetadata {
				if err = out.Metadata(ctx, nextMetadata); err != nil {
					return err, started, preparedIn
				}
				metadata = nextMetadata
			}
		case f := <-q:
			if f.Sample+uint64(len(f.Data)/2) <= first {
				continue
			}
			f = fromSample(f, first)
			if f.Sample != next {
				return errors.New("member queue discontinuity"), started, preparedIn
			}
			// Gain acts on this member's copy; attenuation never boosts another member.
			data := make([]float32, len(f.Data))
			gain := float32(math.Pow(10, plan.AttenuationDB/20))
			for i, v := range f.Data {
				data[i] = v * gain
			}
			f.Data = data
			if err = out.Write(ctx, f); err != nil {
				return err, started, preparedIn
			}
			next += uint64(len(f.Data) / 2)
			s.mu.Lock()
			m.state.SubmittedFrames += uint64(len(f.Data) / 2)
			s.mu.Unlock()
			if !started {
				anchor, startErr := out.Start(ctx, s.epoch, s.presentation(first))
				if startErr != nil {
					return startErr, false, preparedIn
				}
				if anchor.Epoch != s.epoch || anchor.Sample < first || anchor.At.IsZero() {
					return errors.New("invalid acknowledged anchor"), false, preparedIn
				}
				started = true
				startedAt = s.engine.clock.Now()
				stopDeadline()
				s.mu.Lock()
				m.state.State = "streaming"
				m.state.RecoveryUntil = 0
				s.eventLocked(m.id, "transport_started")
				s.mu.Unlock()
			}
			// Preserve the adapter's corrected At/Sample and pending-content-cut evidence.
			if !out.Status().Healthy {
				return errors.New("output transport unhealthy"), started, preparedIn
			}
		}
	}
}
