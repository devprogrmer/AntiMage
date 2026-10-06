package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/protobuf/proto"
)

func anyConnectNativePID() (string, error) {
	if pid := strings.TrimSpace(os.Getenv("ANTIMAGE_ANYCONNECT_NATIVE_PID")); pid != "" {
		return pid, nil
	}
	path := strings.TrimSpace(os.Getenv("ANTIMAGE_ANYCONNECT_NATIVE_PID_FILE"))
	if path == "" {
		return "", fmt.Errorf("ocserv PID and PID file are missing")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read ocserv PID file: %w", err)
	}
	pid := strings.TrimSpace(string(raw))
	if pid == "" {
		return "", fmt.Errorf("ocserv PID file is empty")
	}
	return pid, nil
}

func TestAnyConnectNativePIDFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ocserv.pid")
	if err := os.WriteFile(path, []byte("4321\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTIMAGE_ANYCONNECT_NATIVE_PID", "")
	t.Setenv("ANTIMAGE_ANYCONNECT_NATIVE_PID_FILE", path)
	if pid, err := anyConnectNativePID(); err != nil || pid != "4321" {
		t.Fatalf("ocserv PID from file = %q, %v", pid, err)
	}
}

// TestAnyConnectNativeAccountingStage reads live sessions from the actual
// ocserv control socket and exercises the durable collector across reload.
func TestAnyConnectNativeAccountingStage(t *testing.T) {
	root := os.Getenv("ANTIMAGE_ANYCONNECT_NATIVE_ROOT")
	if root == "" {
		t.Skip("requires isolated native ocserv harness")
	}
	pid, err := anyConnectNativePID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strconv.Atoi(pid); err != nil {
		t.Fatalf("invalid ocserv PID: %v", err)
	}
	if os.Getenv("ANTIMAGE_ANYCONNECT_ACTION") == "admission" {
		configPath := filepath.Join(os.Getenv("ANTIMAGE_ANYCONNECT_NATIVE_STATE"), "anyconnect", "native", "session-helper.json")
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			return
		}
		if err := RunNativeSessionEventHelper([]string{configPath, "start"}); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := filepath.Join(os.Getenv("ANTIMAGE_ANYCONNECT_NATIVE_STATE"), "anyconnect", "native")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ocserv.pid"), []byte(pid), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := anyConnectUsageRuntimeConfig{InboundTag: "native", SocketPath: filepath.Join(root, "ocserv.sock"), Users: map[string]int64{"native-user": 7}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "usage-helper.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	action := os.Getenv("ANTIMAGE_ANYCONNECT_ACTION")
	s := New(Config{DataDir: os.Getenv("ANTIMAGE_ANYCONNECT_NATIVE_STATE")})
	if action == "quota-watch" {
		limit, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("ANTIMAGE_ANYCONNECT_QUOTA_BYTES")), 10, 64)
		if err != nil || limit != 52_428_800 {
			t.Fatalf("AnyConnect native quota must be exactly 50 MiB (52428800 bytes), got %q", os.Getenv("ANTIMAGE_ANYCONNECT_QUOTA_BYTES"))
		}
		var receipt struct {
			EffectiveTotal uint64 `json:"effective_total"`
		}
		receiptRaw, err := os.ReadFile(filepath.Join(os.Getenv("ANTIMAGE_ANYCONNECT_NATIVE_STATE"), "native-panel-receipt.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(receiptRaw, &receipt); err != nil || receipt.EffectiveTotal > math.MaxInt64 {
			t.Fatalf("invalid prior Panel effective usage receipt: %v", err)
		}
		policy := nativeSessionUserPolicy{Status: "active", DataLimit: limit, UsedTraffic: int64(receipt.EffectiveTotal)}
		helper, err := json.Marshal(nativeSessionHelperConfig{
			InboundTag: "native", Protocol: "anyconnect", Users: cfg.Users,
			Policies: map[string]nativeSessionUserPolicy{"native-user": policy},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "session-helper.json"), helper, 0600); err != nil {
			t.Fatal(err)
		}
		ready := filepath.Join(root, "native-quota-ready")
		if err := os.WriteFile(ready, []byte("ready\n"), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		workerDone := make(chan struct{})
		go func() {
			defer close(workerDone)
			s.runLocalAccountingWorker(ctx, "anyconnect", 100*time.Millisecond, time.Second,
				s.checkpointAnyConnectOffline, s.quotaCheckAnyConnectOffline)
		}()
		seen, disconnected := false, false
		deadline := time.Now().Add(3 * time.Minute)
		for time.Now().Before(deadline) {
			snapshots, snapshotErr := s.anyConnectOfflineSnapshots(ctx)
			if snapshotErr != nil {
				cancel()
				<-workerDone
				t.Fatal(snapshotErr)
			}
			active := false
			for _, snapshot := range snapshots {
				for _, session := range snapshot.Sessions {
					if session.Username == "native-user" {
						seen, active = true, true
					}
				}
			}
			if seen && !active {
				disconnected = true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		cancel()
		<-workerDone
		if !seen || !disconnected {
			t.Fatalf("production 50 MiB AnyConnect quota worker did not disconnect the real session: seen=%t disconnected=%t", seen, disconnected)
		}
		s.anyConnectUsageMu.Lock()
		rawDelta := s.anyConnectUsageBaseline[offlineOwnerKey(offlineUsageOwner{7, "native"})]
		s.anyConnectUsageMu.Unlock()
		effectiveDelta := nativeSessionEffectiveLiveUsage(policy, rawDelta)
		effectiveTotal := receipt.EffectiveTotal + uint64(effectiveDelta)
		overshoot := uint64(0)
		if effectiveTotal > uint64(limit) {
			overshoot = effectiveTotal - uint64(limit)
		}
		result := map[string]uint64{"limit_bytes": uint64(limit), "effective_before": receipt.EffectiveTotal,
			"raw_delta": rawDelta, "effective_after": effectiveTotal, "overshoot_bytes": overshoot}
		resultRaw, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "native-quota-result.json"), resultRaw, 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("production AnyConnect 50 MiB offline quota disconnected: raw_delta=%d effective_before=%d effective_after=%d limit=%d overshoot=%d", rawDelta, receipt.EffectiveTotal, effectiveTotal, limit, overshoot)
		return
	}
	if action == "collect-first" || action == "collect-next" {
		// ocserv updates its per-session counters asynchronously. Wait for a
		// real positive native counter after the driver has sent tunnel payload;
		// do not turn a just-connected, zero-counter session into a false pass.
		deadline := time.Now().Add(10 * time.Second)
		for {
			snapshots, snapshotErr := s.anyConnectOfflineSnapshots(context.Background())
			if snapshotErr != nil {
				t.Fatal(snapshotErr)
			}
			var nativeBytes uint64
			for _, snapshot := range snapshots {
				for _, session := range snapshot.Sessions {
					nativeBytes = ikev2SafeAdd(nativeBytes, ikev2SafeAdd(session.Received, session.Sent))
				}
			}
			if nativeBytes > 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("ocserv reported no positive native traffic counters after tunnel payload: snapshots=%+v", snapshots)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	batch, err := s.collectAnyConnectUserUsage(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.anyConnectUsagePending == nil || len(s.anyConnectUsagePending.Samples) != 1 || s.anyConnectUsagePending.Samples[0].UserID != 7 || s.anyConnectUsagePending.Samples[0].Value == 0 {
		t.Fatalf("production collector did not persist positive usage from live ocserv: batch=%v pending=%#v", batch, s.anyConnectUsagePending)
	}
	first := batch.GetBatchId()
	s = New(Config{DataDir: os.Getenv("ANTIMAGE_ANYCONNECT_NATIVE_STATE")})
	replay, err := s.collectAnyConnectUserUsage(context.Background(), nil)
	if err != nil || replay.GetBatchId() != first {
		t.Fatalf("pending batch changed across reload: batch=%v err=%v", replay, err)
	}
	if action == "ack" {
		var receipt struct {
			BatchIDs []string `json:"batch_ids"`
		}
		raw, err := os.ReadFile(filepath.Join(os.Getenv("ANTIMAGE_ANYCONNECT_NATIVE_STATE"), "native-panel-receipt.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &receipt); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, id := range receipt.BatchIDs {
			if id == first {
				found = true
			}
		}
		if !found {
			t.Fatal("refusing ACK without verified Panel DB receipt")
		}
		ack, err := s.ackAnyConnectUserUsage(context.Background(), &nodev1.AckUsageRequest{BatchId: first})
		if err != nil || !ack.GetAcknowledged() {
			t.Fatalf("DB-confirmed AnyConnect ACK: %v %v", ack, err)
		}
		t.Logf("DB-confirmed ACK pruned AnyConnect batch %s", first)
		return
	}
	if action == "collect-first" || action == "collect-next" || action == "collect-final" {
		name := "native-first-batch.pb"
		if action == "collect-next" {
			name = "native-next-batch.pb"
		} else if action == "collect-final" {
			name = "native-final-batch.pb"
		}
		raw, err := proto.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(os.Getenv("ANTIMAGE_ANYCONNECT_NATIVE_STATE"), name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("production ocserv collector durably persisted batch %s with %d bytes", first, s.anyConnectUsagePending.Samples[0].Value)
}
