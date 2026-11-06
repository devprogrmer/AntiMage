package nodecontroller

import (
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestAccountingHealthReachesNodeListWithoutChangingConnectionState(t *testing.T) {
	metrics := &nodev1.MetricsResponse{Accounting: &nodev1.AccountingHealth{
		ReflectionLedgerLoaded: true, AwaitingReflectionBatches: 2, AwaitingReflectionBytes: 55,
		Protocols: []*nodev1.AccountingProtocolStatus{{Protocol: "xray", StateLoaded: true, PendingBatchId: "pending", PendingBytes: 50, LastAckedBatchId: "acked", LastCheckpointUnix: 1234, Failure: "disk full"}},
	}}
	result := runtimeResult(NodeRow{ID: 7, Name: "node", Status: "connected"}, &nodev1.RuntimeState{Connected: true, Started: true}, metrics)
	item := NodeListItem{}
	applyRuntimeToNodeItem(&item, result)
	if item.Status != "connected" || item.AgentStatus != "connected" || item.Accounting == nil {
		t.Fatalf("accounting should not change the node's connection status: %#v", item)
	}
	if item.Accounting.AwaitingReflectionBatches != 2 || item.Accounting.AwaitingReflectionBytes != 55 || len(item.Accounting.Protocols) != 1 {
		t.Fatalf("reflection summary lost: %#v", item.Accounting)
	}
	status := item.Accounting.Protocols[0]
	if status.PendingBatchID != "pending" || status.PendingBytes != 50 || status.LastAckedBatchID != "acked" || status.LastCheckpointUnix != 1234 || status.Failure != "disk full" {
		t.Fatalf("accounting detail lost: %#v", status)
	}
}
