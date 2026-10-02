package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/protobuf/proto"
)

func TestAmneziaWGQuotaTickAggregateAndDiskWriteCounts(t *testing.T) {
	s, cfg, _, removed := awgOfflineFixture(t)
	configs := []amneziaWGUsageRuntimeConfig{*cfg, *cfg}
	for iface := range configs {
		configs[iface].InterfaceName = fmt.Sprintf("awg%d", iface)
		configs[iface].InboundTag = fmt.Sprintf("inbound%d", iface)
		configs[iface].Peers = map[string]int64{}
		configs[iface].Policies = map[string]nativeSessionUserPolicy{}
		for peer := 0; peer < 32; peer++ {
			key := fmt.Sprintf("peer%d", peer)
			configs[iface].Peers[key] = int64(iface*32 + peer + 1)
			configs[iface].Policies[key] = nativeSessionUserPolicy{Status: "active", DataLimit: 50}
		}
	}
	writeAWGUsageConfig(t, s.cfg.DataDir, configs[0])
	dir := filepath.Join(s.cfg.DataDir, "amneziawg", "runtime", "second")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(configs[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "usage-helper.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	oldWrite := amneziaWGOfflineWrite
	t.Cleanup(func() { amneziaWGOfflineWrite = oldWrite })
	writes, snapshots, identities := 0, 0, 0
	amneziaWGOfflineWrite = func(path string, raw []byte) error { writes++; return oldWrite(path, raw) }
	amneziaWGInterfaceIdentity = func(name string) (string, error) { identities++; return "boot:" + name, nil }
	amneziaWGSnapshot = func(string) ([]wireGuardPeerCounters, error) { t.Fatal("per-interface fallback used"); return nil, nil }
	value := uint64(0)
	removeHook := amneziaWGRemovePeer
	amneziaWGRemovePeer = func(iface, key string) error {
		if writes != 2 {
			t.Fatalf("removal before its durable checkpoint: writes=%d", writes)
		}
		state, err := s.readAmneziaWGOfflineState()
		if err != nil {
			t.Fatal(err)
		}
		for _, counter := range state.Counters {
			if counter.Total != 50 {
				t.Fatal("removal preceded fresh snapshot persistence")
			}
		}
		return removeHook(iface, key)
	}
	amneziaWGSnapshotAll = func(_ context.Context, names []string) (map[string][]wireGuardPeerCounters, error) {
		snapshots++
		if len(names) != 2 {
			t.Fatalf("aggregate interface list %v", names)
		}
		result := map[string][]wireGuardPeerCounters{}
		for _, cfg := range configs {
			for key := range cfg.Peers {
				result[cfg.InterfaceName] = append(result[cfg.InterfaceName], wireGuardPeerCounters{PublicKey: key, ReceivedBytes: value})
			}
		}
		return result, nil
	}
	for tick := 1; tick <= 10; tick++ {
		value = uint64(tick)
		if err := s.quotaCheckAmneziaWGOffline(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if writes != 0 || snapshots != 10 || identities != 20 || *removed != 0 {
		t.Fatalf("normal quota: writes=%d snapshots=%d identities=%d removals=%d", writes, snapshots, identities, *removed)
	}
	if _, err := os.Stat(filepath.Join(s.cfg.DataDir, "amneziawg", "offline-accounting.json")); !os.IsNotExist(err) {
		t.Fatalf("normal quota created checkpoint: %v", err)
	}
	if err := s.checkpointAmneziaWGOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	if writes != 1 || snapshots != 11 {
		t.Fatalf("periodic checkpoint writes=%d snapshots=%d", writes, snapshots)
	}
	value = 25
	if err := s.quotaCheckAmneziaWGOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := s.readAmneziaWGOfflineState()
	if err != nil {
		t.Fatal(err)
	}
	for _, counter := range state.Counters {
		if counter.Total != 10 {
			t.Fatal("normal quota overwrote durable checkpoint")
		}
	}
	value = 50
	if err := s.quotaCheckAmneziaWGOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	if writes != 2 || snapshots != 13 || identities != 26 || *removed != 64 {
		t.Fatalf("quota removal: writes=%d snapshots=%d identities=%d removals=%d", writes, snapshots, identities, *removed)
	}
	state, err = s.readAmneziaWGOfflineState()
	if err != nil {
		t.Fatal(err)
	}
	for _, counter := range state.Counters {
		if counter.Total != 50 {
			t.Fatal("pre-removal checkpoint omitted live bytes")
		}
	}
}

func TestAmneziaWGQuotaRemovalWriteFailure(t *testing.T) {
	s, cfg, value, removed := awgOfflineFixture(t)
	cfg.Policies["key"] = nativeSessionUserPolicy{Status: "disabled"}
	writeAWGUsageConfig(t, s.cfg.DataDir, *cfg)
	*value = 100
	oldWrite := amneziaWGOfflineWrite
	t.Cleanup(func() { amneziaWGOfflineWrite = oldWrite })
	amneziaWGOfflineWrite = func(string, []byte) error { return errors.New("checkpoint unavailable") }
	if err := s.quotaCheckAmneziaWGOffline(context.Background()); err == nil {
		t.Fatal("write error hidden")
	}
	if *removed != 0 {
		t.Fatal("removed before successful durable write")
	}
}

func TestAmneziaWGRootDeliveryCreditPendingACKReflectionReload(t *testing.T) {
	s, cfg, value, removed := awgOfflineFixture(t)
	amneziaWGAwaitingReflectionUsage = func(s *Server, uid int64, tag, pending, marker string) (uint64, error) {
		return s.localAwaitingReflectionUsage("amneziawg", uid, tag, pending, marker)
	}
	cfg.Policies["key"] = nativeSessionUserPolicy{Status: "active", DataLimit: 450_000_000, UsageCoefficient: 2, InboundCoefficient: 1.5}
	writeAWGUsageConfig(t, s.cfg.DataDir, *cfg)
	*value = 100_000_000
	first := awgCollectValue(t, s)
	s.combinedUsageLoaded = true
	s.combinedUsagePending = &combinedUsagePendingBatch{BatchID: "combined-awg-root", AmneziaWGBatchID: first.BatchId}
	if err := s.persistCombinedUsageStateLocked(); err != nil {
		t.Fatal(err)
	}
	if err := s.recordLocalUsageDelivery("combined-awg-root"); err != nil {
		t.Fatal(err)
	}
	*value = 120_000_000
	retry := awgCollectValue(t, s)
	if !proto.Equal(first, retry) {
		t.Fatalf("immutable pending changed: %v %v", first, retry)
	}
	if *removed != 0 {
		t.Fatal("pre-send receipt duplicated pending bytes")
	}
	ack, err := s.AckUserUsage(context.Background(), &nodev1.AckUsageRequest{BatchId: "combined-awg-root"})
	if err != nil || !ack.Acknowledged {
		t.Fatalf("root ACK: %v %v", ack, err)
	}
	s = New(Config{DataDir: s.cfg.DataDir})
	if err := s.quotaCheckAmneziaWGOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *removed != 0 {
		t.Fatal("ACK credit duplicated raw totals")
	}
	credit, err := s.localAwaitingReflectionUsage("amneziawg", 7, "awg", "", "")
	if err != nil || credit != 100_000_000 {
		t.Fatalf("reloaded credit %d %v", credit, err)
	}
	policy := cfg.Policies["key"]
	policy.UsedTraffic = 300_000_000
	policy.ReflectedUsageBatchID = "combined-awg-root"
	cfg.Policies["key"] = policy
	writeAWGUsageConfig(t, s.cfg.DataDir, *cfg)
	*value = 140_000_000
	if err := s.quotaCheckAmneziaWGOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *removed != 0 {
		t.Fatal("reflected ACK bytes charged again")
	}
	credit, err = s.localAwaitingReflectionUsage("amneziawg", 7, "awg", "", "")
	if err != nil || credit != 0 {
		t.Fatalf("credit not pruned %d %v", credit, err)
	}
	*value = 150_000_000
	if err := s.quotaCheckAmneziaWGOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *removed != 1 {
		t.Fatal("new durable bytes omitted from quota")
	}
}

func TestAmneziaWGReflectedPendingLostACKKeepsNewerQuota(t *testing.T) {
	s, cfg, value, removed := awgOfflineFixture(t)
	amneziaWGAwaitingReflectionUsage = func(s *Server, uid int64, tag, pending, marker string) (uint64, error) {
		return s.localAwaitingReflectionUsage("amneziawg", uid, tag, pending, marker)
	}
	cfg.Policies["key"] = nativeSessionUserPolicy{Status: "active", DataLimit: 450_000_000, UsageCoefficient: 2, InboundCoefficient: 1.5}
	writeAWGUsageConfig(t, s.cfg.DataDir, *cfg)
	*value = 100_000_000
	first := awgCollectValue(t, s)
	s.combinedUsageLoaded = true
	s.combinedUsagePending = &combinedUsagePendingBatch{BatchID: "combined-lost-ack", AmneziaWGBatchID: first.BatchId}
	if err := s.persistCombinedUsageStateLocked(); err != nil {
		t.Fatal(err)
	}
	if err := s.recordLocalUsageDelivery("combined-lost-ack"); err != nil {
		t.Fatal(err)
	}
	policy := cfg.Policies["key"]
	policy.UsedTraffic = 300_000_000
	policy.ReflectedUsageBatchID = "combined-lost-ack"
	cfg.Policies["key"] = policy
	writeAWGUsageConfig(t, s.cfg.DataDir, *cfg)
	*value = 140_000_000
	s = New(Config{DataDir: s.cfg.DataDir})
	retry := awgCollectValue(t, s)
	if !proto.Equal(first, retry) {
		t.Fatal("reflection mutated immutable pending retry")
	}
	if *removed != 0 {
		t.Fatal("reflected pending bytes counted twice before ACK")
	}
	// The generic helper pruned raw credit but retained root/child identity.
	if err := s.quotaCheckAmneziaWGOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *removed != 0 {
		t.Fatal("pruning erased pending reflection identity")
	}
	s = New(Config{DataDir: s.cfg.DataDir})
	*value = 150_000_000
	if err := s.quotaCheckAmneziaWGOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *removed != 1 {
		t.Fatal("newer durable delta excluded with reflected pending")
	}
}

func TestAmneziaWGLegacyPendingQuotaRetainsHistoricalRaw(t *testing.T) {
	s, _, _, _ := awgOfflineFixture(t)
	s.amneziaWGUsagePending = &amneziaWGUsagePendingBatch{BatchID: "amneziawg-legacy", Samples: []amneziaWGUsageSample{{UserID: 7, InboundTag: "awg", Value: 100}}, NextBaseline: map[string]uint64{"removed": 100}}
	state := amneziaWGOfflineState{Counters: map[string]amneziaWGOfflineCounter{"new": {UserID: 7, InboundTag: "awg", Total: 40}}}
	raw, err := s.amneziaWGUnreflectedRawLocked(state, 7, "awg")
	if err != nil || raw != 140 {
		t.Fatalf("historical pending plus new delta %d %v", raw, err)
	}
}

func TestAmneziaWGNativeIdentityResetOverOld(t *testing.T) {
	s, _, value, _ := awgOfflineFixture(t)
	*value = 100
	if err := s.checkpointAmneziaWGOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	amneziaWGInterfaceIdentity = func(string) (string, error) { return "new-boot:2", nil }
	*value = 150
	b := awgCollectValue(t, New(Config{DataDir: s.cfg.DataDir}))
	if len(b.Stats) != 1 || b.Stats[0].Value != 250 {
		t.Fatalf("identity reset lost traffic: %v", b)
	}
}

func TestAmneziaWGSessionRemovalCheckpointsLateBytesAndRejectsStaleGeneration(t *testing.T) {
	s, cfg, value, removed := awgOfflineFixture(t)
	*value = 100
	first := awgCollectValue(t, s)
	*value = 150
	peer := amneziaWGSessionPeer{interfaceName: "awg0", publicKey: "key", generation: cfg.Generation + "\x00"}
	if err := s.removeAmneziaWGSessionPeerWithCheckpoint("awg", peer); err != nil {
		t.Fatal(err)
	}
	state, err := s.readAmneziaWGOfflineState()
	if err != nil || state.Counters["awg\x00awg0\x00key"].Total != 150 || *removed != 1 {
		t.Fatalf("late bytes/removal %v %v %d", state, err, *removed)
	}
	if got := awgCollectValue(t, s); !proto.Equal(first, got) {
		t.Fatal("late removal changed pending batch")
	}
	cfg.Generation = "replacement"
	writeAWGUsageConfig(t, s.cfg.DataDir, *cfg)
	*value = 10
	if err := s.removeAmneziaWGSessionPeerWithCheckpoint("awg", peer); err != nil {
		t.Fatal(err)
	}
	if *removed != 1 {
		t.Fatal("stale session callback removed a new peer generation")
	}
}

func TestAmneziaWGLegacyBaselineMigration(t *testing.T) {
	s, _, value, _ := awgOfflineFixture(t)
	raw, err := json.Marshal(amneziaWGUsageDiskState{Baseline: map[string]uint64{"awg\x00awg0\x00key": 100}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.amneziaWGUsageStatePath(), raw, 0600); err != nil {
		t.Fatal(err)
	}
	*value = 140
	b := awgCollectValue(t, s)
	if len(b.Stats) != 1 || b.Stats[0].Value != 40 {
		t.Fatalf("migration double counted: %v", b)
	}
}

func TestAmneziaWGACKDoesNotFreeOfflineQuota(t *testing.T) {
	s, cfg, value, removed := awgOfflineFixture(t)
	cfg.Policies["key"] = nativeSessionUserPolicy{Status: "active", DataLimit: 300_000_000, UsageCoefficient: 2, InboundCoefficient: 1.5}
	writeAWGUsageConfig(t, s.cfg.DataDir, *cfg)
	*value = 90_000_000
	b := awgCollectValue(t, s)
	if *removed != 0 {
		t.Fatal("removed below quota")
	}
	if _, err := s.ackAmneziaWGUserUsage(context.Background(), &nodev1.AckUsageRequest{BatchId: b.BatchId}); err != nil {
		t.Fatal(err)
	}
	amneziaWGAwaitingReflectionUsage = func(*Server, int64, string, string, string) (uint64, error) { return 90_000_000, nil }
	*value = 100_000_000
	if err := s.quotaCheckAmneziaWGOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *removed != 1 {
		t.Fatal("ACK incorrectly freed quota")
	}
	state, err := s.readAmneziaWGOfflineState()
	if err != nil || state.Counters["awg\x00awg0\x00key"].Total != 100_000_000 {
		t.Fatalf("raw totals scaled or lost: %v %v", state, err)
	}
}

func TestAmneziaWGAggregateAbsenceAndDirectionalReset(t *testing.T) {
	s, _, _, _ := awgOfflineFixture(t)
	peers := []wireGuardPeerCounters{{PublicKey: "key", ReceivedBytes: 100, SentBytes: 100}}
	calls := 0
	amneziaWGSnapshotAll = func(ctx context.Context, names []string) (map[string][]wireGuardPeerCounters, error) {
		calls++
		if len(names) != 1 || names[0] != "awg0" {
			t.Fatalf("names %v", names)
		}
		return map[string][]wireGuardPeerCounters{"awg0": peers}, nil
	}
	if err := s.checkpointAmneziaWGOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	peers = []wireGuardPeerCounters{{PublicKey: "key", ReceivedBytes: 10, SentBytes: 110}}
	if err := s.checkpointAmneziaWGOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	peers = nil
	if err := s.checkpointAmneziaWGOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	peers = []wireGuardPeerCounters{{PublicKey: "key", ReceivedBytes: 150}}
	b := awgCollectValue(t, New(Config{DataDir: s.cfg.DataDir}))
	if len(b.Stats) != 1 || b.Stats[0].Value != 370 || calls != 4 {
		t.Fatalf("reset/aggregate: %v calls=%d", b, calls)
	}
}

func awgOfflineFixture(t *testing.T) (*Server, *amneziaWGUsageRuntimeConfig, *uint64, *int) {
	t.Helper()
	dir := t.TempDir()
	cfg := &amneziaWGUsageRuntimeConfig{InboundTag: "awg", InterfaceName: "awg0", Generation: "first", Peers: map[string]int64{"key": 7}, AccountingEnabled: true, Policies: map[string]nativeSessionUserPolicy{"key": {Status: "active"}}}
	writeAWGUsageConfig(t, dir, *cfg)
	value := new(uint64)
	removed := new(int)
	oldSnapshot, oldAll, oldRemove := amneziaWGSnapshot, amneziaWGSnapshotAll, amneziaWGRemovePeer
	oldCredit, oldIdentity := amneziaWGAwaitingReflectionUsage, amneziaWGInterfaceIdentity
	amneziaWGAwaitingReflectionUsage = func(*Server, int64, string, string, string) (uint64, error) { return 0, nil }
	amneziaWGInterfaceIdentity = func(string) (string, error) { return "fixture-boot:1", nil }
	amneziaWGSnapshotAll = nil
	amneziaWGSnapshot = func(string) ([]wireGuardPeerCounters, error) {
		return []wireGuardPeerCounters{{PublicKey: "key", ReceivedBytes: *value}}, nil
	}
	amneziaWGRemovePeer = func(string, string) error {
		if _, err := os.Stat(filepath.Join(dir, "amneziawg", "offline-accounting.json")); err != nil {
			t.Fatal("removal before durable checkpoint")
		}
		*removed++
		return nil
	}
	t.Cleanup(func() {
		amneziaWGSnapshot, amneziaWGSnapshotAll, amneziaWGRemovePeer = oldSnapshot, oldAll, oldRemove
		amneziaWGAwaitingReflectionUsage, amneziaWGInterfaceIdentity = oldCredit, oldIdentity
	})
	return New(Config{DataDir: dir}), cfg, value, removed
}

func awgCollectValue(t *testing.T, s *Server) *nodev1.UserUsageBatch {
	t.Helper()
	b, err := s.collectAmneziaWGUserUsage(context.Background(), &nodev1.CollectUsageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAmneziaWGOfflineGenerationReload(t *testing.T) {
	s, cfg, value, _ := awgOfflineFixture(t)
	*value = 100
	if err := s.checkpointAmneziaWGOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg.Generation = "second"
	writeAWGUsageConfig(t, s.cfg.DataDir, *cfg)
	*value = 150 // A reset may already have passed the old native value.
	if err := s.checkpointAmneziaWGOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := awgCollectValue(t, New(Config{DataDir: s.cfg.DataDir}))
	if len(b.Stats) != 1 || b.Stats[0].Value != 250 {
		t.Fatalf("want A+B=250, got %v", b)
	}
}

func TestAmneziaWGPendingSamplingACKLostAndReload(t *testing.T) {
	s, _, value, _ := awgOfflineFixture(t)
	*value = 100
	first := awgCollectValue(t, s)
	*value = 140
	retry := awgCollectValue(t, s)
	if retry.BatchId != first.BatchId || retry.Stats[0].Value != 100 {
		t.Fatalf("changed retry: %v", retry)
	}
	s = New(Config{DataDir: s.cfg.DataDir})
	retry = awgCollectValue(t, s)
	if retry.BatchId != first.BatchId || retry.Stats[0].Value != 100 {
		t.Fatalf("restart changed retry: %v", retry)
	}
	*value = 160
	if err := s.checkpointAmneziaWGOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		ack, err := s.ackAmneziaWGUserUsage(context.Background(), &nodev1.AckUsageRequest{BatchId: first.BatchId})
		if err != nil || !ack.Acknowledged {
			t.Fatalf("ack %v %v", ack, err)
		}
	}
	b := awgCollectValue(t, New(Config{DataDir: s.cfg.DataDir}))
	if len(b.Stats) != 1 || b.Stats[0].Value != 60 {
		t.Fatalf("ACK erased newer bytes: %v", b)
	}
}

func TestAmneziaWGOfflineQuotaAndFactors(t *testing.T) {
	for _, test := range []struct {
		name   string
		raw    uint64
		policy nativeSessionUserPolicy
	}{
		{"50MB", 50_000_000, nativeSessionUserPolicy{Status: "active", DataLimit: 50_000_000}},
		{"factors", 100_000_000, nativeSessionUserPolicy{Status: "active", DataLimit: 300_000_000, UsageCoefficient: 2, InboundCoefficient: 1.5}},
		{"denied", 1, nativeSessionUserPolicy{Status: "disabled"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, cfg, value, removed := awgOfflineFixture(t)
			cfg.Policies["key"] = test.policy
			writeAWGUsageConfig(t, s.cfg.DataDir, *cfg)
			*value = test.raw
			s = New(Config{DataDir: s.cfg.DataDir})
			if err := s.quotaCheckAmneziaWGOffline(context.Background()); err != nil {
				t.Fatal(err)
			}
			if *removed != 1 {
				t.Fatalf("removed=%d", *removed)
			}
			b := awgCollectValue(t, s)
			if len(b.Stats) != 1 || b.Stats[0].Value != test.raw {
				t.Fatalf("must report raw bytes: %v", b)
			}
		})
	}
}

func TestAmneziaWGCheckpointFailurePreventsRemoval(t *testing.T) {
	s, cfg, value, removed := awgOfflineFixture(t)
	*value = 100
	cfg.Policies["key"] = nativeSessionUserPolicy{Status: "disabled"}
	writeAWGUsageConfig(t, s.cfg.DataDir, *cfg)
	path := filepath.Join(s.cfg.DataDir, "amneziawg", "offline-accounting.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.quotaCheckAmneziaWGOffline(context.Background()); err == nil {
		t.Fatal("expected failure")
	}
	if *removed != 0 {
		t.Fatal("removed despite failed checkpoint")
	}
}

func TestAmneziaWGPolicyReflectionAndSnapshotFailure(t *testing.T) {
	s, cfg, value, removed := awgOfflineFixture(t)
	*value = 100
	if err := s.checkpointAmneziaWGOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := awgCollectValue(t, s)
	if _, err := s.ackAmneziaWGUserUsage(context.Background(), &nodev1.AckUsageRequest{BatchId: b.BatchId}); err != nil {
		t.Fatal(err)
	}
	cfg.Policies["key"] = nativeSessionUserPolicy{Status: "active", UsedTraffic: 100, DataLimit: 150, ReflectedUsageBatchID: "root-reflected"}
	amneziaWGAwaitingReflectionUsage = func(_ *Server, uid int64, tag, pending, marker string) (uint64, error) {
		if uid != 7 || tag != "awg" || pending != "" || marker != "root-reflected" {
			t.Fatalf("wrong reflection inputs: %d %s %s %s", uid, tag, pending, marker)
		}
		return 0, nil
	}
	writeAWGUsageConfig(t, s.cfg.DataDir, *cfg)
	if err := s.quotaCheckAmneziaWGOffline(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *removed != 0 {
		t.Fatal("reflected usage double counted")
	}
	amneziaWGSnapshot = func(string) ([]wireGuardPeerCounters, error) { return nil, errors.New("unavailable") }
	if err := s.checkpointAmneziaWGOffline(context.Background()); err == nil {
		t.Fatal("snapshot error hidden")
	}
	state, err := s.readAmneziaWGOfflineState()
	if err != nil || state.Counters["awg\x00awg0\x00key"].Total != 100 {
		t.Fatalf("lost checkpoint: %v %v", state, err)
	}
}
