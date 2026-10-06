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

// TestL2TPNativeAccountingStage is driven by the isolated native L2TP harness
// against xl2tpd, pppd, and a real kernel PPP interface.
func TestL2TPNativeAccountingStage(t *testing.T) {
	stateDir := strings.TrimSpace(os.Getenv("ANTIMAGE_L2TP_NATIVE_STATE"))
	action := strings.TrimSpace(os.Getenv("ANTIMAGE_L2TP_NATIVE_ACTION"))
	if stateDir == "" || action == "" {
		t.Skip("requires isolated native L2TP harness")
	}
	quotaLimit := int64(0)
	var err error
	if raw := strings.TrimSpace(os.Getenv("ANTIMAGE_L2TP_QUOTA_BYTES")); raw != "" {
		quotaLimit, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || quotaLimit < 0 {
			t.Fatalf("invalid L2TP quota %q", raw)
		}
	}
	user := l2TPRuntimeUser{UserID: 7, Username: "native-l2tp", VPNUsername: "native-l2tp",
		Password: "native-l2tp-secret", IPv4Address: "10.67.0.2", Status: "active",
		UsageCoefficient: 1.5, InboundCoefficient: 2}
	if quotaLimit > 0 {
		user.DataLimit = &quotaLimit
	}
	inbound := l2TPRuntimeInbound{Tag: "native-l2tp", Port: defaultL2TPPort,
		Settings: map[string]any{"ipsec_psk": "native-l2tp-psk", "ipv4_pool_cidr": "10.67.0.0/24", "mtu": 1200, "mru": 1200},
		Users:    []l2TPRuntimeUser{user}}
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
		callback := nativeRuntimeSessionCallback{
			URL:    strings.TrimSpace(os.Getenv("ANTIMAGE_L2TP_SESSION_CALLBACK_URL")),
			Token:  strings.TrimSpace(os.Getenv("ANTIMAGE_L2TP_SESSION_CALLBACK_TOKEN")),
			NodeID: 7,
		}
		if callback.URL != "" && callback.Token == "" {
			t.Fatal("L2TP Panel session callback token is required with callback URL")
		}
		files, err := s.prepareL2TPInbound(inbound, callback)
		if err != nil {
			t.Fatal(err)
		}
		if err := installL2TPSystemConfig(files.IPSecConfig, files.IPSecSecrets, files.XL2TPConfig, files.CHAPSecrets); err != nil {
			t.Fatal(err)
		}
		helper, err := filepath.Abs(helperBinary)
		if err != nil {
			t.Fatal(err)
		}
		if err := installL2TPSystemIPPreUpHook(helper, files.SessionConfig); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stateDir, "xl2tpd-config-path"), []byte(files.XL2TPConfig), 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("production L2TP configuration prepared: %s", files.XL2TPConfig)
	case "ipsec-start":
		if err := applyL2TPIPSec(s, inbound.Tag); err != nil {
			t.Fatal(err)
		}
		t.Log("production strongSwan IPsec runtime started from generated L2TP configuration")
	case "cleanup":
		if err := clearL2TPSystemIPPreUpHook(); err != nil {
			t.Fatal(err)
		}
		for _, managed := range []struct{ path, start, end string }{
			{l2TPIPSecConfigPath, l2TPIPSecBlockStart, l2TPIPSecBlockEnd},
			{l2TPIPSecSecretsPath, l2TPSecretBlockStart, l2TPSecretBlockEnd},
			{l2TPCHAPSecretsPath, l2TPCHAPBlockStart, l2TPCHAPBlockEnd},
		} {
			if err := updateL2TPManagedBlock(managed.path, managed.start, managed.end, ""); err != nil {
				t.Fatal(err)
			}
		}
	case "collect", "collect-first", "collect-next", "collect-final":
		batch, err := s.collectL2TPUserUsage(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if l2tpNativeUsageValue(batch) == 0 {
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
		}
		t.Logf("production L2TP collector persisted bytes=%d batch=%s", l2tpNativeUsageValue(batch), batch.GetBatchId())
	case "ack":
		s.l2TPUsageMu.Lock()
		if err := s.ensureL2TPUsageStateLoadedLocked(); err != nil {
			s.l2TPUsageMu.Unlock()
			t.Fatal(err)
		}
		pending := s.l2TPUsagePending
		s.l2TPUsageMu.Unlock()
		if pending == nil {
			t.Fatal("expected durable L2TP batch before ACK")
		}
		ack, err := s.ackL2TPUserUsage(context.Background(), &nodev1.AckUsageRequest{BatchId: pending.BatchID})
		if err != nil || !ack.GetAcknowledged() {
			t.Fatalf("ACK: %v %v", ack, err)
		}
		s.l2TPUsageMu.Lock()
		if err := s.ensureL2TPUsageStateLoadedLocked(); err != nil {
			s.l2TPUsageMu.Unlock()
			t.Fatal(err)
		}
		stillPending := s.l2TPUsagePending != nil
		s.l2TPUsageMu.Unlock()
		if stillPending {
			t.Fatal("durable L2TP pending batch was not pruned after ACK")
		}
		t.Logf("production ACK advanced durable PPP baseline for batch %s", pending.BatchID)
	case "quota-watch":
		if quotaLimit <= 0 {
			t.Fatal("quota-watch requires a positive quota")
		}
		root := filepath.Join(stateDir, "l2tp", l2TPRuntimeDirName(inbound.Tag), "ppp-accounting")
		pidBytes, err := os.ReadFile("/run/ppp0.pid")
		if err != nil {
			t.Fatalf("read native pppd PID before quota enforcement: %v", err)
		}
		pppdPID := strings.TrimSpace(string(pidBytes))
		if pppdPID == "" {
			t.Fatal("native pppd PID is empty before quota enforcement")
		}
		activeEntries, err := os.ReadDir(filepath.Join(root, "active"))
		if err != nil {
			t.Fatal(err)
		}
		var activeRecord pppOfflineSession
		for _, entry := range activeEntries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			raw, readErr := os.ReadFile(filepath.Join(root, "active", entry.Name()))
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
				t.Fatalf("multiple active L2TP records match pppd PID %s: %+v and %+v", pppdPID, activeRecord, record)
			}
			activeRecord = record
		}
		if activeRecord.ID == "" {
			t.Fatalf("no active L2TP record matches native pppd PID %s", pppdPID)
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
			for _, session := range parseL2TPPPPInterfaces(string(raw)) {
				if session.PeerIP == user.IPv4Address {
					removed = false
					goto keepWaiting
				}
			}
			removed = true
			break
		keepWaiting:
			time.Sleep(100 * time.Millisecond)
		}
		cancel()
		<-workerDone
		if !removed {
			t.Fatal("production PPP quota worker did not disconnect the native L2TP session")
		}
		finalPath := filepath.Join(root, "final", activeRecord.ID+".json")
		deadline = time.Now().Add(10 * time.Second)
		var final pppOfflineSession
		for time.Now().Before(deadline) {
			raw, readErr := os.ReadFile(finalPath)
			if readErr == nil {
				if err := json.Unmarshal(raw, &final); err != nil {
					t.Fatal(err)
				}
				break
			}
			if !os.IsNotExist(readErr) {
				t.Fatal(readErr)
			}
			time.Sleep(50 * time.Millisecond)
		}
		policy := nativeSessionUserPolicy{UsageCoefficient: user.UsageCoefficient, InboundCoefficient: user.InboundCoefficient}
		previousEffective := nativePanelEffectiveUsage(t, stateDir)
		if previousEffective >= uint64(quotaLimit) {
			t.Fatalf("L2TP batches before quota traffic already exhausted quota: previous=%d limit=%d", previousEffective, quotaLimit)
		}
		remainingEffective := uint64(quotaLimit) - previousEffective
		finalEffective := nativeSessionEffectiveLiveUsage(policy, final.Total)
		if !final.Final || final.ID != activeRecord.ID || finalEffective < remainingEffective || finalEffective-remainingEffective > 2<<20 {
			t.Fatalf("final L2TP session counters outside remaining effective quota: active=%+v final=%+v previous=%d remaining=%d limit=%d", activeRecord, final, previousEffective, remainingEffective, quotaLimit)
		}
		if err := s.checkpointPPPOffline(context.Background()); err != nil {
			t.Fatal(err)
		}
		batch, err := s.collectL2TPUserUsage(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		total := l2tpNativeUsageValue(batch)
		effective := nativeSessionEffectiveLiveUsage(policy, total)
		if effective == 0 {
			t.Fatalf("native L2TP final collector batch is empty: raw=%d effective=%d", total, effective)
		}
		t.Logf("production L2TP quota worker disconnected at raw=%d effective=%d bytes, limit=%d", total, effective, quotaLimit)
	default:
		t.Fatal(fmt.Sprintf("unknown native L2TP action %q", action))
	}
}

func l2tpNativeUsageValue(batch *nodev1.UserUsageBatch) uint64 {
	for _, sample := range batch.GetStats() {
		if sample.GetUid() == "l2tp:7" {
			return sample.GetValue()
		}
	}
	return 0
}
