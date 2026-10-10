package nodecontroller

import (
	"testing"
	"time"

	operationapp "github.com/antimage/antimage/internal/app/operations"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"google.golang.org/protobuf/proto"
)

func TestNodeFinalizationRequiresCompletedReceiptAndActualFreshRuntime(t *testing.T) {
	target := testRolloutTarget()
	op := NodeUpdateOperation{ID: "finalize-op", DesiredVersion: target.Version, ResolvedTarget: &target, RestartRequestedAt: time.Now().Add(-time.Minute)}
	command := nodeServiceCommandID(op.ID, "commit_update", op.ID)
	stored := operationapp.Operation{Metadata: map[string]any{"finalization_resource_generation": int64(41)}}
	valid := &nodev1.RuntimeState{EvidenceOperationId: op.ID, EvidenceCommandId: command, EvidenceCommandState: "completed", EvidenceResourceGeneration: 41, CurrentResourceGeneration: 42, NodeVersion: target.Version, CommitSha: target.Commit, Connected: true, Started: true, ProcessStartedAtUnixNano: time.Now().Add(-time.Second).UnixNano(), SampledAtUnixNano: time.Now().UnixNano()}
	if !nodeFinalizationReceiptMatches(stored, op, valid, command) {
		t.Fatal("fresh completed receipt rejected")
	}
	for _, test := range []struct {
		name   string
		change func(*nodev1.RuntimeState)
	}{
		{"started-command", func(s *nodev1.RuntimeState) { s.EvidenceCommandState = "started" }},
		{"superseded", func(s *nodev1.RuntimeState) { s.EvidenceCommandState = "superseded" }},
		{"different-command", func(s *nodev1.RuntimeState) { s.EvidenceCommandId = "other-command" }},
		{"different-owner", func(s *nodev1.RuntimeState) { s.EvidenceOperationId = "other-owner" }},
		{"different-generation", func(s *nodev1.RuntimeState) { s.EvidenceResourceGeneration++ }},
		{"old-process", func(s *nodev1.RuntimeState) {
			s.ProcessStartedAtUnixNano = op.RestartRequestedAt.Add(-time.Second).UnixNano()
		}},
		{"stale-probe", func(s *nodev1.RuntimeState) { s.SampledAtUnixNano = time.Now().Add(-time.Minute).UnixNano() }},
		{"wrong-version", func(s *nodev1.RuntimeState) { s.NodeVersion = "v0.0.1" }},
		{"wrong-build", func(s *nodev1.RuntimeState) { s.CommitSha = "wrong" }},
		{"stopped", func(s *nodev1.RuntimeState) { s.Started = false }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := proto.Clone(valid).(*nodev1.RuntimeState)
			test.change(candidate)
			if nodeFinalizationReceiptMatches(stored, op, candidate, command) {
				t.Fatal("unverifiable finalization claimed success")
			}
		})
	}
}
