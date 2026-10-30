//go:build linux

package airplay

import (
	"os/exec"
	"syscall"
)

func ownedPTPProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	return nil
}
