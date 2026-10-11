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

func TestOpenVPNOfflineCheckpointRestartAndLostACK(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := New(Config{DataDir: dir})
	tag := "offline"
	root := filepath.Join(dir, "openvpn", openVPNRuntimeDirName(tag))
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	status := filepath.Join(root, "status.tsv")
	cfg, _ := json.Marshal(openVPNUsageRuntimeConfig{InboundTag: tag, StatusFile: status, Users: map[string]int64{"alice": 42}})
	if err := os.WriteFile(filepath.Join(root, "usage-helper.json"), cfg, 0600); err != nil {
		t.Fatal(err)
	}
	write := func(since string, value uint64) {
		t.Helper()
		raw := fmt.Sprintf("HEADER\tCLIENT_LIST\tCommon Name\tUsername\tClient ID\tConnected Since (time_t)\tBytes Received\tBytes Sent\nCLIENT_LIST\talice\talice\t7\t%s\t%d\t0\n", since, value)
		if err := os.WriteFile(status, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s.openVPNRuntimes[tag] = &openVPNProcess{}
	write("100", 100)
	if err := s.checkpointOpenVPNOffline(ctx); err != nil {
		t.Fatal(err)
	}
	if s.openVPNUsagePending != nil {
		t.Fatal("checkpoint created pending batch")
	}
	// Reused client ID and a NEW counter already exceeding the old value.
	write("200", 250)
	if err := s.checkpointOpenVPNOffline(ctx); err != nil {
		t.Fatal(err)
	}
	s = New(Config{DataDir: dir})
	s.openVPNRuntimes[tag] = &openVPNProcess{}
	first, err := s.collectOpenVPNUserUsage(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Stats) != 1 || first.Stats[0].Value != 350 {
		t.Fatalf("A+B: %v", first)
	}
	write("200", 300)
	if err := s.checkpointOpenVPNOffline(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(status, []byte("HEADER\tCLIENT_LIST\tUsername\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s = New(Config{DataDir: dir})
	replay, err := s.collectOpenVPNUserUsage(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if replay.BatchId != first.BatchId || replay.Stats[0].Value != 350 {
		t.Fatalf("lost ACK replay: %v", replay)
	}
	ack, err := s.ackOpenVPNUserUsage(ctx, &nodev1.AckUsageRequest{BatchId: first.BatchId})
	if err != nil || !ack.Acknowledged {
		t.Fatalf("ACK: %v %v", ack, err)
	}
	next, err := s.collectOpenVPNUserUsage(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Stats) != 1 || next.Stats[0].Value != 50 {
		t.Fatalf("newer ended-session usage: %v", next)
	}
}
