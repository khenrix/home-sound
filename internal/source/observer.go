package source

import (
	"context"
	"errors"
	"github.com/khenrix/home-sound/internal/airplay"
	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/session"
	"io"
	"math"
	"sync"
	"time"
)

type Observation struct {
	Epoch     uint64           `json:"epoch"`
	Group     string           `json:"group"`
	Format    audio.Format     `json:"format"`
	Frames    uint64           `json:"frames"`
	Peak      float64          `json:"peak"`
	RMS       float64          `json:"rms"`
	Metadata  airplay.Metadata `json:"metadata"`
	Transport string           `json:"transport"`
}

// Observer consumes one frame at sample-derived real-time intervals. It has no
// output factory, acoustic profiles or path to a speaker. Statistics are bounded.
type Observer struct {
	op               sync.Mutex
	mu               sync.Mutex
	epoch            uint64
	state            Observation
	sum              float64
	paused           bool
	cancel           context.CancelFunc
	done             chan struct{}
	history          []Observation
	origin, pausedAt time.Time
}

func NewObserver() *Observer { return &Observer{} }
func (o *Observer) Select(s session.Selection) (uint64, error) {
	o.op.Lock()
	defer o.op.Unlock()
	if s.Source == nil || s.Source.Format() != audio.Canonical {
		return 0, errors.New("canonical probe source required")
	}
	o.stop()
	ctx, cancel := context.WithCancel(context.Background())
	o.mu.Lock()
	o.epoch++
	epoch := o.epoch
	o.state = Observation{Epoch: epoch, Group: s.Group, Format: s.Source.Format(), Metadata: s.Source.Metadata(), Transport: "playing"}
	o.sum = 0
	o.paused = false
	o.origin = time.Now()
	o.cancel = cancel
	o.done = make(chan struct{})
	done := o.done
	o.mu.Unlock()
	go o.consume(ctx, s.Source, done)
	return epoch, nil
}
func (o *Observer) consume(ctx context.Context, s session.Source, done chan struct{}) {
	defer close(done)
	defer s.Close()
	var next uint64
	var events <-chan session.SourceEvent
	if e, ok := s.(session.EventSource); ok {
		events = e.Events()
	}

	var pending *audio.Frame
	var metadata []session.SourceEvent
	for {
		o.mu.Lock()
		paused := o.paused
		due := o.origin.Add(time.Duration(next) * time.Second / 48000)
		o.mu.Unlock()
		delay := time.Until(due)
		if paused {
			delay = 10 * time.Millisecond
		}
		timer := time.NewTimer(max(0, delay))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		o.mu.Lock()
		paused = o.paused
		due = o.origin.Add(time.Duration(next) * time.Second / 48000)
		o.mu.Unlock()
		if paused || time.Now().Before(due) {
			continue
		}
		if pending == nil {
			f, e := s.Read(ctx)
			if e != nil {
				o.mu.Lock()
				if errors.Is(e, io.EOF) {
					o.state.Transport = "ended"
				} else if ctx.Err() == nil {
					o.state.Transport = "error"
				}
				o.mu.Unlock()
				return
			}
			if f.Sample != next || len(f.Data) == 0 || len(f.Data) > 1920 || f.Validate(audio.Canonical) != nil {
				o.mu.Lock()
				o.state.Transport = "error"
				o.mu.Unlock()
				return
			}
			pending = &f
		}
		o.mu.Lock()
		if o.paused {
			o.mu.Unlock()
			continue
		}
		// A stalled input has no audio to replay for the elapsed wall time.
		// Restart pacing rather than draining a catch-up burst into statistics.
		due = o.origin.Add(time.Duration(next) * time.Second / 48000)
		if time.Since(due) > 20*time.Millisecond {
			o.origin = time.Now().Add(-time.Duration(next) * time.Second / 48000)
		}
		f := *pending
		pending = nil
		for _, v := range f.Data {
			n := float64(v)
			o.state.Peak = math.Max(o.state.Peak, math.Abs(n))
			o.sum += n * n
		}
		count := uint64(len(f.Data) / 2)
		next += count
		o.state.Frames += count
		o.state.RMS = math.Sqrt(o.sum / float64(o.state.Frames*2))
		select {
		case e := <-events:
			if e.Kind == "metadata" {
				if len(metadata) < 8 {
					metadata = append(metadata, e)
				}
			}
		default:
		}
		for len(metadata) > 0 && (metadata[0].Sample == nil || *metadata[0].Sample <= next) {
			o.state.Metadata = metadata[0].Metadata
			metadata = metadata[1:]
		}
		o.mu.Unlock()
	}
}
func (o *Observer) stop() {
	o.mu.Lock()
	cancel, done := o.cancel, o.done
	o.cancel = nil
	o.done = nil
	o.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
		o.mu.Lock()
		if len(o.history) == 64 {
			o.history = o.history[1:]
		}
		o.history = append(o.history, o.state)
		o.mu.Unlock()
	}
}
func (o *Observer) Stop() { o.op.Lock(); defer o.op.Unlock(); o.stop() }
func (o *Observer) Pause() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cancel == nil {
		return errors.New("no probe source")
	}
	if !o.paused {
		o.pausedAt = time.Now()
	}
	o.paused = true
	o.state.Transport = "paused"
	return nil
}
func (o *Observer) Resume() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cancel == nil {
		return errors.New("no probe source")
	}
	if o.paused {
		o.origin = o.origin.Add(time.Since(o.pausedAt))
	}
	o.paused = false
	o.state.Transport = "playing"
	return nil
}
func (o *Observer) SetTargetDB(v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return errors.New("invalid target")
	}
	return nil
}
func (o *Observer) Mute(bool) error       { return nil }
func (o *Observer) Snapshot() Observation { o.mu.Lock(); defer o.mu.Unlock(); return o.state }
func (o *Observer) History() []Observation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]Observation(nil), o.history...)
}
