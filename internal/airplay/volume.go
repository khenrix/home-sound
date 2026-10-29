package airplay

import (
	"context"
	"errors"
	"fmt"
)

// SetNative submits a speaker native step selected by the coordinator from its
// verified quiet acoustic profile. Percentages alone are not acoustic limits.
// The pinned helper handles VOLUME= directly and supplies no readback. LG always
// uses its separate verified native-control path.
func (a *Adapter) SetNative(ctx context.Context, n int) error {
	if a.cfg.LG != nil || n < 1 || n > 100 {
		return errors.New("speaker native level must be 1 through 100; LG uses native control")
	}
	a.op.Lock()
	defer a.op.Unlock()
	a.mu.Lock()
	ready := a.status.Ready && a.status.Healthy
	commands := a.commands
	a.mu.Unlock()
	if !ready || commands == nil {
		return errors.New("output not ready")
	}
	return writeBounded(ctx, commands, []byte(fmt.Sprintf("VOLUME=%d\n", n)))
}
