//go:build windows

package nodeagent

import "os"

func terminateOpenVPNProcess(
	process *os.Process,
) error {
	if process == nil {
		return nil
	}

	return process.Kill()
}
