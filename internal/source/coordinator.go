package source

import (
	"context"
	"encoding/hex"
	"errors"
	"github.com/khenrix/home-sound/internal/airplay"
	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/config"
	"github.com/khenrix/home-sound/internal/session"
	"math"
	"strings"
	"sync"
)

type Identity struct {
	Provider, Group, Instance string
	Generation                uint64
}
type Lease struct {
	Identity
	Epoch uint64
}
type Sink interface {
	Select(session.Selection) (uint64, error)
	Stop()
	Pause() error
	Resume() error
	SetTargetDB(float64) error
	Mute(bool) error
}

// Coordinator serializes admission and callbacks. Engine remains the authority
// for profile gates, output flush and quarantine. A rejected Source stays with
// this caller, which closes it; successful selection transfers it to the sink.
type Coordinator struct {
	mu              sync.Mutex
	sink            Sink
	policy          func(int) (float64, error)
	epoch           uint64
	owner           Lease
	stream          *eventStream
	release         func()
	expected        []int
	lastVolume      int
	seenVolume      bool
	target          *float64
	commonVolume    int
	hasCommonVolume bool
	muted           bool
	instances       map[string]string
	generations     map[string]uint64
}

func NewCoordinator(s Sink, p func(int) (float64, error)) *Coordinator {
	return &Coordinator{sink: s, policy: p, instances: map[string]string{}, generations: map[string]uint64{}}
}
func (id Identity) key() string { return id.Provider + "/" + id.Group }
func (id Identity) valid() bool {
	_, e := hex.DecodeString(id.Instance)
	return (id.Provider == "spotify" || id.Provider == "youtube") && (id.Group == config.AllSpeakers || id.Group == config.TVAndSpeakers) && len(id.Instance) == 32 && e == nil && id.Generation > 0
}
func (c *Coordinator) Select(id Identity, s session.Source, paused bool, target float64, release func()) (Lease, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s == nil || math.IsNaN(target) || math.IsInf(target, 0) {
		if s != nil {
			s.Close()
		}
		return Lease{}, errors.New("invalid input selection")
	}
	lease, e := c.reserveLocked(id, nil)
	if e != nil {
		s.Close()
		return Lease{}, e
	}
	if e = c.commitLocked(lease, s, paused, target, release); e != nil {
		return Lease{}, e
	}
	return lease, nil
}
func (c *Coordinator) reserveLocked(id Identity, cancel func()) (Lease, error) {
	if !id.valid() || c.sink == nil {
		return Lease{}, errors.New("invalid input selection")
	}
	if nonce := c.instances[id.key()]; nonce != "" && nonce != id.Instance {
		return Lease{}, errors.New("stale input instance")
	}
	if id.Generation <= c.generations[id.key()] {
		return Lease{}, errors.New("stale input generation")
	}
	old, oldRelease, oldStream := c.owner, c.release, c.stream
	c.epoch++
	c.owner = Lease{id, c.epoch}
	c.stream = nil
	c.release = cancel
	c.instances[id.key()] = id.Instance
	c.generations[id.key()] = id.Generation
	c.expected = nil
	c.seenVolume = false
	c.sink.Stop()
	if oldRelease != nil && (oldStream == nil || old.Provider != id.Provider || old.Group != id.Group || old.Instance != id.Instance) {
		oldRelease()
	}
	return c.owner, nil
}

// Reserve invalidates the previous owner before asynchronous media resolution.
// The pending cancellation callback must not re-enter the coordinator.
func (c *Coordinator) Reserve(id Identity, cancel func()) (Lease, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reserveLocked(id, cancel)
}
func (c *Coordinator) Commit(l Lease, s session.Source, paused bool, target float64, release func()) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.commitLocked(l, s, paused, target, release)
}
func (c *Coordinator) commitLocked(l Lease, s session.Source, paused bool, target float64, release func()) error {
	if !c.current(l) || c.stream != nil || s == nil || math.IsNaN(target) || math.IsInf(target, 0) {
		if s != nil {
			s.Close()
		}
		return errors.New("stale or invalid prepared input")
	}
	if c.target != nil {
		target = *c.target
	}
	wrapped := &eventStream{Source: s, events: make(chan session.SourceEvent, 8), ready: make(chan struct{})}
	defer close(wrapped.ready)
	if _, e := c.sink.Select(session.Selection{Group: l.Group, Source: wrapped, TargetDB: &target}); e != nil {
		wrapped.Close()
		if c.release != nil {
			c.release()
		}
		c.owner = Lease{}
		c.release = nil
		return e
	}
	c.stream = wrapped
	c.target = &target
	if e := c.sink.Mute(c.muted); e != nil {
		c.owner = Lease{}
		c.stream = nil
		c.sink.Stop()
		if release != nil {
			release()
		}
		c.release = nil
		return e
	}
	c.release = release
	if paused {
		if e := c.sink.Pause(); e != nil {
			c.owner = Lease{}
			c.sink.Stop()
			if release != nil {
				release()
			}
			c.release = nil
			return e
		}
	}
	return nil
}

func (c *Coordinator) current(l Lease) bool { return l.Epoch != 0 && l == c.owner }
func (c *Coordinator) Current(l Lease) bool { c.mu.Lock(); defer c.mu.Unlock(); return c.current(l) }
func (c *Coordinator) Transport(l Lease, state string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.current(l) || c.stream == nil {
		return false
	}
	switch state {
	case "paused":
		return c.sink.Pause() == nil
	case "playing":
		return c.sink.Resume() == nil
	case "buffering":
		c.stream.event(session.SourceEvent{Kind: "transport", Transport: state})
		return true
	}
	return false
}
func (c *Coordinator) Metadata(l Lease, m airplay.Metadata) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.current(l) || c.stream == nil {
		return false
	}
	for _, v := range []string{m.Title, m.Artist, m.Album} {
		if len(v) > 1024 || strings.ContainsAny(v, "\r\n\x00") {
			return false
		}
	}
	c.stream.event(session.SourceEvent{Kind: "metadata", Metadata: m})
	return true
}
func (c *Coordinator) ExpectVolume(l Lease, value int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.current(l) || value < 0 || value > 100 {
		return
	}
	if len(c.expected) == 16 {
		c.expected = c.expected[1:]
	}
	c.expected = append(c.expected, value)
}
func (c *Coordinator) Volume(l Lease, value int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.current(l) || c.stream == nil || value < 0 || value > 100 || c.policy == nil {
		return false
	}
	for i, v := range c.expected {
		if v == value {
			c.expected = append(c.expected[:i], c.expected[i+1:]...)
			c.lastVolume = value
			c.seenVolume = true
			return false
		}
	}
	if c.seenVolume && c.lastVolume == value {
		return false
	}
	target, e := c.policy(value)
	if e != nil || math.IsNaN(target) || math.IsInf(target, 0) || c.sink.SetTargetDB(target) != nil {
		return false
	}
	if c.sink.Mute(value == 0) != nil {
		return false
	}
	c.muted = value == 0
	c.lastVolume = value
	c.commonVolume = value
	c.hasCommonVolume = true
	c.target = &target
	c.seenVolume = true
	return true
}
func (c *Coordinator) End(l Lease) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.current(l) {
		return false
	}
	release := c.release
	c.owner = Lease{}
	c.release = nil
	c.stream = nil
	c.sink.Stop()
	if release != nil {
		release()
	}
	return true
}

type eventStream struct {
	session.Source
	events chan session.SourceEvent
	ready  chan struct{}
	once   sync.Once
	err    error
}

func (s *eventStream) Events() <-chan session.SourceEvent { return s.events }
func (s *eventStream) event(e session.SourceEvent) {
	select {
	case s.events <- e:
	default:
		select {
		case <-s.events:
		default:
		}
		s.events <- e
	}
}
func (s *eventStream) Close() error { s.once.Do(func() { s.err = s.Source.Close() }); return s.err }

// Register is called by the trusted helper supervisor on a new connection,
// never by arbitrary metadata/control callbacks. Re-registering the same nonce
// preserves its generation floor. Replaced instances lose authority immediately.
func (c *Coordinator) Register(id Identity) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !id.valid() {
		return errors.New("invalid input identity")
	}
	if c.instances[id.key()] == id.Instance {
		return nil
	}
	if c.owner.Epoch != 0 && c.owner.Identity.key() == id.key() {
		release := c.release
		c.owner = Lease{}
		c.release = nil
		c.stream = nil
		c.sink.Stop()
		if release != nil {
			release()
		}
	}
	c.instances[id.key()] = id.Instance
	c.generations[id.key()] = 0
	return nil
}

func (c *Coordinator) Mute(l Lease, muted bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.current(l) || c.stream == nil {
		return false
	}
	if c.hasCommonVolume && c.commonVolume == 0 {
		muted = true
	}
	if c.sink.Mute(muted) != nil {
		return false
	}
	c.muted = muted
	return true
}
func (s *eventStream) BufferedSamples() uint64 {
	if b, ok := s.Source.(interface{ BufferedSamples() uint64 }); ok {
		return b.BufferedSamples()
	}
	return 0
}

func (s *eventStream) Read(ctx context.Context) (audio.Frame, error) {
	select {
	case <-ctx.Done():
		return audio.Frame{}, ctx.Err()
	case <-s.ready:
		return s.Source.Read(ctx)
	}
}

// Exclusive revokes input ownership before an operator replaces/stops the bus.
// It keeps late native callbacks fenced through the entire operator action.
func (c *Coordinator) Exclusive(action func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	release := c.release
	c.owner = Lease{}
	c.release = nil
	c.stream = nil
	c.sink.Stop()
	if release != nil {
		release()
	}
	return action()
}

func (c *Coordinator) CommonVolume() (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.commonVolume, c.hasCommonVolume
}

// Advance accepts a proven natural prefetch boundary within the same stream.
// The helper generation changes, but the bus epoch, converter and outputs do not.
func (c *Coordinator) Advance(l Lease, generation, sample uint64, m airplay.Metadata) (Lease, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.current(l) || c.stream == nil || generation <= l.Generation {
		return Lease{}, errors.New("stale natural boundary")
	}
	for _, v := range []string{m.Title, m.Artist, m.Album} {
		if len(v) > 1024 || strings.ContainsAny(v, "\r\n\x00") {
			return Lease{}, errors.New("invalid boundary metadata")
		}
	}
	event := session.SourceEvent{Kind: "metadata", Metadata: m, Sample: &sample}
	select {
	case c.stream.events <- event:
	default:
		return Lease{}, errors.New("metadata boundary queue full")
	}
	c.owner.Generation = generation
	c.generations[l.Identity.key()] = generation
	return c.owner, nil
}

func (c *Coordinator) SetTargetDB(target float64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if math.IsNaN(target) || math.IsInf(target, 0) {
		return errors.New("invalid target")
	}
	if e := c.sink.SetTargetDB(target); e != nil {
		return e
	}
	c.target = &target
	if c.policy != nil {
		distance := math.Inf(1)
		for n := 0; n <= 100; n++ {
			v, e := c.policy(n)
			if e == nil && math.Abs(v-target) < distance {
				distance = math.Abs(v - target)
				c.commonVolume = n
				c.hasCommonVolume = true
			}
		}
	}
	return nil
}

func (c *Coordinator) SetMuted(muted bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hasCommonVolume && c.commonVolume == 0 {
		muted = true
	}
	if e := c.sink.Mute(muted); e != nil {
		return e
	}
	c.muted = muted
	return nil
}
