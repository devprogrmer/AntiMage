package nodeagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestAmneziaWGPresenceCallbackDoesNotBlockAccounting(t *testing.T) {
	callbackStarted := make(chan struct{}, 1)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callbackStarted <- struct{}{}
		time.Sleep(750 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer httpServer.Close()

	dataDir := t.TempDir()
	key := awgTestKey('n')
	writeAWGUsageConfig(t, dataDir, amneziaWGUsageRuntimeConfig{
		InboundTag: "awg-main", InterfaceName: "awg0",
		Peers: map[string]int64{key: 7}, PeerAddresses: map[string]string{key: "10.72.0.2"},
		Policies: map[string]nativeSessionUserPolicy{key: {Status: "active"}}, AccountingEnabled: true,
		Callback: nativeRuntimeSessionCallback{URL: httpServer.URL, NodeID: 9},
	})

	oldSnapshot := amneziaWGSnapshot
	defer func() { amneziaWGSnapshot = oldSnapshot }()
	amneziaWGSnapshot = func(string) ([]wireGuardPeerCounters, error) {
		return []wireGuardPeerCounters{{PublicKey: key, Endpoint: "198.51.100.7:321", LatestHandshake: time.Now().Unix(), ReceivedBytes: 50}}, nil
	}

	started := time.Now()
	batch, err := New(Config{DataDir: dataDir}).collectAmneziaWGUserUsage(context.Background(), &nodev1.CollectUsageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("accounting waited for presence callback: %s", elapsed)
	}
	if len(batch.Stats) == 0 || batch.Stats[0].Value != 50 {
		t.Fatalf("usage batch = %#v", batch)
	}
	select {
	case <-callbackStarted:
	case <-time.After(time.Second):
		t.Fatal("presence callback was not dispatched")
	}
}

func writeAWGUsageConfig(t *testing.T, dataDir string, cfg amneziaWGUsageRuntimeConfig) {
	t.Helper()
	dir := filepath.Join(dataDir, "amneziawg", "runtime", "test")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(dir, "usage-helper.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAmneziaWGUsageDeltaACKRestartOnlineAndQuota(t *testing.T) {
	dataDir := t.TempDir()
	key := awgTestKey('p')
	writeAWGUsageConfig(t, dataDir, amneziaWGUsageRuntimeConfig{InboundTag: "awg-main", InterfaceName: "awg0", Peers: map[string]int64{key: 7}, PeerAddresses: map[string]string{key: "10.72.0.2"}, Policies: map[string]nativeSessionUserPolicy{key: {Status: "active", UsedTraffic: 90, DataLimit: 100}}, AccountingEnabled: true})
	oldSnapshot, oldRemove := amneziaWGSnapshot, amneziaWGRemovePeer
	defer func() { amneziaWGSnapshot = oldSnapshot; amneziaWGRemovePeer = oldRemove }()
	counter := uint64(100)
	removed := false
	amneziaWGSnapshot = func(string) ([]wireGuardPeerCounters, error) {
		return []wireGuardPeerCounters{{PublicKey: key, Endpoint: "198.51.100.7:321", LatestHandshake: time.Now().Unix(), ReceivedBytes: counter}}, nil
	}
	amneziaWGRemovePeer = func(_ string, got string) error { removed = got == key; return nil }
	s := New(Config{DataDir: dataDir})
	first, err := s.collectAmneziaWGUserUsage(context.Background(), &nodev1.CollectUsageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Stats) < 2 || first.Stats[0].Value != 100 || len(first.OnlineIps) != 1 {
		t.Fatalf("first batch=%#v", first)
	}
	if !removed {
		t.Fatal("quota-reaching peer was not removed immediately")
	}
	retry, _ := s.collectAmneziaWGUserUsage(context.Background(), &nodev1.CollectUsageRequest{})
	if retry.BatchId != first.BatchId || retry.Stats[0].Value != 100 {
		t.Fatalf("pending batch was not stable: %#v", retry)
	}
	ack, err := s.AckUserUsage(context.Background(), &nodev1.AckUsageRequest{BatchId: first.BatchId})
	if err != nil || !ack.Acknowledged {
		t.Fatalf("ack=%#v err=%v", ack, err)
	}
	counter = 140
	restarted := New(Config{DataDir: dataDir})
	second, err := restarted.collectAmneziaWGUserUsage(context.Background(), &nodev1.CollectUsageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Stats) == 0 || second.Stats[0].Value != 40 {
		t.Fatalf("restart-safe delta=%#v, want 40", second.Stats)
	}
}
