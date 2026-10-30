// Package source owns bounded input processes and source ownership.
package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type Helper struct{ Path, SHA256 string }

func (h Helper) Validate() error {
	if !filepath.IsAbs(h.Path) || len(h.SHA256) != 64 {
		return errors.New("explicit helper path and SHA256 required")
	}
	f, e := os.Open(h.Path)
	if e != nil {
		return errors.New("helper unavailable")
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 {
		return errors.New("helper must be executable regular file")
	}
	sum := sha256.New()
	if _, e = io.Copy(sum, f); e != nil || hex.EncodeToString(sum.Sum(nil)) != h.SHA256 {
		return errors.New("helper hash mismatch")
	}
	return nil
}

// Process has one stdout reader and one Wait owner. Its stderr is discarded up
// to a hard cap and never appears in errors. Close kills/reaps the entire owned
// process group; cancellation cannot leave a decoder blocked on a full pipe.
type Process struct {
	stdout  *os.File
	cancel  context.CancelFunc
	done    chan struct{}
	waitErr error
	once    sync.Once
}

func StartProcess(ctx context.Context, h Helper, args []string, lifetime time.Duration) (*Process, error) {
	if lifetime <= 0 || lifetime > 7*time.Hour {
		return nil, errors.New("bounded helper lifetime required")
	}
	run, cancel := context.WithTimeout(ctx, lifetime)
	return startProcess(run, cancel, h, args)
}

// StartDaemon has a context-owned lifetime; all I/O and teardown bounds remain
// the same as media helpers without imposing an arbitrary service restart.
func StartDaemon(ctx context.Context, h Helper, args []string) (*Process, error) {
	run, cancel := context.WithCancel(ctx)
	return startProcess(run, cancel, h, args)
}
func startProcess(run context.Context, cancel context.CancelFunc, h Helper, args []string) (*Process, error) {
	if e := h.Validate(); e != nil {
		cancel()
		return nil, e
	}
	cmd := exec.CommandContext(run, h.Path, args...)
	cmd.WaitDelay = 500 * time.Millisecond
	if e := ownProcessGroup(cmd); e != nil {
		cancel()
		return nil, e
	}
	reader, writer, e := os.Pipe()
	if e != nil {
		cancel()
		return nil, errors.New("source pipe failed")
	}
	if e = boundPipe(reader); e != nil {
		reader.Close()
		writer.Close()
		cancel()
		return nil, errors.New("source pipe capacity unavailable")
	}
	cmd.Stdout = writer
	cmd.Stderr = &discardLimit{remaining: 4096, cancel: cancel}
	if e = cmd.Start(); e != nil {
		reader.Close()
		writer.Close()
		cancel()
		return nil, errors.New("source helper start failed")
	}
	writer.Close()
	p := &Process{stdout: reader, cancel: cancel, done: make(chan struct{})}
	go func() { p.waitErr = cmd.Wait(); close(p.done) }()
	return p, nil
}
func (p *Process) Read(ctx context.Context, b []byte) (int, error) {
	if e := ctx.Err(); e != nil {
		return 0, e
	}
	stop := context.AfterFunc(ctx, func() { p.cancel(); p.stdout.Close() })
	defer stop()
	n, e := p.stdout.Read(b)
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if e == io.EOF {
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
			p.Close()
			return n, errors.New("source helper did not exit after EOF")
		}
		if p.waitErr != nil {
			return n, errors.New("source helper failed")
		}
		return n, io.EOF
	}
	if e != nil {
		return n, errors.New("source helper read failed")
	}
	return n, nil
}
func (p *Process) Close() error {
	p.once.Do(func() { p.cancel(); p.stdout.Close() })
	select {
	case <-p.done:
		return nil
	case <-time.After(time.Second):
		return errors.New("source helper reap timed out")
	}
}

type discardLimit struct {
	remaining int
	cancel    context.CancelFunc
}

func (w *discardLimit) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		w.cancel()
		return 0, errors.New("helper diagnostic limit")
	}
	w.remaining -= len(p)
	return len(p), nil
}
