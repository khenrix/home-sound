package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"testing"
	"time"
)

func testHelper(t *testing.T) Helper {
	t.Helper()
	p, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	s := sha256.Sum256(b)
	return Helper{Path: p, SHA256: hex.EncodeToString(s[:])}
}
func TestProcessFixture(t *testing.T) {
	if len(os.Args) < 2 {
		return
	}
	mode := os.Args[len(os.Args)-1]
	switch mode {
	case "fixture-closed-stdout":
		os.Stdout.Close()
		time.Sleep(time.Hour)
	case "fixture-block":
		time.Sleep(time.Hour)
	case "fixture-data":
		os.Stdout.Write([]byte{1, 2, 3})
		os.Exit(0)
	case "fixture-fail":
		os.Stderr.WriteString("private-token-must-not-escape")
		os.Exit(9)
	case "fixture-fill":
		b := make([]byte, 65536)
		for {
			if _, e := os.Stdout.Write(b); e != nil {
				os.Exit(0)
			}
		}
	}
}
func TestProcessReadAndReap(t *testing.T) {
	p, e := StartProcess(context.Background(), testHelper(t), []string{"-test.run=^TestProcessFixture$", "fixture-data"}, 3*time.Second)
	if e != nil {
		t.Fatalf("start bounded source: %v", e)
	}
	defer p.Close()
	b := make([]byte, 5)
	n, e := p.Read(context.Background(), b)
	if e != nil || n != 3 || b[0] != 1 || b[2] != 3 {
		t.Fatalf("read %d %v %v", n, b, e)
	}
	_, e = p.Read(context.Background(), b)
	if e != io.EOF {
		t.Fatalf("EOF %v", e)
	}
}
func TestProcessBlockedReadCancellation(t *testing.T) {
	p, e := StartProcess(context.Background(), testHelper(t), []string{"-test.run=^TestProcessFixture$", "fixture-block"}, time.Minute)
	if e != nil {
		t.Fatalf("start bounded source: %v", e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, e = p.Read(ctx, make([]byte, 8))
	if e == nil || time.Since(start) > time.Second {
		t.Fatalf("blocked read cancellation: %v", e)
	}
	if e = p.Close(); e != nil {
		t.Fatal(e)
	}
}
func TestProcessFullPipeClose(t *testing.T) {
	p, e := StartProcess(context.Background(), testHelper(t), []string{"-test.run=^TestProcessFixture$", "fixture-fill"}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	time.Sleep(30 * time.Millisecond)
	start := time.Now()
	if e = p.Close(); e != nil {
		t.Fatal(e)
	}
	if time.Since(start) > time.Second {
		t.Fatal("full pipe blocked shutdown")
	}
}
func TestProcessFailureIsSanitized(t *testing.T) {
	p, e := StartProcess(context.Background(), testHelper(t), []string{"-test.run=^TestProcessFixture$", "fixture-fail"}, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	_, e = p.Read(context.Background(), make([]byte, 8))
	if e == nil || e == io.EOF {
		t.Fatalf("nonzero exit became successful EOF: %v", e)
	}
	if e.Error() != "source helper failed" {
		t.Fatalf("private helper diagnostic exposed: %q", e)
	}
}
func TestProcessHashRejectsReplacement(t *testing.T) {
	h := testHelper(t)
	h.SHA256 = string(make([]byte, 64))
	if p, e := StartProcess(context.Background(), h, nil, time.Second); e == nil {
		p.Close()
		t.Fatal("unverified helper executed")
	}
}

func TestProcessClosedStdoutCannotHoldEOFOpen(t *testing.T) {
	p, e := StartProcess(context.Background(), testHelper(t), []string{"-test.run=^TestProcessFixture$", "fixture-closed-stdout"}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	done := make(chan error, 1)
	go func() { _, e := p.Read(context.Background(), make([]byte, 8)); done <- e }()
	select {
	case e := <-done:
		if e == nil || e == io.EOF {
			t.Fatal("unreaped child reported clean EOF")
		}
	case <-time.After(2200 * time.Millisecond):
		t.Fatal("closed stdout held source EOF open")
	}
}

func TestDaemonLifetimeIsOwnedByContext(t *testing.T) {
	starter, ok := any(daemonStarter{}).(interface {
		Start(context.Context, Helper, []string) (*Process, error)
	})
	if !ok {
		t.Fatal("persistent helper lacks context-owned lifecycle")
	}
	ctx, cancel := context.WithCancel(context.Background())
	p, e := starter.Start(ctx, testHelper(t), []string{"-test.run=^TestProcessFixture$", "fixture-block"})
	if e != nil {
		t.Fatal(e)
	}
	cancel()
	if e = p.Close(); e != nil {
		t.Fatal(e)
	}
}

type daemonStarter struct{}

func (daemonStarter) Start(ctx context.Context, h Helper, args []string) (*Process, error) {
	return StartDaemon(ctx, h, args)
}
