package process

import (
	"context"
	"os"
	"os/exec"
	"time"
)

// CommandContext owns a maintenance process tree, rather than only its shell.
// Runtime daemons may also use this constructor with their explicit lifecycle.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	configureCancellation(cmd)
	return cmd
}

// Kill terminates the owned process group configured by CommandContext. Tests
// and callers supplying ordinary exec.Cmd retain the standard process fallback.
func Kill(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	if cmd.Cancel != nil {
		return cmd.Cancel()
	}
	return cmd.Process.Kill()
}
