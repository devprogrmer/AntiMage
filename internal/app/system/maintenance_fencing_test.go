package system

import (
	"context"
	"errors"
	operationapp "github.com/antimage/antimage/internal/app/operations"
	"testing"
)

func TestPanelMaintenanceLeaseRejectsOtherExecutorAndStaleStore(t *testing.T) {
	db := newPanelUpdateDB(t)
	ctx := context.Background()
	first := NewMaintenanceOperationStoreWithDB(db, "sqlite")
	op := first.Start("update", []string{"update", "--version", "v1.2.3"}, "Updating", "v1.2.2")
	if err := first.AcquireExecution(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	args, err := first.FencedArgs(op.ID, []string{"update"})
	if err != nil || len(args) != 11 {
		t.Fatalf("owned args: %v %v", args, err)
	}
	second := NewMaintenanceOperationStoreWithDB(db, "sqlite")
	if err := second.AcquireExecution(ctx, op.ID); !errors.Is(err, operationapp.ErrLeaseHeld) {
		t.Fatalf("live executor taken over: %v", err)
	}
	if _, err := db.Exec(`UPDATE operation_executor_leases SET expires_at=0 WHERE operation_id=?`, op.ID); err != nil {
		t.Fatal(err)
	}
	if err := second.AcquireExecution(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := first.FencedArgs(op.ID, []string{"update"}); !errors.Is(err, operationapp.ErrLeaseLost) {
		t.Fatalf("stale scheduler accepted: %v", err)
	}
	first.MarkVerified(op.ID, "v1.2.3")
	if !errors.Is(first.PersistenceError(), operationapp.ErrLeaseLost) {
		t.Fatal("old Panel result wrote after takeover")
	}
	if !first.Latest().Running {
		t.Fatal("stale executor published local false success")
	}
	stored, err := operationapp.Get(ctx, db, op.ID)
	if err != nil || operationapp.Terminal(stored.State) {
		t.Fatalf("stale result released reservation: %+v %v", stored, err)
	}
}
