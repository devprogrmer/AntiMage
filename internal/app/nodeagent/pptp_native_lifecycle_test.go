//go:build linux

package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

// TestPPTPNativeAccountingStage is driven by the privileged native protocol
// harness against a real pptpd client session and kernel PPP interface.
func TestPPTPNativeAccountingStage(t *testing.T) {
	stateDir := strings.TrimSpace(os.Getenv("ANTIMAGE_PPTP_NATIVE_STATE"))
	action := strings.TrimSpace(os.Getenv("ANTIMAGE_PPTP_NATIVE_ACTION"))
	if stateDir == "" || action == "" {
		t.Skip("requires isolated native PPTP harness")
	}
	quotaLimit := int64(0)
	var err error
	if raw := strings.TrimSpace(os.Getenv("ANTIMAGE_PPTP_QUOTA_BYTES")); raw != "" {
		quotaLimit, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || quotaLimit < 0 {
			t.Fatalf("invalid PPTP quota %q", raw)
		}
	}
	user := pptpRuntimeUser{UserID: 7, Username: "native-pptp", VPNUsername: "native-pptp",
		Password: "native-pptp-secret", IPv4Address: "10.68.0.2", Status: "active"}
	if quotaLimit > 0 {
		user.DataLimit = &quotaLimit
	}
	inbound := pptpRuntimeInbound{Tag: "native-pptp", Port: defaultPPTPPort,
		Settings: map[string]any{"ipv4_pool_cidr": "10.68.0.0/24", "mtu": 1200, "mru": 1200},
		Users:    []pptpRuntimeUser{user}}
	s := New(Config{DataDir: stateDir})
	switch action {
	case "prepare":
		helperBinary := strings.TrimSpace(os.Getenv("ANTIMAGE_NODE_HELPER_BINARY"))
		if helperBinary == "" {
			t.Fatal("native helper binary is required for PPP session hooks")
		}
		oldExecutable := pppSessionHelperExecutable
		pppSessionHelperExecutable = func() (string, error) { return helperBinary, nil }
		defer func() { pppSessionHelperExecutable = oldExecutable }()
		configPath, err := s.preparePPTPInbound(inbound, nativeRuntimeSessionCallback{})
		if err != nil {
			t.Fatal(err)
		}
		secrets := renderPPTPCHAPSecrets(inbound.Users)
		secrets += l2TPConfigQuote("antimage-pptp") + "\t" + l2TPConfigQuote("native-pptp") + "\t" + l2TPConfigQuote(user.Password) + "\t*\n"
		if err := installPPTPSystemCHAPSecrets(secrets); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stateDir, "pptpd-config-path"), []byte(configPath), 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("production PPTP configuration prepared: %s", configPath)
	case "cleanup":
		if err := clearPPTPSystemCHAPSecrets(); err != nil {
			t.Fatal(err)
		}
	case "collect":
		batch, err := s.collectPPTPUserUsage(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if pptpNativeUsageValue(batch) == 0 {
			t.Fatalf("production collector found no native PPP traffic: %v", batch)
		}
		t.Logf("production PPTP collector persisted bytes=%d batch=%s", pptpNativeUsageValue(batch), batch.GetBatchId())
	case "ack":
		s.pptpUsageMu.Lock()
		if err := s.ensurePPTPUsageStateLoadedLocked(); err != nil {
			s.pptpUsageMu.Unlock()
			t.Fatal(err)
		}
		pending := s.pptpUsagePending
		s.pptpUsageMu.Unlock()
		if pending == nil {
			t.Fatal("expected durable PPTP batch before ACK")
		}
		ack, err := s.ackPPTPUserUsage(context.Background(), &nodev1.AckUsageRequest{BatchId: pending.BatchID})
		if err != nil || !ack.GetAcknowledged() {
			t.Fatalf("ACK: %v %v", ack, err)
		}
		t.Logf("production ACK advanced durable PPP baseline for batch %s", pending.BatchID)
	case "quota-watch":
		if quotaLimit <= 0 {
			t.Fatal("quota-watch requires a positive quota")
		}
		ctx, cancel := context.WithCancel(context.Background())
		workerDone := make(chan struct{})
		go func() {
			defer close(workerDone)
			s.runLocalAccountingWorker(ctx, "ppp", 100*time.Millisecond, time.Second,
				s.checkpointPPPOffline, s.quotaCheckPPPOffline)
		}()
		deadline := time.Now().Add(3 * time.Minute)
		removed := false
		for time.Now().Before(deadline) {
			raw, queryErr := pppOfflineQuery(ctx)
			if queryErr != nil && ctx.Err() == nil {
				cancel()
				<-workerDone
				t.Fatalf("query native PPP sessions: %v", queryErr)
			}
			present := false
			for _, session := range parsePPTPPPPInterfaces(string(raw)) {
				present = present || session.PeerIP == user.IPv4Address
			}
			if !present {
				removed = true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		cancel()
		<-workerDone
		if !removed {
			t.Fatal("production PPP quota worker did not disconnect the native PPTP session")
		}
		root := filepath.Join(stateDir, "pptp", pptpRuntimeDirName(inbound.Tag), "ppp-accounting")
		activeDir := filepath.Join(root, "active")
		activeEntries, err := os.ReadDir(activeDir)
		if err != nil {
			t.Fatal(err)
		}
		activeInterface := ""
		var activeRecord pppOfflineSession
		for _, entry := range activeEntries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			raw, readErr := os.ReadFile(filepath.Join(activeDir, entry.Name()))
			if readErr != nil {
				t.Fatal(readErr)
			}
			var record pppOfflineSession
			if err := json.Unmarshal(raw, &record); err != nil {
				t.Fatal(err)
			}
			if record.UserID != user.UserID || record.PeerIP != user.IPv4Address {
				continue
			}
			if _, err := os.Stat(filepath.Join(root, "final", record.ID+".json")); err == nil {
				continue
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
			activeInterface, activeRecord = record.Interface, record
		}
		if activeInterface == "" || activeRecord.ID == "" {
			t.Fatal("no durable active PPTP session record found after quota disconnect")
		}
		finalized := false
		finalDeadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(finalDeadline) {
			_, statErr := os.Stat(filepath.Join(root, "final", activeRecord.ID+".json"))
			if statErr == nil {
				finalized = true
				break
			} else if !os.IsNotExist(statErr) {
				t.Fatal(statErr)
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !finalized {
			t.Fatal("PPTP disconnect hook did not durably record final PPP counters")
		}
		if err := s.checkpointPPPOffline(context.Background()); err != nil {
			t.Fatal(err)
		}
		batch, err := s.collectPPTPUserUsage(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		total := pptpNativeUsageValue(batch)
		limit := uint64(quotaLimit)
		if total < limit || total-limit > 2<<20 {
			raw, _ := json.Marshal(batch)
			t.Fatalf("native PPTP quota overshoot outside 2 MiB bound: raw=%d limit=%d batch=%s", total, limit, raw)
		}
		t.Logf("production PPTP quota worker disconnected session at raw=%d bytes, limit=%d, overshoot=%d", total, limit, total-limit)
	default:
		t.Fatal(fmt.Sprintf("unknown native PPTP action %q", action))
	}
}

func pptpNativeUsageValue(batch *nodev1.UserUsageBatch) uint64 {
	for _, sample := range batch.GetStats() {
		if sample.GetUid() == "pptp:7" {
			return sample.GetValue()
		}
	}
	return 0
}
