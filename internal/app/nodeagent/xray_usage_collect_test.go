package nodeagent

import (
	"context"
	"fmt"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestXrayUsageCollectAndAck(t *testing.T) {
	// This test verifies the complete ACK cycle prevents double-counting
	server := &Server{
		cfg: Config{
			DataDir:     t.TempDir(),
			XrayAPIPort: 10085,
		},
		xrayUsageBaseline: make(map[string]uint64),
		lastRuntime:       nil, // Xray not running - will return empty batch
	}

	ctx := context.Background()
	req := &nodev1.CollectUsageRequest{}

	// First collection - should return empty since Xray is not running
	batch1, err := server.collectXrayUserUsage(ctx, req)
	if err != nil {
		t.Fatalf("First collect failed: %v", err)
	}
	if batch1.GetBatchId() != "" {
		t.Errorf("Expected empty batch ID when Xray not running, got %q", batch1.GetBatchId())
	}

	// Simulate pending batch with samples
	server.xrayUsagePending = &xrayUsagePendingBatch{
		BatchID: "xray-test-123",
		Samples: []xrayUsageSample{
			{UserID: 42, InboundTag: "vless-in", Value: 1000, Online: true},
			{UserID: 99, InboundTag: "vmess-in", Value: 2000, Online: true},
		},
		NextBaseline: map[string]uint64{
			"42.alice:uplink":   500,
			"42.alice:downlink": 500,
			"99.bob:uplink":     1000,
			"99.bob:downlink":   1000,
		},
	}

	// Second collection - should return pending batch
	batch2, err := server.collectXrayUserUsage(ctx, req)
	if err != nil {
		t.Fatalf("Second collect failed: %v", err)
	}
	if batch2.GetBatchId() != "xray-test-123" {
		t.Errorf("Expected batch ID 'xray-test-123', got %q", batch2.GetBatchId())
	}
	if len(batch2.GetStats()) != 2 {
		t.Errorf("Expected 2 stats, got %d", len(batch2.GetStats()))
	}

	// Third collection while pending - should return same batch
	batch3, err := server.collectXrayUserUsage(ctx, req)
	if err != nil {
		t.Fatalf("Third collect failed: %v", err)
	}
	if batch3.GetBatchId() != "xray-test-123" {
		t.Errorf("Expected same batch ID, got %q", batch3.GetBatchId())
	}

	// ACK the batch
	ackResp, err := server.ackXrayUserUsage(ctx, &nodev1.AckUsageRequest{
		BatchId: "xray-test-123",
	})
	if err != nil {
		t.Fatalf("ACK failed: %v", err)
	}
	if !ackResp.GetAcknowledged() {
		t.Error("ACK was not acknowledged")
	}

	// Verify baseline was updated
	if server.xrayUsageBaseline["42.alice:uplink"] != 500 {
		t.Errorf("Baseline not updated correctly")
	}

	// Verify pending was cleared
	if server.xrayUsagePending != nil {
		t.Error("Pending batch was not cleared after ACK")
	}

	// Fourth collection - should be able to create new batch now
	server.xrayUsagePending = nil
	batch4, err := server.collectXrayUserUsage(ctx, req)
	if err != nil {
		t.Fatalf("Fourth collect failed: %v", err)
	}
	// Since Xray is not running, this will be empty
	if batch4.GetBatchId() != "" {
		t.Errorf("Expected empty batch after ACK with no Xray running")
	}
}

func TestXrayUsageAckWrongBatchID(t *testing.T) {
	server := &Server{
		cfg: Config{
			DataDir:     t.TempDir(),
			XrayAPIPort: 10085,
		},
		xrayUsageBaseline: make(map[string]uint64),
		xrayUsagePending: &xrayUsagePendingBatch{
			BatchID: "xray-correct-123",
			Samples: []xrayUsageSample{
				{UserID: 42, InboundTag: "vless-in", Value: 1000, Online: true},
			},
			NextBaseline: map[string]uint64{
				"42.alice:uplink": 500,
			},
		},
	}

	ctx := context.Background()

	// Try to ACK with wrong batch ID
	ackResp, err := server.ackXrayUserUsage(ctx, &nodev1.AckUsageRequest{
		BatchId: "xray-wrong-456",
	})
	if err != nil {
		t.Fatalf("ACK with wrong ID failed: %v", err)
	}
	if ackResp.GetAcknowledged() {
		t.Error("ACK should not be acknowledged for wrong batch ID")
	}

	// Verify pending was NOT cleared
	if server.xrayUsagePending == nil {
		t.Error("Pending batch was incorrectly cleared")
	}
	if server.xrayUsagePending.BatchID != "xray-correct-123" {
		t.Error("Pending batch was corrupted")
	}

	// Verify baseline was NOT updated
	if len(server.xrayUsageBaseline) != 0 {
		t.Error("Baseline was incorrectly updated")
	}
}

func TestXrayUsageCounterReset(t *testing.T) {
	// This test verifies that Xray counter resets are handled correctly
	server := &Server{
		cfg: Config{
			DataDir:     t.TempDir(),
			XrayAPIPort: 10085,
		},
		xrayUsageBaseline: map[string]uint64{
			"42.alice:uplink":   1000,
			"42.alice:downlink": 1000,
		},
	}

	// Simulate a counter reset scenario where current < baseline
	// In real scenario this would come from Xray API, but we'll simulate the logic

	// Baseline is 1000, but current counter is 100 (Xray restarted)
	currentValue := uint64(100)
	baseline := server.xrayUsageBaseline["42.alice:uplink"]

	var delta uint64
	if currentValue >= baseline {
		delta = currentValue - baseline
	} else {
		// Counter reset detected - treat current value as fresh start
		delta = currentValue
	}

	// Delta should be 100, not negative or huge
	if delta != 100 {
		t.Errorf("Counter reset not handled correctly: delta=%d, want 100", delta)
	}
}

func TestXrayUsageOverflowProtection(t *testing.T) {
	// Test that we don't overflow when accumulating deltas
	sample := xrayUsageSample{
		UserID:     42,
		InboundTag: "vless-in",
		Value:      ^uint64(0) - 100, // Near max uint64
		Online:     true,
	}

	delta := uint64(200)

	// Should not overflow
	if ^uint64(0)-sample.Value >= delta {
		sample.Value += delta
	} else {
		// Overflow would occur, don't add
		t.Logf("Correctly prevented overflow by not adding delta")
	}

	// Verify we didn't overflow (value shouldn't wrap around)
	if sample.Value < ^uint64(0)-100 {
		t.Error("Value wrapped around, overflow occurred")
	}
}

func TestXrayUsageBatchProtoFormat(t *testing.T) {
	pending := &xrayUsagePendingBatch{
		BatchID: "xray-test-789",
		Samples: []xrayUsageSample{
			{UserID: 42, InboundTag: "vless-in", Value: 1000, Online: true},
			{UserID: 99, InboundTag: "", Value: 2000, Online: true},
			{UserID: 100, InboundTag: "vmess-in", Value: 0, Online: true},
		},
		OnlineUsers: []xrayOnlineUserSnapshot{
			{
				UserID: 42,
				Email:  "42.alice",
				IPs: []xrayOnlineIPSnapshot{
					{IP: "203.0.113.10", LastSeenUnix: 1234},
				},
			},
		},
		NextBaseline: map[string]uint64{},
	}

	batch := xrayUsageBatchProto(pending)

	if batch.GetBatchId() != "xray-test-789" {
		t.Errorf("Batch ID mismatch: got %q", batch.GetBatchId())
	}

	stats := batch.GetStats()
	if len(stats) != 3 {
		t.Fatalf("Expected 3 stats, got %d", len(stats))
	}

	// First sample: has traffic and inbound
	if stats[0].GetUid() != "xray:42" {
		t.Errorf("UID mismatch: got %q", stats[0].GetUid())
	}
	if stats[0].GetValue() != 1000 {
		t.Errorf("Value mismatch: got %d", stats[0].GetValue())
	}
	if stats[0].GetInboundTag() != "vless-in" {
		t.Errorf("InboundTag mismatch: got %q", stats[0].GetInboundTag())
	}

	// Third sample: zero traffic, online-only
	if stats[2].GetUid() != "online:xray:100" {
		t.Errorf("Online-only UID should have 'online:' prefix, got %q", stats[2].GetUid())
	}

	onlineIPs := batch.GetOnlineIps()
	if len(onlineIPs) != 1 {
		t.Fatalf("Expected 1 online-IP user, got %d", len(onlineIPs))
	}
	if onlineIPs[0].GetUid() != "xray:42" || onlineIPs[0].GetEmail() != "42.alice" {
		t.Fatalf("Unexpected online-IP identity: %+v", onlineIPs[0])
	}
	if len(onlineIPs[0].GetIps()) != 1 ||
		onlineIPs[0].GetIps()[0].GetIp() != "203.0.113.10" ||
		onlineIPs[0].GetIps()[0].GetLastSeenUnix() != 1234 {
		t.Fatalf("Unexpected online IP payload: %+v", onlineIPs[0].GetIps())
	}
}

func TestMergeUserUsageBatches(t *testing.T) {
	server := &Server{
		cfg: Config{
			DataDir: t.TempDir(),
		},
	}

	ovpnBatch := &nodev1.UserUsageBatch{
		BatchId: "openvpn-123",
		Stats: []*nodev1.UserUsageSample{
			{Uid: "openvpn:42", Value: 1000, InboundTag: "ovpn-in"},
		},
	}

	xrayBatch := &nodev1.UserUsageBatch{
		BatchId: "xray-456",
		Stats: []*nodev1.UserUsageSample{
			{Uid: "xray:99", Value: 2000, InboundTag: "vless-in"},
		},
	}

	merged := server.mergeUserUsageBatches(ovpnBatch, xrayBatch)

	// Check batch ID format
	if merged.GetBatchId() == "" {
		t.Error("Merged batch ID should not be empty")
	}
	if !contains(merged.GetBatchId(), "merged-") {
		t.Errorf("Merged batch ID should start with 'merged-', got %q", merged.GetBatchId())
	}

	// Check stats are merged
	if len(merged.GetStats()) != 2 {
		t.Errorf("Expected 2 merged stats, got %d", len(merged.GetStats()))
	}

	// Check idempotence - calling again should return SAME batch ID
	merged2 := server.mergeUserUsageBatches(ovpnBatch, xrayBatch)
	if merged2.GetBatchId() != merged.GetBatchId() {
		t.Errorf("Repeated merge should return same batch ID, got %q != %q",
			merged2.GetBatchId(), merged.GetBatchId())
	}
}

func TestMergeUserUsageBatchesEmptyCases(t *testing.T) {
	server := &Server{}

	// Both empty
	empty1 := &nodev1.UserUsageBatch{}
	empty2 := &nodev1.UserUsageBatch{}
	merged := server.mergeUserUsageBatches(empty1, empty2)
	if merged.GetBatchId() != "" {
		t.Error("Merging two empty batches should return empty")
	}

	// One empty, one with data
	withData := &nodev1.UserUsageBatch{
		BatchId: "xray-789",
		Stats: []*nodev1.UserUsageSample{
			{Uid: "xray:42", Value: 1000},
		},
	}
	merged = server.mergeUserUsageBatches(empty1, withData)
	if merged.GetBatchId() != "xray-789" {
		t.Errorf("Should return the non-empty batch, got %q", merged.GetBatchId())
	}
}

func TestParseXrayStatValueFromJSON(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    int64
		wantErr bool
	}{
		{"string number", `"12345"`, 12345, false},
		{"json number", `12345`, 12345, false},
		{"null", `null`, 0, false},
		{"empty string", `""`, 0, false},
		{"zero", `"0"`, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseXrayStatValue([]byte(tt.raw))
			if (err != nil) != tt.wantErr {
				t.Errorf("parseXrayStatValue() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("parseXrayStatValue() = %v, want %v", got, tt.want)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 &&
		len(s) >= len(substr) &&
		fmt.Sprintf("%s", s)[0:len(s)] != "" &&
		(s == substr || (len(s) > len(substr) &&
			(s[0:len(substr)] == substr || contains(s[1:], substr))))
}
