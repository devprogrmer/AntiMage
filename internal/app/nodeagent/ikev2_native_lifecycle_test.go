package nodeagent

import (
	"context"
	"encoding/json"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/protobuf/proto"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// Each invocation is a new process, talking to the isolated real daemon through
// a swanctl PATH wrapper and recovering the same durable node directory.
func TestIKEv2NativeSpeedStage(t *testing.T) {
	if os.Getenv("ANTIMAGE_IKEV2_NATIVE_STATE") == "" {
		t.Skip("requires isolated native IKEv2 harness")
	}
	user := ikev2RuntimeUser{UserID: 7, Username: "client", Status: "active"}
	secondIdentity := user
	secondIdentity.Username = "client2"
	switch os.Getenv("ANTIMAGE_IKEV2_ACTION") {
	case "upload":
		user.UploadSpeedLimit = 4_000_000
	case "download":
		user.DownloadSpeedLimit = 6_000_000
	default:
		t.Fatal("missing upload/download direction")
	}
	s := New(Config{DataDir: os.Getenv("ANTIMAGE_IKEV2_NATIVE_STATE")})
	s.ikev2Runtimes = map[string]*ikev2Process{"native": {tag: "native", inbound: ikev2RuntimeInbound{Tag: "native", Users: []ikev2RuntimeUser{user, secondIdentity}}}}
	if err := s.quotaCheckIKEv2Offline(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestIKEv2NativeSessionLimitStage(t *testing.T) {
	dir := os.Getenv("ANTIMAGE_IKEV2_NATIVE_STATE")
	if dir == "" {
		t.Skip("requires isolated native IKEv2 harness")
	}
	ctx := context.Background()
	sas, err := ikev2OfflineSnapshot(ctx)
	if err != nil || len(sas) != 2 {
		t.Fatalf("need two real sessions: %v %v", sas, err)
	}
	user := ikev2RuntimeUser{UserID: 7, Username: "client", Status: "active"}
	switch os.Getenv("ANTIMAGE_IKEV2_ACTION") {
	case "device":
		user.DeviceLimit = 1
	case "ip":
		user.IPLimit = 1
		if sas[0].RemoteHost == sas[1].RemoteHost {
			t.Fatal("IP limit requires different real outer IPs")
		}
	default:
		t.Fatal("missing session limit kind")
	}
	s := New(Config{DataDir: dir})
	s.ikev2Runtimes = map[string]*ikev2Process{"native": {tag: "native", inbound: ikev2RuntimeInbound{Tag: "native", Users: []ikev2RuntimeUser{user}}}}
	if err := s.quotaCheckIKEv2Offline(ctx); err != nil {
		t.Fatal(err)
	}
	sas, err = ikev2OfflineSnapshot(ctx)
	if err != nil || len(sas) != 1 {
		t.Fatalf("session limit did not terminate excess real SA: %v %v", sas, err)
	}
	t.Logf("real session limit enforced; survivor outer=%s assigned=%v", sas[0].RemoteHost, sas[0].RemoteVIPs)
}

func TestIKEv2NativeAccountingStage(t *testing.T) {
	dir := os.Getenv("ANTIMAGE_IKEV2_NATIVE_STATE")
	if dir == "" {
		t.Skip("requires isolated native IKEv2 harness")
	}
	expected, err := strconv.ParseUint(os.Getenv("ANTIMAGE_IKEV2_EXPECTED_BYTES"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s := New(Config{DataDir: dir})
	if _, err := os.Stat(filepath.Join(dir, "ikev2", "offline-runtimes.json")); os.IsNotExist(err) {
		s.ikev2Runtimes = map[string]*ikev2Process{"native": {tag: "native", inbound: ikev2RuntimeInbound{Tag: "native", Users: []ikev2RuntimeUser{{UserID: 7, Username: "client", Status: "active", UsageCoefficient: 1.5, InboundCoefficient: 2}}}}}
	}
	if err := s.checkpointIKEv2Offline(ctx); err != nil {
		t.Fatal(err)
	}
	got := s.ikev2UsageBaseline[offlineOwnerKey(offlineUsageOwner{7, "native"})]
	if got != expected {
		t.Fatalf("native durable total: got %d want %d", got, expected)
	}
	action := os.Getenv("ANTIMAGE_IKEV2_ACTION")
	if action == "quota" {
		limit := int64(50 << 20)
		runtimes, err := s.offlineIKEv2Runtimes()
		if err != nil {
			t.Fatal(err)
		}
		inbound := runtimes[ikev2ConnectionName("native")]
		inbound.Users[0].DataLimit = &limit
		s.ikev2Runtimes = map[string]*ikev2Process{"native": {tag: "native", inbound: inbound}}
		if _, err := s.offlineIKEv2RuntimesForCheckpoint(true); err != nil {
			t.Fatal(err)
		}
		if err := s.quotaCheckIKEv2Offline(ctx); err != nil {
			t.Fatal(err)
		}
		sas, err := ikev2OfflineSnapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got*3 >= uint64(limit) && len(sas) != 0 {
			t.Fatalf("quota failed to terminate real SA: %v", sas)
		}
		if got*3 < uint64(limit) && len(sas) == 0 {
			t.Fatal("premature quota termination")
		}
		t.Logf("quota raw=%d effective=%d threshold=%d remaining_SAs=%d", got, got*3, limit, len(sas))
		return
	}
	batch, err := s.collectIKEv2UserUsage(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if action == "ack" {
		var receipt struct {
			BatchIDs []string `json:"batch_ids"`
		}
		raw, err := os.ReadFile(filepath.Join(dir, "native-panel-receipt.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &receipt); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, id := range receipt.BatchIDs {
			if id == batch.BatchId {
				found = true
			}
		}
		if !found {
			t.Fatal("refusing ACK without verified Panel DB receipt")
		}
		ack, err := s.ackIKEv2UserUsage(ctx, &nodev1.AckUsageRequest{BatchId: batch.BatchId})
		if err != nil || !ack.GetAcknowledged() {
			t.Fatalf("ACK: %v %v", ack, err)
		}
		if s.ikev2UsagePending != nil {
			t.Fatal("pending not pruned after ACK")
		}
		// A restarted process must also see the committed ACK.
		s = New(Config{DataDir: dir})
		next, err := s.collectIKEv2UserUsage(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(next.Stats) > 0 && next.Stats[0].Value > 0 {
			raw, err := proto.Marshal(next)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "native-next-batch.pb"), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("DB-confirmed ACK pruned batch %s; next=%v", batch.BatchId, next)
		return
	}
	if len(batch.Stats) != 1 || batch.Stats[0].Value == 0 {
		t.Fatalf("missing real usage: %v", batch)
	}
	saved := filepath.Join(dir, "native-first-batch.pb")
	raw, err := os.ReadFile(saved)
	if os.IsNotExist(err) {
		raw, err = proto.Marshal(batch)
		if err == nil {
			err = os.WriteFile(saved, raw, 0600)
		}
	} else if err == nil {
		first := &nodev1.UserUsageBatch{}
		if err = proto.Unmarshal(raw, first); err == nil && !proto.Equal(first, batch) {
			t.Fatalf("pending changed across rekey/restart: %v != %v", first, batch)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("real native durable bytes=%d; immutable pending=%s", got, batch.BatchId)
}
