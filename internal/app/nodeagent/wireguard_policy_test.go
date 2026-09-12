package nodeagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWireGuardUsageConfigPersistsPeerPoliciesWhenAccountingDisabled(
	t *testing.T,
) {
	dataDir := t.TempDir()
	server := New(Config{DataDir: dataDir})

	limit := int64(1000)
	expire := int64(2_000_000_000)

	if err := server.syncWireGuardUsageConfigs(
		[]wireGuardRuntimeInbound{{
			Tag:        "wg-main",
			ListenPort: 51820,
			Settings: map[string]any{
				"accounting_enabled": false,
				"interface_name":     "wg-test0",
			},
			Peers: []wireGuardRuntimePeer{{
				UserID:      42,
				Username:    "alice",
				PublicKey:   "peer-a",
				Address:     "10.69.0.42",
				Status:      "active",
				UsedTraffic: 900,
				DataLimit:   &limit,
				Expire:      &expire,
				DeviceLimit: 2,
			}},
		}},
	); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(
		dataDir,
		"wireguard",
		"inbounds",
		wireGuardRuntimeDirName("wg-main"),
		"usage-helper.json",
	)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var cfg wireGuardUsageRuntimeConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}

	if cfg.AccountingEnabled == nil || *cfg.AccountingEnabled {
		t.Fatal("accounting_enabled=false was not preserved")
	}

	if got := cfg.Peers["peer-a"]; got != 42 {
		t.Fatalf("peer user id = %d, want 42", got)
	}

	if got := cfg.PeerAddresses["peer-a"]; got != "10.69.0.42" {
		t.Fatalf("peer address = %q", got)
	}

	policy, ok := cfg.Policies["peer-a"]
	if !ok {
		t.Fatal("wireguard peer policy missing")
	}

	if policy.Status != "active" {
		t.Fatalf("policy status = %q, want active", policy.Status)
	}
	if policy.UsedTraffic != 900 {
		t.Fatalf(
			"policy used traffic = %d, want 900",
			policy.UsedTraffic,
		)
	}
	if policy.DataLimit != 1000 {
		t.Fatalf(
			"policy data limit = %d, want 1000",
			policy.DataLimit,
		)
	}
	if policy.Expire != expire {
		t.Fatalf(
			"policy expire = %d, want %d",
			policy.Expire,
			expire,
		)
	}
}
