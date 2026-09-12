package nodeagent

import (
	"context"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"strings"
	"testing"
)

func TestWireGuardLiveUnackedUsageAvoidsPendingCarryKernelDoubleCount(
	t *testing.T,
) {
	server := New(Config{DataDir: t.TempDir()})
	server.wireGuardUsageLoaded = true

	cfg := wireGuardUsageRuntimeConfig{
		InboundTag:    "wg-main",
		InterfaceName: "wg-test0",
		Peers: map[string]int64{
			"peer-a": 42,
			"peer-b": 42,
			"peer-c": 99,
		},
	}

	keyA := wireGuardUsageBaselineKey(
		"wg-main",
		"wg-test0",
		"peer-a",
	)
	keyB := wireGuardUsageBaselineKey(
		"wg-main",
		"wg-test0",
		"peer-b",
	)
	keyC := wireGuardUsageBaselineKey(
		"wg-main",
		"wg-test0",
		"peer-c",
	)
	oldKey := wireGuardUsageBaselineKey(
		"wg-main",
		"wg-old0",
		"peer-old",
	)

	server.wireGuardUsageBaseline = map[string]uint64{
		keyA: 100,
		keyB: 200,
		keyC: 0,
	}

	server.wireGuardUsagePending = &wireGuardUsagePendingBatch{
		BatchID: "wireguard-pending",
		Samples: []wireGuardUsageSample{
			{
				UserID:     42,
				InboundTag: "wg-main",
				Value:      40,
			},
			{
				UserID:     99,
				InboundTag: "wg-main",
				Value:      999,
			},
			{
				UserID:     42,
				InboundTag: "wg-other",
				Value:      999,
			},
		},
		NextBaseline: map[string]uint64{
			keyA: 130,
			keyB: 210,
			keyC: 500,
		},
		CarryValues: map[string]uint64{
			keyA: 10,
		},
	}

	server.wireGuardUsageCarry[keyA] = wireGuardUsageCarry{
		UserID:       42,
		InboundTag:   "wg-main",
		Value:        15,
		NextBaseline: 135,
	}

	// This represents detached usage for the same user/inbound whose peer
	// is no longer present in the current runtime config.
	server.wireGuardUsageCarry[oldKey] = wireGuardUsageCarry{
		UserID:       42,
		InboundTag:   "wg-main",
		Value:        7,
		NextBaseline: 50,
	}

	live, err := server.wireGuardLiveUnackedUsageLocked(
		cfg,
		"wg-test0",
		[]wireGuardPeerCounters{
			{
				PublicKey:     "peer-a",
				ReceivedBytes: 100,
				SentBytes:     50,
			},
			{
				PublicKey:     "peer-b",
				ReceivedBytes: 120,
				SentBytes:     100,
			},
			{
				PublicKey:     "peer-c",
				ReceivedBytes: 500,
				SentBytes:     499,
			},
		},
		42,
	)
	if err != nil {
		t.Fatal(err)
	}

	// 40 pending
	// + (15 carry - 10 already represented by pending)
	// + 7 detached carry
	// + (150 kernel - 135 carry checkpoint)
	// + (220 kernel - 210 pending checkpoint)
	// = 77
	if live != 77 {
		t.Fatalf("live unacked usage = %d, want 77", live)
	}
}

func TestWireGuardLiveUnackedUsageHandlesCounterReset(
	t *testing.T,
) {
	server := New(Config{DataDir: t.TempDir()})
	server.wireGuardUsageLoaded = true

	cfg := wireGuardUsageRuntimeConfig{
		InboundTag:    "wg-main",
		InterfaceName: "wg-test0",
		Peers: map[string]int64{
			"peer-a": 42,
		},
	}

	key := wireGuardUsageBaselineKey(
		"wg-main",
		"wg-test0",
		"peer-a",
	)

	server.wireGuardUsageBaseline = map[string]uint64{
		key: 1000,
	}

	live, err := server.wireGuardLiveUnackedUsageLocked(
		cfg,
		"wg-test0",
		[]wireGuardPeerCounters{{
			PublicKey:     "peer-a",
			ReceivedBytes: 15,
			SentBytes:     10,
		}},
		42,
	)
	if err != nil {
		t.Fatal(err)
	}

	if live != 25 {
		t.Fatalf(
			"live usage after counter reset = %d, want 25",
			live,
		)
	}
}

func TestWireGuardLiveUnackedUsageRejectsPendingCarryUnderflow(
	t *testing.T,
) {
	server := New(Config{DataDir: t.TempDir()})
	server.wireGuardUsageLoaded = true

	cfg := wireGuardUsageRuntimeConfig{
		InboundTag:    "wg-main",
		InterfaceName: "wg-test0",
		Peers: map[string]int64{
			"peer-a": 42,
		},
	}

	key := wireGuardUsageBaselineKey(
		"wg-main",
		"wg-test0",
		"peer-a",
	)

	server.wireGuardUsagePending = &wireGuardUsagePendingBatch{
		BatchID: "wireguard-pending",
		CarryValues: map[string]uint64{
			key: 10,
		},
	}

	server.wireGuardUsageCarry[key] = wireGuardUsageCarry{
		UserID:       42,
		InboundTag:   "wg-main",
		Value:        5,
		NextBaseline: 105,
	}

	_, err := server.wireGuardLiveUnackedUsageLocked(
		cfg,
		"wg-test0",
		nil,
		42,
	)
	if err == nil {
		t.Fatal("expected pending/carry underflow error")
	}

	if !strings.Contains(err.Error(), "carry underflow") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWireGuardLiveUsageDoesNotDoubleCountReflectedPendingBatch(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})
	server.wireGuardUsageLoaded = true

	key := wireGuardUsageBaselineKey(
		"wg-main",
		"wg-test0",
		"peer-a",
	)

	server.wireGuardUsageBaseline[key] = 100
	server.wireGuardUsagePending = &wireGuardUsagePendingBatch{
		BatchID: "wireguard-200",
		Samples: []wireGuardUsageSample{{
			UserID:     42,
			InboundTag: "wg-main",
			Value:      50,
		}},
		NextBaseline: map[string]uint64{
			key: 150,
		},
	}

	accountingEnabled := true
	cfg := wireGuardUsageRuntimeConfig{
		InboundTag:        "wg-main",
		InterfaceName:     "wg-test0",
		AccountingEnabled: &accountingEnabled,
		Peers: map[string]int64{
			"peer-a": 42,
		},
		Policies: map[string]nativeSessionUserPolicy{
			"peer-a": {
				Status:                "active",
				UsedTraffic:           150,
				ReflectedUsageBatchID: "wireguard-200",
			},
		},
	}

	live, err := server.wireGuardLiveUnackedUsageLocked(
		cfg,
		"wg-test0",
		[]wireGuardPeerCounters{{
			PublicKey:     "peer-a",
			ReceivedBytes: 100,
			SentBytes:     50,
		}},
		42,
	)
	if err != nil {
		t.Fatal(err)
	}

	if live != 0 {
		t.Fatalf(
			"reflected pending usage counted again: live=%d want=0",
			live,
		)
	}
}

func TestWireGuardAckedUsageRemainsLiveUntilReflected(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})
	server.wireGuardUsageLoaded = true

	key := wireGuardUsageBaselineKey(
		"wg-main",
		"wg-test0",
		"peer-a",
	)

	server.wireGuardUsageBaseline[key] = 100
	server.wireGuardUsagePending = &wireGuardUsagePendingBatch{
		BatchID: "wireguard-300",
		Samples: []wireGuardUsageSample{{
			UserID:     42,
			InboundTag: "wg-main",
			Value:      50,
		}},
		NextBaseline: map[string]uint64{
			key: 150,
		},
	}

	resp, err := server.ackWireGuardUserUsage(
		context.Background(),
		&nodev1.AckUsageRequest{
			BatchId: "wireguard-300",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("usage ACK was not acknowledged")
	}

	accountingEnabled := true
	cfg := wireGuardUsageRuntimeConfig{
		InboundTag:        "wg-main",
		InterfaceName:     "wg-test0",
		AccountingEnabled: &accountingEnabled,
		Peers: map[string]int64{
			"peer-a": 42,
		},
		Policies: map[string]nativeSessionUserPolicy{
			"peer-a": {
				Status:      "active",
				UsedTraffic: 100,
			},
		},
	}

	live, err := server.wireGuardLiveUnackedUsageLocked(
		cfg,
		"wg-test0",
		[]wireGuardPeerCounters{{
			PublicKey:     "peer-a",
			ReceivedBytes: 100,
			SentBytes:     50,
		}},
		42,
	)
	if err != nil {
		t.Fatal(err)
	}

	if live != 50 {
		t.Fatalf(
			"ACKed but unreflected usage = %d, want 50",
			live,
		)
	}
}

func TestWireGuardAwaitingReflectionPersistsAcrossRestart(t *testing.T) {
	dataDir := t.TempDir()
	key := wireGuardUsageBaselineKey(
		"wg-main",
		"wg-test0",
		"peer-a",
	)

	server1 := New(Config{DataDir: dataDir})
	server1.wireGuardUsageLoaded = true
	server1.wireGuardUsageBaseline[key] = 100
	server1.wireGuardUsagePending = &wireGuardUsagePendingBatch{
		BatchID: "wireguard-restart-reflection",
		Samples: []wireGuardUsageSample{{
			UserID:     42,
			InboundTag: "wg-main",
			Value:      50,
		}},
		NextBaseline: map[string]uint64{
			key: 150,
		},
	}

	if err := server1.persistWireGuardUsageStateLocked(); err != nil {
		t.Fatal(err)
	}

	resp, err := server1.ackWireGuardUserUsage(
		context.Background(),
		&nodev1.AckUsageRequest{
			BatchId: "wireguard-restart-reflection",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("usage ACK was not acknowledged")
	}

	server2 := New(Config{DataDir: dataDir})
	if err := server2.ensureWireGuardUsageStateLoadedLocked(); err != nil {
		t.Fatal(err)
	}

	if server2.wireGuardUsagePending != nil {
		t.Fatal("ACKed batch unexpectedly restored as pending")
	}

	if len(server2.wireGuardUsageAwaitingReflection) != 1 {
		t.Fatalf(
			"awaiting reflection batches = %d, want 1",
			len(server2.wireGuardUsageAwaitingReflection),
		)
	}

	awaiting := server2.wireGuardUsageAwaitingReflection[0]
	if awaiting.BatchID != "wireguard-restart-reflection" {
		t.Fatalf(
			"awaiting reflection batch = %q",
			awaiting.BatchID,
		)
	}
	if len(awaiting.Samples) != 1 ||
		awaiting.Samples[0].UserID != 42 ||
		awaiting.Samples[0].Value != 50 {
		t.Fatalf(
			"unexpected awaiting reflection samples: %#v",
			awaiting.Samples,
		)
	}

	if baseline := server2.wireGuardUsageBaseline[key]; baseline != 150 {
		t.Fatalf("restored baseline = %d, want 150", baseline)
	}

	accountingEnabled := true
	cfg := wireGuardUsageRuntimeConfig{
		InboundTag:        "wg-main",
		InterfaceName:     "wg-test0",
		AccountingEnabled: &accountingEnabled,
		Peers: map[string]int64{
			"peer-a": 42,
		},
		Policies: map[string]nativeSessionUserPolicy{
			"peer-a": {
				Status:      "active",
				UsedTraffic: 100,
			},
		},
	}

	live, err := server2.wireGuardLiveUnackedUsageLocked(
		cfg,
		"wg-test0",
		[]wireGuardPeerCounters{{
			PublicKey:     "peer-a",
			ReceivedBytes: 100,
			SentBytes:     50,
		}},
		42,
	)
	if err != nil {
		t.Fatal(err)
	}

	if live != 50 {
		t.Fatalf(
			"ACKed unreflected usage after restart = %d, want 50",
			live,
		)
	}
}
