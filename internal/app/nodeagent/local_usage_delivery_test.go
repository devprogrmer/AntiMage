package nodeagent

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestLocalDeliveryReflectionLostACKRestartAndCompaction(t *testing.T) {
	s := &Server{cfg: Config{DataDir: t.TempDir()}}
	deliveries := []localUsageDelivery{
		{BatchID: "combined-old", Rows: []localUsageDeliveryRow{{Protocol: "xray", ChildID: "xray-old", UserID: 10, InboundTag: "a", Raw: 100}, {Protocol: "wireguard", ChildID: "wireguard-old", UserID: 11, InboundTag: "b", Raw: 200}}},
		{BatchID: "combined-new", Rows: []localUsageDeliveryRow{{Protocol: "xray", ChildID: "xray-new", UserID: 10, InboundTag: "a", Raw: 50}}},
	}
	if err := s.persistLocalUsageDeliveriesLocked(deliveries); err != nil {
		t.Fatal(err)
	}
	// Pending already contributes raw usage; its prepared receipt must not add it again.
	if got, err := s.localAwaitingReflectionUsage("xray", 10, "a", "xray-new", ""); err != nil || got != 100 {
		t.Fatalf("got=%d err=%v", got, err)
	}
	reflected, err := s.localPendingUsageReflected("xray", 10, "xray-new", "combined-new")
	if err != nil || !reflected {
		t.Fatalf("lost ACK reflection=%v err=%v", reflected, err)
	}
	if got, err := s.localAwaitingReflectionUsage("xray", 10, "a", "xray-new", "combined-new"); err != nil || got != 0 {
		t.Fatalf("got=%d err=%v", got, err)
	}
	restarted := &Server{cfg: s.cfg}
	reflected, err = restarted.localPendingUsageReflected("xray", 10, "xray-new", "combined-new")
	if err != nil || !reflected {
		t.Fatal("restart lost reflected pending identity")
	}
	if got, err := restarted.localAwaitingReflectionUsage("wireguard", 11, "b", "", ""); err != nil || got != 200 {
		t.Fatalf("another user incorrectly pruned: %d %v", got, err)
	}
	if _, err := restarted.localAwaitingReflectionUsage("xray", 10, "a", "", "combined-new"); err != nil {
		t.Fatal(err)
	}
	if len(restarted.localUsageDeliveries) != 1 || restarted.localUsageDeliveries[0].Rows[0].UserID != 11 {
		t.Fatal("reflected rows not safely compacted")
	}
	if restarted.localUsageDeliveryIndex != nil {
		t.Fatal("reflection compaction kept a stale derived index")
	}
}

func TestLocalDeliveryCorruptionPreserved(t *testing.T) {
	s := &Server{cfg: Config{DataDir: t.TempDir()}}
	path := filepath.Join(s.cfg.DataDir, "accounting", "deliveries.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.localAwaitingReflectionUsage("xray", 10, "a", "", ""); err == nil {
		t.Fatal("corruption silently ignored")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "corrupt" {
		t.Fatal("corrupt evidence overwritten")
	}
}

func TestFullLocalDeliveryRecoversFromPersistedReflection(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	s := &Server{cfg: cfg}
	deliveries := make([]localUsageDelivery, 0, maxLocalUsageDeliveries)
	for i := 0; i < maxLocalUsageDeliveries-2; i++ {
		id := fmt.Sprintf("xray-old-%d", i)
		deliveries = append(deliveries, localUsageDelivery{BatchID: id, Rows: []localUsageDeliveryRow{
			{Protocol: "xray", ChildID: id, UserID: 10, InboundTag: "a", Raw: 100},
		}})
	}
	// This batch contains unconfirmed usage, so the whole batch must survive.
	deliveries = append(deliveries, localUsageDelivery{BatchID: "xray-unconfirmed", Rows: []localUsageDeliveryRow{
		{Protocol: "xray", ChildID: "xray-unconfirmed", UserID: 11, InboundTag: "b", Raw: 200},
	}})
	deliveries = append(deliveries, localUsageDelivery{BatchID: "xray-marker", Rows: []localUsageDeliveryRow{
		{Protocol: "xray", ChildID: "xray-marker", UserID: 10, InboundTag: "a", Raw: 50},
	}})
	if err := s.persistLocalUsageDeliveriesLocked(deliveries); err != nil {
		t.Fatal(err)
	}
	s.xrayUsagePending = &xrayUsagePendingBatch{
		BatchID: "xray-pending",
		Samples: []xrayUsageSample{{UserID: 10, InboundTag: "a", Value: 75}},
	}
	if err := s.persistXrayUsageStateLocked(); err != nil {
		t.Fatal(err)
	}

	s = &Server{cfg: cfg}
	if err := s.recordLocalUsageDelivery("xray-pending"); err == nil {
		t.Fatal("accepted another delivery without proof of reflection")
	}
	if len(s.localUsageDeliveries) != maxLocalUsageDeliveries {
		t.Fatal("changed the ledger without proof of reflection")
	}
	if err := s.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{
		ConfigJson:    "{}",
		OvRuntimeJson: `{"xray_policies":[{"user_id":10,"email":"10.a","reflected_usage_batch_id":"xray-marker"}]}`,
	}); err != nil {
		t.Fatal(err)
	}

	s = &Server{cfg: cfg}
	if err := s.recordLocalUsageDelivery("xray-pending"); err != nil {
		t.Fatal(err)
	}
	s = &Server{cfg: cfg}
	if err := s.recordLocalUsageDelivery("xray-pending"); err != nil {
		t.Fatal(err)
	}
	got := s.localUsageDeliveries
	if len(got) != 3 || got[0].BatchID != "xray-unconfirmed" ||
		got[1].BatchID != "xray-marker" || got[2].BatchID != "xray-pending" {
		t.Fatalf("incorrect ledger after restart and retry: %+v", got)
	}
	if reflected, err := s.localPendingUsageReflected("xray", 10, "xray-marker", "xray-marker"); !reflected || err != nil {
		t.Fatalf("lost the retained ACK identity: reflected=%v err=%v", reflected, err)
	}
	if usage, err := s.localAwaitingReflectionUsage("xray", 11, "b", "", ""); usage != 200 || err != nil {
		t.Fatalf("unconfirmed usage=%d err=%v", usage, err)
	}
}
