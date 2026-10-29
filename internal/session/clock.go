package session

import "time"

type Mapping struct {
	Clock    Clock
	OffsetNS int64
}

// Freeze samples civil time only once. Use this same offset for every helper and
// receiver in this process; subsequent NTP/manual wall steps cannot move audio.
func Freeze(c Clock, wall func() time.Time) Mapping {
	before := c.Now()
	at := wall()
	after := c.Now()
	return Mapping{Clock: c, OffsetNS: at.UnixNano() - int64(before+(after-before)/2)}
}
func (m Mapping) Time(d time.Duration) time.Time { return time.Unix(0, m.OffsetNS+int64(d)) }
