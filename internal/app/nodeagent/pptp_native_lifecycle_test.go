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
	"google.golang.org/protobuf/proto"
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
		Password: "native-pptp-secret", IPv4Address: "10.68.0.2", Status: "active",
		UsageCoefficient: 1.5, InboundCoefficient: 2}
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
		if err := installPPTPSystemCHAPSecrets(secrets); err != nil {
			t.Fatal(err)
		}
		helper, err := filepath.Abs(helperBinary)
		if err != nil {
			t.Fatal(err)
		}
		if err := installPPTPSystemIPPreUpHook(helper, filepath.Join(filepath.Dir(configPath), "session-helper.json")); err != nil {
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
		if err := clearPPTPSystemIPPreUpHook(); err != nil {
			t.Fatal(err)
		}
	case "collect", "collect-first", "collect-next", "collect-final":
		batch, err := s.collectPPTPUserUsage(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if pptpNativeUsageValue(batch) == 0 {
			t.Fatalf("production collector found no native PPP traffic: %v", batch)
		}
		if action != "collect" {
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
			if err := os.WriteFile(filepath.Join(stateDir, name), raw, 0600); err != nil {
				t.Fatal(err)
			}
			batchFiles, err := filepath.Glob(filepath.Join(stateDir, "native-batch-*.pb"))
			if err != nil {
				t.Fatal(err)
			}
			batchName := filepath.Join(stateDir, fmt.Sprintf("native-batch-%06d.pb", len(batchFiles)+1))
			if err := os.WriteFile(batchName, raw, 0600); err != nil {
				t.Fatal(err)
			}
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
		restarted := New(Config{DataDir: stateDir})
		restarted.pptpUsageMu.Lock()
		if err := restarted.ensurePPTPUsageStateLoadedLocked(); err != nil {
			restarted.pptpUsageMu.Unlock()
			t.Fatal(err)
		}
		stillPending := restarted.pptpUsagePending != nil
		lastAcked := restarted.pptpUsageLastAckedBatchID
		restarted.pptpUsageMu.Unlock()
		if stillPending || lastAcked != pending.BatchID {
			t.Fatalf("PPTP ACK was not durably applied after restart: pending=%v last_acked=%q want=%q", stillPending, lastAcked, pending.BatchID)
		}
		t.Logf("production ACK advanced durable PPP baseline for batch %s", pending.BatchID)
	case "quota-watch":
		if quotaLimit <= 0 {
			t.Fatal("quota-watch requires a positive quota")
		}
		root := filepath.Join(stateDir, "pptp", pptpRuntimeDirName(inbound.Tag), "ppp-accounting")
		pidBytes, err := os.ReadFile("/run/ppp0.pid")
		if err != nil {
			t.Fatalf("read native pppd PID before quota enforcement: %v", err)
		}
		pppdPID := strings.TrimSpace(string(pidBytes))
		if pppdPID == "" {
			t.Fatal("native pppd PID is empty before quota enforcement")
		}
		activeDir := filepath.Join(root, "active")
		activeEntries, err := os.ReadDir(activeDir)
		if err != nil {
			t.Fatal(err)
		}
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
			if record.UserID != user.UserID || record.PeerIP != user.IPv4Address || record.InboundTag != inbound.Tag || !strings.Contains(record.Process, ":"+pppdPID+":") {
				continue
			}
			if activeRecord.ID != "" {
				t.Fatalf("multiple active PPTP records match pppd PID %s: %+v and %+v", pppdPID, activeRecord, record)
			}
			activeRecord = record
		}
		if activeRecord.ID == "" {
			t.Fatalf("no active PPTP record matches native pppd PID %s", pppdPID)
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
		finalized := false
		finalDeadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(finalDeadline) {
			finalPath := filepath.Join(root, "final", activeRecord.ID+".json")
			raw, readErr := os.ReadFile(finalPath)
			if readErr == nil {
				var finalRecord pppOfflineSession
				if err := json.Unmarshal(raw, &finalRecord); err != nil {
					t.Fatal(err)
				}
				if !finalRecord.Final || finalRecord.ID != activeRecord.ID || finalRecord.UserID != user.UserID || finalRecord.InboundTag != inbound.Tag || finalRecord.PeerIP != user.IPv4Address || finalRecord.Interface != activeRecord.Interface || finalRecord.Process != activeRecord.Process {
					t.Fatalf("final PPTP session record does not match disconnected session: active=%+v final=%+v", activeRecord, finalRecord)
				}
				policy := nativeSessionUserPolicy{UsageCoefficient: user.UsageCoefficient, InboundCoefficient: user.InboundCoefficient}
				previousEffective := nativePanelEffectiveUsage(t, stateDir)
				if previousEffective >= uint64(quotaLimit) {
					t.Fatalf("PPTP batches before quota traffic already exhausted quota: previous=%d limit=%d", previousEffective, quotaLimit)
				}
				effective := nativeSessionEffectiveLiveUsage(policy, finalRecord.Total)
				if effective < uint64(quotaLimit) || effective-uint64(quotaLimit) > 6<<20 {
					t.Fatalf("final PPTP session counters outside effective quota bound: raw=%d effective=%d previous=%d limit=%d", finalRecord.Total, effective, previousEffective, quotaLimit)
				}
				finalized = true
				break
			} else if !os.IsNotExist(readErr) {
				t.Fatal(readErr)
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
		policy := nativeSessionUserPolicy{UsageCoefficient: user.UsageCoefficient, InboundCoefficient: user.InboundCoefficient}
		effective := nativeSessionEffectiveLiveUsage(policy, total)
		limit := uint64(quotaLimit)
		previousEffective := nativePanelEffectiveUsage(t, stateDir)
		if previousEffective >= limit {
			t.Fatalf("PPTP batches before quota traffic already exhausted quota: previous=%d limit=%d", previousEffective, limit)
		}
		remainingEffective := limit - previousEffective
		if effective < remainingEffective || effective-remainingEffective > 6<<20 {
			raw, _ := json.Marshal(batch)
			t.Fatalf("native PPTP quota delta outside 6 MiB effective bound: raw=%d effective=%d previous=%d remaining=%d limit=%d batch=%s", total, effective, previousEffective, remainingEffective, limit, raw)
		}
		t.Logf("production PPTP quota worker disconnected session at raw=%d effective=%d bytes, limit=%d", total, effective, limit)
		for _, line := range s.snapshotLogs() {
			t.Logf("node runtime: %s", line)
		}
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
