package control

import (
	"context"
	"github.com/khenrix/home-sound/internal/airplay"
	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/config"
	"github.com/khenrix/home-sound/internal/source"
	"io"
	"testing"
)

type inputFixture struct{ closed bool }

func (*inputFixture) Format() audio.Format                      { return audio.Canonical }
func (*inputFixture) Metadata() airplay.Metadata                { return airplay.Metadata{} }
func (*inputFixture) Read(context.Context) (audio.Frame, error) { return audio.Frame{}, io.EOF }
func (s *inputFixture) Close() error                            { s.closed = true; return nil }
func TestInputAdmissionRetainsServiceProfileGate(t *testing.T) {
	s := newService(t)
	bridge, ok := any(s).(interface {
		InputCoordinator(func(int) (float64, error)) *source.Coordinator
	})
	if !ok {
		t.Fatal("private service has no input ownership seam")
	}
	co := bridge.InputCoordinator(nil)
	in := &inputFixture{}
	if _, e := co.Select(source.Identity{Provider: "spotify", Group: config.AllSpeakers, Instance: "11111111111111111111111111111111", Generation: 1}, in, false, -30, nil); e == nil || !in.closed {
		t.Fatalf("source bypassed calibration gate or leaked: %v", e)
	}
}
