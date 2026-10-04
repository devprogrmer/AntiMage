package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/protobuf/proto"
)

// The native-vpn-drivers workflow invokes each action in a new test process
// while a real kernel WireGuard peer is exchanging traffic.
func TestWireGuardNativeAccountingStage(t *testing.T) {
	dir := strings.TrimSpace(os.Getenv("ANTIMAGE_WIREGUARD_NATIVE_STATE"))
	pubkey := strings.TrimSpace(os.Getenv("ANTIMAGE_WIREGUARD_NATIVE_PEER"))
	iface := strings.TrimSpace(os.Getenv("ANTIMAGE_WIREGUARD_NATIVE_INTERFACE"))
	if dir == "" || pubkey == "" || iface == "" {
		t.Skip("requires isolated native WireGuard harness")
	}
	if !wireGuardInterfaceNamePattern.MatchString(iface) {
		t.Fatalf("invalid native interface %q", iface)
	}
	root := filepath.Join(dir, "wireguard", "inbounds", "native")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	accounting := true
	cfg := wireGuardUsageRuntimeConfig{
		InboundTag: "native", InterfaceName: iface,
		Peers:             map[string]int64{pubkey: 7},
		AccountingEnabled: &accounting,
		Policies: map[string]nativeSessionUserPolicy{
			pubkey: {Status: "active", DataLimit: 1 << 20},
		},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAtomicMode(filepath.Join(root, "usage-helper.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}

	s := New(Config{DataDir: dir})
	action := strings.ToLower(strings.TrimSpace(os.Getenv("ANTIMAGE_WIREGUARD_ACTION")))
	switch action {
	case "quota":
		if err := s.wireGuardOfflineTick(context.Background(), true, true); err != nil {
			t.Fatal(err)
		}
		path, err := exec.LookPath("wg")
		if err != nil {
			t.Fatal(err)
		}
		peers, err := exec.Command(path, "show", iface, "peers").Output()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(peers), pubkey) {
			counters, _ := wireGuardDumpInterface(context.Background(), iface)
			t.Fatalf("offline quota did not remove native peer; counters=%s", counters)
		}
		t.Log("production WireGuard offline quota removed the over-limit native peer after checkpoint")
	case "ack":
		s.wireGuardUsageMu.Lock()
		if err := s.ensureWireGuardUsageStateLoadedLocked(); err != nil {
			s.wireGuardUsageMu.Unlock()
			t.Fatal(err)
		}
		pending := s.wireGuardUsagePending
		s.wireGuardUsageMu.Unlock()
		if pending == nil {
			t.Fatal("expected durable pending batch before ACK")
		}
		ack, err := s.ackWireGuardUserUsage(context.Background(), &nodev1.AckUsageRequest{BatchId: pending.BatchID})
		if err != nil || !ack.GetAcknowledged() {
			t.Fatalf("ACK: %v %v", ack, err)
		}
		s = New(Config{DataDir: dir})
		batch, err := s.collectWireGuardUserUsage(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if usage := wireGuardNativeUsageValue(batch); usage == 0 {
			t.Fatalf("post-restart usage was not carried into the next batch: %v", batch)
		}
		t.Logf("ACK pruned the immutable batch; later native-generation traffic remained billable: %d bytes", wireGuardNativeUsageValue(batch))
	case "collect":
		batch, err := s.collectWireGuardUserUsage(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if wireGuardNativeUsageValue(batch) == 0 {
			t.Fatalf("no real kernel peer traffic in collector batch: %v", batch)
		}
		firstPath := filepath.Join(dir, "native-first-batch.pb")
		firstRaw, readErr := os.ReadFile(firstPath)
		if os.IsNotExist(readErr) {
			firstRaw, err = proto.Marshal(batch)
			if err == nil {
				err = os.WriteFile(firstPath, firstRaw, 0600)
			}
		} else if readErr == nil {
			first := &nodev1.UserUsageBatch{}
			if err = proto.Unmarshal(firstRaw, first); err == nil && !proto.Equal(first, batch) {
				t.Fatalf("pending batch changed after native interface restart: old=%v new=%v", first, batch)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("real kernel accounting bytes=%d; durable pending batch=%s", wireGuardNativeUsageValue(batch), batch.GetBatchId())
	default:
		t.Fatal(fmt.Sprintf("unknown native WireGuard action %q", action))
	}
}

func wireGuardNativeUsageValue(batch *nodev1.UserUsageBatch) uint64 {
	for _, sample := range batch.GetStats() {
		if sample.GetUid() == "wireguard:7" {
			return sample.GetValue()
		}
	}
	return 0
}
