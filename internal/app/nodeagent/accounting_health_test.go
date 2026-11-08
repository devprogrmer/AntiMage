package nodeagent

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAccountingHealthReportsLoadedPendingACKAndReflectionWithoutDiskRead(t *testing.T) {
	dir := t.TempDir()
	s := New(Config{DataDir: dir})
	s.xrayUsageLoaded = true
	s.xrayUsagePending = &xrayUsagePendingBatch{BatchID: "xray-pending", Samples: []xrayUsageSample{{Value: 12}, {Value: 8}}}
	s.xrayUsageLastAckedBatchID = "xray-acked"
	s.wireGuardUsageLoaded = true
	s.wireGuardUsagePending = &wireGuardUsagePendingBatch{BatchID: "wg-pending", Samples: []wireGuardUsageSample{{Value: 30}}}
	s.localUsageLoaded = true
	s.localUsageDeliveries = []localUsageDelivery{{BatchID: "root-pending", Rows: []localUsageDeliveryRow{{Raw: 40}}}}
	s.localAccountingFailures = map[string]string{"xray": "checkpoint failed", "ppp": "ppp checkpoint failed"}
	s.accountingLastCheckpoint = map[string]int64{"wireguard": 1234, "ppp": 1235}
	// A metrics request must not load unrelated durable state from disk.
	path := filepath.Join(dir, "openvpn", "usage-state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}

	health := s.accountingHealth()
	if !health.GetReflectionLedgerLoaded() || health.GetAwaitingReflectionBatches() != 1 || health.GetAwaitingReflectionBytes() != 40 {
		t.Fatalf("reflection ledger summary = %#v", health)
	}
	status := map[string]struct {
		loaded     bool
		pending    string
		bytes      uint64
		ack        string
		checkpoint int64
		failure    string
	}{}
	for _, item := range health.GetProtocols() {
		status[item.GetProtocol()] = struct {
			loaded     bool
			pending    string
			bytes      uint64
			ack        string
			checkpoint int64
			failure    string
		}{
			item.GetStateLoaded(), item.GetPendingBatchId(), item.GetPendingBytes(), item.GetLastAckedBatchId(), item.GetLastCheckpointUnix(), item.GetFailure(),
		}
	}
	if got := status["xray"]; !got.loaded || got.pending != "xray-pending" || got.bytes != 20 || got.ack != "xray-acked" || got.failure != "checkpoint failed" {
		t.Fatalf("xray status = %#v", got)
	}
	if got := status["wireguard"]; !got.loaded || got.pending != "wg-pending" || got.bytes != 30 || got.checkpoint != 1234 {
		t.Fatalf("wireguard status = %#v", got)
	}
	if got := status["openvpn"]; got.loaded || got.pending != "" || got.bytes != 0 {
		t.Fatalf("unloaded openvpn state claimed a clean loaded snapshot: %#v", got)
	}
	for _, protocol := range []string{"l2tp", "pptp"} {
		if got := status[protocol]; got.checkpoint != 1235 || got.failure != "ppp checkpoint failed" {
			t.Fatalf("shared PPP worker result missing from %s: %#v", protocol, got)
		}
	}
}

func TestAccountingHealthOldestReflectionUsesDurableBatchIdentity(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	s.localUsageLoaded = true
	old := time.Unix(1_800_000_000, 0)
	newer := old.Add(2 * time.Minute)
	s.localUsageDeliveries = []localUsageDelivery{
		{BatchID: "legacy-unknown", Rows: []localUsageDeliveryRow{{Raw: 1}}},
		{BatchID: fmt.Sprintf("combined-%d", old.Add(-time.Minute).UnixNano()), Rows: []localUsageDeliveryRow{{Raw: 0}}},
		{BatchID: fmt.Sprintf("merged-%d", newer.UnixNano()), Rows: []localUsageDeliveryRow{{Raw: 2}}},
		{BatchID: fmt.Sprintf("combined-%d", old.UnixNano()), Rows: []localUsageDeliveryRow{{Raw: 3}}},
	}
	health := s.accountingHealth()
	if health.GetOldestReflectionBatchUnix() != old.Unix() || health.GetAwaitingReflectionBatches() != 4 {
		t.Fatalf("oldest reflection batch not reported: %#v", health)
	}
	if s.localUsageDeliveries[0].BatchID != "legacy-unknown" {
		t.Fatal("health check modified the durable reflection ledger")
	}
}

func TestAccountingHealthSaturatesOverflowWithoutChangingDurableLedger(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	s.xrayUsageLoaded = true
	s.xrayUsagePending = &xrayUsagePendingBatch{BatchID: "pending", Samples: []xrayUsageSample{{Value: ^uint64(0)}, {Value: 1}}}
	s.localUsageLoaded = true
	s.localUsageDeliveries = []localUsageDelivery{{BatchID: "root", Rows: []localUsageDeliveryRow{{Raw: ^uint64(0)}, {Raw: 1}}}}
	health := s.accountingHealth()
	if !health.GetReflectionBytesOverflow() || health.GetAwaitingReflectionBytes() != ^uint64(0) {
		t.Fatalf("reflection overflow was hidden: %#v", health)
	}
	if !health.GetProtocols()[0].GetPendingBytesOverflow() || health.GetProtocols()[0].GetPendingBytes() != ^uint64(0) {
		t.Fatalf("pending overflow was hidden: %#v", health.GetProtocols()[0])
	}
	if s.localUsageDeliveries[0].Rows[1].Raw != 1 || s.xrayUsagePending.Samples[1].Value != 1 {
		t.Fatal("read-only health changed accounting evidence")
	}
}
