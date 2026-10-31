package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func wireGuardGenerationFixture(t *testing.T) (*Server, *uint64, *string) {
	t.Helper()
	server := New(Config{DataDir: t.TempDir()})
	value := uint64(10_000_000_000)
	identity := "boot-a:5"
	oldDump, oldIdentity := wireGuardDumpInterface, wireGuardInterfaceIdentity
	wireGuardDumpInterface = func(context.Context, string) ([]byte, error) {
		return []byte(fmt.Sprintf("priv\tpub\t51820\toff\npeer\t(none)\t(none)\t10.0.0.2/32\t0\t%d\t0\t25\n", value)), nil
	}
	wireGuardInterfaceIdentity = func(string) (string, error) { return identity, nil }
	t.Cleanup(func() { wireGuardDumpInterface, wireGuardInterfaceIdentity = oldDump, oldIdentity })
	if err := server.syncWireGuardUsageConfigs([]wireGuardRuntimeInbound{{Tag: "wg", ListenPort: 51820, Settings: map[string]any{"interface_name": "wg-test"}, Peers: []wireGuardRuntimePeer{{UserID: 42, PublicKey: "peer", Status: "active", IPLimit: 2}}}}); err != nil {
		t.Fatal(err)
	}
	return server, &value, &identity
}

func wireGuardGenerationCollect(t *testing.T, server *Server) *nodev1.UserUsageBatch {
	t.Helper()
	batch, err := server.collectWireGuardUserUsage(context.Background(), &nodev1.CollectUsageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

func wireGuardGenerationAck(t *testing.T, server *Server, batch *nodev1.UserUsageBatch) {
	t.Helper()
	response, err := server.ackWireGuardUserUsage(context.Background(), &nodev1.AckUsageRequest{BatchId: batch.BatchId})
	if err != nil || !response.GetAcknowledged() {
		t.Fatalf("ACK: %v %v", response, err)
	}
}

func TestWireGuardUnchangedCheckpointSkipsAtomicRewrite(t *testing.T) {
	server, _, _ := wireGuardGenerationFixture(t)
	oldWrite := wireGuardAccountingWrite
	writes := 0
	wireGuardAccountingWrite = func(path string, raw []byte) error {
		writes++
		return oldWrite(path, raw)
	}
	t.Cleanup(func() { wireGuardAccountingWrite = oldWrite })
	if err := server.checkpointWireGuardOfflineGeneration(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := server.wireGuardUsageStatePath()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.checkpointWireGuardOfflineGeneration(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if writes != 1 || string(before) != string(after) {
		t.Fatalf("unchanged checkpoint rewrote snapshot: writes=%d contentChanged=%t", writes, string(before) != string(after))
	}
	restarted := New(Config{DataDir: server.cfg.DataDir})
	state, err := restarted.readWireGuardGenerationStateLocked()
	if err != nil || state[wireGuardUsageBaselineKey("wg", "wg-test", "peer")].Total != 10_000_000_000 {
		t.Fatalf("restart generation=%v err=%v", state, err)
	}
}

func TestWireGuardChangedCheckpointUsesJournalAndReplaysAfterRestart(t *testing.T) {
	server, counter, _ := wireGuardGenerationFixture(t)
	oldWrite, oldAppend := wireGuardAccountingWrite, wireGuardAccountingJournalAppend
	snapshotWrites, journalAppends := 0, 0
	wireGuardAccountingWrite = func(path string, raw []byte) error {
		snapshotWrites++
		return oldWrite(path, raw)
	}
	wireGuardAccountingJournalAppend = func(path string, offset int64, raw []byte) (int64, error) {
		journalAppends++
		return oldAppend(path, offset, raw)
	}
	t.Cleanup(func() {
		wireGuardAccountingWrite = oldWrite
		wireGuardAccountingJournalAppend = oldAppend
	})
	if err := server.checkpointWireGuardOfflineGeneration(context.Background()); err != nil {
		t.Fatal(err)
	}
	*counter += 500
	if err := server.checkpointWireGuardOfflineGeneration(context.Background()); err != nil {
		t.Fatal(err)
	}
	if snapshotWrites != 1 || journalAppends != 1 {
		t.Fatalf("writes: snapshots=%d journal=%d, want 1 and 1", snapshotWrites, journalAppends)
	}
	restarted := New(Config{DataDir: server.cfg.DataDir})
	if err := restarted.ensureWireGuardUsageStateLoadedLocked(); err != nil {
		t.Fatal(err)
	}
	key := wireGuardUsageBaselineKey("wg", "wg-test", "peer")
	if got := restarted.wireGuardUsageGenerations[key].Total; got != 10_000_000_500 {
		t.Fatalf("replayed generation total=%d, want 10000000500", got)
	}
}

func TestWireGuardJournalCompactionPreservesGenerationAndCarry(t *testing.T) {
	server, counter, _ := wireGuardGenerationFixture(t)
	if err := server.checkpointWireGuardOfflineGeneration(context.Background()); err != nil {
		t.Fatal(err)
	}
	for range wireGuardJournalCompactRecords {
		*counter++
		if err := server.checkpointWireGuardOfflineGeneration(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if server.wireGuardJournalRecords != 0 || server.wireGuardJournalOffset != 0 {
		t.Fatalf("journal not compacted: records=%d offset=%d", server.wireGuardJournalRecords, server.wireGuardJournalOffset)
	}
	restarted := New(Config{DataDir: server.cfg.DataDir})
	if err := restarted.ensureWireGuardUsageStateLoadedLocked(); err != nil {
		t.Fatal(err)
	}
	key := wireGuardUsageBaselineKey("wg", "wg-test", "peer")
	if got, want := restarted.wireGuardUsageGenerations[key].Total, uint64(10_000_000_000+wireGuardJournalCompactRecords); got != want {
		t.Fatalf("recovered generation total=%d, want %d", got, want)
	}
	if got, want := restarted.wireGuardUsageCarry[key].Value, uint64(10_000_000_000+wireGuardJournalCompactRecords); got != want {
		t.Fatalf("recovered carry=%d, want %d", got, want)
	}
}

func TestWireGuardOfflineGenerationRestartPreservesImmutablePending(t *testing.T) {
	server, value, identity := wireGuardGenerationFixture(t)
	first := wireGuardGenerationCollect(t, server)
	*value, *identity = 11_000_000_000, "boot-b:5"
	restarted := New(Config{DataDir: server.cfg.DataDir})
	retry := wireGuardGenerationCollect(t, restarted)
	if retry.BatchId != first.BatchId || retry.Stats[0].Value != 10_000_000_000 {
		t.Fatalf("pending mutated: %v", retry)
	}
	cfg := wireGuardUsageRuntimeConfig{InboundTag: "wg", InterfaceName: "wg-test", Peers: map[string]int64{"peer": 42}}
	live, err := restarted.wireGuardLiveUnackedUsageLocked(cfg, "wg-test", []wireGuardPeerCounters{{PublicKey: "peer", ReceivedBytes: *value}}, 42)
	if err != nil || live != 21_000_000_000 {
		t.Fatalf("quota live=%d err=%v", live, err)
	}
	wireGuardGenerationAck(t, restarted, retry)
	second := wireGuardGenerationCollect(t, restarted)
	if second.Stats[0].Value != 11_000_000_000 {
		t.Fatalf("new generation=%v", second)
	}
	if first.Stats[0].Value+second.Stats[0].Value != 21_000_000_000 {
		t.Fatal("lost reset bytes")
	}
}

func TestWireGuardOfflineGenerationSurvivesCarryACKPruning(t *testing.T) {
	server, value, identity := wireGuardGenerationFixture(t)
	if err := server.checkpointWireGuardOfflineGeneration(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := wireGuardGenerationCollect(t, server)
	wireGuardGenerationAck(t, server, first)
	if len(server.wireGuardUsageCarry) != 0 {
		t.Fatal("carry not pruned")
	}
	*value, *identity = 11_000_000_000, "boot-a:6"
	restarted := New(Config{DataDir: server.cfg.DataDir})
	second := wireGuardGenerationCollect(t, restarted)
	if second.Stats[0].Value != 11_000_000_000 {
		t.Fatalf("ifindex reset lost: %v", second)
	}
	wireGuardGenerationAck(t, restarted, second)
	third := wireGuardGenerationCollect(t, restarted)
	if len(third.Stats) != 0 {
		t.Fatalf("unchanged generation billed twice: %v", third)
	}
}

func TestWireGuardOfflineGenerationManagedPeerRecreation(t *testing.T) {
	server, value, _ := wireGuardGenerationFixture(t)
	first := wireGuardGenerationCollect(t, server)
	wireGuardGenerationAck(t, server, first)
	prepared := preparedWireGuardRuntime{Tag: "wg", InterfaceName: "wg-test"}
	if err := server.transitionWireGuardRemovedGenerations(prepared, true); err != nil {
		t.Fatal(err)
	}
	if _, err := server.collectWireGuardUserUsage(context.Background(), &nodev1.CollectUsageRequest{}); err == nil {
		t.Fatal("ambiguous transition accepted")
	}
	if err := server.markWireGuardRemovedGenerations(prepared); err != nil {
		t.Fatal(err)
	}
	*value = 11_000_000_000
	second := wireGuardGenerationCollect(t, New(Config{DataDir: server.cfg.DataDir}))
	if second.Stats[0].Value != 11_000_000_000 {
		t.Fatalf("recreated peer lost: %v", second)
	}
}

func TestWireGuardOfflineGenerationRemovalAheadOfCheckpoint(t *testing.T) {
	server, value, _ := wireGuardGenerationFixture(t)
	first := wireGuardGenerationCollect(t, server)
	server.wireGuardUsageMu.Lock()
	err := server.updateWireGuardUsageCarryLocked("wg", "wg-test", "peer", 42, 12_000_000_000, false)
	if err == nil {
		err = server.persistWireGuardUsageStateLocked()
	}
	server.wireGuardUsageMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	prepared := preparedWireGuardRuntime{Tag: "wg", InterfaceName: "wg-test"}
	if err := server.markWireGuardRemovedGenerations(prepared); err != nil {
		t.Fatal(err)
	}
	*value = 11_000_000_000
	restarted := New(Config{DataDir: server.cfg.DataDir})
	retry := wireGuardGenerationCollect(t, restarted)
	wireGuardGenerationAck(t, restarted, retry)
	second := wireGuardGenerationCollect(t, restarted)
	if len(second.Stats) != 1 || first.Stats[0].Value+second.Stats[0].Value != 23_000_000_000 {
		t.Fatalf("stale generation rebilled after removal: first=%v second=%v", first, second)
	}
}

func TestWireGuardOfflineGenerationObservedAbsence(t *testing.T) {
	server, value, _ := wireGuardGenerationFixture(t)
	first := wireGuardGenerationCollect(t, server)
	wireGuardGenerationAck(t, server, first)
	cfg := wireGuardUsageRuntimeConfig{InboundTag: "wg", Peers: map[string]int64{"peer": 42}}
	server.wireGuardUsageMu.Lock()
	err := server.checkpointWireGuardGenerationLocked(cfg, "wg-test", nil, false)
	server.wireGuardUsageMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	*value = 11_000_000_000
	second := wireGuardGenerationCollect(t, server)
	if second.Stats[0].Value != 11_000_000_000 {
		t.Fatalf("absence reset lost: %v", second)
	}
}

func TestWireGuardOfflineGenerationPersistenceFailureRollsBack(t *testing.T) {
	server, value, identity := wireGuardGenerationFixture(t)
	first := wireGuardGenerationCollect(t, server)
	*value, *identity = 11_000_000_000, "boot-b:7"
	oldAppend := wireGuardAccountingJournalAppend
	wireGuardAccountingJournalAppend = func(string, int64, []byte) (int64, error) { return 0, errors.New("disk full") }
	t.Cleanup(func() { wireGuardAccountingJournalAppend = oldAppend })
	before := cloneWireGuardUsageCarryMap(server.wireGuardUsageCarry)
	if _, err := server.collectWireGuardUserUsage(context.Background(), &nodev1.CollectUsageRequest{}); err == nil {
		t.Fatal("write failure ignored")
	}
	if !reflect.DeepEqual(before, server.wireGuardUsageCarry) {
		t.Fatal("carry advanced after failed durable checkpoint")
	}
	if server.wireGuardUsagePending.BatchID != first.BatchId || server.wireGuardUsagePending.Samples[0].Value != 10_000_000_000 {
		t.Fatal("pending mutated after failed persistence")
	}
	wireGuardAccountingJournalAppend = oldAppend
	retry := wireGuardGenerationCollect(t, server)
	wireGuardGenerationAck(t, server, retry)
	second := wireGuardGenerationCollect(t, server)
	if second.Stats[0].Value != 11_000_000_000 {
		t.Fatalf("retry lost bytes: %v", second)
	}
}

func TestWireGuardOfflineGenerationIdentityOncePerInterface(t *testing.T) {
	server, _, _ := wireGuardGenerationFixture(t)
	calls := 0
	wireGuardInterfaceIdentity = func(string) (string, error) { calls++; return "boot:1", nil }
	cfg := wireGuardUsageRuntimeConfig{InboundTag: "wg", Peers: map[string]int64{"a": 42, "b": 43}}
	if err := server.ensureWireGuardUsageStateLoadedLocked(); err != nil {
		t.Fatal(err)
	}
	if err := server.checkpointWireGuardGenerationLocked(cfg, "wg-test", []wireGuardPeerCounters{{PublicKey: "a", ReceivedBytes: 10}, {PublicKey: "b", ReceivedBytes: 20}}, false); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("identity calls=%d", calls)
	}
}

func TestWireGuardOfflineGenerationRetainsBytesObservedAfterPending(t *testing.T) {
	server, value, identity := wireGuardGenerationFixture(t)
	first := wireGuardGenerationCollect(t, server)
	*value = 12_000_000_000
	wireGuardGenerationCollect(t, server)
	*value, *identity = 11_000_000_000, "boot-b:8"
	retry := wireGuardGenerationCollect(t, New(Config{DataDir: server.cfg.DataDir}))
	restarted := New(Config{DataDir: server.cfg.DataDir})
	wireGuardGenerationAck(t, restarted, retry)
	second := wireGuardGenerationCollect(t, restarted)
	if first.Stats[0].Value+second.Stats[0].Value != 23_000_000_000 {
		t.Fatalf("observed old generation tail lost: %v", second)
	}
}

func TestWireGuardOfflineGenerationQuotaUsesBothGenerations(t *testing.T) {
	server, value, identity := wireGuardGenerationFixture(t)
	first := wireGuardGenerationCollect(t, server)
	*value, *identity = 11_000_000_000, "boot-b:9"
	wireGuardGenerationCollect(t, server)
	cfg := wireGuardUsageRuntimeConfig{InboundTag: "wg", Peers: map[string]int64{"peer": 42}, Policies: map[string]nativeSessionUserPolicy{}}
	limit := int64(20_000_000_000)
	cfg.Policies["peer"] = nativeSessionUserPolicy{Status: "active", DataLimit: limit}
	oldRun, oldPath := wireGuardRuntimeRun, wireGuardRuntimeLookPath
	removed := false
	wireGuardRuntimeLookPath = func(string) (string, error) { return "wg", nil }
	wireGuardRuntimeRun = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) == 5 && args[0] == "set" && args[4] == "remove" {
			removed = true
		}
		return nil, nil
	}
	t.Cleanup(func() { wireGuardRuntimeRun, wireGuardRuntimeLookPath = oldRun, oldPath })
	peers := []wireGuardPeerCounters{{PublicKey: "peer", ReceivedBytes: *value}}
	remaining := server.enforceWireGuardPoliciesLocked(cfg, "wg-test", peers, time.Now().UTC())
	if !removed || len(remaining) != 0 {
		t.Fatal("21 GB did not enforce 20 GB quota")
	}
	if server.wireGuardUsagePending.BatchID != first.BatchId || server.wireGuardUsagePending.Samples[0].Value != 10_000_000_000 {
		t.Fatal("quota removal changed pending")
	}
}

func TestWireGuardOfflineGenerationDurableStubCombinedReflectionAndCarryACK(t *testing.T) {
	server, value, identity := wireGuardGenerationFixture(t)
	if err := server.checkpointWireGuardOfflineGeneration(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := wireGuardGenerationCollect(t, server)
	*value, *identity = 11_000_000_000, "boot-b:10"
	wireGuardGenerationCollect(t, server)
	const root = "combined-generation-first"
	if err := server.persistLocalUsageDeliveriesLocked([]localUsageDelivery{{BatchID: root, Rows: []localUsageDeliveryRow{{Protocol: "wireguard", ChildID: first.BatchId, UserID: 42, InboundTag: "wg", Raw: 10_000_000_000}}}}); err != nil {
		t.Fatal(err)
	}
	check := func(s *Server, reflected string, want uint64) {
		t.Helper()
		got, err := s.durablePolicyRaw("wireguard", 42, "wg", reflected)
		if err != nil || got != want {
			t.Fatalf("stub reflected=%q raw=%d want=%d err=%v", reflected, got, want, err)
		}
	}
	check(server, "", 21_000_000_000)
	// Root receipt reflected while immutable child still awaits ACK.
	check(server, root, 11_000_000_000)
	response, err := server.ackWireGuardUserUsageWithReflection(context.Background(), &nodev1.AckUsageRequest{BatchId: first.BatchId}, root)
	if err != nil || !response.GetAcknowledged() {
		t.Fatalf("child ACK=%v %v", response, err)
	}
	restarted := New(Config{DataDir: server.cfg.DataDir})
	check(restarted, root, 11_000_000_000)
	key := wireGuardUsageBaselineKey("wg", "wg-test", "peer")
	if restarted.wireGuardUsageCarry[key].Value != 11_000_000_000 {
		t.Fatal("old carry ACK consumed new generation")
	}
	second := wireGuardGenerationCollect(t, restarted)
	if second.Stats[0].Value != 11_000_000_000 {
		t.Fatalf("second=%v", second)
	}
	check(restarted, root, 11_000_000_000)
	wireGuardGenerationAck(t, restarted, second)
	if len(restarted.wireGuardUsageCarry) != 0 {
		t.Fatal("new carry not pruned")
	}
	check(restarted, second.BatchId, 0)
	if len(wireGuardGenerationCollect(t, restarted).Stats) != 0 {
		t.Fatal("ACK pruning caused rebilling")
	}
}
