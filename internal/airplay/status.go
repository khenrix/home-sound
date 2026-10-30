package airplay

import (
	"bufio"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

// Unknown lines and raw error details never cross the process boundary.
func (a *Adapter) readStatus(r io.ReadCloser) {
	defer r.Close()
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 4096), 16384)
	for s.Scan() {
		a.consume(s.Text())
	}
	if s.Err() != nil {
		a.fail()
	}
}
func (a *Adapter) consume(line string) {
	f := strings.Fields(line)
	if len(f) < 2 || f[0] != "[STATUS]" {
		return
	}
	fields := map[string]string{}
	for _, v := range f[2:] {
		k, v, ok := strings.Cut(v, "=")
		if ok {
			fields[k] = v
		}
	}
	number := func(k string) (int64, bool) {
		v, e := strconv.ParseInt(fields[k], 10, 64)
		return v, e == nil && v >= 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch f[1] {
	case "connected":
		a.status.Connected = true
	case "clock_ready":
		report, ok := parseClockReport(f[2:])
		a.status.ClockMode = report.mode
		a.status.ClockState = report.state
		a.status.ClockMeasurementKnown = report.measured
		a.status.ClockReady = report.ready
		if !ok {
			a.status.Healthy = false
			a.status.Ready = false
			if a.cancel != nil {
				a.cancel()
			}
		}
	case "started", "anchor_corrected":
		requested, ok := number("requested_unix_ms")
		at, okAt := number("at_unix_ms")
		if !ok || !okAt || at == 0 || a.pending == 0 || requested != a.pending {
			return
		}
		if f[1] == "anchor_corrected" {
			from, ok := number("from_unix_ms")
			cut, cutOK := number("content_cut_ms")
			if !ok || !cutOK || a.status.Anchor.At.IsZero() || from != a.status.Anchor.At.UnixMilli() {
				return
			}
			a.cutRequested = cut
			a.status.Anchor.ContentCutPending = true
			a.status.Anchor.Corrected = true
		}
		a.status.Anchor.At = time.UnixMilli(at)
	case "content_cut":
		requested, ok := number("requested_ms")
		cutBytes, okBytes := number("cut_bytes")
		if !ok || !okBytes || !a.status.Anchor.ContentCutPending || requested != a.cutRequested || cutBytes%4 != 0 || uint64(cutBytes/4) > math.MaxUint64-a.status.Anchor.Sample {
			return
		}
		a.status.Anchor.Sample += uint64(cutBytes / 4)
		a.status.Anchor.ContentCutPending = false
	case "error", "stopped", "eof":
		if f[1] == "stopped" {
			a.receiverStopped = true
		}
		if f[1] == "error" {
			a.status.HelperErrorCode, a.status.HelperHTTPStatus = parseHelperError(f[2:])
		}
		a.status.Healthy = false
		a.status.Ready = false
		if f[1] == "error" && a.cancel != nil {
			a.cancel()
		}
	default:
		return
	}
	a.signalLocked()
}

// NTP reports cold/zero indefinitely: the pinned helper cannot measure its clock.
// ClockReady remains a PTP measurement, separate from permission to start NTP.
func clockAllowsStart(s Status) bool {
	return s.ClockReady || (s.ClockMode == "ntp" && s.ClockState == "cold" && !s.ClockMeasurementKnown)
}

type clockReport struct {
	mode, state     string
	measured, ready bool
}

func parseClockReport(tokens []string) (clockReport, bool) {
	if len(tokens) != 6 {
		return clockReport{}, false
	}
	fields := make(map[string]string, 6)
	for _, token := range tokens {
		k, v, ok := strings.Cut(token, "=")
		if !ok || fields[k] != "" || v == "" {
			return clockReport{}, false
		}
		fields[k] = v
	}
	mode, state := fields["mode"], fields["state"]
	if mode != "ntp" && mode != "ptp" {
		return clockReport{}, false
	}
	values := make([]uint64, 4)
	for i, key := range []string{"streak_ms", "exchanges", "ready_in_ms", "ready_at_unix_ms"} {
		raw := fields[key]
		if raw == "" {
			return clockReport{}, false
		}
		for _, c := range raw {
			if c < '0' || c > '9' {
				return clockReport{}, false
			}
		}
		v, e := strconv.ParseUint(raw, 10, 64)
		if e != nil {
			return clockReport{}, false
		}
		values[i] = v
	}
	streak, exchanges, readyIn, readyAt := values[0], values[1], values[2], values[3]
	if exchanges > math.MaxUint32 {
		return clockReport{}, false
	}
	empty := streak == 0 && exchanges == 0 && readyIn == 0 && readyAt == 0
	if mode == "ntp" {
		if state != "cold" || !empty {
			return clockReport{}, false
		}
		return clockReport{mode: mode, state: state}, true
	}
	switch state {
	case "cold", "stalled":
		if !empty {
			return clockReport{}, false
		}
	case "probing":
		if exchanges == 0 || readyIn == 0 || readyAt == 0 {
			return clockReport{}, false
		}
	case "ready":
		if exchanges == 0 || readyIn != 0 || readyAt == 0 {
			return clockReport{}, false
		}
	default:
		return clockReport{}, false
	}
	return clockReport{mode: mode, state: state, measured: state == "probing" || state == "ready", ready: state == "ready"}, true
}
func parseHelperError(tokens []string) (string, int) {
	code := "unknown"
	http := 0
	// Fixed leading fields are authoritative. Never parse key-like text in detail.
	if len(tokens) > 0 {
		value, _ := strings.CutPrefix(tokens[0], "code=")
		switch value {
		case "auth_required", "auth_failed", "connect_failed", "start_failed", "flush_failed", "standby_failed", "play_failed", "pause_failed", "stop_failed", "announce_failed":
			code = value
		}
	}
	if len(tokens) > 1 {
		value, _ := strings.CutPrefix(tokens[1], "http=")
		if v, e := strconv.Atoi(value); e == nil && (v == 0 || (v >= 100 && v <= 599)) {
			http = v
		}
	}
	return code, http
}
