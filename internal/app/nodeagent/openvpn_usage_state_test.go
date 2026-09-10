package nodeagent

import (
	"context"
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestOpenVPNUsagePendingBatchSurvivesRestart(t *testing.T) {
	dataDir := t.TempDir()

	first := New(Config{DataDir: dataDir})

	first.openVPNUsageMu.Lock()
	first.openVPNUsageLoaded = true
	first.openVPNUsageBaseline = map[string]uint64{
		"old-session": 100,
	}
	first.openVPNUsagePending = &openVPNUsagePendingBatch{
		BatchID: "openvpn-test-batch",
		Samples: []openVPNUsageSample{
			{
				UserID:     42,
				InboundTag: "openvpn-main",
				Value:      4600,
				Online:     true,
			},
		},
		NextBaseline: map[string]uint64{
			"old-session": 100,
			"new-session": 4600,
		},
	}

	err := first.persistOpenVPNUsageStateLocked()
	first.openVPNUsageMu.Unlock()

	if err != nil {
		t.Fatal(err)
	}

	restarted := New(Config{DataDir: dataDir})

	batch, err := restarted.CollectUserUsage(
		context.Background(),
		&nodev1.CollectUsageRequest{Reset_: true},
	)
	if err != nil {
		t.Fatal(err)
	}

	if batch.GetBatchId() != "openvpn-test-batch" {
		t.Fatalf(
			"pending batch not restored: %q",
			batch.GetBatchId(),
		)
	}

	if len(batch.GetStats()) != 1 {
		t.Fatalf(
			"expected 1 restored sample, got %d",
			len(batch.GetStats()),
		)
	}

	if got := batch.GetStats()[0].GetValue(); got != 4600 {
		t.Fatalf(
			"unexpected restored value: %d",
			got,
		)
	}
}

func TestOpenVPNUsageAckSurvivesRestart(t *testing.T) {
	dataDir := t.TempDir()

	first := New(Config{DataDir: dataDir})

	first.openVPNUsageMu.Lock()
	first.openVPNUsageLoaded = true
	first.openVPNUsagePending = &openVPNUsagePendingBatch{
		BatchID: "openvpn-ack-test",
		Samples: []openVPNUsageSample{
			{
				UserID:     42,
				InboundTag: "openvpn-main",
				Value:      4600,
				Online:     true,
			},
		},
		NextBaseline: map[string]uint64{
			"session-a": 4600,
		},
	}

	err := first.persistOpenVPNUsageStateLocked()
	first.openVPNUsageMu.Unlock()

	if err != nil {
		t.Fatal(err)
	}

	restarted := New(Config{DataDir: dataDir})

	ack, err := restarted.AckUserUsage(
		context.Background(),
		&nodev1.AckUsageRequest{
			BatchId: "openvpn-ack-test",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if !ack.GetAcknowledged() {
		t.Fatal("expected restored pending batch ACK")
	}

	secondRestart := New(Config{DataDir: dataDir})

	secondRestart.openVPNUsageMu.Lock()
	err = secondRestart.ensureOpenVPNUsageStateLoadedLocked()

	if err != nil {
		secondRestart.openVPNUsageMu.Unlock()
		t.Fatal(err)
	}

	if secondRestart.openVPNUsagePending != nil {
		secondRestart.openVPNUsageMu.Unlock()
		t.Fatal("pending batch survived successful ACK")
	}

	if got := secondRestart.openVPNUsageBaseline["session-a"]; got != 4600 {
		secondRestart.openVPNUsageMu.Unlock()
		t.Fatalf(
			"baseline not persisted after ACK: %d",
			got,
		)
	}

	secondRestart.openVPNUsageMu.Unlock()
}
