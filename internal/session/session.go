// Package session owns the single overlapping group session and its sample timeline.
package session

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/khenrix/home-sound/internal/airplay"
	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/config"
	"github.com/khenrix/home-sound/internal/volume"
)

const chunkFrames = 960
const queueChunks = 150
const sendAhead = 3 * time.Second

// Clock counts monotonic nanoseconds, independent of civil-clock adjustments.
// WaitUntil is only a pacing/cancellation mechanism, never an audio sample clock.
type Clock interface {
	Now() time.Duration
	WaitUntil(context.Context, time.Duration) error
}

// Source returns immutable, contiguous canonical frames of at most 20 ms. Epoch
// is assigned by Engine. EOF ends production; other errors abort the session.
// Read must honor cancellation. Close releases resources after Read has returned.
// A seek is a new Source and Selection, with FirstSample matching its first frame.
type Source interface {
	Format() audio.Format
	Metadata() airplay.Metadata
	Read(context.Context) (audio.Frame, error)
	Close() error
}

// Outputs must honor context cancellation and Stop must finish bounded teardown.
// Every factory call owns exactly one Prepare/START; retries use a fresh output.
type Output interface {
	airplay.Output
	SetNative(context.Context, int) error
}
type DeviceRequest struct {
	ID            string
	Device        config.Device
	Epoch         uint64
	ClockOffsetNS int64
	NativeChanged func(int)
}
type Factory func(context.Context, DeviceRequest) (Output, error)
type Selection struct {
	Group       string
	Source      Source
	TargetDB    *float64
	FirstSample uint64
	// TVCalibration explicitly limits this selection to the TV, at native 1..15.
	// It supplies no acoustic calibration and cannot enable other members.
	TVCalibration *int
}
type MemberStatus struct {
	State            string         `json:"state"`
	QueueFrames      uint64         `json:"queue_frames"`
	QueueOverflows   uint64         `json:"queue_overflows"`
	Errors           uint64         `json:"errors"`
	Attempts         uint64         `json:"attempts"`
	SubmittedFrames  uint64         `json:"submitted_frames"`
	Audibility       string         `json:"audibility"`
	UnderrunEvidence string         `json:"underrun_evidence"`
	LastError        string         `json:"last_error,omitempty"`
	RecoveryUntil    time.Duration  `json:"recovery_until_raw_ns,omitempty"`
	Plan             volume.Plan    `json:"volume"`
	LastRemoteNative *int           `json:"last_remote_native,omitempty"`
	Transport        airplay.Status `json:"transport"`
}
type Event struct {
	Epoch  uint64        `json:"epoch"`
	Device string        `json:"device,omitempty"`
	Kind   string        `json:"kind"`
	At     time.Duration `json:"raw_ns"`
}
type Status struct {
	QuarantinedDevices []string                `json:"quarantined_devices,omitempty"`
	Paused             bool                    `json:"paused"`
	Resuming           bool                    `json:"resuming"`
	PositionSample     uint64                  `json:"position_sample"`
	Epoch              uint64                  `json:"epoch"`
	Group              string                  `json:"group"`
	Active             bool                    `json:"active"`
	Muted              bool                    `json:"muted"`
	SourceState        string                  `json:"source_state"`
	SourceTransport    string                  `json:"source_transport,omitempty"`
	Metadata           airplay.Metadata        `json:"metadata"`
	TargetDB           *float64                `json:"target_db,omitempty"`
	NextSample         uint64                  `json:"next_sample"`
	ReplayFrames       uint64                  `json:"replay_frames"`
	Members            map[string]MemberStatus `json:"members"`
	Events             []Event                 `json:"events"`
}
type Engine struct {
	quarantine []*unresolvedTeardown
	op         sync.Mutex
	mu         sync.Mutex
	cfg        config.Config
	clock      Clock
	offset     int64
	factory    Factory
	epoch      uint64
	current    *running
}
type running struct {
	transportMu               sync.Mutex
	timelineMu                sync.RWMutex
	paused, resuming          bool
	pausedSample, resumeFloor uint64
	resumeAt                  time.Duration
	mu                        sync.Mutex
	engine                    *Engine
	epoch                     uint64
	selection                 Selection
	origin                    time.Duration
	inputReserve              time.Duration
	ctx                       context.Context
	cancel                    context.CancelFunc
	recovery                  context.Context
	cancelRecovery            context.CancelFunc
	done                      chan struct{}
	members                   map[string]*member
	replay                    []audio.Frame
	next                      uint64
	sourceState               string
	sourceTransport           string
	metadata                  airplay.Metadata
	active, muted             bool
	events                    []Event
	changed                   chan struct{}
}
type member struct {
	context       context.Context
	resumePending bool
	reconfigure   bool
	reconfiguring bool
	attemptDone   chan struct{}
	id            string
	state         MemberStatus
	plan          volume.Plan
	feedback      *volume.Feedback
	excluded      bool
	queue         chan audio.Frame
	cancel        context.CancelFunc
	output        Output
	wake          chan struct{}
}

func New(c config.Config, clock Clock, offset int64, f Factory) (*Engine, error) {
	if e := c.Validate(); e != nil {
		return nil, e
	}
	if clock == nil || f == nil {
		return nil, errors.New("clock and output factory required")
	}
	// Configuration belongs to this engine; callers cannot mutate profiles or groups.
	c = cloneConfig(c)
	return &Engine{cfg: c, clock: clock, offset: offset, factory: f}, nil
}
func (e *Engine) Select(sel Selection) (uint64, error) {
	e.op.Lock()
	defer e.op.Unlock()
	if sel.Source == nil || sel.Source.Format() != audio.Canonical {
		return 0, errors.New("canonical source required")
	}
	var reserve time.Duration
	if b, ok := sel.Source.(interface{ BufferedSamples() uint64 }); ok {
		n := b.BufferedSamples()
		if n > 96000 {
			return 0, errors.New("input buffering exceeds two seconds")
		}
		reserve = time.Duration(n) * time.Second / 48000
	}
	if e := sel.Source.Metadata().Validate(); e != nil {
		return 0, e
	}
	ids, ok := e.cfg.Groups[sel.Group]
	if !ok {
		return 0, errors.New("unknown destination")
	}
	if _, err := audio.SampleTime(sel.FirstSample, 48000); err != nil {
		return 0, err
	}
	plans := map[string]volume.Plan{}
	if sel.TVCalibration != nil {
		if sel.Group != config.TVAndSpeakers || sel.TargetDB != nil || *sel.TVCalibration < 1 || *sel.TVCalibration > 15 {
			return 0, errors.New("TV calibration needs TV destination and native 1 through 15")
		}
		for _, id := range ids {
			if e.cfg.Devices[id].Kind == "tv" {
				ids = []string{id}
				plans[id] = volume.Plan{Native: *sel.TVCalibration}
				break
			}
		}
	} else {
		if sel.TargetDB == nil {
			return 0, errors.New("relative dB target required")
		}
		var err error
		plans, err = e.plans(ids, *sel.TargetDB)
		if err != nil {
			return 0, err
		}
	}
	e.stop()
	if err := e.resolveTeardown(ids); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	recovery, cancelRecovery := context.WithCancel(ctx)
	firstTime, _ := audio.SampleTime(sel.FirstSample, 48000)
	e.mu.Lock()
	e.epoch++
	if sel.TargetDB != nil {
		v := *sel.TargetDB
		sel.TargetDB = &v
	}
	if sel.TVCalibration != nil {
		v := *sel.TVCalibration
		sel.TVCalibration = &v
	}
	s := &running{inputReserve: reserve, paused: true, resuming: true, pausedSample: sel.FirstSample, resumeFloor: sel.FirstSample, engine: e, epoch: e.epoch, selection: sel, origin: e.clock.Now() + time.Second - firstTime, ctx: ctx, cancel: cancel, recovery: recovery, cancelRecovery: cancelRecovery, done: make(chan struct{}), members: map[string]*member{}, next: sel.FirstSample, sourceState: "reading", metadata: sel.Source.Metadata(), active: true, changed: make(chan struct{})}
	for _, id := range ids {
		m := &member{id: id, plan: plans[id], wake: make(chan struct{}, 1), state: MemberStatus{State: "preparing", Audibility: "unverified", UnderrunEvidence: "not_reported_by_adapter"}, feedback: volume.NewFeedback(e.cfg.Profiles[id], e.cfg.Devices[id].RoomTrimDB)}
		s.members[id] = m
	}
	e.current = s
	e.mu.Unlock()
	go s.run()
	return s.epoch, nil
}
func (e *Engine) plans(ids []string, target float64) (map[string]volume.Plan, error) {
	if !e.cfg.ReferenceVerified || e.cfg.ReferenceCeilingDB == nil {
		return nil, errors.New("acoustic reference ceiling is unverified")
	}
	plans := map[string]volume.Plan{}
	for _, id := range ids {
		d := e.cfg.Devices[id]
		p, err := e.cfg.Profiles[id].Map(target, d.RoomTrimDB)
		if err != nil {
			return nil, err
		}
		if p.EffectiveDB > *e.cfg.ReferenceCeilingDB {
			return nil, errors.New("target exceeds quiet acoustic reference ceiling")
		}
		if d.Kind == "tv" && p.Native > 15 {
			return nil, errors.New("TV native level exceeds calibration ceiling")
		}
		plans[id] = p
	}
	return plans, nil
}
func (e *Engine) stop() {
	e.mu.Lock()
	s := e.current
	e.mu.Unlock()
	if s != nil {
		s.cancel()
		<-s.done
	}
}
func (e *Engine) Stop()                    { e.op.Lock(); defer e.op.Unlock(); e.stop() }
func (e *Engine) currentSession() *running { e.mu.Lock(); defer e.mu.Unlock(); return e.current }
func (e *Engine) PresentationTime(sample uint64) time.Time {
	s := e.currentSession()
	if s == nil {
		return time.Time{}
	}
	return s.presentation(sample)
}
func (s *running) presentation(sample uint64) time.Time {
	s.timelineMu.RLock()
	defer s.timelineMu.RUnlock()
	d, err := audio.SampleTime(sample, 48000)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(0, s.engine.offset+int64(s.origin+d))
}
func (s *running) rawTime(sample uint64) time.Duration {
	s.timelineMu.RLock()
	defer s.timelineMu.RUnlock()
	d, _ := audio.SampleTime(sample, 48000)
	return s.origin + d
}
func (s *running) eventLocked(id, kind string) {
	if len(s.events) == 64 {
		copy(s.events, s.events[1:])
		s.events = s.events[:63]
	}
	s.events = append(s.events, Event{Epoch: s.epoch, Device: id, Kind: kind, At: s.engine.clock.Now()})
}
func (s *running) signalLocked() { close(s.changed); s.changed = make(chan struct{}) }
func (e *Engine) Status() Status {
	quarantined := e.quarantineStatus()
	s := e.currentSession()
	if s == nil {
		return Status{QuarantinedDevices: quarantined, SourceState: "idle", Members: map[string]MemberStatus{}}
	}
	s.mu.Lock()
	result := Status{QuarantinedDevices: quarantined, Paused: s.paused, Resuming: s.resuming || s.engine.clock.Now() < s.resumeAt, PositionSample: s.positionLocked(), Epoch: s.epoch, Group: s.selection.Group, Active: s.active, Muted: s.muted, SourceState: s.sourceState, SourceTransport: s.sourceTransport, Metadata: s.metadata, NextSample: s.next, Members: map[string]MemberStatus{}, Events: append([]Event(nil), s.events...)}
	if s.selection.TargetDB != nil {
		v := *s.selection.TargetDB
		result.TargetDB = &v
	}
	for _, f := range s.replay {
		result.ReplayFrames += uint64(len(f.Data) / 2)
	}
	outputs := map[string]Output{}
	for id, m := range s.members {
		st := m.state
		if st.LastRemoteNative != nil {
			n := *st.LastRemoteNative
			st.LastRemoteNative = &n
		}
		st.Plan = m.plan
		st.QueueFrames = uint64(len(m.queue) * chunkFrames)
		result.Members[id] = st
		outputs[id] = m.output
	}
	s.mu.Unlock()
	for id, o := range outputs {
		if o != nil {
			st := result.Members[id]
			st.Transport = o.Status()
			result.Members[id] = st
		}
	}
	return result
}
func (e *Engine) Exclude(epoch uint64, id string) bool {
	s := e.currentSession()
	if s == nil || s.epoch != epoch {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.members[id]
	if m == nil || !s.active {
		return false
	}
	m.excluded = true
	m.state.State = "excluded"
	if m.cancel != nil {
		m.cancel()
	}
	s.eventLocked(id, "excluded")
	select {
	case m.wake <- struct{}{}:
	default:
	}
	return true
}
func (e *Engine) SetTargetDB(target float64) error {
	s := e.currentSession()
	if s == nil {
		return errors.New("no session")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.targetLocked(target)
}
func (s *running) targetLocked(target float64) error {
	if !s.active || s.selection.TVCalibration != nil {
		return errors.New("no calibrated active session")
	}
	ids := make([]string, 0, len(s.members))
	for id := range s.members {
		ids = append(ids, id)
	}
	plans, err := s.engine.plans(ids, target)
	if err != nil {
		return err
	}
	s.selection.TargetDB = &target
	for id, p := range plans {
		m := s.members[id]
		if m.plan.Native != p.Native {
			s.reconfigureLocked(m)
		}
		m.plan = p
		select {
		case m.wake <- struct{}{}:
		default:
		}
	}
	s.eventLocked("", "target_changed")
	return nil
}

// Stop/flush before changing the native basis of already-submitted PCM. The
// member worker joins the unchanged sample timeline with a fresh output.
func (s *running) reconfigureLocked(m *member) {
	if m.cancel != nil && (m.state.State == "preparing" || m.state.State == "streaming") {
		m.reconfigure = true
		m.state.State = "reconfiguring"
		m.cancel()
	}
}
func (e *Engine) NativeChanged(epoch uint64, id string, n int) bool {
	return e.nativeChanged(epoch, id, 0, n)
}
func (e *Engine) nativeChanged(epoch uint64, id string, attempt uint64, n int) bool {
	s := e.currentSession()
	if s == nil || s.epoch != epoch {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.members[id]
	if !s.active || m == nil || m.excluded || attempt != 0 && (m.state.Attempts != attempt || m.output == nil || m.context == nil || m.context.Err() != nil) || e.cfg.Devices[id].Kind != "tv" {
		return false
	}
	target, changed, err := m.feedback.Observe(n)
	if err == nil && !changed {
		return false
	}
	// A physical remote step invalidates the gain basis of buffered PCM even
	// when inversion chooses the same measured step as the previous plan.
	m.state.LastRemoteNative = &n
	s.reconfigureLocked(m)
	if err != nil || s.targetLocked(target) != nil {
		s.eventLocked(id, "native_change_rejected")
		return false
	}
	return true
}
func (e *Engine) Mute(muted bool) error {
	s := e.currentSession()
	if s == nil {
		return errors.New("no session")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return errors.New("no active session")
	}
	s.muted = muted
	for _, m := range s.members {
		if muted && m.cancel != nil {
			m.cancel()
		}
		select {
		case m.wake <- struct{}{}:
		default:
		}
	}
	s.eventLocked("", "mute_changed")
	return nil
}
