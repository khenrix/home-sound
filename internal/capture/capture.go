// Package capture runs an explicitly requested, bounded ALSA recording.
package capture

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/khenrix/home-sound/internal/audio"
)

type Config struct{ Helper, Device string }
type Status struct {
	Frames  uint64
	Samples uint64
}
type Capture interface {
	Record(context.Context, time.Duration) ([]float32, Status, error)
}
type ALSA struct{ Config Config }

func (a ALSA) Record(ctx context.Context, duration time.Duration) ([]float32, Status, error) {
	return Record(ctx, a.Config, duration)
}

func Record(ctx context.Context, cfg Config, duration time.Duration) ([]float32, Status, error) {
	var status Status
	if !filepath.IsAbs(cfg.Helper) || cfg.Device == "" {
		return nil, status, errors.New("helper path and device are required")
	}
	if duration <= 0 || duration > 60*time.Second {
		return nil, status, errors.New("capture duration must be in (0, 60s]")
	}
	frames := int(int64(duration) * int64(audio.Canonical.Rate) / int64(time.Second))
	if frames == 0 {
		return nil, status, errors.New("duration shorter than one frame")
	}
	ctx, cancel := context.WithTimeout(ctx, duration+5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cfg.Helper, "-D", cfg.Device, "-t", "raw", "-f", "S16_LE", "-r", "48000", "-c", "2", "-s", strconv.Itoa(frames))
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, status, err
	}
	if err = cmd.Start(); err != nil {
		return nil, status, err
	}
	// The sample limit asks arecord to exit after exactly the requested frames.
	raw := make([]byte, frames*4)
	n, readErr := io.ReadFull(pipe, raw)
	if readErr == nil {
		var extra [1]byte
		nExtra, extraErr := pipe.Read(extra[:])
		if nExtra != 0 || extraErr != io.EOF {
			readErr = errors.New("helper returned excess or unreadable PCM")
			_ = cmd.Process.Kill()
		}
	}
	waitErr := cmd.Wait()
	status.Frames = uint64(n / 4)
	status.Samples = uint64(n / 2)
	if ctx.Err() != nil {
		return nil, status, ctx.Err()
	}
	if readErr != nil {
		return nil, status, fmt.Errorf("capture ended after %d bytes: %w (process: %v)", n, readErr, waitErr)
	}
	if waitErr != nil {
		return nil, status, fmt.Errorf("capture helper failed: %w", waitErr)
	}
	samples := make([]float32, n/2)
	for i := range samples {
		samples[i] = float32(int16(binary.LittleEndian.Uint16(raw[2*i:]))) / 32768
	}
	return samples, status, nil
}
