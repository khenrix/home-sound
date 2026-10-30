package source

import (
	"context"
	"syscall"
	"testing"
	"time"
)

func TestProcessPipeHasExplicitPCMCapacity(t *testing.T) {
	p, e := StartProcess(context.Background(), testHelper(t), []string{"-test.run=^TestProcessFixture$", "fixture-block"}, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	size, _, errno := syscall.Syscall(syscall.SYS_FCNTL, p.stdout.Fd(), syscall.F_GETPIPE_SZ, 0)
	if errno != 0 || size > 4096 {
		t.Fatalf("unbounded configured pipe capacity: %d %v", size, errno)
	}
}
