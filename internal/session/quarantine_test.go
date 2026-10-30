package session

import (
	"errors"
	"testing"

	"github.com/khenrix/home-sound/internal/config"
)

func TestFailedTeardownSurvivesSelectionAndConfiguration(t *testing.T) {
	for _, action := range []string{"recast", "seek", "stop", "profile_alias"} {
		t.Run(action, func(t *testing.T) {
			e, _, d := bufferedEngine(t, -29)
			target := -30.
			if _, err := e.Select(Selection{Group: config.TVAndSpeakers, Source: &testSource{}, TargetDB: &target}); err != nil {
				t.Fatal(err)
			}
			eventually(t, func() bool { return d.latest() != nil && d.latest().Status().Frames >= 96000 })
			old := d.latest()
			d.mu.Lock()
			d.stopErr = errors.New("receiver teardown unresolved")
			d.mu.Unlock()
			if action == "stop" || action == "profile_alias" {
				e.Stop()
			}
			if action == "profile_alias" {
				cfg := cloneConfig(e.cfg)
				device := cfg.Devices["tv"]
				device.Host = "::ffff:192.0.2.10"
				device.Port = 7001
				cfg.Devices["renamed_tv"] = device
				cfg.Profiles["renamed_tv"] = cfg.Profiles["tv"]
				delete(cfg.Devices, "tv")
				delete(cfg.Profiles, "tv")
				cfg.Groups[config.TVAndSpeakers][0] = "renamed_tv"
				if err := e.Reconfigure(cfg); err != nil {
					t.Fatal(err)
				}
			}
			target = -29
			next := &testSource{}
			sel := Selection{Group: config.TVAndSpeakers, Source: next, TargetDB: &target}
			if action == "seek" {
				next.next = 48000
				sel.FirstSample = 48000
			}
			_, err := e.Select(sel)
			if err == nil {
				eventually(t, func() bool { return d.latest() != old && d.latest().Status().Frames > 0 })
			} else {
				next.Close()
			}
			d.mu.Lock()
			peak, native := d.peak, d.native
			d.mu.Unlock()
			if peak > -29+1e-5 || native != 10 {
				t.Fatalf("%s bypassed unknown teardown: native=%d peak=%.1f", action, native, peak)
			}
			if err == nil {
				t.Fatal("selection accepted an unresolved receiver")
			}
			if ids := e.Status().QuarantinedDevices; len(ids) != 1 || ids[0] != "tv" {
				t.Fatalf("unresolved device disappeared from status: %v", ids)
			}
			// Quarantining the TV does not prohibit a selection of healthy speakers.
			target = -30
			if _, err := e.Select(Selection{Group: config.AllSpeakers, Source: &testSource{}, TargetDB: &target}); err != nil {
				t.Fatal(err)
			}
			eventually(t, func() bool { return e.Status().Members["hall"].SubmittedFrames > 0 })
		})
	}
}
func TestOnlySuccessfulOldOutputTeardownReleasesQuarantine(t *testing.T) {
	e, _, d := bufferedEngine(t, -29)
	target := -30.
	if _, err := e.Select(Selection{Group: config.TVAndSpeakers, Source: &testSource{}, TargetDB: &target}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return d.latest() != nil && d.latest().Status().Frames >= 96000 })
	old := d.latest()
	d.mu.Lock()
	d.stopErr = errors.New("receiver unavailable")
	d.mu.Unlock()
	e.Stop()
	d.mu.Lock()
	d.stopErr = nil
	d.mu.Unlock() // Old connection can now actually flush.
	target = -29
	if _, err := e.Select(Selection{Group: config.TVAndSpeakers, Source: &testSource{}, TargetDB: &target}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return d.latest() != old && d.latest().Status().Frames > 0 })
	d.mu.Lock()
	peak := d.peak
	_, retained := d.pending[old]
	d.mu.Unlock()
	if len(e.Status().QuarantinedDevices) != 0 {
		t.Fatal("successful old teardown did not release quarantine")
	}
	if retained || peak > -29+1e-5 {
		t.Fatalf("new output preceded successful old teardown: retained=%v peak=%.1f", retained, peak)
	}
}
