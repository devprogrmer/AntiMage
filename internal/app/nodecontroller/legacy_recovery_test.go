package nodecontroller

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	operationapp "github.com/antimage/antimage/internal/app/operations"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
)

func TestSyncConfigLostACKReconcilesHashWithoutReplay(t *testing.T) {
	c := rolloutTestController(t)
	ctx := legacyConfigEvidence(context.Background(), `{"inbounds":[]}`)
	dispatches := 0
	err := c.executeLegacyNodeCommand(ctx, 1, "lost-config", "sync_config", func(context.Context, *nodev1.DestructiveFence) (*nodev1.RuntimeActionResponse, error) {
		dispatches++
		return nil, context.DeadlineExceeded
	}, nil)
	if !errors.Is(err, ErrCommandOutcomeUnknown) {
		t.Fatal(err)
	}
	op, err := operationapp.Get(ctx, c.repo.db, "lost-config")
	if err != nil {
		t.Fatal(err)
	}
	originalDeadline := op.Metadata["phase_deadline_at_nanos"]
	worker, release, err := c.beginNodeExecution(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	probes := 0
	err = c.reconcileLegacyMaintenance(worker, op.ID, func(context.Context, int64) (*nodev1.RuntimeState, error) {
		probes++
		return &nodev1.RuntimeState{Connected: true, Started: true, SampledAtUnixNano: time.Now().UnixNano(), ActiveConfigSha256: op.Metadata["expected_config_sha256"].(string)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := operationapp.Get(ctx, c.repo.db, op.ID)
	if err != nil || stored.Phase != "outcome_reconciled" || stored.State != "completed" || probes != 1 || dispatches != 1 {
		t.Fatalf("recovery: %+v probes=%d dispatches=%d err=%v", stored, probes, dispatches, err)
	}
	if stored.Metadata["phase_deadline_at_nanos"] != originalDeadline {
		t.Fatal("recovery reset absolute deadline")
	}
}

func TestLegacyRecoveryExpiredDeadlineDoesNotProbeOrReplay(t *testing.T) {
	c := rolloutTestController(t)
	ctx := context.Background()
	err := c.executeLegacyNodeCommand(ctx, 1, "expired-core", "core_update", func(context.Context, *nodev1.DestructiveFence) (*nodev1.RuntimeActionResponse, error) {
		return nil, context.DeadlineExceeded
	}, nil)
	if !errors.Is(err, ErrCommandOutcomeUnknown) {
		t.Fatal(err)
	}
	op, err := operationapp.Get(ctx, c.repo.db, "expired-core")
	if err != nil {
		t.Fatal(err)
	}
	op.Metadata["phase_deadline_at_nanos"] = time.Now().Add(-time.Second).UnixNano()
	if err := operationapp.Save(ctx, c.repo.db, "sqlite", op); err != nil {
		t.Fatal(err)
	}
	worker, release, err := c.beginNodeExecution(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := c.reconcileLegacyMaintenance(worker, op.ID, func(context.Context, int64) (*nodev1.RuntimeState, error) {
		t.Fatal("expired deadline performed probe")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	stored, err := operationapp.Get(ctx, c.repo.db, op.ID)
	if err != nil || stored.Phase != "manual_recovery_required" || operationapp.Terminal(stored.State) {
		t.Fatalf("unsafe expired recovery: %+v %v", stored, err)
	}
	if err := c.repo.StartNodeUpdate(ctx, NodeUpdateOperation{ID: "unsafe-competitor", NodeID: 1}); err == nil {
		t.Fatal("unverified remote outcome released reservation")
	}
}

func TestLegacyRecoveryRequiresActualRunningEvidence(t *testing.T) {
	boundary := time.Now().Add(-time.Minute)
	for _, kind := range []string{"core_update", "core_restart", "sync_config", "node_restart", "host_reboot"} {
		t.Run(kind, func(t *testing.T) {
			op := operationapp.Operation{Type: kind, Metadata: map[string]any{"restart_boundary_nanos": boundary.UnixNano(), "expected_config_sha256": "hash", "expected_core_version": "1.2.3"}}
			state := &nodev1.RuntimeState{Connected: true, Started: true, SampledAtUnixNano: time.Now().UnixNano(), ProcessStartedAtUnixNano: boundary.Add(time.Second).UnixNano(), CoreProcessStartedAtUnixNano: boundary.Add(time.Second).UnixNano(), ActiveConfigSha256: "hash", RunningCoreVersion: "1.2.3"}
			if !legacyRuntimeEvidenceMatches(op, state) {
				t.Fatal("fresh matching runtime rejected")
			}
			state.SampledAtUnixNano = boundary.UnixNano()
			if legacyRuntimeEvidenceMatches(op, state) {
				t.Fatal("stale sample accepted")
			}
			state.SampledAtUnixNano = time.Now().UnixNano()
			state.Started = false
			if legacyRuntimeEvidenceMatches(op, state) {
				t.Fatal("unhealthy runtime accepted")
			}
		})
	}
}

func TestLegacyRecoveryAttemptLimitSurvivesReconstruction(t *testing.T) {
	c := rolloutTestController(t)
	ctx := context.Background()
	err := c.executeLegacyNodeCommand(ctx, 1, "limited-core", "core_update", func(context.Context, *nodev1.DestructiveFence) (*nodev1.RuntimeActionResponse, error) {
		return nil, context.DeadlineExceeded
	}, nil)
	if !errors.Is(err, ErrCommandOutcomeUnknown) {
		t.Fatal(err)
	}
	op, err := operationapp.Get(ctx, c.repo.db, "limited-core")
	if err != nil {
		t.Fatal(err)
	}
	op.Metadata["recovery_attempts"] = 2
	if err := operationapp.Save(ctx, c.repo.db, "sqlite", op); err != nil {
		t.Fatal(err)
	}
	worker, release, err := c.beginNodeExecution(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	probes := 0
	probe := func(context.Context, int64) (*nodev1.RuntimeState, error) {
		probes++
		return nil, errors.New("runtime unavailable")
	}
	if err := c.reconcileLegacyMaintenance(worker, op.ID, probe); err != nil {
		t.Fatal(err)
	}
	stored, err := operationapp.Get(ctx, c.repo.db, op.ID)
	if err != nil || stored.Phase != "manual_recovery_required" || fmt.Sprint(stored.Metadata["recovery_attempts"]) != "3" || probes != 1 {
		t.Fatalf("recovery limit: %+v probes=%d error=%v", stored, probes, err)
	}
	if err := c.reconcileLegacyMaintenance(worker, op.ID, probe); err != nil {
		t.Fatal(err)
	}
	if probes != 1 {
		t.Fatal("reconstructed recovery replenished exhausted attempts")
	}
}
