package nodecontroller

import (
	"testing"

	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestRuntimeResultKeepsWorkerDegradationSeparateFromConnectivity(t *testing.T) {
	state := &nodev1.RuntimeState{
		Connected:    true,
		Capabilities: []string{"accounting_degraded"},
	}
	result := runtimeResult(NodeRow{ID: 9, Status: "connected"}, state, nil)
	if result.Status != "connected" || !result.Connected {
		t.Fatalf("worker degradation changed connectivity: status=%q connected=%v", result.Status, result.Connected)
	}
	if result.AgentStatus != "degraded" {
		t.Fatalf("agent status=%q, want degraded", result.AgentStatus)
	}
}
