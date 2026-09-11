package nodeagent

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"
)

func wireGuardTestKey(fill byte) string {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = fill
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func TestPrepareWireGuardInboundDefaults(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})
	inbound := wireGuardRuntimeInbound{
		Tag:        "wg-main",
		ListenPort: 51820,
		TunnelPort: 41940,
		Settings: map[string]any{
			"private_key": wireGuardTestKey(1),
		},
		Peers: []wireGuardRuntimePeer{{
			UserID:    42,
			PublicKey: wireGuardTestKey(2),
			Address:   "10.69.0.42",
		}},
	}

	prepared, err := server.prepareWireGuardInbound(inbound)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(prepared.InterfaceName, "amwg") {
		t.Fatalf("interface = %q", prepared.InterfaceName)
	}
	if len(prepared.InterfaceName) > 15 {
		t.Fatalf("interface name too long: %q", prepared.InterfaceName)
	}
	if prepared.ServerCIDR != "10.69.0.1/16" {
		t.Fatalf("server CIDR = %q", prepared.ServerCIDR)
	}
	if prepared.SourceCIDR != "10.69.0.0/16" {
		t.Fatalf("source CIDR = %q", prepared.SourceCIDR)
	}

	raw, err := os.ReadFile(prepared.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"[Interface]",
		"PrivateKey = " + wireGuardTestKey(1),
		"ListenPort = 51820",
		"[Peer]",
		"PublicKey = " + wireGuardTestKey(2),
		"AllowedIPs = 10.69.0.42/32",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("config missing %q:\n%s", want, text)
		}
	}
}

func TestPrepareWireGuardInboundUsesGlobalPSKFallback(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})
	psk := wireGuardTestKey(9)
	inbound := wireGuardRuntimeInbound{
		Tag:        "wg-main",
		ListenPort: 51820,
		TunnelPort: 41940,
		Settings: map[string]any{
			"private_key":    wireGuardTestKey(1),
			"pre_shared_key": psk,
		},
		Peers: []wireGuardRuntimePeer{{
			UserID:    1,
			PublicKey: wireGuardTestKey(2),
			Address:   "10.69.0.2",
		}},
	}

	prepared, err := server.prepareWireGuardInbound(inbound)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(prepared.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "PresharedKey = "+psk) {
		t.Fatalf("global PSK was not rendered:\n%s", string(raw))
	}
}

func TestPrepareWireGuardInboundRejectsDuplicatePeerAddress(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})
	inbound := wireGuardRuntimeInbound{
		Tag:        "wg-main",
		ListenPort: 51820,
		Settings: map[string]any{
			"private_key": wireGuardTestKey(1),
		},
		Peers: []wireGuardRuntimePeer{
			{
				UserID:    1,
				PublicKey: wireGuardTestKey(2),
				Address:   "10.69.0.2",
			},
			{
				UserID:    2,
				PublicKey: wireGuardTestKey(3),
				Address:   "10.69.0.2",
			},
		},
	}

	_, err := server.prepareWireGuardInbound(inbound)
	if err == nil ||
		!strings.Contains(err.Error(), "duplicate peer address") {
		t.Fatalf("error = %v", err)
	}
}

func TestApplyWireGuardRuntimeCreatesAndConfiguresInterface(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})
	prepared, err := server.prepareWireGuardInbound(
		wireGuardRuntimeInbound{
			Tag:        "wg-main",
			ListenPort: 51820,
			TunnelPort: 41940,
			Settings: map[string]any{
				"private_key": wireGuardTestKey(1),
				"mtu":         1380,
			},
			Peers: []wireGuardRuntimePeer{{
				UserID:    1,
				PublicKey: wireGuardTestKey(2),
				Address:   "10.69.0.2",
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

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
		if name == "ip" &&
			len(args) >= 4 &&
			args[0] == "link" &&
			args[1] == "show" {
			return nil, errors.New("not found")
		}
		return nil, nil
	}
	t.Cleanup(func() {
		wireGuardRuntimeGOOS = oldGOOS
		wireGuardRuntimeLookPath = oldLookPath
		wireGuardRuntimeRun = oldRun
	})

	if err := server.applyWireGuardRuntime(prepared); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(calls, "\n")
	for _, want := range []string{
		"sysctl -w net.ipv4.ip_forward=1",
		"ip link add dev " + prepared.InterfaceName + " type wireguard",
		"ip link set dev " + prepared.InterfaceName + " alias " + wireGuardOwnershipAlias(prepared.Tag),
		"wg syncconf " + prepared.InterfaceName,
		"ip -4 address flush dev " + prepared.InterfaceName + " scope global",
		"ip address add 10.69.0.1/16 dev " + prepared.InterfaceName,
		"ip link set dev " + prepared.InterfaceName + " mtu 1380",
		"ip link set dev " + prepared.InterfaceName + " up",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing call %q:\n%s", want, joined)
		}
	}

	if _, ok := server.wireGuardRuntimes["wg-main"]; !ok {
		t.Fatal("runtime state not registered")
	}
}

func TestWireGuardRuntimeAddressingAcceptsPlainServerIP(t *testing.T) {
	pool, serverCIDR, err := wireGuardRuntimeAddressing(
		map[string]any{
			"address_pool":   "10.80.0.0/24",
			"server_address": "10.80.0.10",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if pool.String() != "10.80.0.0/24" {
		t.Fatalf("pool = %s", pool)
	}
	if serverCIDR != "10.80.0.10/24" {
		t.Fatalf("server CIDR = %q", serverCIDR)
	}
}

func TestStopAllWireGuardRuntimesRecoversPersistedState(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})
	interfaceName, err := wireGuardGeneratedInterfaceName("wg-main")
	if err != nil {
		t.Fatal(err)
	}
	state := wireGuardRuntimeState{
		Tag:           "wg-main",
		InterfaceName: interfaceName,
		ConfigPath:    "unused",
		ServerCIDR:    "10.69.0.1/16",
		SourceCIDR:    "10.69.0.0/16",
	}
	if err := server.persistWireGuardRuntimeState(state); err != nil {
		t.Fatal(err)
	}

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
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil, nil
	}
	t.Cleanup(func() {
		wireGuardRuntimeGOOS = oldGOOS
		wireGuardRuntimeLookPath = oldLookPath
		wireGuardRuntimeRun = oldRun
	})

	server.stopAllWireGuardRuntimes()

	joined := strings.Join(calls, "\n")
	if !strings.Contains(
		joined,
		"ip link delete dev "+state.InterfaceName,
	) {
		t.Fatalf("persisted interface was not deleted:\n%s", joined)
	}

	if _, err := os.Stat(
		server.wireGuardRuntimeManifestPath(state.Tag),
	); !os.IsNotExist(err) {
		t.Fatalf("runtime manifest still exists: %v", err)
	}
}

func TestApplyWireGuardRuntimeRejectsUnmanagedExplicitExistingInterface(
	t *testing.T,
) {
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
		if name == "ip" &&
			strings.Contains(call, "link show dev wg-external0") {
			return []byte("exists"), nil
		}
		return nil, nil
	}
	t.Cleanup(func() {
		wireGuardRuntimeGOOS = oldGOOS
		wireGuardRuntimeLookPath = oldLookPath
		wireGuardRuntimeRun = oldRun
	})

	err := server.applyWireGuardRuntime(preparedWireGuardRuntime{
		Tag:               "wg-main",
		InterfaceName:     "wg-external0",
		ExplicitInterface: true,
		ConfigPath:        "unused.conf",
		ServerCIDR:        "10.69.0.1/16",
		SourceCIDR:        "10.69.0.0/16",
	})
	if err == nil ||
		!strings.Contains(err.Error(), "not managed by AntiMage") {
		t.Fatalf("error = %v", err)
	}

	joined := strings.Join(calls, "\n")
	for _, forbidden := range []string{
		"sysctl -w net.ipv4.ip_forward=1",
		"wg syncconf",
		"address flush",
		"link set dev wg-external0 up",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf(
				"destructive command %q ran before ownership rejection:\n%s",
				forbidden,
				joined,
			)
		}
	}
}

func TestApplyWireGuardRuntimeAllowsPreviouslyManagedExplicitInterface(
	t *testing.T,
) {
	server := New(Config{DataDir: t.TempDir()})
	server.wireGuardRuntimes["wg-main"] = wireGuardRuntimeState{
		Tag:               "wg-main",
		InterfaceName:     "wg-managed0",
		ExplicitInterface: true,
	}

	oldGOOS := wireGuardRuntimeGOOS
	oldLookPath := wireGuardRuntimeLookPath
	oldRun := wireGuardRuntimeRun
	oldReadFile := wireGuardRuntimeReadFile
	wireGuardRuntimeGOOS = "linux"
	wireGuardRuntimeLookPath = func(name string) (string, error) {
		return name, nil
	}
	wireGuardRuntimeReadFile = func(string) ([]byte, error) {
		return []byte(wireGuardOwnershipAlias("wg-main") + "\n"), nil
	}

	var calls []string
	wireGuardRuntimeRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		call := name + " " + strings.Join(args, " ")
		calls = append(calls, call)
		if name == "ip" &&
			strings.Contains(call, "link show dev wg-managed0") {
			return []byte("exists"), nil
		}
		return nil, nil
	}
	t.Cleanup(func() {
		wireGuardRuntimeGOOS = oldGOOS
		wireGuardRuntimeLookPath = oldLookPath
		wireGuardRuntimeRun = oldRun
		wireGuardRuntimeReadFile = oldReadFile
	})

	err := server.applyWireGuardRuntime(preparedWireGuardRuntime{
		Tag:               "wg-main",
		InterfaceName:     "wg-managed0",
		ExplicitInterface: true,
		ConfigPath:        "managed.conf",
		ServerCIDR:        "10.69.0.1/16",
		SourceCIDR:        "10.69.0.0/16",
	})
	if err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "wg syncconf wg-managed0 managed.conf") {
		t.Fatalf("managed interface was not configured:\n%s", joined)
	}
}

func TestApplyWireGuardRuntimeRejectsExplicitInterfaceOwnedByDifferentRuntime(
	t *testing.T,
) {
	server := New(Config{DataDir: t.TempDir()})
	server.wireGuardRuntimes["wg-other"] = wireGuardRuntimeState{
		Tag:           "wg-other",
		InterfaceName: "wg-shared0",
	}

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
		if name == "ip" &&
			strings.Contains(call, "link show dev wg-shared0") {
			return []byte("exists"), nil
		}
		return nil, nil
	}
	t.Cleanup(func() {
		wireGuardRuntimeGOOS = oldGOOS
		wireGuardRuntimeLookPath = oldLookPath
		wireGuardRuntimeRun = oldRun
	})

	err := server.applyWireGuardRuntime(preparedWireGuardRuntime{
		Tag:               "wg-main",
		InterfaceName:     "wg-shared0",
		ExplicitInterface: true,
		ConfigPath:        "unused.conf",
		ServerCIDR:        "10.69.0.1/16",
		SourceCIDR:        "10.69.0.0/16",
	})
	if err == nil ||
		!strings.Contains(err.Error(), "not managed by AntiMage") {
		t.Fatalf("error = %v", err)
	}

	joined := strings.Join(calls, "\n")
	for _, forbidden := range []string{
		"sysctl -w net.ipv4.ip_forward=1",
		"wg syncconf",
		"address flush",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf(
				"destructive command %q ran for interface owned by another runtime:\n%s",
				forbidden,
				joined,
			)
		}
	}
}

func TestPrepareWireGuardInboundRejectsInvalidMTUBeforeWritingConfig(
	t *testing.T,
) {
	dataDir := t.TempDir()
	server := New(Config{DataDir: dataDir})
	_, err := server.prepareWireGuardInbound(wireGuardRuntimeInbound{
		Tag:        "wg-main",
		ListenPort: 51820,
		TunnelPort: 41940,
		Settings: map[string]any{
			"private_key": wireGuardTestKey(1),
			"mtu":         500,
		},
		Peers: []wireGuardRuntimePeer{{
			UserID:    1,
			PublicKey: wireGuardTestKey(2),
			Address:   "10.69.0.2",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid MTU") {
		t.Fatalf("error = %v", err)
	}
}

func TestStopAllWireGuardRuntimesDoesNotDeleteReusedExplicitInterface(
	t *testing.T,
) {
	server := New(Config{DataDir: t.TempDir()})
	state := wireGuardRuntimeState{
		Tag:               "wg-main",
		InterfaceName:     "wg-external0",
		ExplicitInterface: true,
		ConfigPath:        "unused",
		ServerCIDR:        "10.69.0.1/16",
		SourceCIDR:        "10.69.0.0/16",
	}
	if err := server.persistWireGuardRuntimeState(state); err != nil {
		t.Fatal(err)
	}

	oldGOOS := wireGuardRuntimeGOOS
	oldLookPath := wireGuardRuntimeLookPath
	oldRun := wireGuardRuntimeRun
	oldReadFile := wireGuardRuntimeReadFile
	wireGuardRuntimeGOOS = "linux"
	wireGuardRuntimeLookPath = func(name string) (string, error) {
		return name, nil
	}
	wireGuardRuntimeReadFile = func(string) ([]byte, error) {
		return []byte("owned-by-someone-else\n"), nil
	}

	var calls []string
	wireGuardRuntimeRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return []byte("exists"), nil
	}
	t.Cleanup(func() {
		wireGuardRuntimeGOOS = oldGOOS
		wireGuardRuntimeLookPath = oldLookPath
		wireGuardRuntimeRun = oldRun
		wireGuardRuntimeReadFile = oldReadFile
	})

	server.stopAllWireGuardRuntimes()

	joined := strings.Join(calls, "\n")
	if strings.Contains(joined, "ip link delete dev wg-external0") {
		t.Fatalf("reused external interface was deleted:\n%s", joined)
	}
	if _, err := os.Stat(
		server.wireGuardRuntimeManifestPath(state.Tag),
	); !os.IsNotExist(err) {
		t.Fatalf("stale runtime manifest still exists: %v", err)
	}
}

func TestStopAllWireGuardRuntimesDeletesOwnedExplicitInterface(
	t *testing.T,
) {
	server := New(Config{DataDir: t.TempDir()})
	state := wireGuardRuntimeState{
		Tag:               "wg-main",
		InterfaceName:     "wg-managed0",
		ExplicitInterface: true,
		ConfigPath:        "unused",
		ServerCIDR:        "10.69.0.1/16",
		SourceCIDR:        "10.69.0.0/16",
	}
	if err := server.persistWireGuardRuntimeState(state); err != nil {
		t.Fatal(err)
	}

	oldGOOS := wireGuardRuntimeGOOS
	oldLookPath := wireGuardRuntimeLookPath
	oldRun := wireGuardRuntimeRun
	oldReadFile := wireGuardRuntimeReadFile
	wireGuardRuntimeGOOS = "linux"
	wireGuardRuntimeLookPath = func(name string) (string, error) {
		return name, nil
	}
	wireGuardRuntimeReadFile = func(string) ([]byte, error) {
		return []byte(wireGuardOwnershipAlias("wg-main") + "\n"), nil
	}

	var calls []string
	wireGuardRuntimeRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return []byte("exists"), nil
	}
	t.Cleanup(func() {
		wireGuardRuntimeGOOS = oldGOOS
		wireGuardRuntimeLookPath = oldLookPath
		wireGuardRuntimeRun = oldRun
		wireGuardRuntimeReadFile = oldReadFile
	})

	server.stopAllWireGuardRuntimes()

	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "ip link delete dev wg-managed0") {
		t.Fatalf("owned explicit interface was not deleted:\n%s", joined)
	}
}

func TestStopAllWireGuardRuntimesTreatsLegacyCustomNameAsExplicit(
	t *testing.T,
) {
	server := New(Config{DataDir: t.TempDir()})
	state := wireGuardRuntimeState{
		Tag:           "wg-main",
		InterfaceName: "wg-external0",
		ConfigPath:    "unused",
		ServerCIDR:    "10.69.0.1/16",
		SourceCIDR:    "10.69.0.0/16",
		// ExplicitInterface intentionally omitted to emulate a pre-hardening manifest.
	}
	if err := server.persistWireGuardRuntimeState(state); err != nil {
		t.Fatal(err)
	}

	oldGOOS := wireGuardRuntimeGOOS
	oldLookPath := wireGuardRuntimeLookPath
	oldRun := wireGuardRuntimeRun
	oldReadFile := wireGuardRuntimeReadFile
	wireGuardRuntimeGOOS = "linux"
	wireGuardRuntimeLookPath = func(name string) (string, error) {
		return name, nil
	}
	wireGuardRuntimeReadFile = func(string) ([]byte, error) {
		return []byte("owned-by-someone-else\n"), nil
	}

	var calls []string
	wireGuardRuntimeRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return []byte("exists"), nil
	}
	t.Cleanup(func() {
		wireGuardRuntimeGOOS = oldGOOS
		wireGuardRuntimeLookPath = oldLookPath
		wireGuardRuntimeRun = oldRun
		wireGuardRuntimeReadFile = oldReadFile
	})

	server.stopAllWireGuardRuntimes()

	joined := strings.Join(calls, "\n")
	if strings.Contains(joined, "ip link delete dev wg-external0") {
		t.Fatalf("legacy explicit interface was deleted:\n%s", joined)
	}
	if _, err := os.Stat(
		server.wireGuardRuntimeManifestPath(state.Tag),
	); !os.IsNotExist(err) {
		t.Fatalf("legacy stale manifest still exists: %v", err)
	}
}

func TestWireGuardRuntimeStateUsesGeneratedNameForLegacyManagedState(
	t *testing.T,
) {
	name, err := wireGuardGeneratedInterfaceName("wg-main")
	if err != nil {
		t.Fatal(err)
	}
	state := wireGuardRuntimeState{
		Tag:           "wg-main",
		InterfaceName: name,
	}
	if wireGuardRuntimeStateUsesExplicitInterface(state) {
		t.Fatalf("generated legacy interface %q classified as explicit", name)
	}
}
