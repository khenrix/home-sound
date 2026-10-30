package spotify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/khenrix/home-sound/internal/source"
	"io"
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
	o := source.NewObserver()
	defer o.Stop()
	co := source.NewCoordinator(o, nil)
	sum := sha256.Sum256(b)
	socket := filepath.Join(t.TempDir(), "pcm.sock")
	l, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	admissions, admissionSocket := testAdmissionServer(t, ctx, co)
	process, e := source.StartProcess(ctx, source.Helper{Path: path, SHA256: hex.EncodeToString(sum[:])}, []string{"-test.run=^TestCoreConsumerFixture$", socket, admissionSocket, testInstance}, 5*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer process.Close()
	l.(*net.UnixListener).SetDeadline(time.Now().Add(2 * time.Second))
	conn, e := l.Accept()
	if e != nil {
		t.Fatal(e)
	}
	leases := make(chan source.Lease, 4)
	done := make(chan error, 1)
	go func() {
		done <- (&Input{Group: "All Speakers", Coordinator: co, Admissions: admissions, Lease: func(l source.Lease) { leases <- l }}).Run(ctx, conn)
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
	if h[0].Metadata.ArtworkID != "spotify:image:0000000000000000000000000000000000000000" || h[1].Metadata.ArtworkID != h[0].Metadata.ArtworkID || h[2].Metadata.ArtworkURL != "" || h[0].Metadata.Title != "first" || h[2].Metadata.Title != "second" || h[0].Frames < 960 || h[2].Frames < 960 || h[2].Peak < .7 {
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
	o := source.NewObserver()
	defer o.Stop()
	co := source.NewCoordinator(o, nil)
	sum := sha256.Sum256(b)
	socket := filepath.Join(t.TempDir(), "pcm.sock")
	l, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	admissions, admissionSocket := testAdmissionServer(t, ctx, co)
	p, e := source.StartProcess(ctx, source.Helper{Path: path, SHA256: hex.EncodeToString(sum[:])}, []string{"-test.run=^TestCoreSustainedFixture$", socket, admissionSocket, testInstance}, 5*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	l.(*net.UnixListener).SetDeadline(time.Now().Add(2 * time.Second))
	c, e := l.Accept()
	if e != nil {
		t.Fatal(e)
	}
	_ = (&Input{Group: "All Speakers", Coordinator: co, Admissions: admissions}).Run(ctx, c)
	h := o.History()
	if len(h) != 1 || h[0].Frames < 80000 || h[0].Frames > 100000 {
		t.Fatalf("bounded credit starved real-time PCM: %+v", h)
	}
}

func TestActualNativeQueuedTransferRevokedAndFreshRecast(t *testing.T) {
	path := os.Getenv("HOME_SOUND_FRAMED_FIXTURE")
	if path == "" {
		t.Skip("explicit separately built GPL source fixture required")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(b)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	o := source.NewObserver()
	defer o.Stop()
	co := source.NewCoordinator(o, nil)
	admissions, admissionSocket := testAdmissionServer(t, ctx, co)
	dir := t.TempDir()
	pcmPath, controlPath := filepath.Join(dir, "pcm.sock"), filepath.Join(dir, "control.sock")
	listener, e := net.Listen("unix", pcmPath)
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	controlListener, e := net.Listen("unix", controlPath)
	if e != nil {
		t.Fatal(e)
	}
	defer controlListener.Close()
	proc, e := source.StartProcess(ctx, source.Helper{Path: path, SHA256: hex.EncodeToString(sum[:])}, []string{"-test.run=^TestCoreNativeRecastFixture$", pcmPath, admissionSocket, testInstance, controlPath}, 6*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer proc.Close()
	controlListener.(*net.UnixListener).SetDeadline(time.Now().Add(2 * time.Second))
	control, e := controlListener.Accept()
	if e != nil {
		t.Fatal(e)
	}
	defer control.Close()
	control.SetDeadline(time.Now().Add(4 * time.Second))
	accept := func() (chan source.Lease, chan error) {
		listener.(*net.UnixListener).SetDeadline(time.Now().Add(2 * time.Second))
		c, e := listener.Accept()
		if e != nil {
			t.Fatal(e)
		}
		leases := make(chan source.Lease, 2)
		done := make(chan error, 1)
		go func() {
			done <- (&Input{Group: "All Speakers", Coordinator: co, Admissions: admissions, Lease: func(l source.Lease) { leases <- l }}).Run(ctx, c)
		}()
		return leases, done
	}
	await := func(leases chan source.Lease, done chan error) source.Lease {
		select {
		case l := <-leases:
			return l
		case e := <-done:
			t.Fatalf("input failed before selection: %v", e)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		return source.Lease{}
	}
	leases, done := accept()
	first := await(leases, done)
	owner, e := co.Reserve(source.Identity{Provider: "youtube", Group: "TV + All Speakers", Instance: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Generation: 1}, nil)
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("revocation did not cancel old framed connection")
	}
	control.Write([]byte{'D'})
	var reply [1]byte
	if _, e = io.ReadFull(control, reply[:]); e != nil || reply[0] != 'R' {
		t.Fatal("native delayed command did not reject")
	}
	if !co.Current(owner) {
		t.Fatal("old native transfer stole ownership")
	}
	control.Write([]byte{'F'})
	leases, done = accept()
	second := await(leases, done)
	if second.Epoch <= owner.Epoch || second.Instance == first.Instance || !co.Current(second) {
		t.Fatal("fresh native recast did not reopen/select")
	}
	control.Write([]byte{'Q'})
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
