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
	action := os.Getenv("ANTIMAGE_OPENVPN_ACTION")
	if action == "admission" {
		configPath := filepath.Join(dir, "session-helper.json")
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			return // The driver installs the quota policy after its initial traffic.
		}
		if err := RunNativeSessionEventHelper([]string{configPath, "start"}); err != nil {
			t.Fatal(err)
		}
		return
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
	if action == "quota" || action == "quota-watch" {
		var clientsBefore []openVPNStatusClient
		var statusErr error
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			statusBefore, err := os.ReadFile(cfg.StatusFile)
			if err != nil {
				statusErr = err
				time.Sleep(100 * time.Millisecond)
				continue
			}
			clientsBefore, statusErr = parseOpenVPNStatusV3(string(statusBefore))
			if statusErr == nil && len(clientsBefore) == 1 && clientsBefore[0].ClientID != "" {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if statusErr != nil || len(clientsBefore) != 1 || clientsBefore[0].ClientID == "" {
			t.Fatalf("need one identified native client before quota: clients=%v err=%v", clientsBefore, statusErr)
		}
		limit := int64(1)
		if action == "quota-watch" {
			var err error
			limit, err = strconv.ParseInt(os.Getenv("ANTIMAGE_OPENVPN_QUOTA_BYTES"), 10, 64)
			if err != nil || limit <= 0 {
				t.Fatalf("invalid native OpenVPN quota: %d (%v)", limit, err)
			}
		}
		helper, err := json.Marshal(nativeSessionHelperConfig{
			InboundTag: "native", Protocol: "openvpn", Users: cfg.Users,
			Policies:          map[string]nativeSessionUserPolicy{"antimage-native-client": {Status: "active", DataLimit: limit}},
			ManagementNetwork: "tcp", ManagementAddress: "127.0.0.1:11941",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "session-helper.json"), helper, 0600); err != nil {
			t.Fatal(err)
		}
		s := New(Config{DataDir: os.Getenv("ANTIMAGE_OPENVPN_NATIVE_STATE")})
		if action == "quota-watch" {
			identity, err := offlineProcessIdentity(pid)
			if err != nil {
				t.Fatal(err)
			}
			if err := offlineDurableJSON(filepath.Join(dir, "accounting-generation.json"), identity); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			workerDone := make(chan struct{})
			go func() {
				defer close(workerDone)
				s.runLocalAccountingWorker(ctx, "openvpn", 100*time.Millisecond, time.Second,
					s.checkpointOpenVPNOffline, s.quotaCheckOpenVPNOffline)
			}()
			deadline := time.Now().Add(3 * time.Minute)
			disconnected := false
			stableEmptySince := time.Time{}
			for time.Now().Before(deadline) {
				status, err := os.ReadFile(cfg.StatusFile)
				if err != nil {
					t.Fatal(err)
				}
				clients, err := parseOpenVPNStatusV3(string(status))
				if err != nil {
					t.Fatal(err)
				}
				present := false
				for _, client := range clients {
					if client.Username == "antimage-native-client" {
						present = true
					}
				}
				if !present {
					disconnected = true
					if stableEmptySince.IsZero() {
						stableEmptySince = time.Now()
					}
					if time.Since(stableEmptySince) >= 5*time.Second {
						break
					}
				} else {
					stableEmptySince = time.Time{}
				}
				time.Sleep(100 * time.Millisecond)
			}
			if !disconnected || stableEmptySince.IsZero() || time.Since(stableEmptySince) < 5*time.Second {
				t.Fatalf("native OpenVPN quota did not keep the client disconnected after reconnect attempts")
			}
			cancel()
			<-workerDone
			if _, err := s.collectOpenVPNUserUsage(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			used := s.openVPNUsageBaseline[offlineAccountingTotalKey(7, "native")]
			if used < uint64(limit) || used-uint64(limit) > 2<<20 {
				t.Fatalf("native 50 MiB OpenVPN quota overshoot outside 2 MiB bound: used=%d limit=%d baseline=%#v", used, limit, s.openVPNUsageBaseline)
			}
			t.Logf("production OpenVPN quota worker stopped native/reconnected sessions at raw=%d bytes, limit=%d, overshoot=%d bytes", used, limit, used-uint64(limit))
			return
		}
		if err := s.quotaCheckOpenVPNOffline(context.Background()); err != nil {
			t.Fatal(err)
		}
		deadline = time.Now().Add(5 * time.Second)
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
