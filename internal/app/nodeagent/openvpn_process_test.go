package nodeagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestStartOpenVPNInbound(t *testing.T) {
	oldLookPath := openVPNLookPath
	oldCommand := openVPNCommandContext

	defer func() {
		openVPNLookPath = oldLookPath
		openVPNCommandContext = oldCommand
	}()

	openVPNLookPath = func(string) (string, error) {
		return os.Args[0], nil
	}

	openVPNCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(
			ctx,
			name,
			"-test.run=TestOpenVPNHelperProcess",
			"--",
		)
		cmd.Env = append(os.Environ(), "GO_WANT_OPENVPN_HELPER=1")
		return cmd
	}

	server := New(Config{DataDir: t.TempDir()})

	configPath := filepath.Join(t.TempDir(), "server.conf")
	if err := os.WriteFile(configPath, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := server.startOpenVPNInbound("openvpn-main", configPath); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(server.stopAllOpenVPNRuntimes)

	server.mu.Lock()
	runtimeProcess := server.openVPNRuntimes["openvpn-main"]
	server.mu.Unlock()

	if runtimeProcess == nil ||
		runtimeProcess.cmd == nil ||
		runtimeProcess.cmd.Process == nil {
		t.Fatal("openvpn process was not registered")
	}

	if err := server.stopOpenVPNInbound("openvpn-main"); err != nil {
		t.Fatal(err)
	}

	server.mu.Lock()
	_, exists := server.openVPNRuntimes["openvpn-main"]
	server.mu.Unlock()

	if exists {
		t.Fatal("openvpn process was not removed")
	}
}

func TestStartOpenVPNInboundRequiresBinary(t *testing.T) {
	oldLookPath := openVPNLookPath
	defer func() { openVPNLookPath = oldLookPath }()

	openVPNLookPath = func(string) (string, error) {
		return "", exec.ErrNotFound
	}

	server := New(Config{DataDir: t.TempDir()})

	if err := server.startOpenVPNInbound("openvpn-main", "/tmp/server.conf"); err == nil {
		t.Fatal("expected missing openvpn executable error")
	}
}

func TestOpenVPNHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_OPENVPN_HELPER") != "1" {
		return
	}

	if runtime.GOOS == "windows" {
		for {
			time.Sleep(time.Second)
		}
	}

	select {}
}

func TestStartOpenVPNInboundRejectsEarlyExit(
	t *testing.T,
) {
	oldLookPath := openVPNLookPath
	oldCommand := openVPNCommandContext
	oldGrace := openVPNStartupGrace

	defer func() {
		openVPNLookPath = oldLookPath
		openVPNCommandContext = oldCommand
		openVPNStartupGrace = oldGrace
	}()

	openVPNStartupGrace = 500 * time.Millisecond

	openVPNLookPath = func(
		string,
	) (string, error) {
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
			"-test.run=TestOpenVPNEarlyExitHelperProcess",
			"--",
		)

		cmd.Env = append(
			os.Environ(),
			"GO_WANT_OPENVPN_EARLY_EXIT=1",
		)

		return cmd
	}

	server := New(
		Config{
			DataDir: t.TempDir(),
		},
	)

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

	err := server.startOpenVPNInbound(
		"openvpn-early-exit",
		configPath,
	)

	if err == nil {
		t.Fatal(
			"expected early OpenVPN exit error",
		)
	}

	server.mu.Lock()
	_, exists :=
		server.openVPNRuntimes["openvpn-early-exit"]
	server.mu.Unlock()

	if exists {
		t.Fatal(
			"early-exited process remained registered",
		)
	}
}

func TestOpenVPNEarlyExitHelperProcess(
	t *testing.T,
) {
	if os.Getenv(
		"GO_WANT_OPENVPN_EARLY_EXIT",
	) != "1" {
		return
	}

	os.Exit(23)
}
