//go:build !linux

package nodeagent

import (
	"os/exec"
)

func configureNativeIKEv2ClientProcess(cmd *exec.Cmd) {}

// signalNativeIKEv2Client ends the client process on platforms without
// process-group signalling. The native lifecycle test only runs on Linux, so
// this exists to keep the shared test file buildable elsewhere.
func signalNativeIKEv2Client(cmd *exec.Cmd, force bool) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
