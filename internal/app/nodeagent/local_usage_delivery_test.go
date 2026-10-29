package nodeagent

import (
	"os"
	"path/filepath"
	"testing"
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
