package nodeagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParseWireGuardDump(t *testing.T) {
	raw := "priv\tserver-pub\t51820\toff\n" +
		"peer-a\t(none)\t198.51.100.4:20000\t10.69.0.2/32\t1700000000\t120\t80\t25\n"

	peers, err := parseWireGuardDump(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 {
		t.Fatalf("peers = %d, want 1", len(peers))
	}
	if peers[0].PublicKey != "peer-a" {
		t.Fatalf("public key = %q", peers[0].PublicKey)
	}
	if peers[0].Endpoint != "198.51.100.4:20000" {
		t.Fatalf("endpoint = %q", peers[0].Endpoint)
	}
	total, err := wireGuardPeerTotalBytes(peers[0])
	if err != nil {
		t.Fatal(err)
	}
	if total != 200 {
		t.Fatalf("total = %d, want 200", total)
	}
}

func TestWireGuardInterfaceNameExplicitOnly(t *testing.T) {
	inbound := wireGuardRuntimeInbound{
		Tag:      "wg-main",
		Settings: map[string]any{},
	}
	got, err := wireGuardInterfaceName(inbound)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("default interface name = %q, want empty", got)
	}

	inbound.Settings["interface_name"] = "wg-custom0"
	got, err = wireGuardInterfaceName(inbound)
	if err != nil {
		t.Fatal(err)
	}
	if got != "wg-custom0" {
		t.Fatalf("interface name = %q", got)
	}
}

func TestParseWireGuardAllDump(t *testing.T) {
	raw := "wg-real0\tpriv\tserver-pub\t51820\toff\n" +
		"wg-real0\tpeer-a\t(none)\t198.51.100.4:20000\t10.69.0.2/32\t1700000000\t120\t80\t25\n"

	interfaces, err := parseWireGuardAllDump(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := interfaces["wg-real0"]
	if !ok {
		t.Fatal("wg-real0 not found")
	}
	if got.ListenPort != 51820 {
		t.Fatalf("listen port = %d", got.ListenPort)
	}
	if len(got.Peers) != 1 || got.Peers[0].PublicKey != "peer-a" {
		t.Fatalf("unexpected peers: %#v", got.Peers)
	}
	if got.Peers[0].Endpoint != "198.51.100.4:20000" {
		t.Fatalf("endpoint = %q", got.Peers[0].Endpoint)
	}
}

func TestSyncWireGuardUsageConfigs(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})
	inbound := wireGuardRuntimeInbound{
		Tag:        "wg-main",
		ListenPort: 51820,
		Settings: map[string]any{
			"accounting_enabled": true,
			"interface_name":     "wg-test0",
		},
		Peers: []wireGuardRuntimePeer{{
			UserID:    42,
			PublicKey: "peer-a",
		}},
	}

	if err := server.syncWireGuardUsageConfigs(
		[]wireGuardRuntimeInbound{inbound},
	); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(
		server.cfg.DataDir,
		"wireguard",
		"inbounds",
		wireGuardRuntimeDirName("wg-main"),
		"usage-helper.json",
	)
	if _, err := os.ReadFile(path); err != nil {
		t.Fatal(err)
	}

	inbound.Settings["accounting_enabled"] = false
	if err := server.syncWireGuardUsageConfigs(
		[]wireGuardRuntimeInbound{inbound},
	); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg wireGuardUsageRuntimeConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.AccountingEnabled == nil || *cfg.AccountingEnabled {
		t.Fatal("disabled accounting must persist helper with accounting_enabled=false")
	}
}
