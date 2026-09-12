package nodeagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWireGuardStaticPolicyFilterOmitsDeniedPeerFromRuntimeConfig(
	t *testing.T,
) {
	server := New(Config{DataDir: t.TempDir()})

	disabledKey := wireGuardTestKey(2)
	holdKey := wireGuardTestKey(3)
	expired := int64(1)

	inbound := wireGuardRuntimeInbound{
		Tag:        "wg-main",
		ListenPort: 51820,
		TunnelPort: 41940,
		Settings: map[string]any{
			"private_key": wireGuardTestKey(1),
		},
		Peers: []wireGuardRuntimePeer{
			{
				UserID:    42,
				PublicKey: disabledKey,
				Address:   "10.69.0.2",
				Status:    "disabled",
			},
			{
				UserID:    43,
				PublicKey: holdKey,
				Address:   "10.69.0.3",
				Status:    "on_hold",
				Expire:    &expired,
			},
		},
	}

	filtered, suppressed :=
		filterWireGuardRuntimeInboundByStaticPolicy(inbound)

	if len(filtered.Peers) != 1 ||
		filtered.Peers[0].PublicKey != holdKey {
		t.Fatalf(
			"filtered peers = %#v, want only on_hold peer",
			filtered.Peers,
		)
	}

	if len(suppressed) != 1 ||
		suppressed[0].PublicKey != disabledKey {
		t.Fatalf(
			"suppressed peers = %#v, want disabled peer",
			suppressed,
		)
	}

	prepared, err := server.prepareWireGuardInbound(filtered)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(prepared.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}

	text := string(raw)

	if strings.Contains(text, disabledKey) {
		t.Fatal("disabled peer was rendered into WireGuard config")
	}

	if !strings.Contains(text, holdKey) {
		t.Fatal("on_hold peer was incorrectly removed from WireGuard config")
	}
}

func TestWireGuardDynamicSuppressionOmitsPeerUntilDesiredRemoval(
	t *testing.T,
) {
	server := New(Config{DataDir: t.TempDir()})

	suppressedKey := wireGuardTestKey(2)
	keptKey := wireGuardTestKey(3)
	inbound := wireGuardRuntimeInbound{
		Tag:        "wg-main",
		ListenPort: 51820,
		TunnelPort: 41940,
		Settings: map[string]any{
			"private_key": wireGuardTestKey(1),
		},
		Peers: []wireGuardRuntimePeer{
			{
				UserID:    42,
				PublicKey: suppressedKey,
				Address:   "10.69.0.2",
				Status:    "active",
			},
			{
				UserID:    43,
				PublicKey: keptKey,
				Address:   "10.69.0.3",
				Status:    "active",
			},
		},
	}

	server.suppressWireGuardPeerUntilDesiredRemoval("wg-main", suppressedKey)
	filtered := server.filterWireGuardRuntimeInboundByDynamicSuppression(
		inbound,
	)

	if len(filtered.Peers) != 1 ||
		filtered.Peers[0].PublicKey != keptKey {
		t.Fatalf(
			"filtered peers = %#v, want only unsuppressed peer",
			filtered.Peers,
		)
	}

	prepared, err := server.prepareWireGuardInbound(filtered)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(prepared.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}

	text := string(raw)
	if strings.Contains(text, suppressedKey) {
		t.Fatal("dynamically suppressed peer was rendered into WireGuard config")
	}
	if !strings.Contains(text, keptKey) {
		t.Fatal("unsuppressed peer was removed from WireGuard config")
	}

	withoutSuppressed := inbound
	withoutSuppressed.Peers = []wireGuardRuntimePeer{inbound.Peers[1]}
	_ = server.filterWireGuardRuntimeInboundByDynamicSuppression(
		withoutSuppressed,
	)
	restored := server.filterWireGuardRuntimeInboundByDynamicSuppression(
		inbound,
	)
	if len(restored.Peers) != 2 {
		t.Fatalf(
			"suppression was not cleared after desired removal: %#v",
			restored.Peers,
		)
	}
}

func TestApplyWireGuardRuntimeSnapshotsSuppressedPeerBeforeSyncconf(
	t *testing.T,
) {
	server := New(Config{DataDir: t.TempDir()})
	server.wireGuardUsageLoaded = true

	baselineKey := wireGuardUsageBaselineKey(
		"wg-main",
		"wg-test0",
		"peer-a",
	)

	server.wireGuardUsageBaseline[baselineKey] = 100

	configPath := filepath.Join(t.TempDir(), "wg.conf")
	if err := os.WriteFile(
		configPath,
		[]byte("[Interface]\n"),
		0600,
	); err != nil {
		t.Fatal(err)
	}

	oldGOOS := wireGuardRuntimeGOOS
	oldLookPath := wireGuardRuntimeLookPath
	oldRun := wireGuardRuntimeRun
	oldDump := wireGuardDumpInterface

	wireGuardRuntimeGOOS = "linux"

	wireGuardRuntimeLookPath = func(name string) (string, error) {
		return name, nil
	}

	wireGuardDumpInterface = func(
		context.Context,
		string,
	) ([]byte, error) {
		return []byte(
			"priv\tserver-pub\t51820\toff\n" +
				"peer-a\t(none)\t198.51.100.4:20000\t10.69.0.2/32\t" +
				"1700000000\t100\t50\t25\n",
		), nil
	}

	sawSyncconf := false

	wireGuardRuntimeRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		if name == "wg" &&
			len(args) >= 1 &&
			args[0] == "syncconf" {
			carry, ok := server.wireGuardUsageCarry[baselineKey]
			if !ok {
				t.Fatal("syncconf ran before suppressed usage was persisted")
			}

			if carry.Value != 50 ||
				carry.NextBaseline != 150 {
				t.Fatalf(
					"carry before syncconf = %#v, want value=50 baseline=150",
					carry,
				)
			}

			sawSyncconf = true
		}

		return nil, nil
	}

	t.Cleanup(func() {
		wireGuardRuntimeGOOS = oldGOOS
		wireGuardRuntimeLookPath = oldLookPath
		wireGuardRuntimeRun = oldRun
		wireGuardDumpInterface = oldDump
	})

	accountingEnabled := true

	err := server.applyWireGuardRuntime(
		preparedWireGuardRuntime{
			Tag:           "wg-main",
			InterfaceName: "wg-test0",
			ConfigPath:    configPath,
			ServerCIDR:    "10.69.0.1/16",
			SourceCIDR:    "10.69.0.0/16",
			Inbound: wireGuardRuntimeInbound{
				Settings: map[string]any{
					"accounting_enabled": accountingEnabled,
				},
			},
			SuppressedPeers: []wireGuardRuntimePeer{{
				UserID:    42,
				PublicKey: "peer-a",
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if !sawSyncconf {
		t.Fatal("wg syncconf was not executed")
	}
}

func TestApplyWireGuardRuntimeBlocksSyncconfWhenSuppressedSnapshotFails(
	t *testing.T,
) {
	server := New(Config{DataDir: t.TempDir()})

	configPath := filepath.Join(t.TempDir(), "wg.conf")
	if err := os.WriteFile(
		configPath,
		[]byte("[Interface]\n"),
		0600,
	); err != nil {
		t.Fatal(err)
	}

	oldGOOS := wireGuardRuntimeGOOS
	oldLookPath := wireGuardRuntimeLookPath
	oldRun := wireGuardRuntimeRun
	oldDump := wireGuardDumpInterface

	wireGuardRuntimeGOOS = "linux"

	wireGuardRuntimeLookPath = func(name string) (string, error) {
		return name, nil
	}

	wireGuardDumpInterface = func(
		context.Context,
		string,
	) ([]byte, error) {
		return nil, errors.New("dump failed")
	}

	sawSyncconf := false

	wireGuardRuntimeRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		if name == "wg" &&
			len(args) >= 1 &&
			args[0] == "syncconf" {
			sawSyncconf = true
		}

		return nil, nil
	}

	t.Cleanup(func() {
		wireGuardRuntimeGOOS = oldGOOS
		wireGuardRuntimeLookPath = oldLookPath
		wireGuardRuntimeRun = oldRun
		wireGuardDumpInterface = oldDump
	})

	err := server.applyWireGuardRuntime(
		preparedWireGuardRuntime{
			Tag:           "wg-main",
			InterfaceName: "wg-test0",
			ConfigPath:    configPath,
			ServerCIDR:    "10.69.0.1/16",
			SourceCIDR:    "10.69.0.0/16",
			Inbound: wireGuardRuntimeInbound{
				Settings: map[string]any{
					"accounting_enabled": true,
				},
			},
			SuppressedPeers: []wireGuardRuntimePeer{{
				UserID:    42,
				PublicKey: "peer-a",
			}},
		},
	)

	if err == nil ||
		!strings.Contains(err.Error(), "dump failed") {
		t.Fatalf("error = %v, want suppressed snapshot failure", err)
	}

	if sawSyncconf {
		t.Fatal("syncconf ran after suppressed usage snapshot failure")
	}
}
