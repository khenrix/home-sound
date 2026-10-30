package source

import (
	"context"
	"errors"
	"github.com/khenrix/home-sound/internal/airplay"
	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/config"
	"github.com/khenrix/home-sound/internal/session"
	"io"
	"sync"
	"testing"
)

type sinkFixture struct {
	selected                        []session.Selection
	stops, pauses, resumes, volumes int
	reject                          bool
	muted                           bool
}

func (s *sinkFixture) Select(v session.Selection) (uint64, error) {
	if s.reject {
		return 0, errors.New("profile gate")
	}
	s.selected = append(s.selected, v)
	return uint64(len(s.selected)), nil
}
func (s *sinkFixture) Stop() {
	s.stops++
	if len(s.selected) > 0 {
		s.selected[len(s.selected)-1].Source.Close()
	}
}
func (s *sinkFixture) Pause() error              { s.pauses++; return nil }
func (s *sinkFixture) Resume() error             { s.resumes++; return nil }
func (s *sinkFixture) SetTargetDB(float64) error { s.volumes++; return nil }
func (s *sinkFixture) Mute(v bool) error         { s.muted = v; return nil }

type sourceFixture struct{ closed int }

func (*sourceFixture) Format() audio.Format                      { return audio.Canonical }
func (*sourceFixture) Metadata() airplay.Metadata                { return airplay.Metadata{Title: "fixture"} }
func (*sourceFixture) Read(context.Context) (audio.Frame, error) { return audio.Frame{}, io.EOF }
func (s *sourceFixture) Close() error                            { s.closed++; return nil }
func identity(group string, g uint64) Identity {
	return Identity{Provider: "spotify", Group: group, Instance: "0123456789abcdef0123456789abcdef", Generation: g}
}
func TestCoordinatorHandoffRejectsEveryStaleCallback(t *testing.T) {
	sink := &sinkFixture{}
	c := NewCoordinator(sink, func(p int) (float64, error) { return float64(p) - 100, nil })
	a, b := &sourceFixture{}, &sourceFixture{}
	old, e := c.Select(identity(config.TVAndSpeakers, 1), a, false, -30, nil)
	if e != nil {
		t.Fatal(e)
	}
	now, e := c.Select(identity(config.AllSpeakers, 1), b, false, -30, nil)
	if e != nil {
		t.Fatal(e)
	}
	if old == now || a.closed != 1 {
		t.Fatal("handoff did not close old source")
	}
	if c.Transport(old, "paused") || c.End(old) || c.Volume(old, 80) || c.Metadata(old, airplay.Metadata{Title: "old"}) {
		t.Fatal("stale callback changed successor")
	}
	if sink.pauses != 0 || sink.volumes != 0 || !c.Current(now) {
		t.Fatal("new owner changed")
	}
	if !c.Transport(now, "paused") || sink.pauses != 1 {
		t.Fatal("current transport ignored")
	}
}
func TestCoordinatorRejectClosesSourceAndInactiveVolumeCannotTakeover(t *testing.T) {
	sink := &sinkFixture{reject: true}
	c := NewCoordinator(sink, nil)
	s := &sourceFixture{}
	if _, e := c.Select(identity(config.AllSpeakers, 1), s, false, -30, nil); e == nil || s.closed != 1 {
		t.Fatalf("rejected source cleanup %v %d", e, s.closed)
	}
	if c.Volume(Lease{}, 50) {
		t.Fatal("inactive volume took ownership")
	}
}
func TestCoordinatorSeekPreservesPauseWithoutStoppingNewHelper(t *testing.T) {
	sink := &sinkFixture{}
	c := NewCoordinator(sink, nil)
	released := 0
	first, e := c.Select(identity(config.AllSpeakers, 1), &sourceFixture{}, true, -30, func() { released++ })
	if e != nil {
		t.Fatal(e)
	}
	next, e := c.Select(identity(config.AllSpeakers, 2), &sourceFixture{}, true, -30, func() { released++ })
	if e != nil {
		t.Fatal(e)
	}
	if released != 0 || sink.pauses != 2 || c.Current(first) || !c.Current(next) {
		t.Fatalf("seek ownership/pause %d %d", released, sink.pauses)
	}
}
func TestCoordinatorConcurrentIdentitiesHaveOneOwner(t *testing.T) {
	sink := &sinkFixture{}
	c := NewCoordinator(sink, nil)
	var wg sync.WaitGroup
	leases := make(chan Lease, 2)
	for _, group := range []string{config.AllSpeakers, config.TVAndSpeakers} {
		wg.Add(1)
		go func(group string) {
			defer wg.Done()
			l, e := c.Select(identity(group, 1), &sourceFixture{}, false, -30, nil)
			if e != nil {
				t.Error(e)
			}
			leases <- l
		}(group)
	}
	wg.Wait()
	close(leases)
	n := 0
	for l := range leases {
		if c.Current(l) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("owners %d", n)
	}
}
func TestCoordinatorVolumeEchoSuppression(t *testing.T) {
	sink := &sinkFixture{}
	c := NewCoordinator(sink, func(p int) (float64, error) { return float64(p) - 100, nil })
	l, e := c.Select(identity(config.AllSpeakers, 1), &sourceFixture{}, false, -30, nil)
	if e != nil {
		t.Fatal(e)
	}
	c.ExpectVolume(l, 50)
	if c.Volume(l, 50) || !c.Volume(l, 51) || c.Volume(l, 51) || sink.volumes != 1 {
		t.Fatalf("volume loop %d", sink.volumes)
	}
}
func TestCoordinatorRestartRejectsPriorNonceAndGeneration(t *testing.T) {
	sink := &sinkFixture{}
	c := NewCoordinator(sink, nil)
	old := identity(config.AllSpeakers, 1)
	l, e := c.Select(old, &sourceFixture{}, false, -30, nil)
	if e != nil {
		t.Fatal(e)
	}
	fresh := old
	fresh.Instance = "abcdef0123456789abcdef0123456789"
	register, ok := any(c).(interface{ Register(Identity) error })
	if !ok {
		t.Fatal("new helper instance cannot revoke old callback authority")
	}
	if e = register.Register(fresh); e != nil {
		t.Fatal(e)
	}
	if c.Current(l) {
		t.Fatal("old helper still owner after restart")
	}
	if _, e = c.Select(old, &sourceFixture{}, false, -30, nil); e == nil {
		t.Fatal("old helper returned after replacement")
	}
	if _, e = c.Select(fresh, &sourceFixture{}, false, -30, nil); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Select(fresh, &sourceFixture{}, false, -30, nil); e == nil {
		t.Fatal("duplicate generation reselected")
	}
}
func TestCoordinatorLateResolverCannotStealNewSelection(t *testing.T) {
	sink := &sinkFixture{}
	c := NewCoordinator(sink, nil)
	api, ok := any(c).(interface {
		Reserve(Identity, func()) (Lease, error)
		Commit(Lease, session.Source, bool, float64, func()) error
	})
	if !ok {
		t.Fatal("async input has no ownership reservation before resolution")
	}
	canceled := 0
	old, e := api.Reserve(identity(config.AllSpeakers, 1), func() { canceled++ })
	if e != nil {
		t.Fatal(e)
	}
	fresh, e := api.Reserve(identity(config.TVAndSpeakers, 1), nil)
	if e != nil {
		t.Fatal(e)
	}
	late := &sourceFixture{}
	if e = api.Commit(old, late, false, -30, nil); e == nil || late.closed != 1 || canceled != 1 {
		t.Fatalf("late resolver committed or leaked: %v closed=%d canceled=%d", e, late.closed, canceled)
	}
	if e = api.Commit(fresh, &sourceFixture{}, true, -30, nil); e != nil {
		t.Fatal(e)
	}
	if !c.Current(fresh) || sink.pauses != 1 {
		t.Fatal("current resolver ownership/pause lost")
	}
}

func TestSeekRetainsAcceptedCommonTarget(t *testing.T) {
	s := &sinkFixture{}
	c := NewCoordinator(s, func(n int) (float64, error) { return float64(n) - 100, nil })
	l, e := c.Select(identity(config.AllSpeakers, 1), &sourceFixture{}, false, -40, nil)
	if e != nil {
		t.Fatal(e)
	}
	if !c.Volume(l, 70) {
		t.Fatal("volume rejected")
	}
	if _, e = c.Select(identity(config.AllSpeakers, 2), &sourceFixture{}, false, -40, nil); e != nil {
		t.Fatal(e)
	}
	if got := *s.selected[1].TargetDB; got != -30 {
		t.Fatalf("seek reset accepted common target: %v", got)
	}
}

func TestNaturalBoundaryChangesGenerationWithoutSelectingAgain(t *testing.T) {
	s := &sinkFixture{}
	c := NewCoordinator(s, nil)
	old, e := c.Select(identity(config.AllSpeakers, 1), &sourceFixture{}, false, -30, nil)
	if e != nil {
		t.Fatal(e)
	}
	next, e := c.Advance(old, 2, 4802, airplay.Metadata{Title: "next"})
	if e != nil {
		t.Fatal(e)
	}
	if old.Epoch != next.Epoch || next.Generation != 2 || len(s.selected) != 1 || c.Current(old) || !c.Current(next) || c.Metadata(old, airplay.Metadata{Title: "late"}) {
		t.Fatal("natural generation changed bus or accepted stale callback")
	}
	events := s.selected[0].Source.(session.EventSource).Events()
	event := <-events
	if event.Sample == nil || *event.Sample != 4802 || event.Metadata.Title != "next" {
		t.Fatal("metadata did not retain content sample")
	}
}

func TestOperatorTargetPersistsAcrossInputSeek(t *testing.T) {
	s := &sinkFixture{}
	c := NewCoordinator(s, func(n int) (float64, error) { return float64(n) - 100, nil })
	_, e := c.Select(identity(config.AllSpeakers, 1), &sourceFixture{}, false, -40, nil)
	if e != nil {
		t.Fatal(e)
	}
	bridge, ok := any(c).(interface{ SetTargetDB(float64) error })
	if !ok {
		t.Fatal("operator target has no coordinated input path")
	}
	if e = bridge.SetTargetDB(-35); e != nil {
		t.Fatal(e)
	}
	_, e = c.Select(identity(config.AllSpeakers, 2), &sourceFixture{}, false, -40, nil)
	if e != nil || *s.selected[1].TargetDB != -35 {
		t.Fatal("seek reset operator target")
	}
}

func TestZeroVolumeSilencesAndSeekRetainsMute(t *testing.T) {
	s := &sinkFixture{}
	c := NewCoordinator(s, func(n int) (float64, error) { return float64(n) - 100, nil })
	l, e := c.Select(identity(config.AllSpeakers, 1), &sourceFixture{}, false, -30, nil)
	if e != nil {
		t.Fatal(e)
	}
	if !c.Volume(l, 0) || !s.muted {
		t.Fatal("native volume zero left audible output")
	}
	l, e = c.Select(identity(config.AllSpeakers, 2), &sourceFixture{}, false, -30, nil)
	if e != nil || !s.muted {
		t.Fatal("seek lost mute")
	}
	c.Mute(l, false)
	if !s.muted {
		t.Fatal("explicit mute false bypassed volume zero")
	}
	if !c.Volume(l, 50) || s.muted {
		t.Fatal("raising native volume did not restore output")
	}
}

func TestOperatorMuteSurvivesInputSeek(t *testing.T) {
	s := &sinkFixture{}
	c := NewCoordinator(s, nil)
	_, e := c.Select(identity(config.AllSpeakers, 1), &sourceFixture{}, false, -30, nil)
	if e != nil {
		t.Fatal(e)
	}
	bridge, ok := any(c).(interface{ SetMuted(bool) error })
	if !ok {
		t.Fatal("operator mute bypasses input ownership state")
	}
	if e = bridge.SetMuted(true); e != nil {
		t.Fatal(e)
	}
	_, e = c.Select(identity(config.AllSpeakers, 2), &sourceFixture{}, false, -30, nil)
	if e != nil || !s.muted {
		t.Fatal("seek lost operator mute")
	}
}
