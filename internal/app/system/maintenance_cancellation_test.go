package system

import (
	"context"
	"fmt"
	"testing"
	"time"
)

type interruptedContextScheduler struct {
	deadline       time.Time
	callerCanceled bool
	calls          int
	failure        error
}

func (s *interruptedContextScheduler) Schedule([]string) error {
	return fmt.Errorf("unexpected unfenced scheduling fallback")
}

func (s *interruptedContextScheduler) ScheduleWithProgressContext(ctx context.Context, args []string, onOutput func(string), onDone func(error)) error {
	s.calls++
	s.deadline, _ = ctx.Deadline()
	s.callerCanceled = ctx.Err() != nil
	if s.failure != nil {
		onDone(s.failure)
	} else {
		onDone(context.DeadlineExceeded)
	}
	return nil
}

func TestPanelInstallerNonzeroExitRetainsUnknownOwnership(t *testing.T) {
	db := newPanelUpdateDB(t)
	scheduler := &interruptedContextScheduler{failure: fmt.Errorf("installer exited after a possible file commit")}
	service := NewMaintenanceServiceWithDeps(&mutableMaintenanceRuntime{info: RuntimeInfo{Mode: "binary", RunningVersion: "v1.2.2"}}, nil, scheduler)
	service.ops = NewMaintenanceOperationStoreWithDB(db, "sqlite")
	op, err := service.startOperation(context.Background(), "update", []string{"update", "--version", "v1.2.3"}, "Updating", "v1.2.2", time.Now().Add(-time.Hour).UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	if op.Phase != "outcome_unknown" || !op.Running || op.FinishedAt != nil || scheduler.calls != 1 {
		t.Fatalf("nonzero exit was treated as proof of no side effects: %+v", op)
	}
	if _, err := service.Restart(context.Background()); err == nil {
		t.Fatal("unknown installer outcome allowed a conflicting restart")
	}
}

func TestPanelInstallerTimeoutPersistsUnknownOutcomeWithoutBlindRollback(t *testing.T) {
	db := newPanelUpdateDB(t)
	scheduler := &interruptedContextScheduler{}
	detector := &mutableMaintenanceRuntime{info: RuntimeInfo{Mode: "binary", RunningVersion: "v1.2.2"}}
	service := NewMaintenanceServiceWithDeps(detector, nil, scheduler)
	service.ops = NewMaintenanceOperationStoreWithDB(db, "sqlite")
	request, cancel := context.WithCancel(context.Background())
	cancel()
	op, err := service.startOperation(request, "update", []string{"update", "--version", "v1.2.3"}, "Updating", "v1.2.2", time.Now().Add(-time.Hour).UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	if scheduler.callerCanceled || !scheduler.deadline.Equal(time.Unix(0, op.StartedAtNanos).Add(3*time.Minute)) {
		t.Fatal("installer did not retain its persisted absolute deadline")
	}
	if op.Phase != "outcome_unknown" || !op.Running || op.FinishedAt != nil {
		t.Fatalf("timeout was falsely treated as a known failure: %+v", op)
	}
	expirePanelTestExecutor(t, service.ops)
	service.ops = NewMaintenanceOperationStoreWithDB(db, "sqlite")
	if got := service.Status(); got.Phase != "outcome_unknown" || !got.Running {
		t.Fatalf("unknown outcome lost after reconstruction: %+v", got)
	}
	if scheduler.calls != 1 {
		t.Fatal("unknown outcome caused destructive replay")
	}
	service.ops.mu.Lock()
	service.ops.latest.StartedAtNanos = time.Now().Add(-4 * time.Minute).UnixNano()
	service.ops.persistLocked()
	service.ops.mu.Unlock()
	expirePanelTestExecutor(t, service.ops)
	service.ops = NewMaintenanceOperationStoreWithDB(db, "sqlite")
	if got := service.Status(); got.Phase != "manual_recovery_required" || !got.Running {
		t.Fatalf("expired ambiguous outcome did not retain ownership: %+v", got)
	}
	if scheduler.calls != 1 {
		t.Fatal("timeout caused blind rollback scheduling")
	}
}

func TestPanelRestartWatchdogRetainsUnknownOwnership(t *testing.T) {
	db := newPanelUpdateDB(t)
	scheduler := &interruptedContextScheduler{}
	detector := &mutableMaintenanceRuntime{info: RuntimeInfo{Mode: "binary", RunningVersion: "v1.2.2", ProcessStartedAt: time.Now().Add(-time.Hour).UnixNano()}}
	s := NewMaintenanceServiceWithDeps(detector, nil, scheduler)
	s.ops = NewMaintenanceOperationStoreWithDB(db, "sqlite")
	op := s.ops.StartWithContext(context.Background(), "restart", []string{"restart"}, "Restarting", "v1.2.2")
	s.ops.MarkRestarting(op.ID, "Waiting")
	s.ops.mu.Lock()
	s.ops.latest.StartedAtNanos = time.Now().Add(-4 * time.Minute).UnixNano()
	s.ops.persistLocked()
	s.ops.mu.Unlock()
	got := s.Status()
	if got.Phase != "manual_recovery_required" || !got.Running || got.FinishedAt != nil {
		t.Fatalf("unobserved restart falsely released ownership: %+v", got)
	}
	if scheduler.calls != 0 {
		t.Fatal("restart watchdog replayed destructive command")
	}
	detector.info.ProcessStartedAt = time.Now().UnixNano()
	got = s.Status()
	if got.Phase != "completed" || got.Running {
		t.Fatalf("fresh exact runtime did not reconcile restart: %+v", got)
	}
}
