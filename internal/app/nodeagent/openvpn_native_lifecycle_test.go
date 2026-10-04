package nodeagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestOpenVPNNativeAccountingStage connects production accounting to a real
// OpenVPN status-v3 file written by the isolated native tunnel harness.
func TestOpenVPNNativeAccountingStage(t *testing.T) {
	root := os.Getenv("ANTIMAGE_OPENVPN_NATIVE_ROOT")
	if root == "" {
		t.Skip("requires isolated native OpenVPN harness")
	}
	pid := os.Getenv("ANTIMAGE_OPENVPN_NATIVE_PID")
	if _, err := strconv.Atoi(pid); err != nil {
		t.Fatalf("invalid OpenVPN PID: %v", err)
	}
	dir := filepath.Join(os.Getenv("ANTIMAGE_OPENVPN_NATIVE_STATE"), "openvpn", "native")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := openVPNUsageRuntimeConfig{
		InboundTag: "native",
		StatusFile: filepath.Join(root, "openvpn.status"),
		Users:      map[string]int64{"antimage-native-client": 7},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "usage-helper.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("ANTIMAGE_OPENVPN_ACTION") == "quota" {
		statusBefore, err := os.ReadFile(cfg.StatusFile)
		if err != nil {
			t.Fatal(err)
		}
		clientsBefore, err := parseOpenVPNStatusV3(string(statusBefore))
		if err != nil || len(clientsBefore) != 1 || clientsBefore[0].ClientID == "" {
			t.Fatalf("need one identified native client before quota: clients=%v err=%v", clientsBefore, err)
		}
		helper, err := json.Marshal(nativeSessionHelperConfig{
			InboundTag: "native", Protocol: "openvpn", Users: cfg.Users,
			Policies:          map[string]nativeSessionUserPolicy{"antimage-native-client": {Status: "active", DataLimit: 1}},
			ManagementNetwork: "tcp", ManagementAddress: "127.0.0.1:11941",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "session-helper.json"), helper, 0600); err != nil {
			t.Fatal(err)
		}
		s := New(Config{DataDir: os.Getenv("ANTIMAGE_OPENVPN_NATIVE_STATE")})
		if err := s.quotaCheckOpenVPNOffline(context.Background()); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			status, err := os.ReadFile(cfg.StatusFile)
			if err == nil {
				clients, parseErr := parseOpenVPNStatusV3(string(status))
				if parseErr == nil && (len(clients) == 0 || clients[0].ClientID != clientsBefore[0].ClientID) {
					t.Logf("production OpenVPN offline quota checkpointed and terminated native client session %s; status now has %d sessions", clientsBefore[0].ClientID, len(clients))
					return
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("production OpenVPN offline quota did not replace the terminated over-limit session %s", clientsBefore[0].ClientID)
	}
	identity, err := offlineProcessIdentity(pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := offlineDurableJSON(filepath.Join(dir, "accounting-generation.json"), identity); err != nil {
		t.Fatal(err)
	}
	s := New(Config{DataDir: os.Getenv("ANTIMAGE_OPENVPN_NATIVE_STATE")})
	if _, err := s.collectOpenVPNUserUsage(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if s.openVPNUsagePending == nil || len(s.openVPNUsagePending.Samples) != 1 || s.openVPNUsagePending.Samples[0].UserID != 7 || s.openVPNUsagePending.Samples[0].Value == 0 {
		t.Fatalf("production collector did not persist positive usage from live status: %#v", s.openVPNUsagePending)
	}
	first := s.openVPNUsagePending.BatchID
	// A fresh Server instance models process restart and must replay the same
	// durable pending batch rather than counting the status snapshot twice.
	s = New(Config{DataDir: os.Getenv("ANTIMAGE_OPENVPN_NATIVE_STATE")})
	batch, err := s.collectOpenVPNUserUsage(context.Background(), nil)
	if err != nil || batch.GetBatchId() != first {
		t.Fatalf("pending batch changed across restart: batch=%v err=%v", batch, err)
	}
	t.Logf("production OpenVPN status collector durably persisted batch %s with %d bytes", first, s.openVPNUsagePending.Samples[0].Value)
}
