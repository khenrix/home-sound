package spotify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/khenrix/home-sound/internal/source"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestActualPatchedHelperToCoreSubprocess(t *testing.T) {
	path := os.Getenv("HOME_SOUND_FRAMED_FIXTURE")
	if path == "" {
		t.Skip("explicit separately built GPL source fixture required")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(b)
	socket := filepath.Join(t.TempDir(), "pcm.sock")
	l, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	process, e := source.StartProcess(ctx, source.Helper{Path: path, SHA256: hex.EncodeToString(sum[:])}, []string{"-test.run=^TestCoreConsumerFixture$", socket}, 5*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer process.Close()
	l.(*net.UnixListener).SetDeadline(time.Now().Add(2 * time.Second))
	conn, e := l.Accept()
	if e != nil {
		t.Fatal(e)
	}
	o := source.NewObserver()
	defer o.Stop()
	co := source.NewCoordinator(o, nil)
	leases := make(chan source.Lease, 4)
	done := make(chan error, 1)
	go func() {
		done <- (&Input{Group: "All Speakers", Coordinator: co, Lease: func(l source.Lease) { leases <- l }}).Run(ctx, conn)
	}()
	for g := uint64(1); g <= 3; g++ {
		select {
		case l := <-leases:
			if l.Generation != g {
				t.Fatalf("actual helper generation %d want %d", l.Generation, g)
			}
			if g == 2 && o.Snapshot().Transport != "paused" {
				t.Fatal("actual seek lost pause")
			}
		case e := <-done:
			t.Fatalf("actual helper rejected before generation %d: %v", g, e)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	<-done
	h := o.History()
	if len(h) != 3 {
		t.Fatalf("actual helper history: %+v", h)
	}
	if h[0].Metadata.Title != "first" || h[2].Metadata.Title != "second" || h[0].Frames < 960 || h[2].Frames < 960 || h[2].Peak < .7 {
		t.Fatalf("actual source PCM/metadata missing: %+v", h)
	}
}

func TestActualHelperSustainsRealtimeWithBoundedCredit(t *testing.T) {
	path := os.Getenv("HOME_SOUND_FRAMED_FIXTURE")
	if path == "" {
		t.Skip("explicit separately built GPL source fixture required")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(b)
	socket := filepath.Join(t.TempDir(), "pcm.sock")
	l, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, e := source.StartProcess(ctx, source.Helper{Path: path, SHA256: hex.EncodeToString(sum[:])}, []string{"-test.run=^TestCoreSustainedFixture$", socket}, 5*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	l.(*net.UnixListener).SetDeadline(time.Now().Add(2 * time.Second))
	c, e := l.Accept()
	if e != nil {
		t.Fatal(e)
	}
	o := source.NewObserver()
	defer o.Stop()
	co := source.NewCoordinator(o, nil)
	_ = (&Input{Group: "All Speakers", Coordinator: co}).Run(ctx, c)
	h := o.History()
	if len(h) != 1 || h[0].Frames < 80000 || h[0].Frames > 100000 {
		t.Fatalf("bounded credit starved real-time PCM: %+v", h)
	}
}
