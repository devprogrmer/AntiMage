package nodeagent

import (
	"encoding/base64"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func awgTestKey(fill byte) string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Repeat(string(fill), 32)))
}

func TestFilterAmneziaWGRuntimeInboundEnforcesDisableExpireAndQuota(t *testing.T) {
	now := time.Unix(2_000, 0)
	limit := int64(100)
	expired := int64(1_999)
	inbound := amneziaWGRuntimeInbound{Peers: []amneziaWGRuntimePeer{
		{UserID: 1, Status: "active"},
		{UserID: 2, Status: "disabled"},
		{UserID: 3, Status: "active", UsedTraffic: 100, DataLimit: &limit},
		{UserID: 4, Status: "active", Expire: &expired},
	}}
	filtered := filterAmneziaWGRuntimeInboundByPolicy(inbound, now)
	if len(filtered.Peers) != 1 || filtered.Peers[0].UserID != 1 {
		t.Fatalf("filtered peers=%#v", filtered.Peers)
	}
}

func TestAmneziaWGObfuscationPreservesExplicitZeroJunkCount(t *testing.T) {
	settings, err := amneziaWGObfuscationSettings(map[string]any{"jc": 0, "jmin": 0, "jmax": 0, "s1": 0, "s2": 0, "h1": "101", "h2": "102", "h3": "103", "h4": "104"})
	if err != nil {
		t.Fatal(err)
	}
	if settings.Jc != 0 || settings.Jmin != 0 || settings.Jmax != 0 || settings.S1 != 0 || settings.S2 != 0 {
		t.Fatalf("explicit zeros changed: %#v", settings)
	}
}

func TestStopRemovedAmneziaWGRuntimesRetriesPersistedInterface(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	state := amneziaWGRuntimeState{Tag: "stale", InterfaceName: "awgdeadbeef"}
	s.amneziaWGRuntimes["stale"] = state
	if err := s.persistAmneziaWGRuntimeStates(); err != nil {
		t.Fatal(err)
	}
	s.amneziaWGRuntimes = map[string]amneziaWGRuntimeState{}

	originalRemove := amneziaWGRemoveRuntime
	t.Cleanup(func() { amneziaWGRemoveRuntime = originalRemove })
	var removed string
	amneziaWGRemoveRuntime = func(interfaceName string) error {
		removed = interfaceName
		return nil
	}
	s.stopRemovedAmneziaWGRuntimes(map[string]preparedAmneziaWGRuntime{})
	if removed != state.InterfaceName {
		t.Fatalf("removed interface = %q, want %q", removed, state.InterfaceName)
	}
	loaded, err := s.loadAmneziaWGRuntimeStates()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 0 {
		t.Fatalf("runtime state after removal = %#v, want empty", loaded)
	}
	info, err := os.Stat(s.amneziaWGRuntimeStatePath())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("runtime state permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestParseNativeRuntimePayloadIncludesAmneziaWG(t *testing.T) {
	raw := `{"awg_inbounds":[{"tag":"awg-main","listen_port":51821,"settings":{"private_key":"` + awgTestKey('a') + `"},"peers":[]}]}`
	payload, err := parseNativeRuntimePayload(raw)
	if err != nil {
		t.Fatalf("parse native runtime payload: %v", err)
	}
	if len(payload.AmneziaWGInbounds) != 1 {
		t.Fatalf("AWG inbounds = %d, want 1", len(payload.AmneziaWGInbounds))
	}
	if payload.AmneziaWGInbounds[0].Tag != "awg-main" {
		t.Fatalf("AWG tag = %q", payload.AmneziaWGInbounds[0].Tag)
	}
}

func TestAmneziaWGGeneratedInterfaceNameIsStableAndIndependent(t *testing.T) {
	first, err := amneziaWGGeneratedInterfaceName("primary")
	if err != nil {
		t.Fatal(err)
	}
	second, err := amneziaWGGeneratedInterfaceName("primary")
	if err != nil {
		t.Fatal(err)
	}
	wg, err := wireGuardGeneratedInterfaceName("primary")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("interface name is unstable: %q != %q", first, second)
	}
	if first == wg || !strings.HasPrefix(first, "awg") || len(first) > 15 {
		t.Fatalf("AWG interface %q is not independent from WireGuard %q", first, wg)
	}
}

func TestPrepareAmneziaWGInboundPreservesObfuscationAndDevices(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	inbound := amneziaWGRuntimeInbound{
		Tag:        "awg-main",
		ListenPort: 51821,
		Settings: map[string]any{
			"private_key":    awgTestKey('s'),
			"address_pool":   "10.72.0.0/24",
			"server_address": "10.72.0.1/24",
			"mtu":            1420,
			"jc":             4, "jmin": 8, "jmax": 80, "s1": 77, "s2": 90,
			"h1": "101", "h2": "102", "h3": "103", "h4": "104",
		},
		Peers: []amneziaWGRuntimePeer{
			{UserID: 7, DeviceIndex: 0, PublicKey: awgTestKey('p'), Address: "10.72.0.2", Status: "active"},
			{UserID: 7, DeviceIndex: 1, PublicKey: awgTestKey('q'), Address: "10.72.0.3", Status: "active"},
		},
	}

	prepared, err := s.prepareAmneziaWGInbound(inbound)
	if err != nil {
		t.Fatalf("prepare AWG inbound: %v", err)
	}
	if prepared.Obfuscation.H1 != "101" || prepared.Obfuscation.Jmax != 80 {
		t.Fatalf("obfuscation was not preserved: %#v", prepared.Obfuscation)
	}
	if len(prepared.Inbound.Peers) != 2 || prepared.Inbound.Peers[1].DeviceIndex != 1 {
		t.Fatalf("device peers were not preserved: %#v", prepared.Inbound.Peers)
	}
	if !strings.Contains(prepared.ConfigText, "Jc = 4") || !strings.Contains(prepared.ConfigText, "H4 = 104") {
		t.Fatalf("AWG audit config misses obfuscation:\n%s", prepared.ConfigText)
	}
}

func TestPrepareAmneziaWGInboundRejectsDuplicatePeerAddress(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	inbound := amneziaWGRuntimeInbound{
		Tag: "duplicate", ListenPort: 51821,
		Settings: map[string]any{
			"private_key": awgTestKey('s'), "address_pool": "10.72.0.0/24",
			"server_address": "10.72.0.1/24", "h1": "101", "h2": "102", "h3": "103", "h4": "104",
		},
		Peers: []amneziaWGRuntimePeer{
			{UserID: 1, PublicKey: awgTestKey('a'), Address: "10.72.0.2"},
			{UserID: 2, PublicKey: awgTestKey('b'), Address: "10.72.0.2"},
		},
	}
	if _, err := s.prepareAmneziaWGInbound(inbound); err == nil || !strings.Contains(err.Error(), "duplicate peer address") {
		t.Fatalf("error = %v, want duplicate peer address", err)
	}
}
