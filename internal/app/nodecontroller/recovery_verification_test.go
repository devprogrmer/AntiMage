package nodecontroller

import (
	"context"
	"testing"
	"time"

	operationapp "github.com/antimage/antimage/internal/app/operations"
)

// This acceptance test exercises the generic persistent operation and the
// recovery entry point wired into Server.StartBackground. An expired standalone
// maintenance operation with unknown remote state must retain its reservation.
func TestStartupRecoveryReconcilesExpiredStandaloneMaintenance(t *testing.T) {
	for _, phase := range []string{"downloading", "installing", "waiting_for_reconnect", "rolling_back"} {
		t.Run(phase, func(t *testing.T) {
			controller := rolloutTestController(t)
			ctx := context.Background()
			op := NodeUpdateOperation{ID: "interrupted-" + phase, NodeID: 1, RequestedVersion: "v1.2.4", DesiredVersion: "v1.2.4", PreviousVersion: "v1.2.3", Phase: phase, StartedAt: time.Now().Add(-time.Hour)}
			if phase == "rolling_back" {
				op.Action = "rollback"
			}
			if err := controller.repo.StartNodeUpdate(ctx, op); err != nil {
				t.Fatal(err)
			}
			// Reconstruct the controller while preserving the database and locks.
			reopened := NewController(NewRepository(controller.repo.db, "sqlite"))
			if err := reopened.RecoverRollouts(ctx); err != nil {
				t.Fatal(err)
			}
			stored, err := operationapp.Get(ctx, controller.repo.db, op.ID)
			if err != nil {
				t.Fatal(err)
			}
			if operationapp.Terminal(stored.State) || stored.Phase != "manual_recovery_required" {
				t.Fatalf("unverifiable remote state lost reservation: %s/%s", stored.State, stored.Phase)
			}
			var locks int
			if err := controller.repo.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM operation_locks WHERE operation_id=?", op.ID).Scan(&locks); err != nil {
				t.Fatal(err)
			}
			if locks != 1 {
				t.Fatalf("unknown standalone operation retains %d resource locks; want 1", locks)
			}
		})
	}
}

func TestStartupRecoveryAdjacentPhasesAndIdempotency(t *testing.T) {
	for _, phase := range []string{"verifying", "backing_up", "restarting", "verifying_version", "health_check", "rollback_restarting", "rollback_verifying"} {
		t.Run(phase, func(t *testing.T) {
			c := rolloutTestController(t)
			ctx := context.Background()
			op := NodeUpdateOperation{ID: "recovery-" + phase, NodeID: 1, Phase: phase, StartedAt: time.Now().Add(-time.Hour)}
			if err := c.repo.StartNodeUpdate(ctx, op); err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				reopened := NewController(NewRepository(c.repo.db, "sqlite"))
				if err := reopened.RecoverRollouts(ctx); err != nil {
					t.Fatal(err)
				}
			}
			stored, err := c.repo.NodeUpdateOperation(ctx, op.ID)
			if err != nil || stored.Phase != "manual_recovery_required" || stored.RecoveryError == "" || stored.RollbackError == "" {
				t.Fatalf("missing explicit unsafe-recovery evidence: %+v %v", stored, err)
			}
			var count int
			if err := c.repo.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM operation_events WHERE operation_id=? AND phase='manual_recovery_required'", op.ID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("repeated startup duplicated transition: %d %v", count, err)
			}
		})
	}
}

func TestPersistedReconnectDeadlineIsNotResetByWait(t *testing.T) {
	c := rolloutTestController(t)
	ctx := context.Background()
	op := NodeUpdateOperation{ID: "expired-reconnect", NodeID: 1, Phase: "waiting_for_reconnect"}
	if err := c.repo.StartNodeUpdate(ctx, op); err != nil {
		t.Fatal(err)
	}
	boundary := time.Now().UTC().Add(-time.Hour)
	if err := c.repo.RecordNodeRestart(ctx, op.ID, boundary); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		start := time.Now()
		if _, _, err := c.waitForPersistedNodeUpdate(ctx, op.ID, "v1.2.4"); err == nil {
			t.Fatal("expired reconnect accepted")
		}
		if time.Since(start) > time.Second {
			t.Fatal("expired deadline granted a fresh wait")
		}
	}
	stored, err := c.repo.NodeUpdateOperation(ctx, op.ID)
	if err != nil || !stored.RestartRequestedAt.Equal(boundary) || !stored.ReconnectDeadline.Equal(boundary.Add(4*time.Minute)) {
		t.Fatalf("restart boundary/deadline changed: %+v %v", stored, err)
	}
}

func TestStartupRepairsOrphanLocksOnceAndPreservesActiveOwner(t *testing.T) {
	c := rolloutTestController(t)
	ctx := context.Background()
	active := NodeUpdateOperation{ID: "active-draft-child", NodeID: 1, RolloutID: "draft", Phase: "queued"}
	if err := c.repo.StartNodeUpdate(ctx, active); err != nil {
		t.Fatal(err)
	}
	if _, err := c.repo.db.ExecContext(ctx, "INSERT INTO operation_locks(target_type,target_id,operation_id) VALUES('node','2','missing-owner')"); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := c.RecoverRollouts(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := c.repo.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM operation_locks").Scan(&count); err != nil || count != 1 {
		t.Fatalf("active lock removed or orphan survived: %d %v", count, err)
	}
	if err := c.repo.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM operation_events WHERE operation_id='missing-owner' AND phase='orphan_lock_repaired'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("repair audit duplicated or missing: %d %v", count, err)
	}
}

func TestManualRollbackFailureRetainsDispatchedOwnership(t *testing.T) {
	for _, item := range []struct {
		name       string
		dispatched bool
		failure    error
	}{
		{"lost-ack", true, ErrCommandOutcomeUnknown},
		{"health-timeout", true, context.DeadlineExceeded},
		{"preflight", false, context.DeadlineExceeded},
		{"stale-executor", true, operationapp.ErrLeaseLost},
	} {
		t.Run(item.name, func(t *testing.T) {
			c := rolloutTestController(t)
			op := NodeUpdateOperation{ID: "rollback-" + item.name, NodeID: 1, Action: "rollback", BackupIdentity: "verified-backup", DesiredVersion: "v1.2.3", Phase: "restoring"}
			if err := c.repo.StartNodeUpdate(context.Background(), op); err != nil {
				t.Fatal(err)
			}
			worker, release, err := c.beginNodeExecution(context.Background(), op.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			boundary := time.Now().Add(-time.Minute).UTC()
			if err := c.repo.RecordNodeRestart(worker, op.ID, boundary); err != nil {
				t.Fatal(err)
			}
			err = c.recordRollbackFailure(worker, op, item.dispatched, item.failure)
			if item.failure == operationapp.ErrLeaseLost {
				if err != operationapp.ErrLeaseLost {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			stored, err := operationapp.Get(context.Background(), c.repo.db, op.ID)
			if err != nil {
				t.Fatal(err)
			}
			update, err := decodeNodeUpdate(stored)
			if err != nil {
				t.Fatal(err)
			}
			var locks int
			if err := c.repo.db.QueryRow("SELECT COUNT(*) FROM operation_locks WHERE operation_id=?", op.ID).Scan(&locks); err != nil {
				t.Fatal(err)
			}
			if !item.dispatched {
				if stored.State != "failed" || locks != 0 {
					t.Fatalf("preflight failed incorrectly: %+v locks=%d", stored, locks)
				}
				return
			}
			expected := "outcome_unknown"
			if item.failure == operationapp.ErrLeaseLost {
				expected = "restoring"
			}
			if stored.Phase != expected || operationapp.Terminal(stored.State) || locks != 1 || update.CompletedAt != nil || !update.ReconnectDeadline.Equal(boundary.Add(4*time.Minute)) {
				t.Fatalf("dispatched rollback released or changed evidence: %+v locks=%d", update, locks)
			}
			if err := c.repo.StartNodeUpdate(context.Background(), NodeUpdateOperation{ID: "rival", NodeID: 1}); err == nil {
				t.Fatal("unknown rollback permitted competing update")
			}
		})
	}
}

func TestAutomaticRollbackUnknownOutcomeKeepsRestoreRecoveryPhase(t *testing.T) {
	c := rolloutTestController(t)
	op := NodeUpdateOperation{ID: "automatic-rollback-lost", NodeID: 1, PreviousVersion: "v1.2.3", DesiredVersion: "v1.2.4", Phase: "rolling_back"}
	if err := c.repo.StartNodeUpdate(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	worker, release, err := c.beginNodeExecution(context.Background(), op.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := c.repo.RecordNodeRestart(worker, op.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := c.recordRollbackFailure(worker, op, true, ErrCommandOutcomeUnknown); err != nil {
		t.Fatal(err)
	}
	stored, err := c.repo.NodeUpdateOperation(context.Background(), op.ID)
	if err != nil || stored.Phase != "rollback_verifying" || stored.Action == "rollback" || stored.DesiredVersion != op.DesiredVersion || stored.PreviousVersion != op.PreviousVersion || stored.CompletedAt != nil {
		t.Fatalf("automatic restore misclassified: %+v %v", stored, err)
	}
	if err := c.repo.StartNodeUpdate(context.Background(), NodeUpdateOperation{ID: "competitor", NodeID: 1}); err == nil {
		t.Fatal("unknown automatic restore released resource")
	}
}
