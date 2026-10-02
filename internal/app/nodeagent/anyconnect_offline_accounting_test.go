package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/protobuf/proto"
)

func TestAnyConnectOfflineCheckpointDaemonResetRestartImmutableACK(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	root := filepath.Join(dir, "anyconnect", openVPNRuntimeDirName("vpn"))
	writeOfflineFixture(t, filepath.Join(root, "usage-helper.json"), anyConnectUsageRuntimeConfig{InboundTag: "vpn", SocketPath: "ocserv.sock", Users: map[string]int64{"alice": 7}})
	oldBoot, oldQuery, oldDaemon := offlineReadBootID, anyConnectOfflineQuery, anyConnectOfflineDaemonGeneration
	t.Cleanup(func() {
		offlineReadBootID = oldBoot
		anyConnectOfflineQuery = oldQuery
		anyConnectOfflineDaemonGeneration = oldDaemon
	})
	offlineReadBootID = func() (string, error) { return "boot-a", nil }
	daemon := "pid:start-a"
	anyConnectOfflineDaemonGeneration = func(string) (string, error) { return daemon, nil }
	current, err := parseAnyConnectUsersJSON([]byte(`[{"ID":17,"Username":"alice","Full session":"cookie","Remote IP":"198.51.100.7","raw_rx":40,"raw_tx":60}]`))
	if err != nil {
		t.Fatal(err)
	}
	anyConnectOfflineQuery = func(context.Context, anyConnectUsageRuntimeConfig) ([]anyConnectLiveSession, error) {
		return current, nil
	}
	s := New(Config{DataDir: dir}) // Persisted helper discovery without runtime maps.
	if err := s.checkpointAnyConnectOffline(ctx); err != nil {
		t.Fatal(err)
	}
	batch, err := s.collectAnyConnectUserUsage(ctx, nil)
	if err != nil || batch.Stats[0].Value != 100 {
		t.Fatalf("A: %v %v", batch, err)
	}
	daemon = "pid:start-b"
	current[0].Received = 200
	current[0].Sent = 50 // Reused session ID; new native total > old.
	if err := s.checkpointAnyConnectOffline(ctx); err != nil {
		t.Fatal(err)
	}
	retry, err := s.collectAnyConnectUserUsage(ctx, nil)
	if err != nil || !proto.Equal(batch, retry) {
		t.Fatalf("mutable retry: %v %v", retry, err)
	}
	s = New(Config{DataDir: dir})
	retry, err = s.collectAnyConnectUserUsage(ctx, nil)
	if err != nil || !proto.Equal(batch, retry) {
		t.Fatalf("restart retry: %v %v", retry, err)
	}
	ack, err := s.ackAnyConnectUserUsage(ctx, &nodev1.AckUsageRequest{BatchId: batch.BatchId})
	if err != nil || !ack.Acknowledged {
		t.Fatalf("ACK: %v %v", ack, err)
	}
	current = nil
	if err := s.checkpointAnyConnectOffline(ctx); err != nil {
		t.Fatal(err)
	}
	next, err := s.collectAnyConnectUserUsage(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Stats) != 1 || next.Stats[0].Value != 250 || len(next.OnlineIps) != 0 {
		t.Fatalf("lost B: %v", next)
	}
	wrong, err := s.ackAnyConnectUserUsage(ctx, &nodev1.AckUsageRequest{BatchId: "wrong"})
	if err != nil || wrong.Acknowledged {
		t.Fatal("wrong ACK accepted")
	}
	if _, err := s.ackAnyConnectUserUsage(ctx, &nodev1.AckUsageRequest{BatchId: next.BatchId}); err != nil {
		t.Fatal(err)
	}
	current, err = parseAnyConnectUsersJSON([]byte(`[{"ID":18,"Username":"alice","Full session":"cookie","raw_rx":300,"raw_tx":0}]`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.checkpointAnyConnectOffline(ctx); err != nil {
		t.Fatal(err)
	}
	next, err = s.collectAnyConnectUserUsage(ctx, nil)
	if err != nil || next.Stats[0].Value != 300 {
		t.Fatalf("same cookie/new session generation: %v %v", next, err)
	}
}

func TestAnyConnectOfflineParserPreservesLargeInteger(t *testing.T) {
	sessions, err := parseAnyConnectUsersJSON([]byte(`[{"username":"alice","full_session":"x","raw_rx":9007199254740993,"raw_tx":0}]`))
	if err != nil || len(sessions) != 1 || sessions[0].Received != 9007199254740993 {
		t.Fatalf("counter rounded: %v %v", sessions, err)
	}
}

func TestAnyConnectOfflineIPDeviceAnd50MBQuota(t *testing.T) {
	for _, mode := range []string{"ip", "device", "quota"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			s := New(Config{DataDir: dir})
			root := filepath.Join(dir, "anyconnect", openVPNRuntimeDirName("vpn"))
			writeOfflineFixture(t, filepath.Join(root, "usage-helper.json"), anyConnectUsageRuntimeConfig{InboundTag: "vpn", SocketPath: "ocserv.sock", Users: map[string]int64{"alice": 7}})
			const mib = uint64(1 << 20)
			policy := nativeSessionUserPolicy{Status: "active", IPLimit: 1, DeviceLimit: 10}
			if mode == "device" {
				policy.IPLimit = 10
				policy.DeviceLimit = 2
			}
			if mode == "quota" {
				policy.IPLimit = 0
				policy.DataLimit = int64(50 * mib)
				policy.UsageCoefficient = 1.5
				policy.InboundCoefficient = 2
			}
			writeOfflineFixture(t, filepath.Join(root, "session-helper.json"), nativeSessionHelperConfig{Policies: map[string]nativeSessionUserPolicy{"alice": policy}})
			oldBoot, oldQuery, oldDaemon, oldDisconnect := offlineReadBootID, anyConnectOfflineQuery, anyConnectOfflineDaemonGeneration, anyConnectOfflineDisconnect
			t.Cleanup(func() {
				offlineReadBootID = oldBoot
				anyConnectOfflineQuery = oldQuery
				anyConnectOfflineDaemonGeneration = oldDaemon
				anyConnectOfflineDisconnect = oldDisconnect
			})
			offlineReadBootID = func() (string, error) { return "boot", nil }
			anyConnectOfflineDaemonGeneration = func(string) (string, error) { return "daemon", nil }
			current := []anyConnectLiveSession{}
			for i, remote := range []string{"198.51.100.7", "198.51.100.7", "198.51.100.8"} {
				id := string(rune('1' + i))
				current = append(current, anyConnectLiveSession{Username: "alice", ClientIP: remote, AssignedIP: "10.0.0." + id, ID: id, DisconnectID: id, GenerationID: id})
			}
			if mode == "quota" {
				current = current[:1]
				current[0].Received = 16 * mib
			}
			anyConnectOfflineQuery = func(context.Context, anyConnectUsageRuntimeConfig) ([]anyConnectLiveSession, error) {
				return current, nil
			}
			denied := []string{}
			anyConnectOfflineDisconnect = func(_ context.Context, _ anyConnectUsageRuntimeConfig, id string) error {
				denied = append(denied, id)
				return nil
			}
			if err := s.quotaCheckAnyConnectOffline(context.Background()); err != nil {
				t.Fatal(err)
			}
			if mode == "quota" {
				if len(denied) != 0 {
					t.Fatal("premature cutoff")
				}
				current[0].Received = 17 * mib
				if err := s.quotaCheckAnyConnectOffline(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if len(denied) != 1 || (mode != "quota" && denied[0] != "3") {
				t.Fatalf("wrong disconnections: %v", denied)
			}
		})
	}
}

func TestAnyConnectOfflineUsesDesiredPolicyOverPersistedHelper(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "anyconnect", openVPNRuntimeDirName("vpn"))
	writeOfflineFixture(t, filepath.Join(root, "usage-helper.json"), anyConnectUsageRuntimeConfig{InboundTag: "vpn", SocketPath: "ocserv.sock", Users: map[string]int64{"alice": 99}})
	s := New(Config{DataDir: dir})
	payload := nativeRuntimePayload{AnyConnectInbounds: []anyConnectRuntimeInbound{{Tag: "vpn", Users: []anyConnectRuntimeUser{{UserID: 7, Username: "alice", UsedTraffic: 300, ReflectedUsageBatchID: "root-a"}}}}}
	raw, _ := json.Marshal(payload)
	if err := s.persistRuntimePolicy(&nodev1.RuntimeConfigRequest{ConfigJson: "{}", OvRuntimeJson: string(raw)}); err != nil {
		t.Fatal(err)
	}
	oldQuery, oldDaemon := anyConnectOfflineQuery, anyConnectOfflineDaemonGeneration
	t.Cleanup(func() { anyConnectOfflineQuery = oldQuery; anyConnectOfflineDaemonGeneration = oldDaemon })
	anyConnectOfflineDaemonGeneration = func(string) (string, error) { return "pid:start", nil }
	anyConnectOfflineQuery = func(context.Context, anyConnectUsageRuntimeConfig) ([]anyConnectLiveSession, error) { return nil, nil }
	snapshots, err := s.anyConnectOfflineSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || snapshots[0].Config.Users["alice"] != 7 || snapshots[0].Policies["alice"].UsedTraffic != 300 || snapshots[0].Policies["alice"].ReflectedUsageBatchID != "root-a" {
		t.Fatalf("stale helper policy: %v", snapshots)
	}
}

func TestAnyConnectOfflineParserRejectsInvalidCountersAndResponses(t *testing.T) {
	for _, raw := range []string{`{"error":"control socket unavailable"}`, `[{"username":"alice","raw_rx":18446744073709551616}]`, `[{"username":"alice","raw_rx":-1}]`} {
		if _, err := parseAnyConnectUsersJSON([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid snapshot %s", raw)
		}
	}
}

func TestAnyConnectOfflineQuotaSingleSnapshotNoTickPersistence(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := New(Config{DataDir: dir})
	root := filepath.Join(dir, "anyconnect", openVPNRuntimeDirName("vpn"))
	writeOfflineFixture(t, filepath.Join(root, "usage-helper.json"), anyConnectUsageRuntimeConfig{InboundTag: "vpn", SocketPath: "ocserv.sock", Users: map[string]int64{"alice": 7, "bob": 8}})
	writeOfflineFixture(t, filepath.Join(root, "session-helper.json"), nativeSessionHelperConfig{Policies: map[string]nativeSessionUserPolicy{"alice": {Status: "active", DataLimit: 1000}, "bob": {Status: "active", DataLimit: 1000}}})
	oldBoot, oldQuery, oldDaemon, oldWrite, oldDisconnect := offlineReadBootID, anyConnectOfflineQuery, anyConnectOfflineDaemonGeneration, persistOfflineAccountingFile, anyConnectOfflineDisconnect
	t.Cleanup(func() {
		offlineReadBootID = oldBoot
		anyConnectOfflineQuery = oldQuery
		anyConnectOfflineDaemonGeneration = oldDaemon
		persistOfflineAccountingFile = oldWrite
		anyConnectOfflineDisconnect = oldDisconnect
	})
	offlineReadBootID = func() (string, error) { return "boot", nil }
	anyConnectOfflineDaemonGeneration = func(string) (string, error) { return "pid:start", nil }
	current, err := parseAnyConnectUsersJSON([]byte(`[{"username":"alice","id":17,"full_session":"a","raw_rx":100},{"username":"bob","id":18,"full_session":"b","raw_rx":80}]`))
	if err != nil {
		t.Fatal(err)
	}
	queries, writes, disconnected := 0, 0, 0
	anyConnectOfflineQuery = func(context.Context, anyConnectUsageRuntimeConfig) ([]anyConnectLiveSession, error) {
		queries++
		return current, nil
	}
	persistOfflineAccountingFile = func(path string, raw []byte) error { writes++; return oldWrite(path, raw) }
	for i := 0; i < 10; i++ {
		if err := s.quotaCheckAnyConnectOffline(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if queries != 10 || writes != 0 {
		t.Fatalf("normal ticks: snapshots=%d writes=%d", queries, writes)
	}
	current[0].GenerationID = "new-a"
	current[0].ID = "new-a"
	current[0].Received = 250
	if err := s.quotaCheckAnyConnectOffline(ctx); err != nil {
		t.Fatal(err)
	}
	active := current
	current = nil
	if err := s.checkpointAnyConnectOffline(ctx); err != nil {
		t.Fatal(err)
	}
	if queries != 12 || writes != 1 {
		t.Fatalf("checkpoint: snapshots=%d writes=%d", queries, writes)
	}
	owner := offlineOwnerKey(offlineUsageOwner{7, "vpn"})
	disk := New(Config{DataDir: dir})
	if err := disk.ensureAnyConnectUsageStateLoadedLocked(); err != nil {
		t.Fatal(err)
	}
	if disk.anyConnectUsageBaseline[owner] != 350 {
		t.Fatal("volatile rotation/disappeared session lost")
	}
	current = active
	writeOfflineFixture(t, filepath.Join(root, "session-helper.json"), nativeSessionHelperConfig{Policies: map[string]nativeSessionUserPolicy{"alice": {Status: "active", DataLimit: 300}, "bob": {Status: "active", DataLimit: 1000}}})
	anyConnectOfflineDisconnect = func(context.Context, anyConnectUsageRuntimeConfig, string) error {
		disconnected++
		disk := New(Config{DataDir: dir})
		if err := disk.ensureAnyConnectUsageStateLoadedLocked(); err != nil {
			return err
		}
		if disk.anyConnectUsageBaseline[owner] != 350 {
			t.Fatal("disconnect preceded durable sample")
		}
		return nil
	}
	before := writes
	if err := s.quotaCheckAnyConnectOffline(ctx); err != nil {
		t.Fatal(err)
	}
	if queries != 13 || writes != before+1 || disconnected != 1 {
		t.Fatalf("denial: snapshots=%d writes=%d disconnects=%d", queries, writes, disconnected)
	}
	persistOfflineAccountingFile = func(string, []byte) error { return errors.New("disk failure") }
	if err := s.quotaCheckAnyConnectOffline(ctx); err == nil {
		t.Fatal("ignored persistence failure")
	}
	if disconnected != 1 {
		t.Fatal("disconnected after failed checkpoint")
	}
}
