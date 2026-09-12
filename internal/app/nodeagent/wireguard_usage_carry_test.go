package nodeagent

import (
	"context"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestWireGuardUsageCarryUsesPendingNextBaselineWithoutMutation(
	t *testing.T,
) {
	dataDir := t.TempDir()
	server := New(Config{DataDir: dataDir})

	key := wireGuardUsageBaselineKey(
		"wg-main",
		"wg-test0",
		"peer-a",
	)

	server.wireGuardUsageLoaded = true
	server.wireGuardUsageBaseline = map[string]uint64{
		key: 100,
	}
	server.wireGuardUsagePending = &wireGuardUsagePendingBatch{
		BatchID: "wireguard-pending",
		Samples: []wireGuardUsageSample{{
			UserID:     42,
			InboundTag: "wg-main",
			Value:      50,
		}},
		NextBaseline: map[string]uint64{
			key: 150,
		},
	}

	if err := server.recordWireGuardUsageCarryLocked(
		"wg-main",
		"wg-test0",
		"peer-a",
		42,
		170,
	); err != nil {
		t.Fatal(err)
	}

	carry, ok := server.wireGuardUsageCarry[key]
	if !ok {
		t.Fatal("carry entry missing")
	}
	if carry.Value != 20 {
		t.Fatalf("carry value = %d, want 20", carry.Value)
	}
	if carry.NextBaseline != 170 {
		t.Fatalf(
			"carry next baseline = %d, want 170",
			carry.NextBaseline,
		)
	}

	if server.wireGuardUsagePending.BatchID != "wireguard-pending" {
		t.Fatalf(
			"pending batch id changed: %q",
			server.wireGuardUsagePending.BatchID,
		)
	}
	if got := server.wireGuardUsagePending.NextBaseline[key]; got != 150 {
		t.Fatalf(
			"pending next baseline mutated: %d, want 150",
			got,
		)
	}
	if len(server.wireGuardUsagePending.Samples) != 1 ||
		server.wireGuardUsagePending.Samples[0].Value != 50 {
		t.Fatalf(
			"pending samples mutated: %#v",
			server.wireGuardUsagePending.Samples,
		)
	}

	// Recording the same kernel snapshot again must be idempotent.
	if err := server.recordWireGuardUsageCarryLocked(
		"wg-main",
		"wg-test0",
		"peer-a",
		42,
		170,
	); err != nil {
		t.Fatal(err)
	}

	if got := server.wireGuardUsageCarry[key].Value; got != 20 {
		t.Fatalf(
			"idempotent carry value = %d, want 20",
			got,
		)
	}
}

func TestWireGuardUsageCarryPersistsAcrossRestart(t *testing.T) {
	dataDir := t.TempDir()
	server1 := New(Config{DataDir: dataDir})

	key := wireGuardUsageBaselineKey(
		"wg-main",
		"wg-test0",
		"peer-a",
	)

	server1.wireGuardUsageLoaded = true
	server1.wireGuardUsageBaseline = map[string]uint64{
		key: 100,
	}

	if err := server1.recordWireGuardUsageCarryLocked(
		"wg-main",
		"wg-test0",
		"peer-a",
		42,
		150,
	); err != nil {
		t.Fatal(err)
	}

	server2 := New(Config{DataDir: dataDir})
	if err := server2.ensureWireGuardUsageStateLoadedLocked(); err != nil {
		t.Fatal(err)
	}

	carry, ok := server2.wireGuardUsageCarry[key]
	if !ok {
		t.Fatal("persisted carry entry missing after restart")
	}
	if carry.UserID != 42 {
		t.Fatalf("carry user id = %d, want 42", carry.UserID)
	}
	if carry.InboundTag != "wg-main" {
		t.Fatalf(
			"carry inbound tag = %q, want wg-main",
			carry.InboundTag,
		)
	}
	if carry.Value != 50 {
		t.Fatalf("carry value = %d, want 50", carry.Value)
	}
	if carry.NextBaseline != 150 {
		t.Fatalf(
			"carry next baseline = %d, want 150",
			carry.NextBaseline,
		)
	}
}

func TestWireGuardUsageCarryHandlesCounterReset(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})

	key := wireGuardUsageBaselineKey(
		"wg-main",
		"wg-test0",
		"peer-a",
	)

	server.wireGuardUsageLoaded = true
	server.wireGuardUsageCarry[key] = wireGuardUsageCarry{
		UserID:       42,
		InboundTag:   "wg-main",
		Value:        20,
		NextBaseline: 170,
	}

	// A lower counter means a new kernel counter epoch.
	if err := server.recordWireGuardUsageCarryLocked(
		"wg-main",
		"wg-test0",
		"peer-a",
		42,
		10,
	); err != nil {
		t.Fatal(err)
	}

	carry := server.wireGuardUsageCarry[key]
	if carry.Value != 30 {
		t.Fatalf(
			"carry value after reset = %d, want 30",
			carry.Value,
		)
	}
	if carry.NextBaseline != 10 {
		t.Fatalf(
			"carry next baseline after reset = %d, want 10",
			carry.NextBaseline,
		)
	}
}
func TestWireGuardUsageCarryFlowsThroughBatchAndAck(t *testing.T) {
	dataDir := t.TempDir()
	server := New(Config{DataDir: dataDir})
	server.wireGuardUsageLoaded = true

	key := wireGuardUsageBaselineKey(
		"wg-main",
		"wg-test0",
		"peer-a",
	)

	server.wireGuardUsageBaseline[key] = 100
	server.wireGuardUsageCarry[key] = wireGuardUsageCarry{
		UserID:       42,
		InboundTag:   "wg-main",
		Value:        50,
		NextBaseline: 150,
	}

	batch, err := server.collectWireGuardUserUsage(
		context.Background(),
		&nodev1.CollectUsageRequest{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if batch.GetBatchId() == "" {
		t.Fatal("carry usage did not create an accounting batch")
	}
	if len(batch.GetStats()) != 1 ||
		batch.GetStats()[0].GetValue() != 50 {
		t.Fatalf("carry batch stats = %#v, want one sample with value 50", batch.GetStats())
	}

	if server.wireGuardUsagePending == nil {
		t.Fatal("pending batch missing")
	}
	if got := server.wireGuardUsagePending.CarryValues[key]; got != 50 {
		t.Fatalf("pending carry value = %d, want 50", got)
	}
	if got := server.wireGuardUsagePending.NextBaseline[key]; got != 150 {
		t.Fatalf("pending baseline = %d, want 150", got)
	}
	if got := server.wireGuardUsageCarry[key].Value; got != 50 {
		t.Fatalf("carry was consumed before ACK: %d", got)
	}

	resp, err := server.ackWireGuardUserUsage(
		context.Background(),
		&nodev1.AckUsageRequest{BatchId: batch.GetBatchId()},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("carry batch ACK was not acknowledged")
	}
	if _, ok := server.wireGuardUsageCarry[key]; ok {
		t.Fatal("ACK did not consume carried usage")
	}
	if got := server.wireGuardUsageBaseline[key]; got != 150 {
		t.Fatalf("baseline after ACK = %d, want 150", got)
	}

	restarted := New(Config{DataDir: dataDir})
	if err := restarted.ensureWireGuardUsageStateLoadedLocked(); err != nil {
		t.Fatal(err)
	}
	if _, ok := restarted.wireGuardUsageCarry[key]; ok {
		t.Fatal("consumed carry reappeared after restart")
	}
	if got := restarted.wireGuardUsageBaseline[key]; got != 150 {
		t.Fatalf("restarted baseline = %d, want 150", got)
	}
}

func TestWireGuardUsageCarryAddedWhilePendingSurvivesAck(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})
	server.wireGuardUsageLoaded = true

	key := wireGuardUsageBaselineKey(
		"wg-main",
		"wg-test0",
		"peer-a",
	)

	server.wireGuardUsageBaseline[key] = 150
	server.wireGuardUsageCarry[key] = wireGuardUsageCarry{
		UserID:       42,
		InboundTag:   "wg-main",
		Value:        20,
		NextBaseline: 170,
	}

	first, err := server.collectWireGuardUserUsage(
		context.Background(),
		&nodev1.CollectUsageRequest{},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := server.recordWireGuardUsageCarryLocked(
		"wg-main",
		"wg-test0",
		"peer-a",
		42,
		190,
	); err != nil {
		t.Fatal(err)
	}

	if got := server.wireGuardUsageCarry[key].Value; got != 40 {
		t.Fatalf("carry before ACK = %d, want 40", got)
	}

	resp, err := server.ackWireGuardUserUsage(
		context.Background(),
		&nodev1.AckUsageRequest{BatchId: first.GetBatchId()},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("first carry batch ACK was not acknowledged")
	}

	remaining, ok := server.wireGuardUsageCarry[key]
	if !ok {
		t.Fatal("post-pending carry was incorrectly deleted by ACK")
	}
	if remaining.Value != 20 {
		t.Fatalf("remaining carry = %d, want 20", remaining.Value)
	}
	if remaining.NextBaseline != 190 {
		t.Fatalf(
			"remaining carry next baseline = %d, want 190",
			remaining.NextBaseline,
		)
	}

	second, err := server.collectWireGuardUserUsage(
		context.Background(),
		&nodev1.CollectUsageRequest{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if second.GetBatchId() == "" ||
		second.GetBatchId() == first.GetBatchId() {
		t.Fatalf(
			"second carry batch id = %q, first = %q",
			second.GetBatchId(),
			first.GetBatchId(),
		)
	}
	if len(second.GetStats()) != 1 ||
		second.GetStats()[0].GetValue() != 20 {
		t.Fatalf(
			"second carry batch stats = %#v, want value 20",
			second.GetStats(),
		)
	}
}

func TestWireGuardPendingBatchReplaysWithoutHelperDirectory(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})
	server.wireGuardUsageLoaded = true

	key := wireGuardUsageBaselineKey(
		"wg-main",
		"wg-test0",
		"peer-a",
	)

	server.wireGuardUsagePending = &wireGuardUsagePendingBatch{
		BatchID: "wireguard-no-helper",
		Samples: []wireGuardUsageSample{{
			UserID:     42,
			InboundTag: "wg-main",
			Value:      50,
		}},
		NextBaseline: map[string]uint64{
			key: 150,
		},
	}

	batch, err := server.collectWireGuardUserUsage(
		context.Background(),
		&nodev1.CollectUsageRequest{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if batch.GetBatchId() != "wireguard-no-helper" {
		t.Fatalf(
			"pending batch id = %q, want wireguard-no-helper",
			batch.GetBatchId(),
		)
	}
	if len(batch.GetStats()) != 1 ||
		batch.GetStats()[0].GetValue() != 50 {
		t.Fatalf(
			"pending stats = %#v, want preserved value 50",
			batch.GetStats(),
		)
	}
}

func TestWireGuardUsageCarryAggregatesMultiplePeersForSameUser(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})
	server.wireGuardUsageLoaded = true

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

	server.wireGuardUsageCarry[keyA] = wireGuardUsageCarry{
		UserID:       42,
		InboundTag:   "wg-main",
		Value:        20,
		NextBaseline: 120,
	}
	server.wireGuardUsageCarry[keyB] = wireGuardUsageCarry{
		UserID:       42,
		InboundTag:   "wg-main",
		Value:        30,
		NextBaseline: 230,
	}

	batch, err := server.collectWireGuardUserUsage(
		context.Background(),
		&nodev1.CollectUsageRequest{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.GetStats()) != 1 {
		t.Fatalf("stats count = %d, want 1", len(batch.GetStats()))
	}
	if got := batch.GetStats()[0].GetValue(); got != 50 {
		t.Fatalf("aggregated carry = %d, want 50", got)
	}
	if len(server.wireGuardUsagePending.CarryValues) != 2 {
		t.Fatalf(
			"pending carry keys = %d, want 2",
			len(server.wireGuardUsagePending.CarryValues),
		)
	}
}
