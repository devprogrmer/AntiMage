//go:build linux

package nodeagent

import (
	"os/exec"
	"syscall"
	"testing"
)

// TestRuntimeStateAdvertisesIKEv2TargetedMutationCapabilities pins the IKEv2
// capability advertisement that the native targeted E2E gate enabled.
func TestRuntimeStateAdvertisesIKEv2TargetedMutationCapabilities(t *testing.T) {
	advertised := map[string]bool{}
	for _, capability := range (&Server{cfg: Config{DataDir: t.TempDir()}}).runtimeState("test").GetCapabilities() {
		advertised[capability] = true
	}
	for _, want := range []string{"targeted_user_update_ikev2", "targeted_user_add_remove_ikev2"} {
		if !advertised[want] {
			t.Fatalf("IKEv2 capability %q was not advertised", want)
		}
	}
}

// configureNativeIKEv2ClientProcess puts the client command in its own process
// group so a single signal reaches charon-cmd even though it runs as a
// descendant of "ip netns exec".
func configureNativeIKEv2ClientProcess(cmd *exec.Cmd) {
	if cmd != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
}

// signalNativeIKEv2Client terminates the whole client process group so
// charon-cmd (a descendant of "ip netns exec") exits and stops answering DPD.
// It falls back to the leader pid if the group signal is not permitted.
func signalNativeIKEv2Client(cmd *exec.Cmd, force bool) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	pid := cmd.Process.Pid
	if pid > 0 {
		if err := syscall.Kill(-pid, sig); err == nil {
			return
		}
	}
	_ = cmd.Process.Signal(sig)
}
