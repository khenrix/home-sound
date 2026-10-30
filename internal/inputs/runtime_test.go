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
	b := string(SpotifyYAML(Identity{Group: "TV + All Speakers", DeviceID: strings.Repeat("a", 40)}, "/private/0/pcm.sock", 38401, 23, 38421))
	for _, want := range []string{"device_name: \"TV + All Speakers\"", "audio_backend: framed", "external_volume: true", "normalisation_disabled: true", "crossfade_duration: 0", "ignore_last_volume: true", "initial_volume: 23", "port: 38401", "zeroconf_port: 38421", "address: 127.0.0.1"} {
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

func TestPinnedFFmpegReleaseAcceptsMeasuredTagSpelling(t *testing.T) {
	if !deploymentDecoderVersion("n9.0.2") {
		t.Fatal("measured pinned FFmpeg n9.0.2 release rejected")
	}
	for _, v := range []string{"9.0.1", "n9.0.3", "n9.0.2-custom", "master"} {
		if deploymentDecoderVersion(v) {
			t.Fatal("unapproved FFmpeg release accepted")
		}
	}
}
