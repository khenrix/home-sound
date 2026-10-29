//go:build !linux

package session

import "errors"

func NewRawClock() (Clock, error) {
	return nil, errors.New("group service requires Linux CLOCK_MONOTONIC_RAW")
}
