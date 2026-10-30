package inputs

import (
	"context"
	"github.com/khenrix/home-sound/internal/source"
	"strings"
	"testing"
)

func TestRuntimeDefaultsStartNoProcesses(t *testing.T) {
	r, e := Start(context.Background(), Config{Version: 1}, source.NewCoordinator(source.NewObserver(), nil), true)
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Close(); e != nil {
		t.Fatal(e)
	}
}
func TestSpotifyConfigHasPrivateDistinctIdentityAndFramedExternalPCM(t *testing.T) {
	b := string(SpotifyYAML(Identity{Group: "TV + All Speakers", DeviceID: strings.Repeat("a", 40)}, "/private/0/pcm.sock", 38401, 23))
	for _, want := range []string{"device_name: \"TV + All Speakers\"", "audio_backend: framed", "external_volume: true", "normalisation_disabled: true", "crossfade_duration: 0", "ignore_last_volume: true", "initial_volume: 23", "port: 38401"} {
		if !strings.Contains(b, want) {
			t.Fatalf("missing required helper configuration %q", want)
		}
	}
}

func TestRuntimeFailedValidationCleansUpWithoutPanic(t *testing.T) {
	c := Config{Version: 1, StateDir: t.TempDir(), Spotify: SpotifyConfig{Enabled: true, Ports: [2]int{38101, 38102}}, Volume: VolumeConfig{MinDB: -50, MaxDB: -30, Initial: 20}}
	if _, e := Start(context.Background(), c, source.NewCoordinator(source.NewObserver(), nil), true); e == nil {
		t.Fatal("missing pinned helper admitted")
	}
}
