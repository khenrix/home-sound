package inputs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/khenrix/home-sound/internal/source"
)

func TestInputDiagnosticFixture(t *testing.T) {
	if len(os.Args) < 2 {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "input-stdout-limit":
		os.Stdout.WriteString(strings.Repeat("secret-not-for-errors", 500))
		time.Sleep(time.Hour)
	case "input-panic-limit":
		os.Stderr.WriteString("panic: " + strings.Repeat("secret-not-for-errors", 500))
		time.Sleep(time.Hour)
	case "input-stderr-limit":
		os.Stderr.WriteString(strings.Repeat("secret-not-for-errors", 500))
		time.Sleep(time.Hour)
	case "input-exit":
		os.Stderr.WriteString("secret-not-for-errors")
		os.Exit(7)
	}
}

func TestNativeHelperFailureIdentifiesClassAndSlotWithoutPayload(t *testing.T) {
	path, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(b)
	h := source.Helper{Path: path, SHA256: hex.EncodeToString(sum[:])}
	for slot, tc := range []struct{ mode, want string }{
		{"input-stdout-limit", "input runtime stopped: spotify_stdout_limit slot=0 exit=-1"},
		{"input-stderr-limit", "input runtime stopped: spotify_stderr_limit slot=1 exit=-1 stderr_panic=0 stderr_fatal=0 stderr_errors=0 stderr_output=0 stderr_restore=0"},
		{"input-exit", "input runtime stopped: spotify_exit slot=2 exit=7"},
		{"input-panic-limit", "input runtime stopped: spotify_stderr_limit slot=3 exit=-1 stderr_panic=1 stderr_fatal=0 stderr_errors=0 stderr_output=0 stderr_restore=0"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			r := &Runtime{cancel: cancel, errors: make(chan error, 1)}
			p, e := source.StartDaemon(ctx, h, []string{"-test.run=^TestInputDiagnosticFixture$", tc.mode})
			if e != nil {
				t.Fatal(e)
			}
			defer p.Close()
			r.workers.Go(func() { r.watchHelper(ctx, p, slot) })
			defer r.Close()
			select {
			case e := <-r.Errors():
				if e.Error() != tc.want {
					t.Fatalf("failure class lost or diagnostic leaked: %q, want %q", e, tc.want)
				}
			case <-ctx.Done():
				t.Fatal("helper failure was not reported")
			}
		})
	}
}
