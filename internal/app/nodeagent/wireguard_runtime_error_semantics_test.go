package nodeagent

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWireGuardInterfaceExistsRecognizesMissingDevice(t *testing.T) {
	oldRun := wireGuardRuntimeRun
	wireGuardRuntimeRun = func(
		_ context.Context,
		_ string,
		_ ...string,
	) ([]byte, error) {
		return []byte(`Device "amwgdeadbeef" does not exist.`), errors.New("exit status 1")
	}
	t.Cleanup(func() {
		wireGuardRuntimeRun = oldRun
	})

	exists, err := wireGuardInterfaceExists(
		context.Background(),
		"ip",
		"amwgdeadbeef",
	)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("missing interface reported as existing")
	}
}

func TestApplyWireGuardRuntimeUnknownProbeFailsSafe(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})

	oldGOOS := wireGuardRuntimeGOOS
	oldLookPath := wireGuardRuntimeLookPath
	oldRun := wireGuardRuntimeRun
	wireGuardRuntimeGOOS = "linux"
	wireGuardRuntimeLookPath = func(name string) (string, error) {
		return name, nil
	}

	var calls []string
	wireGuardRuntimeRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		call := name + " " + strings.Join(args, " ")
		calls = append(calls, call)
		if strings.Contains(call, "ip link show dev amwgdeadbeef") {
			return []byte("RTNETLINK answers: Operation not permitted"), errors.New("exit status 2")
		}
		return nil, nil
	}
	t.Cleanup(func() {
		wireGuardRuntimeGOOS = oldGOOS
		wireGuardRuntimeLookPath = oldLookPath
		wireGuardRuntimeRun = oldRun
	})

	err := server.applyWireGuardRuntime(preparedWireGuardRuntime{
		Tag:           "wg-main",
		InterfaceName: "amwgdeadbeef",
		ConfigPath:    "unused.conf",
		ServerCIDR:    "10.69.0.1/16",
		SourceCIDR:    "10.69.0.0/16",
	})
	if err == nil || !strings.Contains(err.Error(), "probe wireguard interface") {
		t.Fatalf("error = %v", err)
	}

	joined := strings.Join(calls, "\n")
	for _, forbidden := range []string{
		"sysctl -w net.ipv4.ip_forward=1",
		"ip link add dev amwgdeadbeef",
		"wg syncconf",
		"address flush",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("destructive command %q ran after unknown probe error:\n%s", forbidden, joined)
		}
	}
}

func TestRemoveWireGuardInterfaceUnknownProbeFailsSafe(t *testing.T) {
	oldGOOS := wireGuardRuntimeGOOS
	oldLookPath := wireGuardRuntimeLookPath
	oldRun := wireGuardRuntimeRun
	wireGuardRuntimeGOOS = "linux"
	wireGuardRuntimeLookPath = func(name string) (string, error) {
		return name, nil
	}

	var calls []string
	wireGuardRuntimeRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		call := name + " " + strings.Join(args, " ")
		calls = append(calls, call)
		if strings.Contains(call, "ip link show dev amwgdeadbeef") {
			return []byte("RTNETLINK answers: Operation not permitted"), errors.New("exit status 2")
		}
		return nil, nil
	}
	t.Cleanup(func() {
		wireGuardRuntimeGOOS = oldGOOS
		wireGuardRuntimeLookPath = oldLookPath
		wireGuardRuntimeRun = oldRun
	})

	err := removeWireGuardInterface("amwgdeadbeef")
	if err == nil || !strings.Contains(err.Error(), "probe wireguard interface") {
		t.Fatalf("error = %v", err)
	}

	joined := strings.Join(calls, "\n")
	if strings.Contains(joined, "ip link delete dev amwgdeadbeef") {
		t.Fatalf("interface deleted after unknown probe error:\n%s", joined)
	}
}
