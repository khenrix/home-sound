package airplay

import (
	"errors"
	"fmt"
	"strconv"
)

// Transport describes the helper wire format; the public bus remains 48k stereo.
type Transport struct {
	Rate   int    `json:"rate,omitempty"`
	Timing string `json:"timing,omitempty"`
}

func (t Transport) Normalize() (Transport, error) {
	if t.Rate == 0 {
		t.Rate = 48000
	}
	if t.Timing == "" {
		t.Timing = "ntp"
	}
	if (t.Rate != 48000 && t.Rate != 44100) || (t.Timing != "ntp" && t.Timing != "ptp") {
		return Transport{}, errors.New("transport requires 44100 or 48000 Hz and ntp or ptp")
	}
	return t, nil
}
func (t Transport) Fingerprint(helperSHA string) string {
	t, err := t.Normalize()
	if err != nil {
		return ""
	}
	filter := "identity"
	if t.Rate == 44100 {
		filter = "fir256-blackman-v1"
	}
	return fmt.Sprintf("airplay2/native/realtime/%d/stereo/pcm16/%s/%s/%s", t.Rate, t.Timing, filter, helperSHA)
}
func (a *Adapter) rejectLocked() {
	a.status.Healthy = false
	a.status.Ready = false
	if a.cancel != nil {
		a.cancel()
	}
}
func (a *Adapter) routeLocked(fields map[string]string) {
	t, _ := a.cfg.Transport.Normalize()
	if len(fields) != 4 || fields["protocol"] != "airplay2" || fields["flow"] != "native" || fields["timing"] != t.Timing || fields["buffered"] != "0" {
		a.rejectLocked()
		return
	}
	a.status.RouteVerified = true
}
func (a *Adapter) capabilitiesLocked(fields map[string]string) {
	t, _ := a.cfg.Transport.Normalize()
	want := uint64(1 << 20)
	if t.Rate == 44100 {
		want = 1 << 18
	}
	if len(fields) != 5 {
		a.rejectLocked()
		return
	}
	values := map[string]uint64{}
	for _, key := range []string{"requested", "realtime_formats", "buffered_formats"} {
		v, err := strconv.ParseUint(fields[key], 0, 64)
		if err != nil {
			a.rejectLocked()
			return
		}
		values[key] = v
	}
	for _, key := range []string{"realtime_known", "buffered_known"} {
		if fields[key] != "0" && fields[key] != "1" {
			a.rejectLocked()
			return
		}
	}
	if values["requested"] != want || (fields["realtime_known"] == "1" && values["realtime_formats"]&want == 0) {
		a.rejectLocked()
		return
	}
	a.status.CapabilitiesVerified = true
}
