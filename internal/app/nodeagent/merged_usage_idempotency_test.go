package nodeagent

import (
	"context"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestMergedUsageACKIsIdempotent(t *testing.T) {
	server := New(Config{DataDir: t.TempDir()})
	ctx := context.Background()

	server.openVPNUsagePending = &openVPNUsagePendingBatch{
		BatchID:      "openvpn-idempotent",
		NextBaseline: map[string]uint64{"ov": 10},
	}
	server.openVPNUsageLoaded = true
	if err := server.persistOpenVPNUsageStateLocked(); err != nil {
		t.Fatal(err)
	}

	server.xrayUsagePending = &xrayUsagePendingBatch{
		BatchID:      "xray-idempotent",
		NextBaseline: map[string]uint64{"xr": 20},
	}
	server.xrayUsageLoaded = true
	if err := server.persistXrayUsageStateLocked(); err != nil {
		t.Fatal(err)
	}

	server.mergedUsagePending = &mergedUsagePendingBatch{
		MergedBatchID:  "merged-idempotent",
		OpenVPNBatchID: "openvpn-idempotent",
		XrayBatchID:    "xray-idempotent",
	}
	server.mergedUsageLoaded = true
	if err := server.persistMergedUsageStateLocked(); err != nil {
		t.Fatal(err)
	}

	req := &nodev1.AckUsageRequest{BatchId: "merged-idempotent"}
	first, err := server.ackMergedUserUsage(ctx, req)
	if err != nil || !first.GetAcknowledged() {
		t.Fatalf("first merged ACK failed: %v", err)
	}
	second, err := server.ackMergedUserUsage(ctx, req)
	if err != nil || !second.GetAcknowledged() {
		t.Fatalf(
			"second merged ACK should be idempotent: %v",
			err,
		)
	}
}
