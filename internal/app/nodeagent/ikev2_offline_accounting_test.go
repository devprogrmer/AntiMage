package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/protobuf/proto"
)

func TestIKEv2OfflineCheckpointRotationRestartImmutableACK(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := New(Config{DataDir: dir})
	s.ikev2Runtimes = map[string]*ikev2Process{"vpn": {tag: "vpn", inbound: ikev2RuntimeInbound{Tag: "vpn", Users: []ikev2RuntimeUser{{UserID: 7, Username: "alice"}}}}}
	oldBoot, oldSnapshot := offlineReadBootID, ikev2OfflineSnapshot
	t.Cleanup(func() { offlineReadBootID = oldBoot; ikev2OfflineSnapshot = oldSnapshot })
	boot := "boot-a"
	offlineReadBootID = func() (string, error) { return boot, nil }
	raw := `list-sa event {CONN {uniqueid=1 state=ESTABLISHED remote-eap-id=alice remote-host=198.51.100.7 initiator-spi=aa responder-spi=bb child-sas {child {uniqueid=1 state=INSTALLED spi-in=11 spi-out=22 bytes-in=40 bytes-out=60}}}}`
	fixture := func(childID, spi string, bytes uint64) []ikev2RawSA {
		sas := parseIKEv2SwanctlRaw(raw)
		sas[0].ConnectionName = ikev2ConnectionName("vpn")
		sas[0].Children[0].UniqueID = childID
		sas[0].Children[0].SPIIn = spi
		sas[0].Children[0].BytesIn = bytes
		sas[0].Children[0].BytesOut = 0
		return sas
	}
	current := fixture("1", "11", 100)
	ikev2OfflineSnapshot = func(context.Context) ([]ikev2RawSA, error) { return current, nil }
	if err := s.checkpointIKEv2Offline(ctx); err != nil {
		t.Fatal(err)
	}
	batch, err := s.collectIKEv2UserUsage(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Stats[0].Value != 100 {
		t.Fatal(batch)
	}
	current = fixture("2", "33", 250) // New generation is larger than the old counter.
	if err := s.checkpointIKEv2Offline(ctx); err != nil {
		t.Fatal(err)
	}
	retry, err := s.collectIKEv2UserUsage(ctx, nil)
	if err != nil || !proto.Equal(batch, retry) {
		t.Fatalf("mutable retry: %v %v", retry, err)
	}
	s = New(Config{DataDir: dir}) // No runtime map: recover persisted identity and usage.
	retry, err = s.collectIKEv2UserUsage(ctx, nil)
	if err != nil || !proto.Equal(batch, retry) {
		t.Fatalf("restart retry: %v %v", retry, err)
	}
	ack, err := s.ackIKEv2UserUsage(ctx, &nodev1.AckUsageRequest{BatchId: batch.BatchId})
	if err != nil || !ack.Acknowledged {
		t.Fatalf("ACK: %v %v", ack, err)
	}
	current = nil // Disappeared session retains B, but is no longer reported online.
	if err := s.checkpointIKEv2Offline(ctx); err != nil {
		t.Fatal(err)
	}
	next, err := s.collectIKEv2UserUsage(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Stats) != 1 || next.Stats[0].Value != 250 || len(next.OnlineIps) != 0 {
		t.Fatalf("lost B: %v", next)
	}
	if _, err := s.ackIKEv2UserUsage(ctx, &nodev1.AckUsageRequest{BatchId: next.BatchId}); err != nil {
		t.Fatal(err)
	}
	boot = "boot-b"
	current = fixture("2", "33", 300)
	if err := s.checkpointIKEv2Offline(ctx); err != nil {
		t.Fatal(err)
	}
	next, err = s.collectIKEv2UserUsage(ctx, nil)
	if err != nil || next.Stats[0].Value != 300 {
		t.Fatalf("boot reset: %v %v", next, err)
	}
}

func TestOfflineLegacyPendingSeedsOnceAndKeepsNewGeneration(t *testing.T) {
	owner := offlineUsageOwner{7, "vpn"}
	old := map[string]uint64{"legacy": 100}
	a := offlineUsageObservation{Generation: offlineGeneration("boot", "a"), Legacy: "legacy", Owner: owner, Native: 150}
	state, err := advanceOfflineUsage(old, old, []offlineUsageObservation{a})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := offlineUnackedUsage(state, owner, old, []ikev2UsageSample{{UserID: 7, InboundTag: "vpn", Value: 100}})
	if err != nil || raw != 150 {
		t.Fatalf("legacy quota raw=%d err=%v", raw, err)
	}
	a.Generation = offlineGeneration("boot", "b")
	a.Native = 250
	state, err = advanceOfflineUsage(state, old, []offlineUsageObservation{a})
	if err != nil || state[offlineOwnerKey(owner)] != 300 {
		t.Fatalf("generation seed reused: %v %v", state, err)
	}
	after, err := offlineACKBaseline(state, old)
	if err != nil || after[offlineOwnerKey(owner)] != 300 {
		t.Fatalf("legacy ACK erased new usage: %v %v", after, err)
	}
}

func TestOfflineAccountingFailsSafelyAtOverflowAndCapacity(t *testing.T) {
	owner := offlineUsageOwner{7, "vpn"}
	key := offlineOwnerKey(owner)
	state := map[string]uint64{key: ^uint64(0)}
	_, err := advanceOfflineUsage(state, nil, []offlineUsageObservation{{Generation: offlineGeneration("a"), Owner: owner, Native: 1}})
	if err == nil || state[key] != ^uint64(0) {
		t.Fatal("overflow discarded usage")
	}
	state = map[string]uint64{}
	for i := 0; i < maxAccountingCounterSeries; i++ {
		raw, _ := json.Marshal(i)
		state[string(raw)] = 0
	}
	if _, err := advanceOfflineUsage(state, nil, []offlineUsageObservation{{Generation: offlineGeneration("a"), Owner: owner, Native: 1}}); err == nil {
		t.Fatal("capacity silently discarded state")
	}
}

func TestOfflineQuotaReflectedPendingKeepsOnlyNewerRaw(t *testing.T) {
	for _, protocol := range []string{"ikev2", "anyconnect"} {
		t.Run(protocol, func(t *testing.T) {
			const mib = uint64(1024 * 1024)
			s := New(Config{DataDir: t.TempDir()})
			s.localUsageLoaded = true
			s.localUsageDeliveries = []localUsageDelivery{
				{BatchID: "root-a", Rows: []localUsageDeliveryRow{{Protocol: protocol, UserID: 7, InboundTag: "vpn", ChildID: "child-a", Raw: 100 * mib}}},
			}
			owner := offlineUsageOwner{7, "vpn"}
			baseline := map[string]uint64{offlineOwnerKey(owner): 120 * mib}
			captured := map[string]uint64{offlineBatchMarker: 1, offlineOwnerKey(owner): 100 * mib}
			samples := []ikev2UsageSample{{UserID: 7, InboundTag: "vpn", Value: 100 * mib}}
			raw, err := s.offlineQuotaRaw(protocol, owner, baseline, captured, samples, "child-a", "")
			if err != nil || raw != 120*mib {
				t.Fatalf("pending counted twice: raw=%d err=%v", raw, err)
			}
			raw, err = s.offlineQuotaRaw(protocol, owner, baseline, captured, samples, "child-a", "root-a")
			if err != nil || raw != 20*mib {
				t.Fatalf("reflected pending lost newer bytes: raw=%d err=%v", raw, err)
			}
			policy := nativeSessionUserPolicy{Status: "active", UsedTraffic: int64(300 * mib), DataLimit: int64(400 * mib), UsageCoefficient: 1.5, InboundCoefficient: 2}
			if allowed, _ := nativeSessionUserPolicyAllowedWithLiveUsage(policy, raw, time.Now()); !allowed {
				t.Fatal("premature quota from reflected bytes")
			}
			s.localUsageDeliveries = []localUsageDelivery{{BatchID: "root-b", Rows: []localUsageDeliveryRow{{Protocol: protocol, UserID: 7, InboundTag: "vpn", ChildID: "child-b", Raw: 100 * mib}}}}
			raw, err = s.offlineQuotaRaw(protocol, owner, map[string]uint64{offlineOwnerKey(owner): 20 * mib}, nil, nil, "", "")
			if err != nil || raw != 120*mib {
				t.Fatalf("ACK credit missing: %d %v", raw, err)
			}
			if effective := nativeSessionEffectiveLiveUsage(policy, raw); effective != 360*mib {
				t.Fatalf("coefficients applied incorrectly: %d", effective)
			}
		})
	}
}

func TestOfflineBootCompactionPreservesUnsentUsage(t *testing.T) {
	owner := offlineUsageOwner{7, "vpn"}
	state := map[string]uint64{offlineGeneration("old-boot", "sa"): 100, offlineOwnerKey(owner): 100, "legacy": 100, "\x00migrated:legacy": 1}
	compacted, err := compactOfflineBoot(state, "new-boot")
	if err != nil {
		t.Fatal(err)
	}
	if compacted[offlineOwnerKey(owner)] != 100 || len(compacted) != 2 {
		t.Fatalf("unsafe compaction: %v", compacted)
	}
	next, err := advanceOfflineUsage(compacted, state, []offlineUsageObservation{{Generation: offlineGeneration("new-boot", "sa"), Legacy: "legacy", Owner: owner, Native: 250}})
	if err != nil || next[offlineOwnerKey(owner)] != 350 {
		t.Fatalf("reboot incorrectly seeded old counters: %v %v", next, err)
	}
}

func TestIKEv2OfflineUsesDesiredSnapshotWithEmptyRuntimeMaps(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	ctx := context.Background()
	payload := nativeRuntimePayload{IKEv2Inbounds: []ikev2RuntimeInbound{{Tag: "vpn", Users: []ikev2RuntimeUser{{UserID: 7, Username: "alice", ReflectedUsageBatchID: "root-a"}}}}}
	raw, _ := json.Marshal(payload)
	if err := s.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: "{}", OvRuntimeJson: string(raw)}); err != nil {
		t.Fatal(err)
	}
	runtimes, err := s.offlineIKEv2Runtimes()
	if err != nil {
		t.Fatal(err)
	}
	if got := runtimes[ikev2ConnectionName("vpn")].Users[0].ReflectedUsageBatchID; got != "root-a" {
		t.Fatalf("lost reflected marker: %q", got)
	}
	oldBoot, oldSnapshot := offlineReadBootID, ikev2OfflineSnapshot
	t.Cleanup(func() { offlineReadBootID = oldBoot; ikev2OfflineSnapshot = oldSnapshot })
	offlineReadBootID = func() (string, error) { return "boot", nil }
	ikev2OfflineSnapshot = func(context.Context) ([]ikev2RawSA, error) {
		return parseIKEv2SwanctlRaw(`list-sa event {` + ikev2ConnectionName("vpn") + ` {uniqueid=1 state=ESTABLISHED initiator-spi=aa responder-spi=bb remote-eap-id=alice child-sas {child {uniqueid=1 state=INSTALLED bytes-in=100 bytes-out=0}}}}`), nil
	}
	if err := s.checkpointIKEv2Offline(ctx); err != nil {
		t.Fatal(err)
	}
	ikev2OfflineSnapshot = func(context.Context) ([]ikev2RawSA, error) { return nil, errors.New("daemon disappeared") }
	batch, err := s.collectIKEv2UserUsage(ctx, nil)
	if err != nil || len(batch.Stats) != 1 || batch.Stats[0].Value != 100 || len(batch.OnlineIps) != 0 {
		t.Fatalf("daemon loss hid retained usage: %v %v", batch, err)
	}
}

func writeOfflineFixture(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestOfflineGenerationLargerReplacementAndDisappearance(t *testing.T) {
	const gib = uint64(1 << 30)
	owner := offlineUsageOwner{7, "vpn"}
	old := offlineUsageObservation{Generation: offlineGeneration("boot", "old"), Owner: owner, Native: 10 * gib}
	newSA := offlineUsageObservation{Generation: offlineGeneration("boot", "new"), Owner: owner, Native: 11 * gib}
	state, err := advanceOfflineUsage(nil, nil, []offlineUsageObservation{old})
	if err != nil {
		t.Fatal(err)
	}
	state, err = advanceOfflineUsage(state, nil, []offlineUsageObservation{newSA})
	if err != nil || state[offlineOwnerKey(owner)] != 21*gib {
		t.Fatalf("replacement: %v %v", state, err)
	}
	state, err = advanceOfflineUsage(state, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	state, err = advanceOfflineUsage(state, nil, []offlineUsageObservation{old, newSA})
	if err != nil || state[offlineOwnerKey(owner)] != 21*gib {
		t.Fatalf("reappearance double counted: %v %v", state, err)
	}
}

func TestOfflineRemoteIPLimitDistinctFromDevices(t *testing.T) {
	ips := map[string]bool{}
	for _, remote := range []string{"198.51.100.7", "::ffff:198.51.100.7", "198.51.100.7", "", "invalid", "0.0.0.0"} {
		if offlineIPLimitExceeded(ips, remote, 1) {
			t.Fatalf("same or unavailable endpoint rejected: %q", remote)
		}
	}
	if !offlineIPLimitExceeded(ips, "198.51.100.8", 1) || len(ips) != 1 {
		t.Fatal("second real endpoint allowed")
	}
}

func TestIKEv2OfflineFallbackCacheDoesNotMutateRuntimeCredentials(t *testing.T) {
	s := New(Config{DataDir: t.TempDir()})
	s.ikev2Runtimes = map[string]*ikev2Process{"vpn": {tag: "vpn", inbound: ikev2RuntimeInbound{Tag: "vpn", Users: []ikev2RuntimeUser{{UserID: 7, Username: "alice", Password: "secret"}}}}}
	if _, err := s.offlineIKEv2RuntimesForCheckpoint(true); err != nil {
		t.Fatal(err)
	}
	if s.ikev2Runtimes["vpn"].inbound.Users[0].Password != "secret" {
		t.Fatal("checkpoint mutated live credentials")
	}
	raw, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "ikev2", "offline-runtimes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cached map[string]ikev2RuntimeInbound
	if err := json.Unmarshal(raw, &cached); err != nil {
		t.Fatal(err)
	}
	if cached[ikev2ConnectionName("vpn")].Users[0].Password != "" {
		t.Fatal("credential persisted in accounting cache")
	}
}

func TestIKEv2OfflineIPLimitAnd50MBQuota(t *testing.T) {
	for _, mode := range []string{"ip", "quota"} {
		t.Run(mode, func(t *testing.T) {
			s := New(Config{DataDir: t.TempDir()})
			const mib = uint64(1 << 20)
			limit := int64(50 * mib)
			user := ikev2RuntimeUser{UserID: 7, Username: "alice", Status: "active", IPLimit: 1, DeviceLimit: 10}
			if mode == "quota" {
				user.IPLimit = 0
				user.DataLimit = &limit
				user.UsageCoefficient = 1.5
				user.InboundCoefficient = 2
			}
			s.ikev2Runtimes = map[string]*ikev2Process{"vpn": {tag: "vpn", inbound: ikev2RuntimeInbound{Tag: "vpn", Users: []ikev2RuntimeUser{user}}}}
			oldBoot, oldQuery, oldTerminate := offlineReadBootID, ikev2OfflineSnapshot, ikev2OfflineTerminate
			t.Cleanup(func() {
				offlineReadBootID = oldBoot
				ikev2OfflineSnapshot = oldQuery
				ikev2OfflineTerminate = oldTerminate
			})
			offlineReadBootID = func() (string, error) { return "boot", nil }
			current := []ikev2RawSA{}
			for i, remote := range []string{"198.51.100.7", "198.51.100.7", "198.51.100.8"} {
				id := string(rune('1' + i))
				current = append(current, ikev2RawSA{ConnectionName: ikev2ConnectionName("vpn"), UniqueID: id, State: "ESTABLISHED", RemoteEAPID: "alice", RemoteHost: remote, InitiatorSPI: id, ResponderSPI: id, Children: []ikev2RawChildSA{{UniqueID: id, BytesIn: 0}}})
			}
			if mode == "quota" {
				current = current[:1]
				current[0].Children[0].BytesIn = 16 * mib
			}
			ikev2OfflineSnapshot = func(context.Context) ([]ikev2RawSA, error) { return current, nil }
			denied := []string{}
			ikev2OfflineTerminate = func(_ *Server, _ context.Context, sa ikev2RawSA, _ string) error {
				denied = append(denied, sa.UniqueID)
				return nil
			}
			if err := s.quotaCheckIKEv2Offline(context.Background()); err != nil {
				t.Fatal(err)
			}
			if mode == "quota" {
				if len(denied) != 0 {
					t.Fatal("premature cutoff")
				}
				current[0].Children[0].BytesIn = 17 * mib
				if err := s.quotaCheckIKEv2Offline(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if len(denied) != 1 || (mode == "ip" && denied[0] != "3") {
				t.Fatalf("wrong disconnections: %v", denied)
			}
		})
	}
}

func TestIKEv2OfflineQuotaSingleSnapshotNoTickPersistence(t *testing.T) {
	ctx := context.Background()
	s := New(Config{DataDir: t.TempDir()})
	limit := int64(1000)
	inbound := ikev2RuntimeInbound{Tag: "vpn", Users: []ikev2RuntimeUser{{UserID: 7, Username: "alice", Status: "active", DataLimit: &limit}}}
	// Exercise runtime-map discovery too: it must not write its fallback cache on ticks.
	s.ikev2Runtimes = map[string]*ikev2Process{"vpn": {tag: "vpn", inbound: inbound}}
	oldBoot, oldSnapshot, oldWrite, oldTerminate := offlineReadBootID, ikev2OfflineSnapshot, persistOfflineAccountingFile, ikev2OfflineTerminate
	t.Cleanup(func() {
		offlineReadBootID = oldBoot
		ikev2OfflineSnapshot = oldSnapshot
		persistOfflineAccountingFile = oldWrite
		ikev2OfflineTerminate = oldTerminate
	})
	offlineReadBootID = func() (string, error) { return "boot", nil }
	queries, writes, disconnected := 0, 0, 0
	current := parseIKEv2SwanctlRaw(`list-sa event {` + ikev2ConnectionName("vpn") + ` {uniqueid=1 state=ESTABLISHED initiator-spi=aa responder-spi=bb remote-eap-id=alice child-sas {child {uniqueid=1 state=INSTALLED bytes-in=100 bytes-out=0}}}}`)
	ikev2OfflineSnapshot = func(context.Context) ([]ikev2RawSA, error) { queries++; return current, nil }
	persistOfflineAccountingFile = func(path string, raw []byte) error { writes++; return oldWrite(path, raw) }
	for i := 0; i < 10; i++ {
		if err := s.quotaCheckIKEv2Offline(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if queries != 10 || writes != 0 {
		t.Fatalf("normal ticks: snapshots=%d writes=%d", queries, writes)
	}
	current[0].Children[0].UniqueID = "2"
	current[0].Children[0].BytesIn = 250
	if err := s.quotaCheckIKEv2Offline(ctx); err != nil {
		t.Fatal(err)
	}
	current = nil
	if err := s.checkpointIKEv2Offline(ctx); err != nil {
		t.Fatal(err)
	}
	if queries != 12 || writes != 2 {
		t.Fatalf("checkpoint: snapshots=%d writes=%d", queries, writes)
	} // Policy fallback + ledger.
	reloaded := New(Config{DataDir: s.cfg.DataDir})
	if err := reloaded.ensureIKEv2UsageStateLoadedLocked(); err != nil {
		t.Fatal(err)
	}
	owner := offlineOwnerKey(offlineUsageOwner{7, "vpn"})
	if reloaded.ikev2UsageBaseline[owner] != 350 {
		t.Fatal("volatile rotation/disappeared SA was lost at checkpoint")
	}
	current = parseIKEv2SwanctlRaw(`list-sa event {` + ikev2ConnectionName("vpn") + ` {uniqueid=1 state=ESTABLISHED initiator-spi=aa responder-spi=bb remote-eap-id=alice child-sas {child {uniqueid=2 state=INSTALLED bytes-in=250 bytes-out=0}}}}`)
	limit = 300
	ikev2OfflineTerminate = func(_ *Server, _ context.Context, _ ikev2RawSA, _ string) error {
		disconnected++
		disk := New(Config{DataDir: s.cfg.DataDir})
		if err := disk.ensureIKEv2UsageStateLoadedLocked(); err != nil {
			return err
		}
		if disk.ikev2UsageBaseline[owner] != 350 {
			t.Fatal("disconnect preceded durable sample")
		}
		return nil
	}
	before := writes
	if err := s.quotaCheckIKEv2Offline(ctx); err != nil {
		t.Fatal(err)
	}
	if queries != 13 || writes != before+1 || disconnected != 1 {
		t.Fatalf("denial: snapshots=%d writes=%d disconnects=%d", queries, writes, disconnected)
	}
	persistOfflineAccountingFile = func(string, []byte) error { return errors.New("disk failure") }
	if err := s.quotaCheckIKEv2Offline(ctx); err == nil {
		t.Fatal("ignored persistence failure")
	}
	if disconnected != 1 {
		t.Fatal("disconnected after failed checkpoint")
	}
}
