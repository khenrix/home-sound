package airplay

import (
	"context"
	"encoding/binary"
	"errors"
	"github.com/khenrix/home-sound/internal/audio"
	"math"
)

// Finish drains only the converter's bounded lookahead. The persistent helper
// stays open until Stop flushes the receiver; closing stdin here would end it.
func (a *Adapter) Finish(ctx context.Context) error {
	a.op.Lock()
	defer a.op.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	valid := a.status.Ready && a.status.Healthy
	input := a.input
	a.mu.Unlock()
	if !valid {
		return errors.New("output not ready")
	}
	if a.finished {
		return nil
	}
	a.finished = true
	a.mu.Lock()
	rate := uint64(a.cfg.Transport.Rate)
	remainder := (a.status.Frames % 48000) * rate % 48000
	if remainder != 0 {
		den := 48000 * rate
		a.status.DurationPaddingNS = ((48000-remainder)*1000000000 + den - 1) / den
	}
	a.mu.Unlock()
	if a.converter == nil {
		return nil
	}
	data, err := a.converter.Finish()
	if err != nil {
		a.fail()
		return err
	}
	raw := make([]byte, len(data)*2)
	for i, v := range data {
		q, _ := audio.PCM16(v)
		binary.LittleEndian.PutUint16(raw[i*2:], uint16(q))
	}
	a.mu.Lock()
	if len(raw) > 0 {
		a.pcmSubmitted = true
	}
	a.mu.Unlock()
	if err = writeBounded(ctx, input, raw); err != nil {
		a.fail()
	}
	return err
}

// Report cuts in canonical frames rounded upward. The explicit accumulated
// uncertainty bounds the content-time loss from that conservative rounding.
func canonicalCut(frames uint64, rate int) (uint64, uint64, bool) {
	if rate == 0 {
		rate = 48000
	}
	if rate != 44100 && rate != 48000 {
		return 0, 0, false
	}
	seconds, remaining := frames/uint64(rate), frames%uint64(rate)
	if seconds > math.MaxUint64/48000 {
		return 0, 0, false
	}
	numerator := remaining * 48000
	fraction := (numerator + uint64(rate) - 1) / uint64(rate)
	count := seconds * 48000
	if fraction > math.MaxUint64-count {
		return 0, 0, false
	}
	count += fraction
	remainder := numerator % uint64(rate)
	var uncertainty uint64
	if remainder != 0 {
		den := uint64(rate) * 48000
		uncertainty = ((uint64(rate)-remainder)*1000000000 + den - 1) / den
	}
	return count, uncertainty, true
}
