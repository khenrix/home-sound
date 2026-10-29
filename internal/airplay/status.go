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
		a.status.ClockReady = fields["state"] == "ready"
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
