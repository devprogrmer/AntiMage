package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"os"
	"path/filepath"
	"testing"
)

func TestPPPFinalCaptureDisappearedInterfaceRestartLostACK(t *testing.T) {
	previousQuery, previousCounter, previousIdentity := pppOfflineQuery, pppOfflineReadCounter, pppOfflineReadIdentity
	t.Cleanup(func() {
		pppOfflineQuery = previousQuery
		pppOfflineReadCounter = previousCounter
		pppOfflineReadIdentity = previousIdentity
	})
	for _, protocol := range []string{"l2tp", "pptp"} {
		t.Run(protocol, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			root := filepath.Join(dir, protocol, "fixture")
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			if err := offlineDurableJSON(filepath.Join(root, "usage-helper.json"), map[string]any{"inbound_tag": "tag", "users": map[string]int64{"10.0.0.2": 42}}); err != nil {
				t.Fatal(err)
			}
			first := pppOfflineSession{ID: "session-a", UserID: 42, InboundTag: "tag", Interface: "ppp0", PeerIP: "10.0.0.2", Identity: "boot:1", Process: "boot:123:456"}
			if err := pppOfflineSessionEvent(root, "start", first); err != nil {
				t.Fatal(err)
			}
			s := New(Config{DataDir: dir})
			current := uint64(100)
			interfaceIdentity := "boot:1"
			pppOfflineReadCounter = func(_ string, counter string) (uint64, error) {
				if counter == "rx_bytes" {
					return current, nil
				}
				return 0, nil
			}
			pppOfflineReadIdentity = func(string) (string, error) { return interfaceIdentity, nil }
			pppOfflineQuery = func(context.Context) ([]byte, error) {
				return []byte("20: ppp0 inet 10.0.0.1 peer 10.0.0.2/32 scope global ppp0"), nil
			}
			if err := s.checkpointPPPOffline(ctx); err != nil {
				t.Fatal(err)
			}
			first.Total = 150
			if err := pppOfflineSessionEvent(root, "stop", first); err != nil {
				t.Fatal(err)
			}
			// New session reuses the interface and its counter overtakes A.
			second := first
			second.ID = "session-b"
			second.Identity = "boot:2"
			second.Process = "boot:124:999"
			second.Total = 300
			if err := pppOfflineSessionEvent(root, "start", second); err != nil {
				t.Fatal(err)
			}
			current = 250
			interfaceIdentity = "boot:2"
			if err := s.checkpointPPPOffline(ctx); err != nil {
				t.Fatal(err)
			}
			if err := pppOfflineSessionEvent(root, "stop", second); err != nil {
				t.Fatal(err)
			}
			pppOfflineQuery = func(context.Context) ([]byte, error) { return nil, fmt.Errorf("interface command unavailable") }
			s = New(Config{DataDir: dir}) // No runtime maps, no interface, only durable helpers.
			collect := func() (*nodev1.UserUsageBatch, error) {
				if protocol == "l2tp" {
					return s.collectL2TPUserUsage(ctx, nil)
				}
				return s.collectPPTPUserUsage(ctx, nil)
			}
			batch, err := collect()
			if err != nil {
				t.Fatal(err)
			}
			if len(batch.Stats) != 1 || batch.Stats[0].Value != 450 {
				t.Fatalf("final A+B: %v", batch)
			}
			second.Total = 350
			if err := pppOfflineSessionEvent(root, "stop", second); err != nil {
				t.Fatal(err)
			}
			replay, err := collect()
			if err != nil {
				t.Fatal(err)
			}
			if replay.BatchId != batch.BatchId || replay.Stats[0].Value != 450 {
				t.Fatalf("immutable replay: %v", replay)
			}
			s = New(Config{DataDir: dir})
			replay, err = collect()
			if err != nil {
				t.Fatal(err)
			}
			if replay.BatchId != batch.BatchId {
				t.Fatal("lost ACK batch identity")
			}
			var ack *nodev1.AckUsageResponse
			if protocol == "l2tp" {
				ack, err = s.ackL2TPUserUsage(ctx, &nodev1.AckUsageRequest{BatchId: batch.BatchId})
			} else {
				ack, err = s.ackPPTPUserUsage(ctx, &nodev1.AckUsageRequest{BatchId: batch.BatchId})
			}
			if err != nil || !ack.Acknowledged {
				t.Fatalf("ACK %v %v", ack, err)
			}
			next, err := collect()
			if err != nil {
				t.Fatal(err)
			}
			if len(next.Stats) != 1 || next.Stats[0].Value != 50 {
				t.Fatalf("retained newer final: %v", next)
			}
		})
	}
}

func TestPPPFinalRejectsMismatchedStopMetadata(t *testing.T) {
	root := t.TempDir()
	record := pppOfflineSession{ID: "a", UserID: 42, InboundTag: "tag", Interface: "ppp0", PeerIP: "peer", Process: "boot:123:456"}
	if err := pppOfflineSessionEvent(root, "start", record); err != nil {
		t.Fatal(err)
	}
	record.Process = "boot:123:999"
	if err := pppOfflineSessionEvent(root, "stop", record); err == nil {
		t.Fatal("accepted reused PID")
	}
	files, _ := filepath.Glob(filepath.Join(root, "ppp-accounting", "final", "*.json"))
	if len(files) != 0 {
		t.Fatal("persisted untrusted final")
	}
}

func TestPPPFinalRecordPersistsBeforeCallback(t *testing.T) {
	// Exercise the actual helper stop path without requiring a live interface.
	root := t.TempDir()
	cfg := nativeSessionHelperConfig{Protocol: "pptp", InboundTag: "tag", Users: map[string]int64{"alice": 42}, StateDir: filepath.Join(root, "sessions")}
	raw, _ := json.Marshal(cfg)
	path := filepath.Join(root, "session-helper.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	pid := "123"
	identity := "fixture-boot:123:456"
	previousProcess := pppOfflineReadProcess
	pppOfflineReadProcess = func(value string) (string, error) {
		if value != pid {
			return "", fmt.Errorf("unexpected PID")
		}
		return identity, nil
	}
	t.Cleanup(func() { pppOfflineReadProcess = previousProcess })
	record := pppOfflineSession{ID: "trusted", UserID: 42, InboundTag: "tag", Interface: "ppp0", PeerIP: "10.0.0.2", Process: identity}
	if err := pppOfflineSessionEvent(root, "start", record); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PEERNAME", "alice")
	t.Setenv("IFNAME", "ppp0")
	t.Setenv("IPREMOTE", "10.0.0.2")
	t.Setenv("PPPD_PID", pid)
	t.Setenv("BYTES_SENT", "70")
	t.Setenv("BYTES_RCVD", "80")
	// User was removed from the current policy, but the trusted start record
	// still owns final accounting.
	cfg.Users = map[string]int64{}
	if err := offlineDurableJSON(path, cfg); err != nil {
		t.Fatal(err)
	}
	// The callback/session state is absent; durable accounting must still happen.
	if err := RunNativeSessionEventHelper([]string{path, "stop"}); err != nil {
		t.Fatal(err)
	}
	records, err := pppOfflineFinalRecords(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Total != 150 {
		t.Fatalf("final records: %v", records)
	}
}
