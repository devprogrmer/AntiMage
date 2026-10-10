package nodecontroller

import (
	operationapp "github.com/antimage/antimage/internal/app/operations"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"testing"
	"time"
)

func TestStopRuntimeRecoveryRequiresMatchingDurableReceipt(t *testing.T) {
	boundary := time.Now().Add(-time.Minute).UnixNano()
	op := operationapp.Operation{ID: "stop-op", Type: "stop_runtime", Metadata: map[string]any{"restart_boundary_nanos": boundary, "command_id": "stop-command", "dispatched_resource_generation": int64(41)}}
	good := func() *nodev1.RuntimeState {
		return &nodev1.RuntimeState{Connected: true, SampledAtUnixNano: time.Now().UnixNano(), RuntimeStopVerified: true, EvidenceOperationId: op.ID, EvidenceCommandId: "stop-command", EvidenceResourceGeneration: 41, EvidenceCommandState: "completed", ProcessStartedAtUnixNano: boundary - 1, EvidenceProcessStartedAtUnixNano: boundary - 1}
	}
	if !legacyRuntimeEvidenceMatches(op, good()) {
		t.Fatal("verified stop receipt rejected")
	}
	cases := map[string]func(*nodev1.RuntimeState){
		"stop not dispatched":   func(s *nodev1.RuntimeState) { s.EvidenceCommandState = "accepted"; s.RuntimeStopVerified = false },
		"runtime still running": func(s *nodev1.RuntimeState) { s.Started = true },
		"supervisor restarted":  func(s *nodev1.RuntimeState) { s.Started = true; s.CoreProcessStartedAtUnixNano = time.Now().UnixNano() },
		"newer fenced owner":    func(s *nodev1.RuntimeState) { s.EvidenceResourceGeneration++; s.EvidenceCommandState = "superseded" },
		"node process replaced": func(s *nodev1.RuntimeState) { s.ProcessStartedAtUnixNano++ },
		"different command":     func(s *nodev1.RuntimeState) { s.EvidenceCommandId = "other" },
		"old node":              func(s *nodev1.RuntimeState) { s.RuntimeStopVerified = false; s.EvidenceCommandId = "" },
		"stale sample":          func(s *nodev1.RuntimeState) { s.SampledAtUnixNano = boundary },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s := good()
			change(s)
			if legacyRuntimeEvidenceMatches(op, s) {
				t.Fatal("unsafe stop evidence accepted")
			}
		})
	}
}
