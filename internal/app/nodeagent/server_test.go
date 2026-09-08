package nodeagent

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestNodeAgentHelperProcess(t *testing.T) {
	mode := ""
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
			break
		}
	}
	if mode == "" {
		return
	}

	switch mode {
	case "validate-fail":
		fmt.Fprintln(os.Stderr, "synthetic invalid xray config")
		os.Exit(2)
	case "runtime-exit":
		os.Exit(0)
	default:
		fmt.Fprintln(os.Stderr, "unknown helper mode:", mode)
		os.Exit(3)
	}
}

func withRuntimeCommand(t *testing.T, mode string) {
	t.Helper()

	previous := runtimeCommand
	runtimeCommand = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command(
			os.Args[0],
			"-test.run=TestNodeAgentHelperProcess",
			"--",
			mode,
		)
	}

	t.Cleanup(func() {
		runtimeCommand = previous
	})
}

func TestValidateXrayConfigRejectsInvalidConfig(t *testing.T) {
	withRuntimeCommand(t, "validate-fail")

	s := New(Config{
		XrayPath:      "xray",
		XrayAssetsDir: t.TempDir(),
	})

	err := s.validateXrayConfig("invalid.json")
	if err == nil {
		t.Fatal("expected invalid Xray config to be rejected")
	}
	if !strings.Contains(err.Error(), "synthetic invalid xray config") {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestStartXrayClearsRuntimeAfterProcessExit(t *testing.T) {
	withRuntimeCommand(t, "runtime-exit")

	s := New(Config{
		XrayPath:      "xray",
		XrayAssetsDir: t.TempDir(),
	})

	if err := s.startXray("config.json"); err != nil {
		t.Fatalf("startXray returned error: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		running := s.lastRuntime != nil
		s.mu.Unlock()

		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("runtime process exited but lastRuntime was not cleared")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPublicIPsFromAddrs(t *testing.T) {
	addrs := []net.Addr{
		&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
		&net.IPNet{IP: net.ParseIP("10.20.30.40"), Mask: net.CIDRMask(8, 32)},
		&net.IPNet{IP: net.ParseIP("100.64.1.2"), Mask: net.CIDRMask(10, 32)},
		&net.IPNet{IP: net.ParseIP("8.8.8.8"), Mask: net.CIDRMask(32, 32)},
		&net.IPNet{IP: net.ParseIP("2606:4700:4700::1111"), Mask: net.CIDRMask(128, 128)},
	}

	ipv4, ipv6 := publicIPsFromAddrs(addrs)

	if ipv4 != "8.8.8.8" {
		t.Fatalf("unexpected public IPv4: %q", ipv4)
	}
	if ipv6 != "2606:4700:4700::1111" {
		t.Fatalf("unexpected public IPv6: %q", ipv6)
	}
}
