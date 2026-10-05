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
	if raw := strings.TrimSpace(os.Getenv("ANTIMAGE_AWG_QUOTA_BYTES")); raw != "" {
		quotaLimit, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || quotaLimit < 0 {
			t.Fatalf("invalid AWG quota %q", raw)
		}
	}
	peer := amneziaWGRuntimePeer{UserID: 7, Username: "native-awg", DeviceIndex: 1,
		PublicKey: clientPublic, Address: "10.74.0.2", Status: "active"}
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
		prepared, err := s.prepareAmneziaWGInbound(inbound)
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
	case "collect":
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
		t.Logf("production collector persisted native AWG bytes=%d batch=%s", total, batch.GetBatchId())
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
		limit := uint64(quotaLimit)
		if total < limit || total-limit > 2<<20 {
			t.Fatalf("native AWG quota overshoot outside 2 MiB bound: raw=%d limit=%d batch=%v", total, limit, batch)
		}
		t.Logf("production AWG quota worker removed native peer at raw=%d bytes, limit=%d, overshoot=%d", total, limit, total-limit)
	default:
		t.Fatal(fmt.Sprintf("unknown native AmneziaWG action %q", action))
	}
}
