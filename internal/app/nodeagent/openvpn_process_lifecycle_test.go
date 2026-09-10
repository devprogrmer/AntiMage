package nodeagent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestStopOpenVPNProcessInvokesTerminateHook(
	t *testing.T,
) {
	oldLookPath := openVPNLookPath
	oldCommand := openVPNCommandContext
	oldTerminate := openVPNTerminateProcess
	oldStartupGrace := openVPNStartupGrace

	defer func() {
		openVPNLookPath = oldLookPath
		openVPNCommandContext = oldCommand
		openVPNTerminateProcess = oldTerminate
		openVPNStartupGrace = oldStartupGrace
	}()

	openVPNStartupGrace = 100 * time.Millisecond

	openVPNLookPath = func(string) (string, error) {
		return os.Args[0], nil
	}

	openVPNCommandContext = func(
		ctx context.Context,
		name string,
		args ...string,
	) *exec.Cmd {
		cmd := exec.CommandContext(
			ctx,
			name,
			"-test.run=TestOpenVPNHelperProcess",
			"--",
		)

		cmd.Env = append(
			os.Environ(),
			"GO_WANT_OPENVPN_HELPER=1",
		)

		return cmd
	}

	terminateCalled := false

	openVPNTerminateProcess = func(
		process *os.Process,
	) error {
		terminateCalled = true
		return process.Kill()
	}

	server := New(Config{
		DataDir: t.TempDir(),
	})

	configPath := filepath.Join(
		t.TempDir(),
		"server.conf",
	)

	if err := os.WriteFile(
		configPath,
		[]byte("test"),
		0600,
	); err != nil {
		t.Fatal(err)
	}

	if err := server.startOpenVPNInbound(
		"openvpn-stop-test",
		configPath,
	); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(server.stopAllOpenVPNRuntimes)

	if err := server.stopOpenVPNInbound(
		"openvpn-stop-test",
	); err != nil {
		t.Fatal(err)
	}

	if !terminateCalled {
		t.Fatal(
			"stop did not invoke terminate hook",
		)
	}
}

func TestStopOpenVPNProcessFallsBackToKill(
	t *testing.T,
) {
	oldLookPath := openVPNLookPath
	oldCommand := openVPNCommandContext
	oldTerminate := openVPNTerminateProcess
	oldStartupGrace := openVPNStartupGrace

	defer func() {
		openVPNLookPath = oldLookPath
		openVPNCommandContext = oldCommand
		openVPNTerminateProcess = oldTerminate
		openVPNStartupGrace = oldStartupGrace
	}()

	openVPNStartupGrace = 100 * time.Millisecond

	openVPNLookPath = func(string) (string, error) {
		return os.Args[0], nil
	}

	openVPNCommandContext = func(
		ctx context.Context,
		name string,
		args ...string,
	) *exec.Cmd {
		cmd := exec.CommandContext(
			ctx,
			name,
			"-test.run=TestOpenVPNHelperProcess",
			"--",
		)

		cmd.Env = append(
			os.Environ(),
			"GO_WANT_OPENVPN_HELPER=1",
		)

		return cmd
	}

	terminateCalled := false

	openVPNTerminateProcess = func(
		_ *os.Process,
	) error {
		terminateCalled = true
		return errors.New("simulated terminate failure")
	}

	server := New(Config{
		DataDir: t.TempDir(),
	})

	configPath := filepath.Join(
		t.TempDir(),
		"server.conf",
	)

	if err := os.WriteFile(
		configPath,
		[]byte("test"),
		0600,
	); err != nil {
		t.Fatal(err)
	}

	if err := server.startOpenVPNInbound(
		"openvpn-kill-fallback",
		configPath,
	); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(server.stopAllOpenVPNRuntimes)

	if err := server.stopOpenVPNInbound(
		"openvpn-kill-fallback",
	); err != nil {
		t.Fatal(err)
	}

	if !terminateCalled {
		t.Fatal(
			"forced-kill path did not attempt terminate first",
		)
	}
}
