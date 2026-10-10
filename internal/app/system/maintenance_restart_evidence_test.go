package system

import (
	"context"
	"testing"
	"time"
)

func TestReconstructedPanelRetriesObservationAfterLeaseExpires(t *testing.T) {
	db := newPanelUpdateDB(t)
	previous := NewMaintenanceOperationStoreWithDB(db, "sqlite")
	op := previous.Start("restart", []string{"restart"}, "Restarting", "v1.2.3")
	previous.MarkRestarting(op.ID, "Waiting for restart")
	if err := previous.AcquireExecution(context.Background(), op.ID); err != nil {
		t.Fatal(err)
	}
	detector := &mutableMaintenanceRuntime{info: RuntimeInfo{RunningVersion: "v1.2.3", ProcessStartedAt: op.StartedAtNanos + int64(time.Second)}}
	scheduler := &recordingMaintenanceScheduler{}
	service := NewMaintenanceServiceWithDeps(detector, nil, scheduler)
	service.ops = NewMaintenanceOperationStoreWithDB(db, "sqlite")
	service.recoveryPending = true
	if got := service.Status(); !got.Running || got.Phase != "restarting" {
		t.Fatalf("live previous lease must retain ownership: %+v", got)
	}
	if _, err := db.Exec(`UPDATE operation_executor_leases SET expires_at=0 WHERE operation_id=?`, op.ID); err != nil {
		t.Fatal(err)
	}
	if got := service.Status(); got.Running || got.Phase != "completed" {
		t.Fatalf("poll did not reconcile fresh runtime after takeover: %+v", got)
	}
	if len(scheduler.calls) != 0 {
		t.Fatalf("recovery replayed restart: %+v", scheduler.calls)
	}
}

func TestOldPanelProcessDoesNotPrematurelyTriggerRollback(t *testing.T) {
	db := newPanelUpdateDB(t)
	store := NewMaintenanceOperationStoreWithDB(db, "sqlite")
	op := store.Start("update", []string{"update", "--version", "v1.2.4"}, "Preparing update", "v1.2.3")
	store.MarkRestarting(op.ID, "Waiting for restart")
	detector := &mutableMaintenanceRuntime{info: RuntimeInfo{RunningVersion: "v1.2.3", ProcessStartedAt: op.StartedAtNanos - int64(time.Second)}}
	scheduler := &recordingMaintenanceScheduler{}
	service := NewMaintenanceServiceWithDeps(detector, nil, scheduler)
	service.ops = NewMaintenanceOperationStoreWithDB(db, "sqlite")
	service.reconcilePersistedOperation()
	if got := service.Status(); got.Phase != "restarting" || !got.Running {
		t.Fatalf("old process triggered premature failure: %+v", got)
	}
	if len(scheduler.calls) != 0 {
		t.Fatalf("old process triggered rollback: %+v", scheduler.calls)
	}
}
