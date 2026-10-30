package session

import (
	"github.com/khenrix/home-sound/internal/config"
	"testing"
	"time"
)

type bufferedSource struct{ testSource }

func (*bufferedSource) BufferedSamples() uint64 { return 48000 }
func TestExternalPCMReserveSharesSendAhead(t *testing.T) {
	e, _, _ := setup(t)
	s := &bufferedSource{}
	selectSource(t, e, config.AllSpeakers, s)
	eventually(t, func() bool { return s.count() >= 50 })
	time.Sleep(30 * time.Millisecond)
	if got := s.count(); got > 50 {
		t.Fatalf("external one-second PCM reserve ignored: %d reads, want <=50 before one-second presentation lead", got)
	}
}
