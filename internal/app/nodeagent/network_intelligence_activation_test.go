package nodeagent

import (
	"encoding/json"
	"testing"
)

func TestNetworkIntelligenceActivationOptInAndSnapshot(t *testing.T) {
	disabled := New(Config{})
	if disabled.networkIntelligenceLifecycle != nil || disabled.networkIntelligenceSnapshot() != "" {
		t.Fatal("default must not initialize capture")
	}
	enabled := New(Config{NetworkIntelligenceEnabled: true})
	defer enabled.stopNetworkIntelligence()
	if enabled.networkIntelligenceLifecycle == nil || !enabled.networkIntelligenceLifecycle.Enabled() {
		t.Fatal("opt-in unavailable")
	}
	var snapshot struct {
		Version int
		Flows   []any
	}
	if err := json.Unmarshal([]byte(enabled.networkIntelligenceSnapshot()), &snapshot); err != nil || snapshot.Version != 1 || len(snapshot.Flows) != 0 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	enabled.stopNetworkIntelligence()
	if enabled.networkIntelligenceLifecycle.Enabled() {
		t.Fatal("kill switch did not disable")
	}
}

func TestNetworkIntelligencePublishRequiresAuthoritativeNodeIdentity(t *testing.T) {
	s := New(Config{NetworkIntelligenceEnabled: true})
	defer s.stopNetworkIntelligence()
	if err := s.publishNetworkIntelligence(preparedWireGuardRuntime{}); err == nil {
		t.Fatal("missing policy admitted")
	}
	if len(s.networkIntelligenceTracker.Snapshot(s.startedAt)) != 0 {
		t.Fatal("failed verification published flows")
	}
}
