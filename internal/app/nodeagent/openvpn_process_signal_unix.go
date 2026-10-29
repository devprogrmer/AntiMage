//go:build !windows

package nodeagent

import (
	"os"
	"syscall"
)

func terminateOpenVPNProcess(
	process *os.Process,
) error {
	if process == nil {
		return nil
	}

	return process.Signal(syscall.SIGTERM)
}
