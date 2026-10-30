package youtube

import (
	"context"
	"errors"
	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/session"
	"github.com/khenrix/home-sound/internal/source"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

type MediaResolver interface {
	Resolve(context.Context, string) (Media, error)
}
type MediaDecoder interface {
	Decode(context.Context, Media, time.Duration) (session.Source, error)
}
type PlaybackController struct {
	mu           sync.Mutex
	group, nonce string
	co           *source.Coordinator
	resolver     MediaResolver
	decoder      MediaDecoder
	target       float64
	q            *Queue
	gen          uint64
	lease        source.Lease
	cancel       context.CancelFunc
	workers      sync.WaitGroup
	closed       bool
	current      atomic.Uint64
	ended        atomic.Uint64
	failed       atomic.Uint64
	lastError    string
	volumeDirty  bool
	muteDirty    bool
}

func NewPlaybackController(group, nonce string, co *source.Coordinator, r MediaResolver, d MediaDecoder, target float64) *PlaybackController {
	return &PlaybackController{group: group, nonce: nonce, co: co, resolver: r, decoder: d, target: target, q: NewQueue()}
}
func (p *PlaybackController) Handle(ctx context.Context, m Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("input closed")
	}
	if m.Name == "__local_eof" {
		if !p.ended.CompareAndSwap(p.gen, 0) || !p.co.Current(p.lease) {
			return nil
		}
		m.Name = "next"
	}
	a, e := p.q.ApplyGeneration(m, p.gen+1)
	if e != nil {
		return e
	}
	switch a.Kind {
	case "load":
		if p.cancel != nil {
			p.cancel()
		}
		p.gen++
		p.current.Store(p.gen)
		run, cancel := context.WithCancel(ctx)
		p.cancel = cancel
		id := source.Identity{Provider: "youtube", Group: p.group, Instance: p.nonce, Generation: p.gen}
		if p.gen == 1 {
			if e = p.co.Register(id); e != nil {
				return e
			}
		}
		l, e := p.co.Reserve(id, cancel)
		if e != nil {
			cancel()
			return e
		}
		p.lease = l
		p.lastError = ""
		p.workers.Go(func() { p.prepare(run, l, a) })
	case "pause":
		p.co.Transport(p.lease, "paused")
	case "resume":
		p.co.Transport(p.lease, "playing")
	case "volume":
		p.volumeDirty = true
		p.co.Volume(p.lease, a.Volume)
		if a.Muted != nil {
			p.muteDirty = true
			p.co.Mute(p.lease, *a.Muted)
		}
	case "stop":
		p.current.Store(0)
		if p.cancel != nil {
			p.cancel()
		}
		p.co.End(p.lease)
	}
	return nil
}
func (p *PlaybackController) prepare(ctx context.Context, l source.Lease, a Action) {
	var prepared session.Source
	var first audio.Frame
	var e error
	var duration time.Duration
	for attempt := 0; attempt < 2; attempt++ {
		if ctx.Err() != nil {
			return
		}
		var m Media
		m, e = p.resolver.Resolve(ctx, a.VideoID)
		if e != nil {
			break
		}
		if m.Expired(time.Now()) {
			e = errors.New("expired media")
			continue
		}
		duration = m.Duration
		prepared, e = p.decoder.Decode(ctx, m, a.Position)
		if e != nil {
			continue
		}
		first, e = prepared.Read(ctx)
		if e == nil {
			break
		}
		prepared.Close()
		prepared = nil
		if errors.Is(e, io.EOF) {
			e = errors.New("empty media")
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if ctx.Err() != nil || p.closed || p.lease != l {
		if prepared != nil {
			prepared.Close()
		}
		return
	}
	if e != nil || prepared == nil {
		p.lastError = "media unavailable"
		p.q.Observed(a.Position, true)
		p.co.End(l)
		return
	}
	s := &observedSource{Source: prepared, first: &first, position: a.Position, observe: func(pos time.Duration, end bool, failed bool) {
		if p.current.Load() == l.Generation && p.q.ObservedGeneration(l.Generation, pos, end || failed) {
			if failed {
				p.failed.Store(l.Generation)
			} else if end {
				p.ended.Store(l.Generation)
			}
		}
	}}
	p.q.setDuration(duration)
	paused := p.q.Paused()
	if e = p.co.Commit(l, s, paused, p.target, p.cancel); e != nil {
		p.lastError = "input selection rejected"
		return
	}
	if p.volumeDirty {
		p.co.Volume(l, p.q.Snapshot().Volume)
		p.volumeDirty = false
	} else if v, ok := p.co.CommonVolume(); ok {
		p.q.initialVolume(v)
	}
	if p.muteDirty {
		p.co.Mute(l, p.q.Snapshot().Muted)
		p.muteDirty = false
	}
}
func (p *PlaybackController) Snapshot() (Playback, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gen != 0 && p.failed.Load() == p.gen {
		return p.q.Snapshot(), "media decoder failed"
	}
	return p.q.Snapshot(), p.lastError
}
func (p *PlaybackController) Close() {
	p.mu.Lock()
	p.closed = true
	p.current.Store(0)
	if p.cancel != nil {
		p.cancel()
	}
	p.co.End(p.lease)
	p.mu.Unlock()
	p.workers.Wait()
}

type observedSource struct {
	session.Source
	first    *audio.Frame
	position time.Duration
	frames   uint64
	observe  func(time.Duration, bool, bool)
}

func (s *observedSource) Read(ctx context.Context) (audio.Frame, error) {
	var f audio.Frame
	var e error
	if s.first != nil {
		f = *s.first
		s.first = nil
	} else {
		f, e = s.Source.Read(ctx)
	}
	if e == nil {
		s.frames += uint64(len(f.Data) / 2)
	}
	s.observe(s.position+time.Duration(s.frames)*time.Second/48000, errors.Is(e, io.EOF), e != nil && !errors.Is(e, io.EOF) && ctx.Err() == nil)
	return f, e
}
func (s *observedSource) BufferedSamples() uint64 { return 48000 }

func (p *PlaybackController) Tick(ctx context.Context) {
	_ = p.Handle(ctx, Message{Name: "__local_eof"})
}

func (p *PlaybackController) InitialVolume(v int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if current, ok := p.co.CommonVolume(); ok {
		v = current
	}
	p.q.initialVolume(v)
	p.volumeDirty = v == 0
}
