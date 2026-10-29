// Package airplay adapts the pinned cliairplay persistent PCM session.
package airplay

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/webos"
)

const HelperCommit = "0a58c239d5fb3cf8927b9707a71c6fe897266b6a"

type NativeVolume interface {
	SetVolume(context.Context, int) error
	Volume(context.Context) (webos.Volume, error)
	WatchVolume(context.Context) (<-chan webos.Volume, error)
}
type Config struct {
	Helper, HelperSHA256, Host, PairingFile string
	Port, InitialVolume                     int
	ClockOffsetNS                           *int64
	LG                                      NativeVolume
}
type Anchor struct {
	Epoch, Sample                uint64
	Requested, At                time.Time
	Corrected, ContentCutPending bool
}
type Status struct {
	Healthy, Connected, ClockReady, Ready bool
	Anchor                                Anchor
	Frames                                uint64
}
type Metadata struct{ Title, Artist, Album string }
type Output interface {
	Prepare(context.Context, uint64, uint64) error
	Write(context.Context, audio.Frame) error
	Start(context.Context, uint64, time.Time) (Anchor, error)
	Stop() error
	Status() Status
	Metadata(context.Context, Metadata) error
}
type Adapter struct {
	cfg                Config
	op                 sync.Mutex
	mu                 sync.Mutex
	status             Status
	changed            chan struct{}
	cmd                *exec.Cmd
	input, commands    *os.File
	cancel             context.CancelFunc
	done               chan struct{}
	dir                string
	prepared, stopped  bool
	epoch, first, next uint64
	pending            int64
	cutRequested       int64
}

func New(cfg Config) (*Adapter, error) {
	if !filepath.IsAbs(cfg.Helper) || net.ParseIP(cfg.Host).To4() == nil || cfg.Port < 1 || cfg.Port > 65535 {
		return nil, errors.New("absolute helper path, IPv4 address and port required")
	}
	if cfg.LG != nil {
		if cfg.InitialVolume != 0 {
			return nil, errors.New("LG requires suppressed AirPlay volume setter")
		}
	} else if cfg.InitialVolume < 1 || cfg.InitialVolume > 15 {
		return nil, errors.New("explicit low initial AirPlay volume (1 through 15) required")
	}
	if err := verifyHelper(cfg.Helper, cfg.HelperSHA256); err != nil {
		return nil, err
	}
	if cfg.ClockOffsetNS != nil {
		offset := *cfg.ClockOffsetNS
		cfg.ClockOffsetNS = &offset
	}
	return &Adapter{cfg: cfg, changed: make(chan struct{})}, nil
}
func verifyHelper(path, checksum string) error {
	want, e := hex.DecodeString(checksum)
	if e != nil || len(want) != 32 {
		return errors.New("helper SHA256 required")
	}
	f, e := os.Open(path)
	if e != nil {
		return errors.New("cannot open AirPlay helper")
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil || !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), checksum) {
		return errors.New("AirPlay helper checksum mismatch")
	}
	return nil
}
func (a *Adapter) signalLocked()  { close(a.changed); a.changed = make(chan struct{}) }
func (a *Adapter) Status() Status { a.mu.Lock(); defer a.mu.Unlock(); return a.status }
func (a *Adapter) fail() {
	a.mu.Lock()
	a.stopped = true
	a.status.Healthy = false
	a.status.Ready = false
	a.signalLocked()
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
func (a *Adapter) gate(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if a.cfg.LG == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if a.cfg.LG.SetVolume(ctx, 10) != nil {
		return errors.New("native LG volume gate failed")
	}
	v, e := a.cfg.LG.Volume(ctx)
	if e != nil || !v.Valid || v.Muted || v.Level != 10 {
		return errors.New("native LG volume readback failed")
	}
	return nil
}
func (a *Adapter) Prepare(ctx context.Context, epoch, first uint64) (resultErr error) {
	a.op.Lock()
	defer a.op.Unlock()
	a.mu.Lock()
	if a.prepared || a.stopped {
		a.mu.Unlock()
		return errors.New("output is single-session; create a new adapter for a new epoch")
	}
	childCtx, cancel := context.WithCancel(ctx)
	defer func() {
		if resultErr != nil {
			a.fail()
			cancel()
		}
	}()
	a.cancel = cancel
	a.prepared = true
	a.epoch = epoch
	a.first = first
	a.next = first
	a.mu.Unlock()
	if err := a.gate(childCtx); err != nil {
		cancel()
		return err
	}
	if err := verifyHelper(a.cfg.Helper, a.cfg.HelperSHA256); err != nil {
		cancel()
		return err
	}
	args := []string{"--protocol", "airplay2", "--ap2-native", "--port", strconv.Itoa(a.cfg.Port), "--volume", strconv.Itoa(a.cfg.InitialVolume), "--samplerate", "48000", "--bitdepth", "16", "--channels", "2", "--debug", "0"}
	if a.cfg.ClockOffsetNS != nil {
		args = append(args, "--clock-offset-ns", strconv.FormatInt(*a.cfg.ClockOffsetNS, 10))
	}
	if a.cfg.PairingFile != "" {
		p, e := loadCredentials(a.cfg.PairingFile, a.cfg.Host, a.cfg.Port)
		if e != nil {
			return e
		}
		args = append(args, "--auth", p.Auth, "--dacp", p.DACP)
	}
	dir, e := os.MkdirTemp("", "home-sound-airplay-*")
	if e != nil {
		return errors.New("cannot create private helper directory")
	}
	a.dir = dir
	fifo := filepath.Join(dir, "commands")
	if syscall.Mkfifo(fifo, 0600) != nil {
		os.RemoveAll(dir)
		return errors.New("cannot create helper command pipe")
	}
	commands, e := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0600)
	if e != nil {
		os.RemoveAll(dir)
		return errors.New("cannot open helper command pipe")
	}
	cmd := exec.CommandContext(childCtx, a.cfg.Helper, append(args, "--cmdpipe", fifo, a.cfg.Host)...)
	inputR, inputW, e := os.Pipe()
	if e != nil {
		commands.Close()
		cancel()
		os.RemoveAll(dir)
		return errors.New("cannot create helper input")
	}
	outputR, outputW, e := os.Pipe()
	if e != nil {
		inputR.Close()
		inputW.Close()
		commands.Close()
		cancel()
		os.RemoveAll(dir)
		return errors.New("cannot create helper status pipe")
	}
	cmd.Stdin = inputR
	cmd.Stdout = outputW
	cmd.Stderr = outputW
	if cmd.Start() != nil {
		inputR.Close()
		inputW.Close()
		outputR.Close()
		outputW.Close()
		commands.Close()
		cancel()
		os.RemoveAll(dir)
		return errors.New("cannot start AirPlay helper")
	}
	inputR.Close()
	outputW.Close()
	a.mu.Lock()
	a.cmd = cmd
	a.input = inputW
	a.commands = commands
	a.cancel = cancel
	a.done = make(chan struct{})
	a.status.Healthy = !a.stopped
	a.signalLocked()
	done := a.done
	a.mu.Unlock()
	go a.readStatus(outputR)
	go func() {
		cmd.Wait()
		outputR.Close()
		a.fail()
		inputW.Close()
		commands.Close()
		os.RemoveAll(dir)
		close(done)
	}()
	if e = a.wait(ctx, func(s Status) bool { return s.Connected && s.ClockReady }); e != nil {
		a.fail()
		return e
	}
	if e = a.gate(ctx); e != nil {
		a.fail()
		return e
	}
	if a.cfg.LG != nil {
		updates, err := a.cfg.LG.WatchVolume(childCtx)
		if err != nil {
			a.fail()
			return errors.New("native LG monitoring failed")
		}
		select {
		case <-childCtx.Done():
			a.fail()
			return childCtx.Err()
		case v, ok := <-updates:
			if !ok || !v.Valid || v.Muted || v.Level != 10 {
				a.fail()
				return errors.New("native LG subscription not safe")
			}
		case <-time.After(5 * time.Second):
			a.fail()
			return errors.New("native LG subscription not acknowledged")
		}
		go func() {
			for {
				select {
				case <-childCtx.Done():
					return
				case v, ok := <-updates:
					if !ok || !v.Valid || v.Muted || v.Level < 0 || v.Level > 15 {
						a.fail()
						return
					}
				}
			}
		}()
	}
	a.mu.Lock()
	if !a.status.Healthy {
		a.mu.Unlock()
		return errors.New("AirPlay helper ended")
	}
	a.status.Ready = true
	a.signalLocked()
	a.mu.Unlock()
	return nil
}
func (a *Adapter) wait(ctx context.Context, predicate func(Status) bool) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		a.mu.Lock()
		s, ch := a.status, a.changed
		a.mu.Unlock()
		if !s.Healthy {
			return errors.New("AirPlay helper failed")
		}
		if predicate(s) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}
func writeBounded(ctx context.Context, f *os.File, p []byte) error {
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if f.SetWriteDeadline(deadline) != nil {
		return errors.New("helper pipe does not support deadlines")
	}
	stop := context.AfterFunc(ctx, func() { f.SetWriteDeadline(time.Now()) })
	defer stop()
	for len(p) > 0 {
		n, e := f.Write(p)
		if e != nil {
			return errors.New("helper pipe write failed")
		}
		p = p[n:]
	}
	return nil
}
func (a *Adapter) Write(ctx context.Context, f audio.Frame) error {
	if err := f.Validate(audio.Canonical); err != nil {
		return err
	}
	if len(f.Data) > 48000*2 {
		return errors.New("frame exceeds one second")
	}
	a.op.Lock()
	defer a.op.Unlock()
	a.mu.Lock()
	s := a.status
	valid := s.Ready && s.Healthy && f.Epoch == a.epoch && f.Sample == a.next
	input := a.input
	a.mu.Unlock()
	if !valid {
		return errors.New("output not ready or stale/noncontiguous frame")
	}
	raw := make([]byte, len(f.Data)*2)
	for i, v := range f.Data {
		q, _ := audio.PCM16(v)
		binary.LittleEndian.PutUint16(raw[i*2:], uint16(q))
	}
	if err := writeBounded(ctx, input, raw); err != nil {
		a.fail()
		return err
	}
	a.mu.Lock()
	a.next += uint64(len(f.Data) / 2)
	a.status.Frames += uint64(len(f.Data) / 2)
	a.mu.Unlock()
	return nil
}
func (a *Adapter) Start(ctx context.Context, epoch uint64, at time.Time) (Anchor, error) {
	a.op.Lock()
	defer a.op.Unlock()
	a.mu.Lock()
	if !a.status.Ready || !a.status.Healthy || !a.status.ClockReady || epoch != a.epoch || a.pending != 0 || at.UnixMilli() <= 0 {
		a.mu.Unlock()
		return Anchor{}, errors.New("invalid or stale start")
	}
	a.pending = at.UnixMilli()
	a.status.Anchor = Anchor{Epoch: epoch, Sample: a.first, Requested: time.UnixMilli(at.UnixMilli())}
	a.mu.Unlock()
	if err := writeBounded(ctx, a.commands, []byte(fmt.Sprintf("START_UNIX_MS=%d\nACTION=START\n", at.UnixMilli()))); err != nil {
		a.fail()
		return Anchor{}, err
	}
	if err := a.wait(ctx, func(s Status) bool { return !s.Anchor.At.IsZero() }); err != nil {
		a.fail()
		return Anchor{}, err
	}
	return a.Status().Anchor, nil
}
func (a *Adapter) Metadata(ctx context.Context, m Metadata) error {
	for _, v := range []string{m.Title, m.Artist, m.Album} {
		if len(v) > 1024 || strings.ContainsAny(v, "\r\n\x00") {
			return errors.New("invalid metadata")
		}
	}
	a.op.Lock()
	defer a.op.Unlock()
	s := a.Status()
	if !s.Ready || !s.Healthy {
		return errors.New("output not ready")
	}
	if err := writeBounded(ctx, a.commands, []byte("TITLE="+m.Title+"\nARTIST="+m.Artist+"\nALBUM="+m.Album+"\nACTION=SENDMETA\n")); err != nil {
		a.fail()
		return err
	}
	return nil
}
func (a *Adapter) Stop() error {
	a.mu.Lock()
	healthy, commands, done := a.status.Healthy, a.commands, a.done
	a.stopped = true
	a.status.Healthy = false
	a.status.Ready = false
	a.signalLocked()
	a.mu.Unlock()
	if healthy && commands != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		err := writeBounded(ctx, commands, []byte("ACTION=STOP\n"))
		cancel()
		if err == nil {
			select {
			case <-done:
				return nil
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	a.fail()
	if done != nil {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			return errors.New("helper teardown timed out")
		}
	}
	return nil
}
