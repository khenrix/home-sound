//go:build !linux

package airplay

import (
	"errors"
	"os/exec"
)

func ownedPTPProcess(cmd *exec.Cmd) error {
	return errors.New("owned PTP lifecycle currently requires Linux")
}
