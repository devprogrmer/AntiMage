package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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
	action := strings.ToLower(strings.TrimSpace(os.Getenv("ANTIMAGE_WIREGUARD_ACTION")))
	var quotaLimit int64
	if action == "quota" || action == "quota-watch" {
		quotaLimit = wireGuardNativeQuotaLimit(t)
	}
	root := filepath.Join(dir, "wireguard", "inbounds", "native")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	accounting := true
	callback := nativeRuntimeSessionCallback{
		URL:    strings.TrimSpace(os.Getenv("ANTIMAGE_WIREGUARD_SESSION_CALLBACK_URL")),
		Token:  strings.TrimSpace(os.Getenv("ANTIMAGE_WIREGUARD_SESSION_CALLBACK_TOKEN")),
		NodeID: 7,
	}
	if callback.URL != "" && callback.Token == "" {
		t.Fatal("WireGuard Panel session callback token is required with callback URL")
	}
	cfg := wireGuardUsageRuntimeConfig{
		InboundTag: "native", InterfaceName: iface,
		Peers:             map[string]int64{pubkey: 7},
		PeerAddresses:     map[string]string{pubkey: "10.200.0.2"},
		AccountingEnabled: &accounting,
		Callback:          callback,
		Policies: map[string]nativeSessionUserPolicy{
			pubkey: {Status: "active", DataLimit: quotaLimit},
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
	case "quota-watch":
		// Exercise the same serial scheduler used by the node agent while the
		// isolated kernel peer sends traffic. The test exits only after the
		// production quota worker removes that peer.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		initial, err := exec.Command("wg", "show", iface, "peers").Output()
		if err != nil || !strings.Contains(string(initial), pubkey) {
			t.Fatalf("quota watch requires configured native peer: peers=%s err=%v", initial, err)
		}
		t.Logf("watching configured peer %s with limit=%d", pubkey, wireGuardNativeQuotaLimit(t))
		workerDone := make(chan struct{})
		go func() {
			defer close(workerDone)
			s.runLocalAccountingWorker(ctx, "wireguard", 100*time.Millisecond, time.Second,
				func(ctx context.Context) error { return s.wireGuardOfflineTick(ctx, false, true) },
				func(ctx context.Context) error { return s.wireGuardOfflineTick(ctx, true, false) })
		}()
		deadline := time.Now().Add(2 * time.Minute)
		for time.Now().Before(deadline) {
			peers, err := exec.Command("wg", "show", iface, "peers").Output()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(peers), pubkey) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		peers, err := exec.Command("wg", "show", iface, "peers").Output()
		if err != nil || strings.Contains(string(peers), pubkey) {
			t.Fatalf("scheduler did not remove the peer at the quota boundary: peers=%s err=%v", peers, err)
		}
		cancel()
		<-workerDone
		batch, err := s.collectWireGuardUserUsage(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var total uint64
		for _, sample := range batch.GetStats() {
			if sample.GetUid() == "wireguard:7" {
				total = sample.GetValue()
			}
		}
		limit := uint64(wireGuardNativeQuotaLimit(t))
		if total < limit || total-limit >= 1<<20 {
			state, _ := os.ReadFile(s.wireGuardUsageStatePath())
			counters, _ := wireGuardDumpInterface(context.Background(), iface)
			t.Fatalf("native 50 MiB quota overshoot outside 1 MiB bound: total=%d limit=%d batch=%v peer_dump=%s state=%s", total, limit, batch, counters, state)
		}
		t.Logf("production quota scheduler removed real peer at raw=%d bytes, limit=%d, overshoot=%d bytes", total, limit, total-limit)
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
		waitForNativeLifecycleMarker(t)
		t.Logf("real kernel accounting bytes=%d; durable pending batch=%s", wireGuardNativeUsageValue(batch), batch.GetBatchId())
	case "session":
		batch, err := s.collectWireGuardUserUsage(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		waitForNativeLifecycleMarker(t)
		t.Logf("production WireGuard session reconciliation observed native online IPs=%v", batch.GetOnlineIps())
	default:
		t.Fatal(fmt.Sprintf("unknown native WireGuard action %q", action))
	}
}

func waitForNativeLifecycleMarker(t *testing.T) {
	t.Helper()
	path := strings.TrimSpace(os.Getenv("ANTIMAGE_NATIVE_SESSION_EXPECT_MARKER"))
	if path == "" {
		return
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("production native session callback did not create marker %q", path)
}

func wireGuardNativeQuotaLimit(t *testing.T) int64 {
	t.Helper()
	if raw := strings.TrimSpace(os.Getenv("ANTIMAGE_WIREGUARD_NATIVE_QUOTA_BYTES")); raw != "" {
		limit, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || limit <= 0 {
			t.Fatalf("invalid native WireGuard quota %q", raw)
		}
		return limit
	}
	return 1 << 20
}

func wireGuardNativeUsageValue(batch *nodev1.UserUsageBatch) uint64 {
	for _, sample := range batch.GetStats() {
		if sample.GetUid() == "wireguard:7" {
			return sample.GetValue()
		}
	}
	return 0
}
