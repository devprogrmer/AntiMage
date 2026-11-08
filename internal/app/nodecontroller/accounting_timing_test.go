package nodecontroller

import (
	"strconv"
	"testing"
	"time"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestRuntimeAccountingTimingWarnings(t *testing.T) {
	sampledAt := int64(1_800_000_000)
	batchAt := func(seconds int64) string {
		return "xray-" + strconv.FormatInt(time.Unix(seconds, 0).UnixNano(), 10)
	}
	for _, tc := range []struct {
		name, protocol, batch string
		sample, checkpoint    int64
		loaded, stale, stuck  bool
	}{
		{"normal", "xray", batchAt(sampledAt - 20), sampledAt, sampledAt - 5, true, false, false},
		{"checkpoint missed", "xray", "", sampledAt, sampledAt - 181, true, true, false},
		{"ACK overdue", "xray", batchAt(sampledAt - 601), sampledAt, sampledAt - 1, true, false, true},
		{"old node without sample time", "xray", batchAt(sampledAt - 601), 0, sampledAt - 181, true, false, false},
		{"never recorded", "xray", "", sampledAt, 0, true, false, false},
		{"unloaded state", "xray", batchAt(sampledAt - 601), sampledAt, sampledAt - 181, false, false, false},
		{"clock moved back", "xray", batchAt(sampledAt + 600), sampledAt, sampledAt + 200, true, false, false},
		{"opaque batch", "xray", "xray-42", sampledAt, sampledAt - 2, true, false, false},
		{"incorrect prefix", "wireguard", batchAt(sampledAt - 601), sampledAt, sampledAt - 2, true, false, false},
		{"unknown protocol", "policy", "policy-1700000000000000000", sampledAt, sampledAt - 181, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metrics := &nodev1.MetricsResponse{
				SampledAtUnix: tc.sample,
				Accounting: &nodev1.AccountingHealth{Protocols: []*nodev1.AccountingProtocolStatus{{
					Protocol: tc.protocol, StateLoaded: tc.loaded,
					PendingBatchId: tc.batch, LastCheckpointUnix: tc.checkpoint,
				}}},
			}
			result := runtimeResult(NodeRow{}, nil, metrics)
			got := result.Accounting.Protocols[0]
			if got.CheckpointOverdue != tc.stale || got.PendingACKOverdue != tc.stuck {
				t.Fatalf("timing warnings: checkpoint=%t ACK=%t, want %t/%t", got.CheckpointOverdue, got.PendingACKOverdue, tc.stale, tc.stuck)
			}
		})
	}
}

func TestAccountingReflectionOverdueUsesNodeSampleTime(t *testing.T) {
	sampledAt := int64(1_800_000_000)
	for _, tc := range []struct {
		name   string
		health *nodev1.AccountingHealth
		sample int64
		want   bool
	}{
		{"fresh", &nodev1.AccountingHealth{ReflectionLedgerLoaded: true, AwaitingReflectionBatches: 1, OldestReflectionBatchUnix: sampledAt - 600}, sampledAt, false},
		{"stale", &nodev1.AccountingHealth{ReflectionLedgerLoaded: true, AwaitingReflectionBatches: 1, OldestReflectionBatchUnix: sampledAt - 601}, sampledAt, true},
		{"old node", &nodev1.AccountingHealth{ReflectionLedgerLoaded: true, AwaitingReflectionBatches: 1, OldestReflectionBatchUnix: sampledAt - 601}, 0, false},
		{"unloaded", &nodev1.AccountingHealth{AwaitingReflectionBatches: 1, OldestReflectionBatchUnix: sampledAt - 601}, sampledAt, false},
		{"empty", &nodev1.AccountingHealth{ReflectionLedgerLoaded: true, OldestReflectionBatchUnix: sampledAt - 601}, sampledAt, false},
		{"unknown age", &nodev1.AccountingHealth{ReflectionLedgerLoaded: true, AwaitingReflectionBatches: 1}, sampledAt, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metrics := &nodev1.MetricsResponse{SampledAtUnix: tc.sample, Accounting: tc.health}
			result := runtimeResult(NodeRow{}, nil, metrics)
			if got := result.Accounting.ReflectionOverdue; got != tc.want {
				t.Fatalf("reflection overdue = %t, want %t", got, tc.want)
			}
		})
	}
}
