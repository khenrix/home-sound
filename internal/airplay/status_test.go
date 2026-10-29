package airplay

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/webos"
)

func TestNTPCanPrepareAndStartWithoutMeasuredReadiness(t *testing.T) {
	cfg := fixture(t, "ntp_cold")
	cfg.InitialVolume = 0
	v := &volumeFixture{updates: make(chan webos.Volume, 1)}
	v.updates <- webos.Volume{Level: 10, Valid: true}
	cfg.LG = v
	o, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err = o.Prepare(ctx, 12, 48000); err != nil {
		t.Fatalf("observed NTP cold session did not prepare: %v", err)
	}
	if v.calls != 2 {
		t.Fatal("NTP path bypassed native pre/post checks")
	}
	s := o.Status()
	raw, _ := json.Marshal(s)
	var telemetry map[string]any
	json.Unmarshal(raw, &telemetry)
	if telemetry["ClockMode"] != "ntp" || telemetry["ClockState"] != "cold" || telemetry["ClockMeasurementKnown"] != false {
		t.Fatalf("missing unmeasurable NTP telemetry: %s", raw)
	}
	if !s.Ready || s.ClockReady {
		t.Fatalf("NTP transport readiness misrepresented as measured clock readiness: %+v", s)
	}
	if err = o.Write(ctx, audio.Frame{Epoch: 12, Sample: 48000, Data: []float32{.1, .1}}); err != nil {
		t.Fatal(err)
	}
	if _, err = o.Start(ctx, 12, time.UnixMilli(2000000)); err != nil {
		t.Fatalf("NTP START remained gated on unmeasurable readiness: %v", err)
	}
}

func TestInvalidClockReadinessCannotPrepare(t *testing.T) {
	for _, mode := range []string{"unknown_clock", "missing_clock_mode", "bad_clock_number", "ntp_badstate", "ntp_nonzero", "ptp_cold", "ptp_probing", "ptp_stalled"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			o, err := New(fixture(t, mode))
			if err != nil {
				t.Fatal(err)
			}
			defer o.Stop()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if o.Prepare(ctx, 1, 0) == nil {
				t.Fatalf("%s bypassed readiness", mode)
			}
			if o.Status().Ready {
				t.Fatal("invalid/unready clock exposed ready output")
			}
		})
	}
}

func TestStructuredHelperFailureIsRedacted(t *testing.T) {
	const secret = "secret-canary-never-disclose"
	for _, tc := range []struct {
		line, code string
		http       int
	}{
		{`[STATUS] error code=connect_failed http=503 detail="` + secret + ` code=auth_failed http=401"`, "connect_failed", 503},
		{`[STATUS] error code=auth_failed http=470 detail="` + secret + `"`, "auth_failed", 470},
		{`[STATUS] error code=` + secret + ` http=0 detail="failure"`, "unknown", 0},
		{`[STATUS] error code=start_failed http=` + secret + ` detail="failure"`, "start_failed", 0},
	} {
		o, err := New(fixture(t, "ready"))
		if err != nil {
			t.Fatal(err)
		}
		o.consume(tc.line)
		raw, err := json.Marshal(o.Status())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), secret) {
			t.Fatal("raw helper error leaked")
		}
		var status map[string]any
		if json.Unmarshal(raw, &status) != nil {
			t.Fatal("invalid status")
		}
		if status["HelperErrorCode"] != tc.code || status["HelperHTTPStatus"] != float64(tc.http) {
			t.Fatalf("missing/suspect structured diagnostics: %s", raw)
		}
	}
}

func TestNTPStillEnforcesNativeGateAndMonitor(t *testing.T) {
	for _, failGate := range []bool{true, false} {
		t.Run(map[bool]string{true: "post_gate", false: "monitor"}[failGate], func(t *testing.T) {
			cfg := fixture(t, "ntp_cold")
			cfg.InitialVolume = 0
			v := &volumeFixture{updates: make(chan webos.Volume, 1)}
			v.updates <- webos.Volume{Level: 10, Valid: true}
			if failGate {
				v.failAt = 2
			}
			cfg.LG = v
			o, e := New(cfg)
			if e != nil {
				t.Fatal(e)
			}
			defer o.Stop()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			e = o.Prepare(ctx, 9, 0)
			if failGate {
				if e == nil {
					t.Fatal("NTP bypassed failing native post-readiness gate")
				}
			} else {
				if e != nil {
					t.Fatal(e)
				}
				v.updates <- webos.Volume{Level: 16, Valid: true}
				select {
				case <-o.done:
				case <-ctx.Done():
					t.Fatal("NTP bypassed native subscription monitor")
				}
			}
			if o.Write(ctx, audio.Frame{Epoch: 9, Data: []float32{.1, .1}}) == nil {
				t.Fatal("NTP released PCM after native failure")
			}
			if _, e = o.Start(ctx, 9, time.UnixMilli(2000000)); e == nil {
				t.Fatal("NTP released START after native failure")
			}
		})
	}
}

func TestPTPReadyTelemetryRemainsMeasured(t *testing.T) {
	o, e := New(fixture(t, "ready"))
	if e != nil {
		t.Fatal(e)
	}
	defer o.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e = o.Prepare(ctx, 1, 0); e != nil {
		t.Fatal(e)
	}
	s := o.Status()
	if !s.ClockReady || !s.ClockMeasurementKnown || s.ClockMode != "ptp" || s.ClockState != "ready" {
		t.Fatalf("lost measured PTP status: %+v", s)
	}
}

func TestProcessFailureKeepsSanitizedDiagnostics(t *testing.T) {
	for i := 0; i < 20; i++ {
		o, e := New(fixture(t, "error_status"))
		if e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		e = o.Prepare(ctx, 1, 0)
		cancel()
		o.Stop()
		if e == nil {
			t.Fatal("failed helper prepared")
		}
		s := o.Status()
		if s.HelperErrorCode != "connect_failed" || s.HelperHTTPStatus != 503 {
			t.Fatalf("lost process failure diagnostics: %+v", s)
		}
		if strings.Contains(e.Error(), "secret-canary") {
			t.Fatal("process error leaked detail")
		}
	}
}
