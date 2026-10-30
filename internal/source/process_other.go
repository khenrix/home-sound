//go:build !linux

package source

import (
	"errors"
	"os"
	"os/exec"
)

func ownProcessGroup(*exec.Cmd) error {
	return errors.New("input subprocess containment requires Linux")
}

func boundPipe(*os.File) error { return errors.New("source pipe bounds require Linux") }
