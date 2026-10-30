package airplay

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// The helper's STOP stops transport but leaves its command loop alive. SIGTERM
// runs its disconnect/TEARDOWN cleanup; Command.WaitDelay bounds a stuck helper.
func (a *Adapter) cancelHelper(cmd *exec.Cmd, commands *os.File) error {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if writeBounded(ctx, commands, []byte("ACTION=STOP\n")) == nil {
		for {
			a.mu.Lock()
			stopped, changed := a.receiverStopped, a.changed
			a.mu.Unlock()
			if stopped {
				break
			}
			select {
			case <-ctx.Done():
				return cmd.Process.Signal(syscall.SIGTERM)
			case <-changed:
			}
		}
	}
	return cmd.Process.Signal(syscall.SIGTERM)
}

func (a *Adapter) Stop() error {
	a.fail()
	a.mu.Lock()
	done := a.done
	a.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			return errors.New("helper teardown timed out")
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopErr != nil {
		return a.stopErr
	}
	if a.status.HelperErrorCode == "stop_failed" || a.status.HelperErrorCode == "flush_failed" {
		return errors.New("helper could not stop receiver transport")
	}
	return nil
}
