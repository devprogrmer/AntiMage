package nodeagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestApplyTorProxyStartsConfiguredLoopbackSocks(t *testing.T) {
	previousCommand := torCommandContext
	previousLookPath := torLookPath
	defer func() {
		torCommandContext = previousCommand
		torLookPath = previousLookPath
	}()
	torLookPath = func(string) (string, error) {
		return os.Executable()
	}
	torCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		allArgs := append([]string{"-test.run=TestHelperProcess", "--", "tor"}, args...)
		cmd := exec.CommandContext(ctx, name, allArgs...)
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		return cmd
	}

	dataDir := t.TempDir()
	server := New(Config{DataDir: dataDir})
	res, err := server.ApplyTorProxy(context.Background(), &nodev1.TorProxyRequest{
		OperationId: "tor-1",
		SocksPort:   19050,
		ExitCountry: "de",
		StrictExit:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.GetAccepted() || !strings.Contains(res.GetMessage(), "127.0.0.1:19050") {
		t.Fatalf("unexpected response: %#v", res)
	}
	t.Cleanup(func() {
		server.mu.Lock()
		for _, cmd := range server.torProxies {
			if cmd != nil && cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}
		server.mu.Unlock()
	})

	torrc, err := os.ReadFile(filepath.Join(dataDir, "tor", "19050", "torrc"))
	if err != nil {
		t.Fatal(err)
	}
	config := string(torrc)
	for _, want := range []string{
		"SocksPort 127.0.0.1:19050",
		"ExitNodes {de}",
		"StrictNodes 1",
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("torrc missing %q:\n%s", want, config)
		}
	}
	if capabilities := res.GetRuntime().GetCapabilities(); !containsString(capabilities, "tor_proxy") || !containsString(capabilities, "tor_proxy_running") {
		t.Fatalf("missing Tor capabilities: %v", capabilities)
	}
}

func TestApplyTorProxyRejectsMissingTor(t *testing.T) {
	previousLookPath := torLookPath
	defer func() { torLookPath = previousLookPath }()
	torLookPath = func(string) (string, error) {
		return "", exec.ErrNotFound
	}

	_, err := New(Config{DataDir: t.TempDir()}).ApplyTorProxy(context.Background(), &nodev1.TorProxyRequest{SocksPort: 19050})
	if err == nil || !strings.Contains(err.Error(), "tor is not installed") {
		t.Fatalf("expected missing tor error, got %v", err)
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	if runtime.GOOS == "windows" {
		select {}
	}
	select {}
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
