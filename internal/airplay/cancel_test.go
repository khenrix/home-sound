package airplay

import (
	"context"
	"errors"
	"fmt"
	"github.com/khenrix/home-sound/internal/audio"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestCancellationRequestsStopThenOrderlyHelperExit(t *testing.T) {
	cfg := fixture(t, "graceful")
	out, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer out.Stop()
	if err = out.Prepare(ctx, 1, 0); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-out.done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not finish bounded teardown")
	}
	if err = out.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(cfg.Helper + ".order.terminated"); err != nil {
		t.Fatal("helper killed without STOP followed by orderly SIGTERM cleanup")
	}
}
func TestHungHelperCancellationFailsClosedWithinBound(t *testing.T) {
	for _, mode := range []string{"hangstop", "blockedcommands", "closedcommands"} {
		t.Run(mode, func(t *testing.T) {
			fixtureMode := mode
			if mode == "closedcommands" {
				fixtureMode = "hangstop"
			}
			out, err := New(fixture(t, fixtureMode))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			defer out.Stop()
			if err = out.Prepare(ctx, 1, 0); err != nil {
				t.Fatal(err)
			}
			if mode == "blockedcommands" {
				buf := make([]byte, 4096)
				for {
					_, err = syscall.Write(int(out.commands.Fd()), buf)
					if errors.Is(err, syscall.EAGAIN) {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode == "closedcommands" {
				out.commands.Close()
			}
			before := time.Now()
			cancel()
			if err = out.Stop(); err == nil {
				t.Fatal("forced helper kill reported successful receiver teardown")
			}
			if elapsed := time.Since(before); elapsed > 3*time.Second {
				t.Fatalf("teardown exceeded bound: %v", elapsed)
			}
			select {
			case <-out.done:
			default:
				t.Fatal("force-kill fallback left helper running")
			}
		})
	}
}

func TestNonzeroExitWithSubmittedPCMDoesNotConfirmTeardown(t *testing.T) {
	for _, withPCM := range []bool{false, true} {
		t.Run(fmt.Sprint(withPCM), func(t *testing.T) {
			out, err := New(fixture(t, "abruptstop"))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			defer out.Stop()
			if err = out.Prepare(ctx, 1, 0); err != nil {
				t.Fatal(err)
			}
			if withPCM {
				if err = out.Write(ctx, audio.Frame{Epoch: 1, Sample: 0, Data: make([]float32, 1920)}); err != nil {
					t.Fatal(err)
				}
			}
			cancel()
			err = out.Stop()
			if withPCM && err == nil {
				t.Fatal("nonzero exit with submitted PCM reported successful teardown")
			}
			if !withPCM && err != nil {
				t.Fatalf("empty connection cannot retain PCM: %v", err)
			}
		})
	}
}
