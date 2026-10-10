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
	// Production runtime commands own a process group. Signal that group so
	// graceful shutdown includes daemon workers; ordinary injected commands
	// keep their existing single-process behavior.
	if group, err := syscall.Getpgid(process.Pid); err == nil && group == process.Pid {
		return syscall.Kill(-group, syscall.SIGTERM)
	}

	return process.Signal(syscall.SIGTERM)
}
