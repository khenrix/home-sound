package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"
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
	case "fixture-panic-limit":
		panic(strings.Repeat("synthetic-private-marker", 400))
	case "fixture-output-log-limit":
		os.Stderr.WriteString(strings.Repeat("time=\"fixture\" level=error msg=\"output device failed\" error=\"synthetic-private-marker\"\n", 100))
		time.Sleep(time.Hour)
	case "fixture-stderr-limit":
		os.Stderr.WriteString(strings.Repeat("private-token-must-not-escape", 400))
		time.Sleep(time.Hour)
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

func TestProcessExitDiagnosticClassifiesWithoutPayload(t *testing.T) {
	for _, tc := range []struct {
		mode, class string
		code        int
	}{
		{"fixture-fail", "exit", 9},
		{"fixture-stderr-limit", "stderr_limit", -1},
		{"fixture-data", "exit", 0},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			p, e := StartDaemon(context.Background(), testHelper(t), []string{"-test.run=^TestProcessFixture$", tc.mode})
			if e != nil {
				t.Fatal(e)
			}
			defer p.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			for {
				_, e = p.Read(ctx, make([]byte, 64))
				if e != nil {
					break
				}
			}
			status, ok := any(p).(interface{ ExitDiagnostic() (string, int) })
			if !ok {
				t.Fatal("process cannot distinguish nonzero exit from bounded diagnostic overflow")
			}
			class, code := status.ExitDiagnostic()
			if class != tc.class || code != tc.code {
				t.Fatalf("diagnostic %q/%d, want %q/%d", class, code, tc.class, tc.code)
			}
		})
	}
}

func TestStderrBudgetDiagnosticDistinguishesPanicFromOutputErrorStorm(t *testing.T) {
	for _, mode := range []string{"fixture-panic-limit", "fixture-output-log-limit"} {
		t.Run(mode, func(t *testing.T) {
			p, e := StartDaemon(context.Background(), testHelper(t), []string{"-test.run=^TestProcessFixture$", mode})
			if e != nil {
				t.Fatal(e)
			}
			defer p.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			for {
				_, e = p.Read(ctx, make([]byte, 1024))
				if e != nil {
					break
				}
			}
			summary := p.StderrSummary()
			if mode == "fixture-panic-limit" {
				if summary.Panic != 1 || summary.Errors != 0 {
					t.Fatalf("Go panic was not safely classified: %+v", summary)
				}
			} else if summary.Panic != 0 || summary.OutputFailures < 2 || summary.Errors != summary.OutputFailures {
				t.Fatalf("output-error burst was not safely classified: %+v", summary)
			}
			class, _ := p.ExitDiagnostic()
			if class != "stderr_limit" {
				t.Fatalf("diagnostic classification changed original limit: %q", class)
			}
		})
	}
}

func TestStderrSummaryMatchesAcrossWritesWithoutInspectingPayloadFields(t *testing.T) {
	scan := &diagnosticScan{}
	stopped := false
	w := &discardLimit{remaining: 4096, scan: scan, cancel: func() { stopped = true }}
	text := "panic: synthetic\nfatal error: synthetic\ntime=\"fixture\" level=error msg=\"output device failed\" error=\"synthetic panic: private\"\ntime=\"fixture\" level=error msg=\"failed restoring session after logout\" error=\"synthetic\"\n"
	for n := range []byte(text) {
		if _, e := w.Write([]byte{text[n]}); e != nil {
			t.Fatal(e)
		}
	}
	if scan.count != [5]uint16{1, 1, 2, 1, 1} || stopped {
		t.Fatalf("split fixed-header classification %v stopped=%v", scan.count, stopped)
	}
	// Header-like text in an error field is escaped by the actual TextFormatter;
	// neither embedded panic text nor its field values are emitted or retained.
}

func TestStderrSummaryIgnoresQuotedHeaderLookalikesAcrossChunks(t *testing.T) {
	text := "time=\"fixture\" level=info msg=ordinary error=\"remote text level=error more\"\n" +
		"time=\"fixture\" level=info msg=\"quoted \\\"escape\\\" level=error more\"\n" +
		"time=\"fixture\" level=error msg=\"output device failed\" error=\"remote level=error value\"\n" +
		"level=error msg=\"failed restoring session after logout\" error=\"remote level=error value\"\n"
	for _, width := range []int{1, 7, len(text)} {
		scan := &diagnosticScan{}
		w := &discardLimit{remaining: 4096, scan: scan, cancel: func() { t.Fatal("short fixture hit byte limit") }}
		for pos := 0; pos < len(text); pos += width {
			if _, e := w.Write([]byte(text[pos:min(pos+width, len(text))])); e != nil {
				t.Fatal(e)
			}
		}
		if scan.count != [5]uint16{0, 0, 2, 1, 1} {
			t.Errorf("chunk width %d counted value text or missed real header: %v", width, scan.count)
		}
	}
}
