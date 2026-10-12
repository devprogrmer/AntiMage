//go:build !linux

package process

import (
	"os"
	"os/exec"
)

func Interrupt(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	return cmd.Process.Signal(os.Interrupt)
}

func configureCancellation(*exec.Cmd) {}
