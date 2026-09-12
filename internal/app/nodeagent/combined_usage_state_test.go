package nodeagent

import (
	"context"
	"strings"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestCombinedUsageThreeSourcesAckAndRetry(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})
	ctx := context.Background()

	server.openVPNUsagePending = &openVPNUsagePendingBatch{
		BatchID:      "openvpn-three",
		NextBaseline: map[string]uint64{"ov": 100},
	}
	server.openVPNUsageLoaded = true
	if err := server.persistOpenVPNUsageStateLocked(); err != nil {
		t.Fatal(err)
	}

	server.xrayUsagePending = &xrayUsagePendingBatch{
		BatchID:      "xray-three",
		NextBaseline: map[string]uint64{"xr": 200},
	}
	server.xrayUsageLoaded = true
	if err := server.persistXrayUsageStateLocked(); err != nil {
		t.Fatal(err)
	}

	server.wireGuardUsagePending = &wireGuardUsagePendingBatch{
		BatchID:      "wireguard-three",
		NextBaseline: map[string]uint64{"wg": 300},
	}
	server.wireGuardUsageLoaded = true
	if err := server.persistWireGuardUsageStateLocked(); err != nil {
		t.Fatal(err)
	}

	core := server.mergeUserUsageBatches(
		&nodev1.UserUsageBatch{
			BatchId: "openvpn-three",
			Stats: []*nodev1.UserUsageSample{{
				Uid:   "openvpn:1",
				Value: 10,
			}},
		},
		&nodev1.UserUsageBatch{
			BatchId: "xray-three",
			Stats: []*nodev1.UserUsageSample{{
				Uid:   "xray:2",
				Value: 20,
			}},
		},
	)
	wg := &nodev1.UserUsageBatch{
		BatchId: "wireguard-three",
		Stats: []*nodev1.UserUsageSample{{
			Uid:   "wireguard:3",
			Value: 30,
		}},
	}

	combined, err := server.combineUserUsageBatches(core, wg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(combined.GetBatchId(), "combined-") {
		t.Fatalf("combined batch id = %q", combined.GetBatchId())
	}
	if len(combined.GetStats()) != 3 {
		t.Fatalf("combined stats = %d, want 3", len(combined.GetStats()))
	}

	resp, err := server.ackCombinedUserUsage(
		ctx,
		&nodev1.AckUsageRequest{BatchId: combined.GetBatchId()},
	)
	if err != nil || !resp.GetAcknowledged() {
		t.Fatalf("combined ACK failed: %v", err)
	}

	if server.openVPNUsageBaseline["ov"] != 100 {
		t.Fatal("OpenVPN baseline did not advance")
	}
	if server.xrayUsageBaseline["xr"] != 200 {
		t.Fatal("Xray baseline did not advance")
	}
	if server.wireGuardUsageBaseline["wg"] != 300 {
		t.Fatal("WireGuard baseline did not advance")
	}

	resp2, err := server.ackCombinedUserUsage(
		ctx,
		&nodev1.AckUsageRequest{BatchId: combined.GetBatchId()},
	)
	if err != nil || !resp2.GetAcknowledged() {
		t.Fatalf("combined retry ACK failed: %v", err)
	}
}

func TestCombinedUsageWireGuardAwaitingReflectionUsesOuterBatchID(
	t *testing.T,
) {
	dataDir := t.TempDir()
	server := New(Config{DataDir: dataDir})
	ctx := context.Background()

	server.openVPNUsageLoaded = true
	server.openVPNUsagePending = &openVPNUsagePendingBatch{
		BatchID: "openvpn-combined-reflection",
		NextBaseline: map[string]uint64{
			"ov": 100,
		},
	}

	server.wireGuardUsageLoaded = true
	server.wireGuardUsagePending = &wireGuardUsagePendingBatch{
		BatchID: "wireguard-combined-reflection",
		Samples: []wireGuardUsageSample{{
			UserID:     42,
			InboundTag: "wg-main",
			Value:      50,
		}},
		NextBaseline: map[string]uint64{
			"wg": 150,
		},
	}

	core := &nodev1.UserUsageBatch{
		BatchId: "openvpn-combined-reflection",
		Stats: []*nodev1.UserUsageSample{{
			Uid:   "openvpn:1",
			Value: 10,
		}},
	}

	wg := &nodev1.UserUsageBatch{
		BatchId: "wireguard-combined-reflection",
		Stats: []*nodev1.UserUsageSample{{
			Uid:        "wireguard:42",
			Value:      50,
			InboundTag: "wg-main",
		}},
	}

	combined, err := server.combineUserUsageBatches(core, wg)
	if err != nil {
		t.Fatal(err)
	}

	outerBatchID := combined.GetBatchId()
	if !strings.HasPrefix(outerBatchID, "combined-") {
		t.Fatalf(
			"outer batch id = %q, want combined-*",
			outerBatchID,
		)
	}

	resp, err := server.ackCombinedUserUsage(
		ctx,
		&nodev1.AckUsageRequest{
			BatchId: outerBatchID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("combined usage ACK was not acknowledged")
	}

	if len(server.wireGuardUsageAwaitingReflection) != 1 {
		t.Fatalf(
			"awaiting reflection batches = %d, want 1",
			len(server.wireGuardUsageAwaitingReflection),
		)
	}

	awaiting := server.wireGuardUsageAwaitingReflection[0]

	if awaiting.BatchID != outerBatchID {
		t.Fatalf(
			"awaiting reflection batch = %q, want outer %q",
			awaiting.BatchID,
			outerBatchID,
		)
	}

	if len(awaiting.Samples) != 1 ||
		awaiting.Samples[0].UserID != 42 ||
		awaiting.Samples[0].Value != 50 {
		t.Fatalf(
			"unexpected awaiting reflection samples: %#v",
			awaiting.Samples,
		)
	}

	// The outer mapping must also survive a node restart.
	server2 := New(Config{DataDir: dataDir})

	if err := server2.ensureWireGuardUsageStateLoadedLocked(); err != nil {
		t.Fatal(err)
	}

	if len(server2.wireGuardUsageAwaitingReflection) != 1 {
		t.Fatalf(
			"restarted awaiting reflection batches = %d, want 1",
			len(server2.wireGuardUsageAwaitingReflection),
		)
	}

	if got := server2.wireGuardUsageAwaitingReflection[0].BatchID; got != outerBatchID {
		t.Fatalf(
			"restarted awaiting reflection batch = %q, want outer %q",
			got,
			outerBatchID,
		)
	}
}
