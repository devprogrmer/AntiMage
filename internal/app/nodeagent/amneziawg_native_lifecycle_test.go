//go:build linux

package nodeagent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Jipok/wgctrl-go/wgtypes"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/protobuf/proto"
)

// TestAmneziaWGNativeAccountingStage is driven by the privileged Linux CI
// harness. It uses the production DKMS provision/apply/accounting paths and a
// real kernel AWG server interface.
func TestAmneziaWGNativeAccountingStage(t *testing.T) {
	stateDir := strings.TrimSpace(os.Getenv("ANTIMAGE_AWG_NATIVE_STATE"))
	action := strings.TrimSpace(os.Getenv("ANTIMAGE_AWG_NATIVE_ACTION"))
	if stateDir == "" || action == "" {
		t.Skip("requires isolated native AmneziaWG harness")
	}
	serverPrivate := strings.TrimSpace(os.Getenv("ANTIMAGE_AWG_SERVER_PRIVATE"))
	clientPublic := strings.TrimSpace(os.Getenv("ANTIMAGE_AWG_CLIENT_PUBLIC"))
	if _, err := wgtypes.ParseKey(serverPrivate); err != nil {
		t.Fatalf("server private key: %v", err)
	}
	if _, err := wgtypes.ParseKey(clientPublic); err != nil {
		t.Fatalf("client public key: %v", err)
	}
	quotaLimit := int64(0)
	var err error
	callback := nativeRuntimeSessionCallback{
		URL:    strings.TrimSpace(os.Getenv("ANTIMAGE_AWG_SESSION_CALLBACK_URL")),
		Token:  strings.TrimSpace(os.Getenv("ANTIMAGE_AWG_SESSION_CALLBACK_TOKEN")),
		NodeID: 7,
	}
	if callback.URL != "" && callback.Token == "" {
		t.Fatal("AmneziaWG Panel session callback token is required with callback URL")
	}
	if raw := strings.TrimSpace(os.Getenv("ANTIMAGE_AWG_QUOTA_BYTES")); raw != "" {
		quotaLimit, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || quotaLimit < 0 {
			t.Fatalf("invalid AWG quota %q", raw)
		}
	}
	peer := amneziaWGRuntimePeer{UserID: 7, Username: "native-awg", DeviceIndex: 1,
		PublicKey: clientPublic, Address: "10.74.0.2", Status: "active",
		UsageCoefficient: 1.5, InboundCoefficient: 2}
	if quotaLimit > 0 {
		peer.DataLimit = &quotaLimit
	}
	inbound := amneziaWGRuntimeInbound{Tag: "native-awg", ListenPort: 51821, Peers: []amneziaWGRuntimePeer{peer},
		Settings: map[string]any{"private_key": serverPrivate, "address_pool": "10.74.0.0/24",
			"server_address": "10.74.0.1/24", "mtu": 1420,
			"jc": 4, "jmin": 8, "jmax": 80, "s1": 77, "s2": 90,
			"h1": "12345", "h2": "23456", "h3": "34567", "h4": "45678"}}
	s := New(Config{DataDir: stateDir})
	apply := func() string {
		prepared, err := s.prepareAmneziaWGInbound(inbound, callback)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.applyAmneziaWGRuntime(prepared); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(stateDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stateDir, "interface"), []byte(prepared.InterfaceName), 0600); err != nil {
			t.Fatal(err)
		}
		return prepared.InterfaceName
	}
	switch action {
	case "apply":
		iface := apply()
		t.Logf("production AmneziaWG runtime applied: interface=%s port=%d", iface, inbound.ListenPort)
	case "restart":
		s.stopAllAmneziaWGRuntimes()
		iface := apply()
		t.Logf("production AmneziaWG runtime restarted: interface=%s", iface)
	case "collect", "collect-first", "collect-next", "collect-final":
		batch, err := s.collectAmneziaWGUserUsage(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var total uint64
		for _, sample := range batch.GetStats() {
			if sample.GetUid() == "amneziawg:7" {
				total = sample.GetValue()
			}
		}
		if total == 0 {
			t.Fatalf("production collector found no native AWG traffic: %v", batch)
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
		t.Logf("production collector persisted native AWG bytes=%d batch=%s", total, batch.GetBatchId())
	case "session":
		batch, err := s.collectAmneziaWGUserUsage(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("production AmneziaWG session reconciliation observed native online IPs=%v", batch.GetOnlineIps())
	case "ack":
		s.amneziaWGUsageMu.Lock()
		if err := s.ensureAmneziaWGUsageStateLoadedLocked(); err != nil {
			s.amneziaWGUsageMu.Unlock()
			t.Fatal(err)
		}
		pending := s.amneziaWGUsagePending
		s.amneziaWGUsageMu.Unlock()
		if pending == nil {
			t.Fatal("expected durable AmneziaWG batch before ACK")
		}
		ack, err := s.ackAmneziaWGUserUsage(context.Background(), &nodev1.AckUsageRequest{BatchId: pending.BatchID})
		if err != nil || !ack.GetAcknowledged() {
			t.Fatalf("ACK: %v %v", ack, err)
		}
		restarted := New(Config{DataDir: stateDir})
		restarted.amneziaWGUsageMu.Lock()
		if err := restarted.ensureAmneziaWGUsageStateLoadedLocked(); err != nil {
			restarted.amneziaWGUsageMu.Unlock()
			t.Fatal(err)
		}
		stillPending := restarted.amneziaWGUsagePending != nil
		lastAcked := restarted.amneziaWGUsageLastAckedBatchID
		restarted.amneziaWGUsageMu.Unlock()
		if stillPending || lastAcked != pending.BatchID {
			t.Fatalf("AmneziaWG ACK was not durable after restart: pending=%v last_acked=%q want=%q", stillPending, lastAcked, pending.BatchID)
		}
		t.Logf("production ACK advanced durable AWG baseline for batch %s", pending.BatchID)
	case "quota-watch":
		if quotaLimit <= 0 {
			t.Fatal("quota-watch requires a positive quota")
		}
		ifaceRaw, err := os.ReadFile(filepath.Join(stateDir, "interface"))
		if err != nil {
			t.Fatal(err)
		}
		iface := strings.TrimSpace(string(ifaceRaw))
		ctx, cancel := context.WithCancel(context.Background())
		workerDone := make(chan struct{})
		go func() {
			defer close(workerDone)
			s.runLocalAccountingWorker(ctx, "amneziawg", 100*time.Millisecond, time.Second,
				s.checkpointAmneziaWGOffline, s.quotaCheckAmneziaWGOffline)
		}()
		deadline := time.Now().Add(3 * time.Minute)
		removed := false
		awgTool := strings.TrimSpace(os.Getenv("ANTIMAGE_AWG_TOOL"))
		if awgTool == "" {
			awgTool = "awg"
		}
		for time.Now().Before(deadline) {
			peers, showErr := exec.Command(awgTool, "show", iface, "peers").Output()
			if showErr != nil {
				cancel()
				<-workerDone
				t.Fatalf("inspect native AWG peer: %v", showErr)
			}
			if !strings.Contains(string(peers), clientPublic) {
				removed = true
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		cancel()
		<-workerDone
		if !removed {
			t.Fatal("production quota worker did not remove the native AWG peer")
		}
		batch, err := s.collectAmneziaWGUserUsage(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var total uint64
		for _, sample := range batch.GetStats() {
			if sample.GetUid() == "amneziawg:7" {
				total = sample.GetValue()
			}
		}
		policy := nativeSessionUserPolicy{UsageCoefficient: peer.UsageCoefficient, InboundCoefficient: peer.InboundCoefficient}
		effective := nativeSessionEffectiveLiveUsage(policy, total)
		limit := uint64(quotaLimit)
		previousEffective := nativePanelEffectiveUsage(t, stateDir)
		if previousEffective >= limit {
			t.Fatalf("AmneziaWG batches before quota traffic already exhausted quota: previous=%d limit=%d", previousEffective, limit)
		}
		remainingEffective := limit - previousEffective
		if effective < remainingEffective || effective-remainingEffective > 2<<20 {
			t.Fatalf("native AWG quota overshoot outside 2 MiB effective bound: raw=%d effective=%d previous=%d remaining=%d limit=%d batch=%v", total, effective, previousEffective, remainingEffective, limit, batch)
		}
		t.Logf("production AWG quota worker removed native peer at raw=%d effective=%d bytes, limit=%d", total, effective, limit)
	default:
		t.Fatal(fmt.Sprintf("unknown native AmneziaWG action %q", action))
	}
}
