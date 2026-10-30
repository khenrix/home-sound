package source

import (
	"os"
	"os/exec"
	"syscall"
)

func ownProcessGroup(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	return nil
}

func boundPipe(f *os.File) error {
	_, _, e := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_SETPIPE_SZ, 4096)
	if e != 0 {
		return e
	}
	return nil
}
