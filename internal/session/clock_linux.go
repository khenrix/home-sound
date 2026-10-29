//go:build linux

package session

import (
	"context"
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

type rawClock struct{}

func readRaw() (time.Duration, error) {
	var ts syscall.Timespec
	_, _, errno := syscall.Syscall(syscall.SYS_CLOCK_GETTIME, 4, uintptr(unsafe.Pointer(&ts)), 0)
	if errno != 0 {
		return 0, errno
	}
	return time.Duration(ts.Sec)*time.Second + time.Duration(ts.Nsec), nil
}
func NewRawClock() (Clock, error) {
	if _, err := readRaw(); err != nil {
		return nil, fmt.Errorf("CLOCK_MONOTONIC_RAW unavailable: %w", err)
	}
	return &rawClock{}, nil
}
func (c *rawClock) Now() time.Duration {
	n, err := readRaw()
	if err != nil {
		panic("CLOCK_MONOTONIC_RAW failed after initialization")
	}
	return n
}
func (c *rawClock) WaitUntil(ctx context.Context, at time.Duration) error {
	for {
		remaining := at - c.Now()
		if remaining <= 0 {
			return ctx.Err()
		}
		timer := time.NewTimer(min(remaining, time.Second))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
